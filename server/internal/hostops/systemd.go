package hostops

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/chinmay28/deployer/server/internal/sshx"
	"github.com/chinmay28/deployer/server/internal/store"
)

// A machine runs hundreds of systemd services and cares about a handful. The
// ones worth a screen on a phone are the ones someone put there by hand — the
// unit files under /etc/systemd/system and /usr/local/lib/systemd/system — not
// the hundreds the distribution ships in /usr/lib/systemd/system. That is what
// "user created" means here, and it is decided by where the unit file lives,
// which is the same rule systemd itself uses to decide who wins.
//
// Timers count as much as services. A job that runs on a schedule is written as
// a pair — a .service with no [Install] section and a .timer that starts it —
// and the timer is the half that carries the schedule and the half that gets
// enabled. Listing only the services showed such a job as a unit nothing
// appeared to start, and a timer-only install, where the service being
// scheduled is the distribution's, showed nothing at all.
//
// systemctl show is the source of the state: one call describes every unit in
// key=value lines, which parse the same on every version worth supporting, and
// unlike `status` it never wraps, colours or truncates what it says.

const (
	// MaxUnitBytes bounds a unit file. Real ones are a dozen lines.
	MaxUnitBytes = 64 << 10
	// MaxUnits bounds one listing. A host with more hand-written services than
	// this has bigger problems than a phone screen.
	MaxUnits = 300
	// MaxLogBytes bounds one journal fetch. The tail is what matters, so an
	// over-long answer keeps its end rather than its beginning.
	MaxLogBytes = 256 << 10
	// Log line counts the UI may ask for.
	MinLogLines     = 20
	MaxLogLines     = 2000
	DefaultLogLines = 200
)

// unitPattern is what systemd allows in a unit name, narrowed to the two types
// HostMan manages. The name reaches a command line as a quoted argument either
// way; this is what keeps a typo from becoming a confusing error from the host.
var unitPattern = regexp.MustCompile(`^[A-Za-z0-9_:@][A-Za-z0-9_.:@\\-]{0,238}\.(service|timer)$`)

// unitActions is every action HostMan will ask systemd for. Anything else —
// masking, editing state files, isolating targets — belongs on a terminal, not
// on a phone.
var unitActions = map[string]bool{
	"start":   true,
	"stop":    true,
	"restart": true,
	"reload":  true,
	"enable":  true,
	"disable": true,
}

// Unit is one service, as systemd describes it.
type Unit struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Load is systemd's LoadState: "loaded", "not-found", "masked", "error".
	Load string `json:"load"`
	// Active is ActiveState: "active", "inactive", "failed", "activating",
	// "deactivating".
	Active string `json:"active"`
	// Sub is the finer state within Active: "running", "exited", "dead".
	Sub string `json:"sub"`
	// FileState is UnitFileState: "enabled", "disabled", "static", "masked",
	// or empty where systemd has no opinion (a template, say).
	FileState string `json:"fileState"`
	// Path is the unit file systemd actually read, which is what the editor
	// opens. Empty for a unit with no fragment, such as a masked one.
	Path string `json:"path"`
	// Template marks a foo@.service, which is a pattern for instances rather
	// than something that can be started on its own.
	Template bool `json:"template"`
	// Timer marks a .timer, which runs nothing itself and starts another unit
	// on a schedule. Almost every field below means something else on one, so
	// the screens branch on it rather than showing a memory figure for a clock.
	Timer bool `json:"timer"`
	// Triggers is the unit a timer starts — the other half of the pair, and the
	// mirror of StartedBy. Empty on anything that is not a timer.
	Triggers string `json:"triggers,omitempty"`
	// NextS is how many seconds until a timer next fires, and LastS how long
	// ago it last did. Both are 0 where systemd does not say: a timer that is
	// not running has no next, and one that has never fired has no last.
	NextS   int64 `json:"nextS,omitempty"`
	LastS   int64 `json:"lastS,omitempty"`
	MainPID int   `json:"mainPid"`
	// Memory is what the unit's cgroup is using now; 0 where nothing on the
	// host counts it.
	Memory int64 `json:"memory"`
	// MemoryFrom says where that figure came from, because the two sources are
	// not the same promise. "cgroup" is the kernel's own accounting for the
	// unit — the number systemd reports when it is accounting, and the same
	// number read off the cgroup when it is not. "rss" is the resident size of
	// the unit's processes added up, which is the only answer left on a host
	// with no memory controller and counts shared pages once per process.
	// Empty where there is no figure at all. Only the single-unit call fills
	// it in; a listing does not ask.
	MemoryFrom string `json:"memoryFrom,omitempty"`
	// Restarts is how many times systemd has restarted it by itself, which is
	// the difference between "running" and "flapping".
	Restarts int `json:"restarts"`
	// SinceS is how long it has been in its current state, in seconds.
	SinceS int64 `json:"sinceS"`
	// Result is why it last stopped — "success", "exit-code", "signal",
	// "timeout" — which is the first thing to know about a failed unit.
	Result string `json:"result"`
	// LoadError is why systemd could not read the unit file, on the units
	// where it could not. Empty otherwise.
	LoadError string `json:"loadError,omitempty"`
	// StartedBy names the units that pull this one in, most specific relation
	// first. It is what "static" leaves unsaid: a unit with no [Install]
	// section is started by somebody, and this is who. Empty on a listing,
	// which does not ask for it.
	StartedBy []string `json:"startedBy,omitempty"`
}

