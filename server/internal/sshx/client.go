package sshx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrHostKeyChanged means the host presented a different key than the one
// pinned when HostMan first connected. Treated as fatal: it is either a
// reinstalled host or someone in the middle.
var ErrHostKeyChanged = errors.New("host key changed since it was first trusted")

// ErrAuthFailed means the host refused the credentials offered. Distinguished
// from a transport failure so callers can say "wrong password" rather than
// "unreachable".
var ErrAuthFailed = errors.New("authentication failed")

// DialTimeout bounds the TCP connect and SSH handshake.
const DialTimeout = 10 * time.Second

// Target describes where and how to connect.
type Target struct {
	Address string
	Port    int
	User    string
	// HostKey is the pinned authorized-key line. Empty means trust-on-first-use:
	// whatever the host presents is recorded in Client.HostKey.
	HostKey string
}

// Client is a live SSH connection to a host.
type Client struct {
	// HostKey is the key the host presented, in authorized_keys form. Callers
	// pin this on first connect.
	HostKey string

	conn *ssh.Client
	once sync.Once
}

// Dial opens an SSH connection using HostMan's keypair, honouring ctx for the
// connect phase.
func Dial(ctx context.Context, t Target, id *Identity) (*Client, error) {
	return dial(ctx, t, []ssh.AuthMethod{ssh.PublicKeys(id.Signer)})
}

// DialPassword opens an SSH connection with a password, for the one-time setup
// that installs HostMan's key on a host. The password is used for this
// handshake and nothing else: it is never written to the database or the log.
//
// Keyboard-interactive is offered alongside plain password auth because that is
// how sshd asks when it delegates to PAM, which is the default on most distros.
func DialPassword(ctx context.Context, t Target, password string) (*Client, error) {
	interactive := ssh.KeyboardInteractive(func(_, _ string, questions []string, echos []bool) ([]string, error) {
		answers := make([]string, len(questions))
		for i := range questions {
			// Only answer prompts with the echo off — those are password
			// prompts. An echoing prompt is asking for something else.
			if i < len(echos) && echos[i] {
				continue
			}
			answers[i] = password
		}
		return answers, nil
	})
	return dial(ctx, t, []ssh.AuthMethod{ssh.Password(password), interactive})
}

func dial(ctx context.Context, t Target, auth []ssh.AuthMethod) (*Client, error) {
	port := t.Port
	if port == 0 {
		port = 22
	}
	addr := net.JoinHostPort(t.Address, strconv.Itoa(port))

	var presented string
	cfg := &ssh.ClientConfig{
		User:    t.User,
		Auth:    auth,
		Timeout: DialTimeout,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			presented = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
			if t.HostKey == "" {
				return nil // trust on first use; caller pins it
			}
			if presented != strings.TrimSpace(t.HostKey) {
				return fmt.Errorf("%w (now %s)", ErrHostKeyChanged, ssh.FingerprintSHA256(key))
			}
			return nil
		},
	}

	dialer := &net.Dialer{Timeout: DialTimeout}
	netConn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", addr, err)
	}
	// The handshake has no context-aware form; the deadline covers it, and
	// cancelling ctx closes the socket out from under it.
	stop := context.AfterFunc(ctx, func() { netConn.Close() })
	defer stop()

	if err := netConn.SetDeadline(time.Now().Add(DialTimeout)); err != nil {
		netConn.Close()
		return nil, err
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(netConn, addr, cfg)
	if err != nil {
		netConn.Close()
		if isAuthFailure(err) {
			return nil, fmt.Errorf("%w for %s@%s", ErrAuthFailed, t.User, addr)
		}
		return nil, fmt.Errorf("ssh handshake with %s: %w", addr, err)
	}
	if err := netConn.SetDeadline(time.Time{}); err != nil {
		sshConn.Close()
		return nil, err
	}
	return &Client{HostKey: presented, conn: ssh.NewClient(sshConn, chans, reqs)}, nil
}

// Close releases the connection. Safe to call more than once.
func (c *Client) Close() error {
	var err error
	c.once.Do(func() { err = c.conn.Close() })
	return err
}

// Result is the outcome of a remote command.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Run executes cmd and waits for it to finish. A non-zero exit is reported in
// Result.ExitCode, not as an error; only transport failures return an error.
func (c *Client) Run(ctx context.Context, cmd string) (*Result, error) {
	return c.RunInput(ctx, cmd, "")
}

// RunInput is Run with stdin fed from a string. It exists for `sudo -S`, which
// wants the password on stdin — passing it on the command line would put it in
// the host's process list.
func (c *Client) RunInput(ctx context.Context, cmd, stdin string) (*Result, error) {
	session, err := c.conn.NewSession()
	if err != nil {
		return nil, fmt.Errorf("open session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	if stdin != "" {
		session.Stdin = strings.NewReader(stdin)
	}

	stop := context.AfterFunc(ctx, func() { session.Signal(ssh.SIGKILL); session.Close() })
	defer stop()

	res := &Result{}
	err = session.Run(cmd)
	res.Stdout = stdout.String()
	res.Stderr = stderr.String()
	if err != nil {
		var exitErr *ssh.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitStatus()
			return res, nil
		}
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		return res, fmt.Errorf("run %q: %w", firstLine(cmd), err)
	}
	return res, nil
}

// Stream executes cmd, copying stdout and stderr into out as they arrive, and
// returns the exit code.
func (c *Client) Stream(ctx context.Context, cmd string, out io.Writer) (int, error) {
	session, err := c.conn.NewSession()
	if err != nil {
		return -1, fmt.Errorf("open session: %w", err)
	}
	defer session.Close()

	// Interleave both streams in the order the remote command wrote them.
	var mu sync.Mutex
	w := &lockedWriter{mu: &mu, w: out}
	session.Stdout = w
	session.Stderr = w

	stop := context.AfterFunc(ctx, func() { session.Signal(ssh.SIGKILL); session.Close() })
	defer stop()

	if err := session.Run(cmd); err != nil {
		var exitErr *ssh.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitStatus(), nil
		}
		if ctx.Err() != nil {
			return -1, ctx.Err()
		}
		return -1, fmt.Errorf("run %q: %w", firstLine(cmd), err)
	}
	return 0, nil
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// isAuthFailure recognises the credentials being refused, as opposed to the
// host being unreachable or presenting the wrong key.
func isAuthFailure(err error) bool {
	if errors.Is(err, ErrHostKeyChanged) {
		return false
	}
	return strings.Contains(err.Error(), "unable to authenticate")
}

// Quote wraps s so a POSIX shell reads it as one literal word: a single quote
// inside single quotes has to be closed, escaped, and reopened.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + "..."
	}
	return s
}
