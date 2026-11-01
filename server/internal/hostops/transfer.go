package hostops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chinmay28/hostman/server/internal/sshx"
	"github.com/chinmay28/hostman/server/internal/store"
)

// Copying a folder, moving it to another disk, or sending it to another host
// is work measured in minutes rather than milliseconds, and a phone cannot be
// trusted to keep a request open for that long: the screen locks, another app
// comes to the front, wifi hands over to cellular. So these run as jobs. The
// request starts one and comes straight back; the job runs on HostMan's side
// of the connection, in a goroutine that holds the SSH session open until the
// host is done; and the screen follows it by asking how it is getting on.
//
// A job lives in HostMan's memory and not on the host, unlike an ollama pull
// or a deluge update, because sending to another host needs HostMan on the
// line: the source host signs in to the destination with HostMan's key,
// borrowed through a forwarded agent for exactly as long as the session lasts.
// The price is that HostMan restarting takes its jobs with it — a self-update
// in the middle of a transfer ends the transfer. The screen says so rather
// than the job vanishing.
//
// Sending is tar over ssh rather than scp. It carries a folder with its modes,
// owners and symlinks intact, it needs nothing on either host beyond tar and
// an ssh client, and the far end can be `sudo tar` where scp could only ever
// write as the SSH user. The destination's key is checked against the one
// HostMan pinned when it first connected there — the source host is handed a
// known_hosts file with that one line in it and told to accept nothing else.

// Kinds of job.
const (
	KindCopy = "copy"
	KindMove = "move"
	KindSend = "send"
)