// UnitList is every hand-installed service and timer on a host.
type UnitList struct {
	Units []Unit `json:"units"`
	// AsUser is who the commands ran as — root wherever sudo is available.
	AsUser string `json:"asUser"`
	// Truncated means the host has more unit files than MaxUnits.
	Truncated bool `json:"truncated"`
}

// UnitLog is the tail of one service's journal.
type UnitLog struct {
	Name    string `json:"name"`
	Lines   int    `json:"lines"`
	Content string `json:"content"`
	// Truncated means the log was longer than MaxLogBytes and the beginning
	// was dropped — the end is the part worth keeping.
	Truncated bool   `json:"truncated"`
	AsUser    string `json:"asUser"`
}

// showProps is the property set every screen is built from. Id comes first
// because it is what separates one unit's block from the next.
//
// The last four are a timer's: Unit is what it starts, and the elapse stamps
// are when it next will and when it last did. systemd leaves out a property the
// unit type does not have rather than erroring, so they cost a service nothing
// but the asking.
const showProps = `-p Id -p Description -p LoadState -p ActiveState -p SubState ` +
	`-p UnitFileState -p FragmentPath -p MainPID -p MemoryCurrent -p NRestarts ` +
	`-p Result -p LoadError -p ActiveEnterTimestampMonotonic -p InactiveEnterTimestampMonotonic ` +
	`-p Unit -p NextElapseUSecRealtime -p NextElapseUSecMonotonic -p LastTriggerUSec`

// startedByOrder is systemd's reverse dependencies, most specific relation
// first, and the properties that carry them. Every one of these starts the unit
// when it starts itself: Wants, Requires, BindsTo and Upholds all pull their
// target up, and TriggeredBy is the socket, timer or path unit that activates
// it on demand — the usual answer for a service with no [Install] section.
//
// PartOf and Requisite are deliberately absent. The first only propagates stop
// and restart, the second refuses to start rather than starting anything, so
// neither belongs under the word "started".
//
// systemd only names units it currently has loaded, so this answers "what is
// pulling it in now" rather than "what could". A property an older systemd has
// never heard of is left out of the output rather than being an error, which is
// why UpheldBy can be asked for unconditionally.
var startedByOrder = []string{"TriggeredBy", "BoundBy", "RequiredBy", "UpheldBy", "WantedBy"}

// startedByProps asks for them. It is only added to the single-unit call: a
// listing shows a badge per row and has no room for the answer, and asking
// would grow every row's output for a question only one screen asks.
const startedByProps = ` -p TriggeredBy -p BoundBy -p RequiredBy -p UpheldBy -p WantedBy`

// cgroupProps is what a memory figure and a PID need when systemd will not give
// them: where the unit's cgroup is, so the kernel's own numbers can be read
// directly, and the last process systemd itself spawned.
//
// Those two figures are the ones systemd most often declines. MemoryCurrent is
// "[not set]" unless the unit's cgroup has memory accounting on, which is the
// default only from systemd 238 and still needs the controller present on a
// cgroup v1 host — so a perfectly healthy service reports no memory at all on
// plenty of machines. MainPID is 0 on a Type=forking unit whose PIDFile=
// systemd could not follow, and on anything active with nothing running.
//
// Neither fact is actually missing. The kernel is still counting the cgroup and
// still has the processes in it; systemd is only declining to be the one to say
// so. Like startedByProps this is added to the single-unit call alone: the
// listing shows neither figure, and asking would grow every row's output.
const cgroupProps = ` -p ControlGroup -p ExecMainPID`

// cgroupRoot is where the kernel publishes a cgroup's own accounting, and
// procDir where it publishes a process's. They are arguments rather than script
// text for the same reason unitDirs is: a test can point the same script at a
// tree it built.
var (
	cgroupRoot = "/sys/fs/cgroup"
	procDir    = "/proc"
)

// unitDirs is where an administrator's own unit files live. /usr/lib and /lib
// are the distribution's, and everything in them is somebody else's business.
// They are arguments rather than script text so a test can point the same
// script at a directory it built.
var unitDirs = []string{"/etc/systemd/system", "/usr/local/lib/systemd/system"}

