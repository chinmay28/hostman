package hosts

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/chinmay28/deployer/server/internal/sshx"
	"github.com/chinmay28/deployer/server/internal/store"
)

// Provisioning is the one-time setup that a host otherwise needs done by hand:
// authorizing HostMan's public key, and granting the SSH user passwordless
// sudo. Both are done over a password-authenticated SSH session that lasts for
// the length of the request. The password is held in memory for that session
// and nothing else — it is never persisted, never logged, and never returned.

// authorizeScript appends HostMan's key to the user's authorized_keys, and is
// safe to run twice: a key already present is left alone. It also fixes the
// permissions sshd insists on, and the missing final newline that would
// otherwise splice the new key onto the last one.
const authorizeScript = `set -e
umask 077
mkdir -p "$HOME/.ssh"
chmod 700 "$HOME/.ssh"
touch "$HOME/.ssh/authorized_keys"
chmod 600 "$HOME/.ssh/authorized_keys"
if grep -qxF %[1]s "$HOME/.ssh/authorized_keys"; then
  echo already
else
  if [ -s "$HOME/.ssh/authorized_keys" ] && [ -n "$(tail -c1 "$HOME/.ssh/authorized_keys")" ]; then
    printf '\n' >> "$HOME/.ssh/authorized_keys"
  fi
  printf '%%s\n' %[1]s >> "$HOME/.ssh/authorized_keys"
  echo added
fi
`

// sudoersScript writes the NOPASSWD rule. It validates the file with visudo
// before putting it in place, because a malformed drop-in breaks sudo for
// everyone on the machine, including the user fixing it.
const sudoersScript = `set -e
mkdir -p /etc/sudoers.d
chmod 750 /etc/sudoers.d
tmp=/etc/sudoers.d/.deployer.new
printf '%%s ALL=(ALL) NOPASSWD:ALL\n' %[1]s > "$tmp"
if command -v visudo >/dev/null 2>&1; then
  visudo -cqf "$tmp" >/dev/null
fi
chmod 440 "$tmp"
mv "$tmp" /etc/sudoers.d/deployer
`

// ProvisionStep is one action taken during setup, in the order it was tried.
type ProvisionStep struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// ProvisionResult reports how far setup got. OK means HostMan can now reach
// the host with its own key; SudoOK means unattended installs will work too.
type ProvisionResult struct {
	OK     bool            `json:"ok"`
	Error  string          `json:"error,omitempty"`
	SudoOK bool            `json:"sudoOk"`
	Steps  []ProvisionStep `json:"steps"`
	Hints  []string        `json:"hints,omitempty"`
}

func (r *ProvisionResult) step(name string, ok bool, detail string) {
	r.Steps = append(r.Steps, ProvisionStep{Name: name, OK: ok, Detail: detail})
}

// fail records the reason setup stopped and returns the result for the caller
// to hand straight back.
func (r *ProvisionResult) fail(step, cause string, hints ...string) *ProvisionResult {
	r.step(step, false, cause)
	r.Error = cause
	r.Hints = append(r.Hints, hints...)
	return r
}

