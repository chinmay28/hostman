package hostops

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/chinmay28/deployer/server/internal/sshx"
	"github.com/chinmay28/deployer/server/internal/store"
)

// A crontab is edited whole, the way `crontab -e` edits it: HostMan reads the
// file, the user changes it, and the whole thing goes back through `crontab -`,
// which parses it and refuses to install anything it cannot read. That refusal
// is the validation — cron's own parser is the only one that counts.

// MaxCrontabBytes bounds a crontab. Real ones are a few hundred bytes.
const MaxCrontabBytes = 256 << 10

// userPattern is what a POSIX account name may look like. Checked before the
// name reaches a command line, even though it is quoted there anyway.
var userPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}\$?$`)

// Crontab is one user's crontab.
type Crontab struct {
	User    string `json:"user"`
	Content string `json:"content"`
	// Exists is false where the user simply has no crontab yet, which is not
	// an error and reads as an empty one.
	Exists bool `json:"exists"`
}

// readCronScript prints the crontab, base64-encoded like a file so no line of
// it can be mistaken for one of the markers around it. A user who has never had
// a crontab is not an error, and cron saying so is not a failure to report.
//
// stderr goes to a file rather than into the output, so a chatty cron cannot
// prepend a warning to someone's crontab.
const readCronScript = `set -u
u=$1
err=$(mktemp /tmp/deployer-cron.XXXXXX) || { printf 'cannot write a temporary file\n' >&2; exit 2; }
trap 'rm -f "$err"' EXIT
if [ -n "$u" ]; then out=$(crontab -l -u "$u" 2>"$err"); status=$?
else out=$(crontab -l 2>"$err"); status=$?; fi
if [ $status -ne 0 ]; then
  if grep -qi 'no crontab for' "$err"; then printf '@@missing\n'; exit 0; fi
  cat "$err" >&2
  exit $status
fi
printf '@@content\n'
printf '%s\n' "$out" | base64
`

// ReadCrontab returns a user's crontab. An empty user means the SSH user's own,
// which needs no privileges; anyone else's is read through sudo.
func (s *Service) ReadCrontab(ctx context.Context, h *store.Host, user string) (*Crontab, error) {
	target, own, err := cronTarget(h, user)
	if err != nil {
		return nil, err
	}
	cmd := asUser(readCronScript, target)
	if !own {
		cmd = elevate(readCronScript, target)
	}
	res, err := s.run(ctx, h, cmd, "")
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, cronError(res, h, user, own)
	}
	return parseCrontab(res.Stdout, cronUser(h, user))
}

// parseCrontab reads the script's output. A missing crontab reads as an empty
// one, which is what the editor should open on.
func parseCrontab(out, user string) (*Crontab, error) {
	found := sections(out)
	cron := &Crontab{User: user}
	lines, ok := found["content"]
	if !ok {
		return cron, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.Join(lines, ""))
	if err != nil {
		return nil, fmt.Errorf("the host returned something that is not a crontab: %w", err)
	}
	cron.Exists = true
	// A crontab whose last line has no newline after it is a crontab whose
	// last line cron ignores, so it goes back with one either way.
	cron.Content = ensureFinalNewline(string(raw))
	return cron, nil
}

// writeCronScript installs a crontab from base64 on stdin. cron parses it and
// keeps the old one if it cannot.
const writeCronScript = `set -u
u=$1
tmp=$(mktemp /tmp/deployer-cron.XXXXXX) || { printf 'cannot write a temporary file\n' >&2; exit 2; }
trap 'rm -f "$tmp"' EXIT
base64 -d > "$tmp" || { printf 'could not decode the crontab\n' >&2; exit 3; }
if [ -n "$u" ]; then crontab -u "$u" -- "$tmp" || exit 4
else crontab -- "$tmp" || exit 4; fi
printf 'installed\n'
`

// WriteCrontab replaces a user's crontab. cron validates the content; a syntax
// error leaves the existing crontab in place and comes back as the error.
func (s *Service) WriteCrontab(ctx context.Context, h *store.Host, user, content string) error {
	target, own, err := cronTarget(h, user)
	if err != nil {
		return err
	}
	if len(content) > MaxCrontabBytes {
		return invalid("that crontab is too large (over %d KB)", MaxCrontabBytes/1024)
	}
	// cron ignores a last line with no newline after it, silently. Adding one
	// is the difference between a job that runs and a job that does not.
	encoded := base64.StdEncoding.EncodeToString([]byte(ensureFinalNewline(content)))

	cmd := asUser(writeCronScript, target)
	if !own {
		cmd = elevate(writeCronScript, target)
	}
	res, err := s.run(ctx, h, cmd, encoded)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return cronError(res, h, user, own)
	}
	return nil
}

// cronTarget resolves the user to act on. It returns the argument for the
// script — empty for "the account HostMan signs in as", which needs no
// privileges — and whether that is the case.
func cronTarget(h *store.Host, user string) (target string, own bool, err error) {
	user = strings.TrimSpace(user)
	if user == "" || user == h.Username {
		return "", true, nil
	}
	if !userPattern.MatchString(user) {
		return "", false, invalid("that is not a valid user name")
	}
	return user, false, nil
}

// cronUser is the name to report back, which is never empty.
func cronUser(h *store.Host, user string) string {
	if user = strings.TrimSpace(user); user != "" {
		return user
	}
	return h.Username
}

// cronError turns cron's complaint into something worth reading. Editing
// another user's crontab needs root, and that is the failure worth naming.
func cronError(res *sshx.Result, h *store.Host, user string, own bool) error {
	err := failure(res, "the crontab command failed")
	detail := strings.ToLower(res.Stderr + res.Stdout)
	switch {
	case strings.Contains(detail, "must be privileged"), strings.Contains(detail, "permission denied"), strings.Contains(detail, "not allowed"):
		if !own {
			return fmt.Errorf("%s needs passwordless sudo to edit %s's crontab", h.Username, user)
		}
	case strings.Contains(detail, "command not found"):
		return errors.New("cron is not installed on this host")
	}
	return err
}

// ensureFinalNewline appends the newline cron needs, leaving an empty crontab
// empty.
func ensureFinalNewline(s string) string {
	if s == "" || strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}