// unitListScript names every service and timer file in the directories it is
// given, then describes them all in one systemctl call. Globs that match
// nothing expand to themselves, hence the -e test; a directory that does not
// exist is skipped rather than being an error.
//
// The clock and the uptime go back too: systemd reports state changes as
// microseconds since boot and a timer's next run as microseconds since either
// the epoch or boot, depending on how the timer was written. Only the machine's
// own clock turns those into "for three days" and "in four hours", and it has
// to be the machine's rather than HostMan's — the two are on different boxes
// and need not agree.
const unitListScript = `set -u
limit=$1
shift
command -v systemctl >/dev/null 2>&1 || { printf 'systemd is not installed on this host\n' >&2; exit 2; }
printf '@@user\n%s\n' "$(id -un 2>/dev/null || echo unknown)"
printf '@@uptime\n%s\n' "$(cut -d' ' -f1 /proc/uptime 2>/dev/null || echo 0)"
printf '@@now\n%s\n' "$(date +%s 2>/dev/null || echo 0)"
seen=' '
names=''
n=0
for d in "$@"; do
  [ -d "$d" ] || continue
  for f in "$d"/*.service "$d"/*.timer; do
    [ -e "$f" ] || [ -L "$f" ] || continue
    b=${f##*/}
    case "$b" in *[!A-Za-z0-9_.:@-]*) continue;; esac
    case "$seen" in *" $b "*) continue;; esac
    seen="$seen$b "
    n=$((n + 1))
    if [ "$n" -gt "$limit" ]; then printf '@@truncated\nyes\n'; break 2; fi
    names="$names $b"
  done
done
printf '@@units\n'
[ -n "$names" ] || exit 0
set -f
systemctl show --no-pager ` + showProps + ` -- $names
`

// Units lists the services and timers someone installed on this host by hand.
//
// $names goes to systemctl unquoted so the shell splits it back into one
// argument per unit — safe because the loop above drops any name with a
// character systemd would not accept in one, and set -f stops what is left
// from being read as a glob.
func (s *Service) Units(ctx context.Context, h *store.Host) (*UnitList, error) {
	args := append([]string{strconv.Itoa(MaxUnits)}, unitDirs...)
	res, err := s.run(ctx, h, elevate(unitListScript, args...), "")
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, unitError(res, h, "could not list the services on "+h.Name)
	}
	return parseUnitList(res.Stdout), nil
}

// showScript describes one unit, which need not be one of the hand-installed
// ones — a link from somewhere else in the UI is allowed to land anywhere.
//
// After systemd has had its say the script asks the kernel the two questions
// systemd most often leaves blank, which it can only do here: the answers are
// files on the host, and there is one SSH session to read them in.
//
// The cgroup is looked for in both layouts. Under cgroup v2 the unit's own
// directory holds everything, so ControlGroup hangs straight off the root;
// under v1 each controller is its own tree and the same path hangs off
// /sys/fs/cgroup/memory and /sys/fs/cgroup/systemd. A host with no memory
// controller at all has neither, and the last resort is adding up what the
// unit's processes have resident — a different measure, reported as one.
//
// /proc/uptime stays where it is. It is the machine's clock rather than any
// process's, and it is what every age on these screens is measured against.
const showScript = `set -u
u=$1
cgroot=$2
procdir=$3
command -v systemctl >/dev/null 2>&1 || { printf 'systemd is not installed on this host\n' >&2; exit 2; }
printf '@@user\n%s\n' "$(id -un 2>/dev/null || echo unknown)"
printf '@@uptime\n%s\n' "$(cut -d' ' -f1 /proc/uptime 2>/dev/null || echo 0)"
printf '@@now\n%s\n' "$(date +%s 2>/dev/null || echo 0)"
shown=$(systemctl show --no-pager ` + showProps + startedByProps + cgroupProps + ` -- "$u") || exit 3
printf '@@units\n%s\n' "$shown"

cg=$(printf '%s\n' "$shown" | sed -n 's/^ControlGroup=//p' | head -n 1)
pids=
mem=
from=
if [ -n "$cg" ] && [ "$cg" != / ]; then
  for f in "$cgroot$cg/cgroup.procs" "$cgroot/systemd$cg/cgroup.procs"; do
    [ -r "$f" ] || continue
    pids=$(tr '\n' ' ' <"$f")
    break
  done
  for f in "$cgroot$cg/memory.current" "$cgroot/memory$cg/memory.usage_in_bytes"; do
    [ -r "$f" ] || continue
    mem=$(head -n 1 "$f" 2>/dev/null) || continue
    from=cgroup
    break
  done
  if [ -z "$mem" ] && [ -n "$pids" ]; then
    page=$(getconf PAGESIZE 2>/dev/null || echo 4096)
    case "$page" in ''|*[!0-9]*) page=4096;; esac
    total=0
    for p in $pids; do
      r=$(cut -d' ' -f2 "$procdir/$p/statm" 2>/dev/null) || continue
      case "$r" in ''|*[!0-9]*) continue;; esac
      total=$((total + r))
    done
    if [ "$total" -gt 0 ]; then mem=$((total * page)); from=rss; fi
  fi
fi
printf '@@cgroup\n'
if [ -n "$mem" ]; then printf 'memory=%s\nfrom=%s\n' "$mem" "$from"; fi
if [ -n "$pids" ]; then printf 'procs=%s\n' "$pids"; fi
exit 0
`

