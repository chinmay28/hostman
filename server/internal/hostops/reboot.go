package hostops

import (
	"context"
	"errors"
	"fmt"

	"github.com/chinmay28/deployer/server/internal/store"
)

// Rebooting is offered; shutting down is not. HostMan can watch a machine come
// back from a restart, but it cannot bring one back from off — that needs
// someone at the plug. A button that can strand the host it manages is worse
// than no button, so the only way down is one the machine comes back up from.

// rebootScript schedules the restart in a detached process and returns
// immediately, so HostMan gets a clean answer instead of the connection dying
// mid-command and having to guess what that meant. The few seconds' delay is
// what buys that: sshd hangs up the remote command when its client goes away,
// so the command that takes the machine down must outlive the session it was
// asked from — the same reason a self-update runs detached.
//
// systemd first, then the SysV commands, so this works on a host without it.
const rebootScript = `set -u
[ "$(id -u)" = 0 ] || { printf 'this needs root\n' >&2; exit 2; }
c='systemctl reboot || shutdown -r now || reboot'
if command -v setsid >/dev/null 2>&1; then
  nohup setsid sh -c "sleep 3; $c" >/dev/null 2>&1 </dev/null &
else
  nohup sh -c "sleep 3; $c" >/dev/null 2>&1 </dev/null &
fi
printf 'scheduled\n'
`

// Reboot restarts a host. It returns once the host has accepted the request,
// which is a few seconds before the machine actually goes down.
func (s *Service) Reboot(ctx context.Context, h *store.Host) error {
	res, err := s.run(ctx, h, elevate(rebootScript), "")
	if err != nil {
		return err
	}
	if res.ExitCode == 2 {
		return errNeedsRoot(h.Username, h.Name)
	}
	if res.ExitCode != 0 {
		return failure(res, "could not restart "+h.Name)
	}
	return nil
}

// ErrNeedsRoot means the operation was refused for want of privileges, rather
// than having failed on its own terms.
var ErrNeedsRoot = errors.New("this needs root on the host")

func errNeedsRoot(user, host string) error {
	return fmt.Errorf("%w: %s needs passwordless sudo on %s to do that", ErrNeedsRoot, user, host)
}