// Job states.
const (
	StateRunning   = "running"
	StateDone      = "done"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

const (
	// MaxJobLogBytes is how much of a job's output is kept. tar and cp are
	// quiet when things go well, so this is room for what went wrong.
	MaxJobLogBytes = 16 << 10
	// MaxJobDuration is the longest a job may run. A library being sent over
	// wifi takes hours; nothing takes a day.
	MaxJobDuration = 24 * time.Hour
	// KeepFinished is how long a finished job stays listed after it ends, so
	// a screen that was away sees how it went. Dismissing it is quicker.
	KeepFinished = time.Hour
)

// ErrJobNotFound means the job id is unknown: it never existed, or it finished
// long enough ago to have been forgotten.
var ErrJobNotFound = errors.New("that job is no longer known")

// ErrJobRunning means a job was asked to be forgotten while still running.
// Cancelling it is the step before that.
var ErrJobRunning = errors.New("that job is still running")

// Job is one copy, move or send, as the screen sees it.
type Job struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// HostID and Host are where the job runs: the source.
	HostID int64  `json:"hostId"`
	Host   string `json:"host"`
	// Path is what is being copied, moved or sent.
	Path string `json:"path"`
	// To is the destination directory. What is sent keeps its name inside it.
	To string `json:"to"`
	// DestHostID and DestHost are set on a send: where To is.
	DestHostID int64  `json:"destHostId,omitempty"`
	DestHost   string `json:"destHost,omitempty"`
	State      string `json:"state"`
	// Error is why it failed, in the host's words where it gave any.
	Error string `json:"error,omitempty"`
	// Log is the tail of what the host printed.
	Log        string     `json:"log"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// Forwarder is a Connector that can also lend HostMan's key to a host for the
// length of one command. hosts.Service satisfies it.
type Forwarder interface {
	Connector
	Identity() *sshx.Identity
}

// Transfers owns the jobs.
type Transfers struct {
	log *slog.Logger
	// run is the one call that reaches a host: connect, run one command with
	// its output streamed, and hang up. Tests replace it with a local shell,
	// so what they prove is the bookkeeping and the scripts rather than sshd.
	run func(ctx context.Context, h *store.Host, cmd, stdin string, out io.Writer, forward bool) (int, error)
	now func() time.Time

	mu   sync.Mutex
	jobs map[string]*job
}

type job struct {
	Job
	cancel context.CancelFunc
	out    *tailBuffer
}

// NewTransfers builds a Transfers that connects through c.
func NewTransfers(c Forwarder, log *slog.Logger) *Transfers {
	t := &Transfers{log: log, now: time.Now, jobs: map[string]*job{}}
	t.run = func(ctx context.Context, h *store.Host, cmd, stdin string, out io.Writer, forward bool) (int, error) {
		client, err := c.Connect(ctx, h)
		if err != nil {
			return -1, err
		}
		defer client.Close()
		if forward {
			return client.StreamAgent(ctx, cmd, stdin, out, c.Identity())
		}
		return client.StreamInput(ctx, cmd, stdin, out)
	}
	return t
}

// Copy duplicates path into the directory dir on the same host, keeping its
// name. An existing entry of that name is never overwritten.
func (t *Transfers) Copy(ctx context.Context, h *store.Host, from, dir string) (*Job, error) {
	from, dest, err := placement(from, dir)
	if err != nil {
		return nil, err
	}
	return t.start(ctx, &Job{Kind: KindCopy, HostID: h.ID, Host: h.Name, Path: from, To: dir},
		h, elevate(copyScript, from, dest), "", false)
}

// Move moves path into the directory dir on the same host, keeping its name.
// Across filesystems — an external drive to the SD card — that is a copy and
// a delete, which is why it is a job and not a rename.
func (t *Transfers) Move(ctx context.Context, h *store.Host, from, dir string) (*Job, error) {
	from, dest, err := placement(from, dir)
	if err != nil {
		return nil, err
	}
	return t.start(ctx, &Job{Kind: KindMove, HostID: h.ID, Host: h.Name, Path: from, To: dir},
		h, elevate(moveScript, from, dest), "", false)
}

// Send copies path on src into the directory dir on dst, keeping its name.
// The source host does the sending, signed in to the destination with
// HostMan's key for the duration.
func (t *Transfers) Send(ctx context.Context, src *store.Host, from string, dst *store.Host, dir string) (*Job, error) {
	from, err := CleanPath(from)
	if err != nil {
		return nil, err
	}
	if from == "/" {
		return nil, invalid("refusing to send /")
	}
	dir, err = CleanPath(dir)
	if err != nil {
		return nil, err
	}
	if src.ID == dst.ID {
		return nil, invalid("%s is where it already is; use Copy to copy it within the host", path.Base(from))
	}
	target, known, err := destination(dst)
	if err != nil {
		return nil, err
	}
	port := dst.Port
	if port == 0 {
		port = 22
	}
	remote := elevate(receiveScript, dir, path.Base(from))
	cmd := withAgent(sendScript, from, target, strconv.Itoa(port), remote)
	return t.start(ctx, &Job{Kind: KindSend, HostID: src.ID, Host: src.Name, Path: from, To: dir,
		DestHostID: dst.ID, DestHost: dst.Name}, src, cmd, known+"\n", true)
}

// placement works out where a copy or move lands: the source's name under the
// destination directory. It refuses the moves that cannot end well.
func placement(from, dir string) (string, string, error) {
	from, err := CleanPath(from)
	if err != nil {
		return "", "", err
	}
	if from == "/" {
		return "", "", invalid("refusing to copy or move /")
	}
	dir, err = CleanPath(dir)
	if err != nil {
		return "", "", err
	}
	name := path.Base(from)
	dest := path.Join(dir, name)
	switch {
	case dest == from:
		return "", "", invalid("%s is already in %s", name, dir)
	case dir == from || strings.HasPrefix(dir, from+"/"):
		return "", "", invalid("%s cannot go inside itself", name)
	}
	return from, dest, nil
}

// destination is what the source host needs to reach dst: user@address, and
// the one known_hosts line that lets it trust the key HostMan pinned there.
func destination(dst *store.Host) (target, known string, err error) {
	if dst.HostKey == "" {
		return "", "", invalid("HostMan has not connected to %s yet, so it cannot vouch for that host's key; check the host first", dst.Name)
	}
	if ip := net.ParseIP(dst.Address); ip != nil && ip.IsLoopback() || strings.EqualFold(dst.Address, "localhost") {
		return "", "", invalid("%s is known to HostMan as %s, which means nothing to another host; edit the host and give it its address on the network", dst.Name, dst.Address)
	}
	if strings.ContainsAny(dst.Address+dst.Username, " \t\n@") {
		return "", "", invalid("%s's address or user name cannot be used from another host", dst.Name)
	}
	host := dst.Address
	if dst.Port != 0 && dst.Port != 22 {
		host = fmt.Sprintf("[%s]:%d", dst.Address, dst.Port)
	}
	return dst.Username + "@" + dst.Address, host + " " + strings.TrimSpace(dst.HostKey), nil
}

// start records the job and runs it in the background. ctx covers only the
// start: the job has its own context, so the request that began it can end.
func (t *Transfers) start(_ context.Context, j *Job, h *store.Host, cmd, stdin string, forward bool) (*Job, error) {
	id, err := newJobID()
	if err != nil {
		return nil, err
	}
	j.ID = id
	j.State = StateRunning
	j.StartedAt = t.now()
	ctx, cancel := context.WithTimeout(context.Background(), MaxJobDuration)
	rec := &job{Job: *j, cancel: cancel, out: &tailBuffer{max: MaxJobLogBytes}}

	t.mu.Lock()
	t.prune()
	t.jobs[id] = rec
	t.mu.Unlock()
	t.log.Info("transfer: started", "job", id, "kind", j.Kind, "host", h.Name, "path", j.Path, "to", j.To, "dest", j.DestHost)

	// The host passed in belongs to the request; the goroutine keeps its own.
	host := *h
	go func() {
		defer cancel()
		code, err := t.run(ctx, &host, cmd, stdin, rec.out, forward)
		t.finish(rec, code, err, ctx.Err())
	}()
	return rec.snapshot(), nil
}

// finish writes down how a job ended. A cancelled job reads as cancelled
// whatever the host said on its way out, because the host's last words are
// about being killed, not about the files.
func (t *Transfers) finish(rec *job, code int, err, ctxErr error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	rec.FinishedAt = &now
	switch {
	case rec.State == StateCancelled:
	case ctxErr != nil && errors.Is(ctxErr, context.DeadlineExceeded):
		rec.State = StateFailed
		rec.Error = fmt.Sprintf("gave up after %s", MaxJobDuration)
	case err != nil:
		rec.State = StateFailed
		rec.Error = err.Error()
	case code != 0:
		rec.State = StateFailed
		rec.Error = lastLine(rec.out.String(), fmt.Sprintf("the host reported failure (exit %d)", code))
	default:
		rec.State = StateDone
	}
	t.log.Info("transfer: finished", "job", rec.ID, "state", rec.State, "err", rec.Error)
}

// Get returns one job.
func (t *Transfers) Get(id string) (*Job, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	rec, ok := t.jobs[id]
	if !ok {
		return nil, ErrJobNotFound
	}
	return rec.snapshot(), nil
}

// ForHost lists the jobs a host is part of, as source or destination, newest
// first. Jobs that finished long enough ago are dropped on the way.
func (t *Transfers) ForHost(hostID int64) []*Job {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prune()
	out := []*Job{}
	for _, rec := range t.jobs {
		if rec.HostID == hostID || rec.DestHostID == hostID {
			out = append(out, rec.snapshot())
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].StartedAt.After(out[k].StartedAt) })
	return out
}

// Cancel stops a running job. The session is killed, which ends whatever the
// host was running under it; a copy stopped half way is removed by its own
// script, a move stopped half way is left as the host's mv left it.
func (t *Transfers) Cancel(id string) error {
	t.mu.Lock()
	rec, ok := t.jobs[id]
	if ok && rec.State == StateRunning {
		rec.State = StateCancelled
	}
	t.mu.Unlock()
	if !ok {
		return ErrJobNotFound
	}
	rec.cancel()
	return nil
}

// Forget drops a finished job from the list.
func (t *Transfers) Forget(id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	rec, ok := t.jobs[id]
	if !ok {
		return ErrJobNotFound
	}
	if rec.State == StateRunning {
		return ErrJobRunning
	}
	delete(t.jobs, id)
	return nil
}

// prune forgets jobs that finished more than KeepFinished ago. Caller holds mu.
func (t *Transfers) prune() {
	cutoff := t.now().Add(-KeepFinished)
	for id, rec := range t.jobs {
		if rec.FinishedAt != nil && rec.FinishedAt.Before(cutoff) {
			delete(t.jobs, id)
		}
	}
}

// snapshot copies the job for a caller, with the log as it stands. Caller
// holds mu, or the job is not yet shared.
func (j *job) snapshot() *Job {
	out := j.Job
	out.Log = j.out.String()
	return &out
}

func newJobID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// lastLine is the last non-empty line of a host's output, or fallback.
func lastLine(out, fallback string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return fallback
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = append([]byte(nil), b.buf[len(b.buf)-b.max:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

// withAgent is elevate for a script that needs the forwarded agent. sudo
// scrubs SSH_AUTH_SOCK from the environment it hands on, so the socket's path
// is given to the script as its first argument by the login shell that still
// has it, and the script exports it again on the far side of sudo.
func withAgent(script string, args ...string) string {
	var b strings.Builder
	b.WriteString("sh -c ")
	b.WriteString(sshx.Quote(script))
	b.WriteString(` hostman "$SSH_AUTH_SOCK"`)
	for _, a := range args {
		b.WriteString(" ")
		b.WriteString(sshx.Quote(a))
	}
	plain := b.String()
	return fmt.Sprintf("if sudo -n true 2>/dev/null; then sudo -n -- %s; else %s; fi", plain, plain)
}

// copyScript duplicates $1 as $2. It refuses to overwrite, and a copy that
// fails part way is removed rather than left looking like a copy that worked.
// cp -a keeps modes, owners, times and symlinks as they are — a folder copied
// as root is the same folder afterwards, not a folder now owned by root.
const copyScript = `set -u
from=$1
to=$2
[ -e "$from" ] || [ -L "$from" ] || { printf 'no such file: %s\n' "$from" >&2; exit 2; }
if [ -e "$to" ] || [ -L "$to" ]; then printf '%s already exists\n' "$to" >&2; exit 3; fi
d=$(dirname -- "$to")
[ -d "$d" ] || { printf 'no such directory: %s\n' "$d" >&2; exit 4; }
trap 'code=$?; if [ "$code" -ne 0 ]; then rm -rf -- "$to"; printf "removed the partial copy at %s\n" "$to" >&2; fi' EXIT
trap 'exit 130' HUP INT TERM
cp -a -- "$from" "$to" || exit 5
printf 'copied\n'
`

// moveScript is mv, refusing to overwrite. Within a filesystem it is a
// rename; across one it is a copy followed by a delete, which mv does itself
// and which is what makes it worth following rather than waiting on.
const moveScript = `set -u
from=$1
to=$2
[ -e "$from" ] || [ -L "$from" ] || { printf 'no such file: %s\n' "$from" >&2; exit 2; }
if [ -e "$to" ] || [ -L "$to" ]; then printf '%s already exists\n' "$to" >&2; exit 3; fi
d=$(dirname -- "$to")
[ -d "$d" ] || { printf 'no such directory: %s\n' "$d" >&2; exit 4; }
mv -- "$from" "$to" || exit 5
printf 'moved\n'
`

// sendScript runs on the source host. $1 is the agent socket handed through
// sudo, $2 what to send, $3 user@host, $4 the port, and $5 the command to run
// on the far side; the destination's known_hosts line arrives on stdin. The
// archive is made with tar and read by tar on the other end; tar's own exit
// status is kept to one side so that a file it could not read is reported as
// the failure it is, instead of being quietly left out of an archive that
// then extracts without complaint.
const sendScript = `set -u
SSH_AUTH_SOCK=$1
export SSH_AUTH_SOCK
src=$2
target=$3
port=$4
remote=$5
[ -e "$src" ] || [ -L "$src" ] || { printf 'no such file: %s\n' "$src" >&2; exit 2; }
command -v ssh >/dev/null 2>&1 || { printf 'ssh is not installed on this host\n' >&2; exit 5; }
command -v tar >/dev/null 2>&1 || { printf 'tar is not installed on this host\n' >&2; exit 5; }
[ -n "$SSH_AUTH_SOCK" ] && [ -S "$SSH_AUTH_SOCK" ] || { printf 'the SSH agent was not forwarded to this host\n' >&2; exit 6; }
known=$(mktemp) || { printf 'could not write a temporary file\n' >&2; exit 7; }
status=$(mktemp) || { rm -f "$known"; printf 'could not write a temporary file\n' >&2; exit 7; }
trap 'rm -f "$known" "$status"' EXIT
cat > "$known"
d=$(dirname -- "$src")
n=$(basename -- "$src")
{ tar -C "$d" -cf - "./$n"; printf '%s\n' "$?" > "$status"; } | ssh -p "$port" \
  -o BatchMode=yes -o StrictHostKeyChecking=yes -o CheckHostIP=no -o LogLevel=ERROR \
  -o UserKnownHostsFile="$known" -o GlobalKnownHostsFile=/dev/null -o ConnectTimeout=20 \
  "$target" "$remote"
rc=$?
[ "$rc" -eq 0 ] || exit "$rc"
if [ "$(cat "$status" 2>/dev/null)" != 0 ]; then
  printf 'not everything under %s could be read, so the copy on the other host is incomplete\n' "$src" >&2
  exit 8
fi
printf 'sent\n'
`

// receiveScript runs on the destination, through ssh from the source. It
// unpacks what arrives on stdin into $1 as $2, refusing to overwrite, and
// removes what it unpacked if the archive stops short — a connection that
// dropped half way must not leave half a folder that reads as the whole one.
const receiveScript = `set -u
dir=$1
name=$2
[ -d "$dir" ] || { printf 'no such directory on the destination: %s\n' "$dir" >&2; exit 2; }
if [ -e "$dir/$name" ] || [ -L "$dir/$name" ]; then printf '%s already exists on the destination\n' "$dir/$name" >&2; exit 3; fi
command -v tar >/dev/null 2>&1 || { printf 'tar is not installed on the destination\n' >&2; exit 5; }
trap 'code=$?; if [ "$code" -ne 0 ]; then rm -rf -- "$dir/$name"; printf "removed the partial copy at %s\n" "$dir/$name" >&2; fi' EXIT
trap 'exit 130' HUP INT TERM
tar -C "$dir" -xpf - || exit 4
printf 'received\n'
`