// Unit describes one service. A name systemd has never heard of is not an
// error: it answers with LoadState=not-found, which is a state the UI shows
// rather than a failure it reports.
func (s *Service) Unit(ctx context.Context, h *store.Host, name string) (*Unit, error) {
	clean, err := CleanUnit(name)
	if err != nil {
		return nil, err
	}
	res, err := s.run(ctx, h, elevate(showScript, clean, cgroupRoot, procDir), "")
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, unitError(res, h, "could not read "+clean)
	}
	list := parseUnitList(res.Stdout)
	if len(list.Units) == 0 {
		return nil, fmt.Errorf("the host said nothing about %s", clean)
	}
	unit := list.Units[0]
	applyCgroup(&unit, sections(res.Stdout)["cgroup"])
	return &unit, nil
}

// applyCgroup fills in from the kernel what systemd would not say, and never
// overrules it: where systemd gave a figure, systemd's is the one kept, because
// the two are reading the same cgroup and systemd read it first.
func applyCgroup(u *Unit, lines []string) {
	values := map[string]string{}
	for _, line := range lines {
		if key, value, ok := strings.Cut(line, "="); ok {
			values[key] = strings.TrimSpace(value)
		}
	}

	if u.Memory > 0 {
		// MemoryCurrent is the cgroup's own number, so it is named as one.
		u.MemoryFrom = "cgroup"
	} else if mem := memoryBytes(values["memory"]); mem > 0 {
		// The script writes one of two words here and the screen branches on
		// them, so anything else is dropped rather than forwarded: this is a
		// string that came back over SSH.
		switch from := values["from"]; from {
		case "cgroup", "rss":
			u.Memory, u.MemoryFrom = mem, from
		}
	}

	// The processes in the unit's cgroup, when systemd is watching none of
	// them. This is the first one listed rather than the main one — the kernel
	// does not rank them — but on a forking daemon whose PIDFile systemd could
	// not follow, that is the parent, and it is a live PID either way. A unit
	// that is active with nothing running has an empty file here, which leaves
	// the answer at 0 rather than inventing one.
	if u.MainPID == 0 {
		if pids := strings.Fields(values["procs"]); len(pids) > 0 {
			u.MainPID = atoi(pids[0])
		}
	}
}

// actionScript runs one systemctl verb against one unit. systemctl says what
// went wrong on stderr and nothing at all when it worked, so the marker is what
// tells success from a silent failure.
const actionScript = `set -u
u=$1
a=$2
command -v systemctl >/dev/null 2>&1 || { printf 'systemd is not installed on this host\n' >&2; exit 2; }
systemctl --no-pager "$a" -- "$u" || exit 3
printf 'done\n'
`

// Act runs start, stop, restart, reload, enable or disable against a service.
// It returns once systemd has finished — a start that waits for the service to
// come up waits here too, which is the answer worth having.
func (s *Service) Act(ctx context.Context, h *store.Host, name, action string) error {
	clean, err := CleanUnit(name)
	if err != nil {
		return err
	}
	if !unitActions[action] {
		return invalid("%q is not something HostMan will ask systemd to do", action)
	}
	res, err := s.run(ctx, h, elevate(actionScript, clean, action), "")
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return unitError(res, h, fmt.Sprintf("could not %s %s", action, clean))
	}
	return nil
}

// writeUnitScript installs a new unit file and makes systemd read it. It
// refuses to write over anything: a name systemd already knows — including one
// the distribution ships — would be shadowed rather than replaced, and a
// service that quietly overrides sshd is not something to do from a phone.
//
// The file arrives base64-encoded on stdin, and is written through a temporary
// file in the same directory so systemd never sees half of one.
const writeUnitScript = `set -u
u=$1
dir=$2
command -v systemctl >/dev/null 2>&1 || { printf 'systemd is not installed on this host\n' >&2; exit 2; }
mkdir -p -- "$dir" || { printf 'cannot create %s\n' "$dir" >&2; exit 3; }
p="$dir/$u"
[ -e "$p" ] && { printf '%s already exists\n' "$p" >&2; exit 4; }
if systemctl cat -- "$u" >/dev/null 2>&1; then
  printf 'this host already has a service called %s\n' "$u" >&2; exit 5
fi
tmp=$(mktemp "$dir/.deployer.XXXXXX") || { printf 'cannot write in %s\n' "$dir" >&2; exit 6; }
trap 'rm -f "$tmp"' EXIT
base64 -d > "$tmp" || { printf 'could not decode the unit file\n' >&2; exit 7; }
chmod 644 -- "$tmp" 2>/dev/null || true
mv -f -- "$tmp" "$p" || { printf 'could not write %s\n' "$p" >&2; exit 8; }
trap - EXIT
systemctl daemon-reload || { printf 'could not reload systemd\n' >&2; exit 9; }
printf '@@user\n%s\n' "$(id -un 2>/dev/null || echo unknown)"
printf '@@uptime\n%s\n' "$(cut -d' ' -f1 /proc/uptime 2>/dev/null || echo 0)"
printf '@@now\n%s\n' "$(date +%s 2>/dev/null || echo 0)"
printf '@@units\n'
systemctl show --no-pager ` + showProps + ` -- "$u"
`