// Provision sets a host up for HostMan using a password the user supplies
// once. It authorizes HostMan's key, grants passwordless sudo, and then proves
// the result by reconnecting with the key alone.
//
// Every step is idempotent, so a partial run can simply be repeated.
func (s *Service) Provision(ctx context.Context, h *store.Host, password string) *ProvisionResult {
	res := &ProvisionResult{}
	if strings.TrimSpace(password) == "" {
		return res.fail("Sign in", "a password is required to set the host up")
	}

	client, err := sshx.DialPassword(ctx, sshx.Target{
		Address: h.Address,
		Port:    h.Port,
		User:    h.Username,
		HostKey: h.HostKey,
	}, password)
	if err != nil {
		return res.fail(fmt.Sprintf("Sign in as %s", h.Username), err.Error(), signInHints(h, err)...)
	}
	defer client.Close()

	// Same trust-on-first-use rule as an ordinary connection: whatever the host
	// presented now is what it must present later.
	if h.HostKey == "" && client.HostKey != "" {
		if err := s.db.SetHostKey(ctx, h.ID, client.HostKey); err != nil {
			return res.fail("Trust the host key", err.Error())
		}
		h.HostKey = client.HostKey
	}
	res.step(fmt.Sprintf("Signed in as %s", h.Username), true, "")

	pub := s.identity.Load().AuthorizedKey()
	out, err := client.Run(ctx, fmt.Sprintf(authorizeScript, sshx.Quote(pub)))
	if err != nil {
		return res.fail("Authorize HostMan's key", err.Error())
	}
	if out.ExitCode != 0 {
		return res.fail("Authorize HostMan's key", commandError(out),
			fmt.Sprintf("Check that %s has a home directory that it can write to.", h.Username))
	}
	if strings.Contains(out.Stdout, "already") {
		res.step("HostMan's key was already authorized", true, "")
	} else {
		res.step("Authorized HostMan's key", true, "")
	}

	// Sudo is best-effort: a host without it is still usable for installs that
	// don't need root, so a failure here is a warning, not the end of setup.
	sudoErr := s.grantSudo(ctx, client, h, password)
	if sudoErr == "" {
		res.step("Enabled passwordless sudo", true, "")
	} else {
		res.step("Enable passwordless sudo", false, sudoErr)
		res.Hints = append(res.Hints,
			fmt.Sprintf("Deploys that need root will fail until %s has passwordless sudo. Settings has the command to run by hand.", h.Username))
	}

	// The proof: connect again as HostMan normally would, with the key only.
	probe, err := s.Probe(ctx, h)
	if err != nil {
		return res.fail("Connect with HostMan's key", err.Error(),
			"The key was installed but the host still refused it. Check that sshd allows public key authentication, and that the home directory is not writable by anyone else — sshd ignores authorized_keys when it is.")
	}
	res.step("Connected with HostMan's key", true, "")
	res.OK = true
	res.SudoOK = probe.Facts.SudoOK
	return res
}

// grantSudo installs the NOPASSWD drop-in, returning "" on success or the
// reason it could not. The password goes to `sudo -S` on stdin so it never
// reaches the host's process list.
func (s *Service) grantSudo(ctx context.Context, client *sshx.Client, h *store.Host, password string) string {
	script := fmt.Sprintf(sudoersScript, sshx.Quote(h.Username))
	// Root has nothing to elevate, and a minimal image may not have sudo at all.
	cmd := fmt.Sprintf(`if [ "$(id -u)" = 0 ]; then sh -c %[1]s; else sudo -S -p '' sh -c %[1]s; fi`, sshx.Quote(script))

	out, err := client.RunInput(ctx, cmd, password+"\n")
	if err != nil {
		return err.Error()
	}
	if out.ExitCode == 0 {
		return ""
	}
	detail := commandError(out)
	switch {
	case strings.Contains(detail, "try again"), strings.Contains(detail, "incorrect password"):
		return "sudo did not accept that password"
	case strings.Contains(detail, "not in the sudoers file"), strings.Contains(detail, "not allowed to run sudo"):
		return fmt.Sprintf("%s is not a sudoer on this host", h.Username)
	case strings.Contains(detail, "command not found"):
		return "sudo is not installed on this host"
	case strings.Contains(detail, "must have a tty"):
		return "sudo on this host refuses to run without a terminal"
	}
	return detail
}

// signInHints turns a failed password login into something actionable.
func signInHints(h *store.Host, err error) []string {
	switch {
	case errors.Is(err, sshx.ErrHostKeyChanged):
		return []string{"The host's SSH key changed. If you reinstalled it, remove and re-add the host to trust the new key."}
	case errors.Is(err, sshx.ErrAuthFailed):
		return []string{
			fmt.Sprintf("Check the password for %s on %s.", h.Username, h.Address),
			"If the host has password logins turned off, use the two commands in Settings instead.",
		}
	default:
		return []string{
			fmt.Sprintf("Check the address, port and username, and that %s is powered on and reachable.", h.Address),
		}
	}
}

// commandError picks the most useful line out of a failed remote command,
// preferring stderr and never returning an empty string.
func commandError(res *sshx.Result) string {
	for _, stream := range []string{res.Stderr, res.Stdout} {
		if msg := strings.TrimSpace(stream); msg != "" {
			lines := strings.Split(msg, "\n")
			return strings.TrimSpace(lines[len(lines)-1])
		}
	}
	return fmt.Sprintf("the command failed with exit code %d", res.ExitCode)
}
