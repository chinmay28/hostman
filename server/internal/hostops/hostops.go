// Package hostops runs administrative operations on a host over SSH: browsing
// and editing files, managing the systemd services someone installed by hand,
// reading and writing the crontab, and restarting the machine.
//
// Like the rest of HostMan there is no agent on the host. Everything here is a
// short POSIX shell script fed to one SSH session, with every value the user
// supplied passed as a quoted positional argument rather than spliced into the
// script text — a path can never become a command.
package hostops

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/chinmay28/deployer/server/internal/sshx"
	"github.com/chinmay28/deployer/server/internal/store"
)

// Connector opens SSH connections to hosts. hosts.Service satisfies it.
type Connector interface {
	Connect(ctx context.Context, h *store.Host) (*sshx.Client, error)
}

// Service performs operations on hosts.
type Service struct {
	conn Connector
}

// NewService builds a Service that connects through c.
func NewService(c Connector) *Service { return &Service{conn: c} }

// asUser builds `sh -c SCRIPT deployer ARGS...`, so every value the user
// supplied arrives as "$1", "$2" — a quoted argument the script reads, never
// text the shell parses. A path can therefore never become a command.
func asUser(script string, args ...string) string {
	var b strings.Builder
	b.WriteString("sh -c ")
	b.WriteString(sshx.Quote(script))
	b.WriteString(" deployer")
	for _, a := range args {
		b.WriteString(" ")
		b.WriteString(sshx.Quote(a))
	}
	return b.String()
}

// elevate is asUser, run as root wherever passwordless sudo is available and as
// the connecting user wherever it is not. Asking the host rather than trusting
// the sudo flag recorded on the last probe means the answer is never stale.
//
// Browsing as root is the point: HostMan already holds a key that can run
// anything on this machine, so a file browser that could not open /etc would be
// hiding the reality rather than limiting it.
func elevate(script string, args ...string) string {
	plain := asUser(script, args...)
	return fmt.Sprintf("if sudo -n true 2>/dev/null; then sudo -n -- %s; else %s; fi", plain, plain)
}

// run opens a connection, runs one script, and closes it again.
func (s *Service) run(ctx context.Context, h *store.Host, cmd, stdin string) (*sshx.Result, error) {
	client, err := s.conn.Connect(ctx, h)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	return client.RunInput(ctx, cmd, stdin)
}

// sections splits marker-delimited script output. Unlike the metrics probe's
// parser it keeps every byte of a line intact: file listings are tab-separated
// and a name may legitimately start or end with a space.
func sections(out string) map[string][]string {
	found := map[string][]string{}
	current := ""
	// The newline that ends the last line terminates it; it does not start an
	// empty one after it.
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if name, ok := strings.CutPrefix(line, "@@"); ok {
			current = strings.TrimSpace(name)
			found[current] = []string{}
			continue
		}
		if current == "" {
			continue
		}
		found[current] = append(found[current], line)
	}
	return found
}

// first returns the first line of a section, or "".
func first(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[0])
}

// ErrInvalid marks a refusal by HostMan rather than by the host: a path it
// will not send, a name it will not accept, a file too large to carry. The
// caller can tell it apart from "the host said no", which is a different
// problem with a different fix.
var ErrInvalid = errors.New("invalid request")

// invalidError keeps its own wording — "invalid request: ..." in front of a
// perfectly clear sentence would only get in the way.
type invalidError struct{ msg string }

func (e invalidError) Error() string { return e.msg }

func (e invalidError) Is(target error) bool { return target == ErrInvalid }

func invalid(format string, a ...any) error {
	return invalidError{fmt.Sprintf(format, a...)}
}

// failure turns a non-zero exit into the most useful message the host gave,
// falling back to something better than silence.
func failure(res *sshx.Result, fallback string) error {
	for _, stream := range []string{res.Stderr, res.Stdout} {
		if msg := strings.TrimSpace(stream); msg != "" {
			lines := strings.Split(msg, "\n")
			return fmt.Errorf("%s", strings.TrimSpace(lines[len(lines)-1]))
		}
	}
	return fmt.Errorf("%s (exit %d)", fallback, res.ExitCode)
}