// CreateUnit writes a new service and hands it to systemd. It is created
// stopped and not enabled; starting it is a separate decision, and a separate
// call, so a unit that will not start is a service sitting there rather than a
// half-finished install.
//
// systemd is the one that says whether the file is a unit file. Anything it
// refuses to load is taken straight back off the disk — a file in
// /etc/systemd/system that systemd cannot read is worse than no file at all,
// because it stays there being wrong.
func (s *Service) CreateUnit(ctx context.Context, h *store.Host, name, content string) (*Unit, error) {
	clean, err := CleanUnit(name)
	if err != nil {
		return nil, err
	}
	if isTemplate(clean) {
		return nil, invalid("HostMan does not create template units")
	}
	if strings.TrimSpace(content) == "" {
		return nil, invalid("a unit file needs something in it")
	}
	if len(content) > MaxUnitBytes {
		return nil, invalid("that unit file is too large (over %d KB)", MaxUnitBytes/1024)
	}

	encoded := base64.StdEncoding.EncodeToString([]byte(ensureFinalNewline(content)))
	res, err := s.run(ctx, h, elevate(writeUnitScript, clean, unitInstallDir), encoded)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, unitError(res, h, "could not create "+clean)
	}

	list := parseUnitList(res.Stdout)
	if len(list.Units) == 0 {
		return nil, fmt.Errorf("%s was written, but the host said nothing about it", clean)
	}
	unit := list.Units[0]
	if unit.Load != "loaded" {
		s.discardUnit(ctx, h, clean)
		if unit.LoadError != "" {
			return nil, fmt.Errorf("systemd would not load that unit file: %s", unit.LoadError)
		}
		return nil, fmt.Errorf("systemd would not load that unit file (%s)", unit.Load)
	}
	return &unit, nil
}

// unitInstallDir is where a unit HostMan creates goes. /etc/systemd/system is
// the administrator's own directory and the one that wins over every other.
const unitInstallDir = "/etc/systemd/system"

// removeUnitScript takes a unit off the machine: the enable symlinks first, so
// nothing is left pointing at a file that has gone, then the file, then any
// drop-in directory belonging to it, which is meaningless without it. Clearing
// the failed state last stops a service that died on its way out from haunting
// `systemctl --failed` after it no longer exists.
const removeUnitScript = `set -u
u=$1
p=$2
command -v systemctl >/dev/null 2>&1 || { printf 'systemd is not installed on this host\n' >&2; exit 2; }
[ -e "$p" ] || { printf 'no such unit file: %s\n' "$p" >&2; exit 3; }
systemctl disable -- "$u" >/dev/null 2>&1 || true
rm -f -- "$p" || { printf 'could not delete %s\n' "$p" >&2; exit 4; }
rm -rf -- "$p.d"
systemctl daemon-reload || { printf 'could not reload systemd\n' >&2; exit 5; }
systemctl reset-failed -- "$u" >/dev/null 2>&1 || true
printf 'removed\n'
`

// RemoveUnit deletes a service that is not running: its unit file, the symlinks
// enabling it, and its drop-in overrides. Whatever the service actually ran is
// left where it is — this removes systemd's knowledge of it, not the program.
//
// Two things it will not do. A running service is stopped first, deliberately
// by the person deleting it, because deleting the unit of something still
// running leaves the process up with nothing left to describe or stop it. And
// only a unit file in an administrator's own directory can go: the
// distribution's, in /usr/lib, belongs to the package manager.
func (s *Service) RemoveUnit(ctx context.Context, h *store.Host, name string) error {
	clean, err := CleanUnit(name)
	if err != nil {
		return err
	}
	unit, err := s.Unit(ctx, h, clean)
	if err != nil {
		return err
	}
	if err := removable(unit); err != nil {
		return err
	}

	res, err := s.run(ctx, h, elevate(removeUnitScript, clean, unit.Path), "")
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return unitError(res, h, "could not delete "+clean)
	}
	return nil
}

// removable says whether a service can be deleted, and why not when it cannot.
func removable(u *Unit) error {
	switch u.Active {
	case "active", "activating", "reloading", "deactivating":
		return fmt.Errorf("%w: %s is still running — stop it first", ErrUnitRunning, u.Name)
	}
	if u.Path == "" {
		return invalid("systemd has no unit file for %s to delete", u.Name)
	}
	if !ownUnitFile(u.Path) {
		return invalid("%s belongs to the distribution, not to you — remove it with the package manager", u.Path)
	}
	return nil
}

// discardUnit removes a unit file HostMan has just written and that systemd
// then refused. Whether the cleanup works is not reported: the failure it is
// cleaning up after is the one worth telling the caller about.
func (s *Service) discardUnit(ctx context.Context, h *store.Host, name string) {
	_, _ = s.run(ctx, h, elevate(removeUnitScript, name, unitInstallDir+"/"+name), "")
}

// ownUnitFile reports whether a unit file is one an administrator put there, as
// opposed to one the distribution ships.
func ownUnitFile(p string) bool {
	dir := path.Dir(path.Clean(p))
	for _, own := range unitDirs {
		if dir == own {
			return true
		}
	}
	return false
}

// ErrUnitRunning means the service has to be stopped before what was asked can
// happen. It is the caller's next step, not a failure of theirs.
var ErrUnitRunning = errors.New("the service is still running")

// loadError tidies systemd's LoadError, which is a D-Bus error name and a
// message: `org.freedesktop.DBus.Error.InvalidArgs "Invalid argument"`. The
// name is noise to everyone; the message is the part that says what is wrong.
func loadError(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if open := strings.Index(value, `"`); open >= 0 {
		if quoted := strings.TrimSuffix(value[open+1:], `"`); quoted != "" {
			return quoted
		}
	}
	return value
}

// reloadScript makes systemd re-read the unit files on disk. Editing a unit
// without this changes the file and nothing else.
const reloadScript = `set -u
command -v systemctl >/dev/null 2>&1 || { printf 'systemd is not installed on this host\n' >&2; exit 2; }
systemctl daemon-reload || exit 3
printf 'done\n'
`

// Reload runs `systemctl daemon-reload`.
func (s *Service) Reload(ctx context.Context, h *store.Host) error {
	res, err := s.run(ctx, h, elevate(reloadScript), "")
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return unitError(res, h, "could not reload the unit files on "+h.Name)
	}
	return nil
}

// logScript prints the tail of a unit's journal, base64-encoded so no line of a
// log can be mistaken for one of the markers around it.
//
// --no-hostname drops a column that is the same on every line and costs a phone
// a third of its width; it arrived in systemd 230, so a host that refuses it
// gets the same command without it rather than no logs at all.
const logScript = `set -u
u=$1
n=$2
cap=$3
command -v journalctl >/dev/null 2>&1 || { printf 'journalctl is not installed on this host\n' >&2; exit 2; }
tmp=$(mktemp /tmp/deployer-log.XXXXXX) || { printf 'cannot write a temporary file\n' >&2; exit 3; }
trap 'rm -f "$tmp"' EXIT
journalctl --no-pager --no-hostname -o short-iso -n "$n" -u "$u" >"$tmp" 2>/dev/null ||
  journalctl --no-pager -o short-iso -n "$n" -u "$u" >"$tmp" || exit 4
printf '@@user\n%s\n' "$(id -un 2>/dev/null || echo unknown)"
printf '@@size\n%s\n' "$(wc -c <"$tmp" | tr -d ' ')"
printf '@@log\n'
tail -c "$cap" "$tmp" | base64
`

// Log returns the last lines of a service's journal, newest last. The tail is
// what is wanted, so a log that overruns MaxLogBytes loses its beginning.
func (s *Service) Log(ctx context.Context, h *store.Host, name string, lines int) (*UnitLog, error) {
	clean, err := CleanUnit(name)
	if err != nil {
		return nil, err
	}
	lines = clampLines(lines)
	cmd := elevate(logScript, clean, strconv.Itoa(lines), strconv.Itoa(MaxLogBytes))
	res, err := s.run(ctx, h, cmd, "")
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, unitError(res, h, "could not read the log for "+clean)
	}
	return parseUnitLog(res.Stdout, clean, lines)
}

// clampLines keeps a request for the journal within what a phone can hold.
func clampLines(lines int) int {
	switch {
	case lines <= 0:
		return DefaultLogLines
	case lines < MinLogLines:
		return MinLogLines
	case lines > MaxLogLines:
		return MaxLogLines
	}
	return lines
}

// parseUnitLog decodes the journal and says whether its beginning was dropped.
func parseUnitLog(out, name string, lines int) (*UnitLog, error) {
	found := sections(out)
	log := &UnitLog{Name: name, Lines: lines, AsUser: first(found["user"])}

	raw, err := base64.StdEncoding.DecodeString(strings.Join(found["log"], ""))
	if err != nil {
		return nil, fmt.Errorf("the host returned something that is not a log: %w", err)
	}
	body := string(raw)
	if size, err := strconv.ParseInt(first(found["size"]), 10, 64); err == nil && size > int64(len(raw)) {
		log.Truncated = true
		// Taking the last bytes cuts the oldest line in half. A half line at
		// the top reads as corruption, so it goes.
		if cut := strings.IndexByte(body, '\n'); cut >= 0 {
			body = body[cut+1:]
		}
	}
	log.Content = body
	return log, nil
}

// parseUnitList reads the output of unitListScript or showScript.
func parseUnitList(out string) *UnitList {
	found := sections(out)
	list := &UnitList{
		Units:     []Unit{},
		AsUser:    first(found["user"]),
		Truncated: len(found["truncated"]) > 0,
	}
	clock := hostClock{
		uptime: parseSeconds(first(found["uptime"])),
		now:    int64(parseSeconds(first(found["now"]))),
	}
	for _, unit := range parseShowBlocks(found["units"], clock) {
		list.Units = append(list.Units, unit)
	}
	// By name, always. A list that reordered itself as services started and
	// stopped would move the row out from under the thumb reaching for it; the
	// filters above it are what put failures in front of you.
	sort.SliceStable(list.Units, func(i, j int) bool {
		return strings.ToLower(list.Units[i].Name) < strings.ToLower(list.Units[j].Name)
	})
	return list
}

// hostClock is the host's own sense of time, which every age and countdown on
// these screens is measured against. systemd answers in microseconds since boot
// or microseconds since the epoch and never in words, and the two boxes need
// not agree about either, so both come back with the properties they explain.
type hostClock struct {
	// uptime is seconds since boot, and now seconds since the epoch. Zero for
	// either means the host did not say, which leaves the stamps it explains
	// unreadable rather than wrong.
	uptime float64
	now    int64
}

// parseShowBlocks splits systemctl's key=value output into one unit per Id.
// systemctl prints properties in the order they were asked for and Id is asked
// for first, so an Id line is where the next unit begins.
func parseShowBlocks(lines []string, clock hostClock) []Unit {
	var units []Unit
	var props map[string]string
	flush := func() {
		if props == nil {
			return
		}
		if unit, ok := unitFromProps(props, clock); ok {
			units = append(units, unit)
		}
		props = nil
	}
	for _, line := range lines {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if key == "Id" {
			flush()
		}
		if props == nil {
			props = map[string]string{}
		}
		props[key] = value
	}
	flush()
	return units
}

// unitFromProps turns one block of properties into a Unit, dropping anything
// that did not come with a name.
func unitFromProps(props map[string]string, clock hostClock) (Unit, bool) {
	name := strings.TrimSpace(props["Id"])
	if name == "" {
		return Unit{}, false
	}
	unit := Unit{
		Name:        name,
		Description: props["Description"],
		Load:        props["LoadState"],
		Active:      props["ActiveState"],
		Sub:         props["SubState"],
		FileState:   props["UnitFileState"],
		Path:        props["FragmentPath"],
		Result:      props["Result"],
		LoadError:   loadError(props["LoadError"]),
		Template:    isTemplate(name),
		Timer:       strings.HasSuffix(name, ".timer"),
		MainPID:     mainPID(props),
		Restarts:    atoi(props["NRestarts"]),
		Memory:      memoryBytes(props["MemoryCurrent"]),
		StartedBy:   startedBy(props),
	}
	// A running unit has been running since it went active; a stopped or
	// failed one has been that way since it went inactive. Reading the wrong
	// one of the pair is how a service that died an hour ago comes to claim it
	// has been up for a week.
	stamp := props["InactiveEnterTimestampMonotonic"]
	if unit.Active == "active" || unit.Active == "activating" || unit.Active == "reloading" {
		stamp = props["ActiveEnterTimestampMonotonic"]
	}
	unit.SinceS = sinceSeconds(stamp, clock.uptime)
	if unit.Timer {
		unit.Triggers = strings.TrimSpace(props["Unit"])
		unit.NextS = nextElapse(props, clock)
		unit.LastS = agoSeconds(props["LastTriggerUSec"], clock.now)
	}
	return unit, true
}

// mainPID is the process systemd is watching. MainPID is the answer wherever
// systemd has one; ExecMainPID is the last process systemd itself spawned, and
// is the answer on a Type=forking unit whose PIDFile= systemd could not follow,
// where MainPID stays 0 for the life of the service.
//
// The fallback only applies while something is running. A unit that is active
// with SubState=exited — a Type=oneshot with RemainAfterExit=yes — has no
// process at all, and its ExecMainPID is the number of one that died: a PID
// nobody can look up is worse than no PID.
//
// ExecMainPID is only asked for on the single-unit call, so on a listing this
// is atoi(MainPID) and nothing more.
func mainPID(props map[string]string) int {
	if pid := atoi(props["MainPID"]); pid > 0 {
		return pid
	}
	switch props["ActiveState"] {
	case "active", "activating", "reloading", "deactivating":
		if sub := props["SubState"]; sub != "exited" && sub != "dead" {
			return atoi(props["ExecMainPID"])
		}
	}
	return 0
}

// isTemplate reports whether a unit name is a pattern rather than a unit —
// systemd's foo@.service, whose instance is empty. A timer is as capable of
// being one as a service.
func isTemplate(name string) bool {
	return strings.HasSuffix(name, "@.service") || strings.HasSuffix(name, "@.timer")
}

// nextElapse is how long until a timer fires again, in seconds.
//
// systemd answers in whichever clock the timer was written against: OnCalendar
// gives a realtime stamp, OnBootSec and OnUnitActiveSec give a monotonic one,
// and a timer with both has both. The realtime one is preferred because it is
// the one a calendar timer — by far the common case — is precise about, and the
// unused half comes back as 0 or as an unsigned -1 rather than being absent.
//
// A timer that is not running has neither, which is 0: no next run rather than
// one due this instant. A stamp in the past is 0 too, which reads as "due".
func nextElapse(props map[string]string, clock hostClock) int64 {
	if at := microSeconds(props["NextElapseUSecRealtime"]); at > 0 && clock.now > 0 {
		return max(at-clock.now, 0)
	}
	if at := microSeconds(props["NextElapseUSecMonotonic"]); at > 0 && clock.uptime > 0 {
		return max(at-int64(clock.uptime), 0)
	}
	return 0
}

// agoSeconds turns a realtime stamp into how long ago it was. Zero means it has
// not happened — a timer that has never fired since boot has no last run.
func agoSeconds(stamp string, now int64) int64 {
	at := microSeconds(stamp)
	if at <= 0 || now <= 0 {
		return 0
	}
	return max(now-at, 0)
}

// microSeconds reads one of systemd's microsecond stamps as whole seconds. The
// ones that do not apply come back as 0, as "infinity", or as an unsigned -1 —
// a number bigger than any clock — and all three mean the same thing here.
func microSeconds(value string) int64 {
	n, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil || n > math.MaxInt64 {
		return 0
	}
	return int64(n / 1e6)
}

// startedBy collects the units that pull this one in, in the order of
// startedByOrder and without repeats: the same unit usually both wants and
// requires another, and naming it twice reads as two answers to one question.
// The unit itself is dropped where it appears — a socket and its service share
// a name often enough that systemd naming one from the other is worth guarding
// against, and "started by itself" is not an answer.
func startedBy(props map[string]string) []string {
	var names []string
	seen := map[string]bool{strings.TrimSpace(props["Id"]): true}
	for _, key := range startedByOrder {
		for _, name := range strings.Fields(props[key]) {
			if seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

// sinceSeconds turns systemd's microseconds-since-boot into an age. A zero
// stamp means it has never been in that state, which is not an age at all.
func sinceSeconds(stamp string, uptime float64) int64 {
	micros, err := strconv.ParseInt(strings.TrimSpace(stamp), 10, 64)
	if err != nil || micros <= 0 || uptime <= 0 {
		return 0
	}
	age := int64(uptime) - micros/1e6
	if age < 0 {
		return 0
	}
	return age
}

// memoryBytes reads MemoryCurrent, which is "[not set]" where the unit has no
// cgroup accounting and an unsigned -1 — a number larger than any machine's
// memory — on the versions that say it that way instead.
func memoryBytes(value string) int64 {
	n, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil || n > math.MaxInt64 {
		return 0
	}
	return int64(n)
}

func atoi(value string) int {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return n
}

// parseSeconds reads /proc/uptime's first field.
func parseSeconds(value string) float64 {
	secs, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0
	}
	return secs
}

// CleanUnit normalizes a unit name from the browser. A name without a suffix
// gets ".service" rather than being refused — it is the part nobody types, and
// a timer is always named with its own suffix because that is the whole point
// of the name.
func CleanUnit(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", invalid("a service name is required")
	}
	if !strings.Contains(name, ".") {
		name += ".service"
	}
	if !unitPattern.MatchString(name) {
		return "", invalid("%q is not a systemd service name", name)
	}
	return name, nil
}

// unitError separates "systemd refused you" from "systemd refused". Starting a
// service needs root, and where sudo is not set up systemd asks for a password
// nobody is there to type — which is worth naming rather than passing on as
// "Interactive authentication required".
func unitError(res *sshx.Result, h *store.Host, fallback string) error {
	detail := strings.ToLower(res.Stderr + res.Stdout)
	switch {
	case strings.Contains(detail, "interactive authentication required"),
		strings.Contains(detail, "access denied"),
		strings.Contains(detail, "permission denied"):
		return errNeedsRoot(h.Username, h.Name)
	}
	return failure(res, fallback)
}
