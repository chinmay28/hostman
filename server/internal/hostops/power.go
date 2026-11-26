package hostops

import (
	"context"
	"errors"
	"fmt"

	"github.com/chinmay28/hostman/server/internal/store"
)

// PowerAction is one of the three things HostMan will do to a host's power
// state. Only a reboot is something it can watch the machine come back from: a
// host that was shut down needs someone at the button, and one that is asleep
// needs a key pressed or a wake-on-LAN packet. The API offers all three all
// the same, and the UI asks before each, because turning off the machine in
// the cupboard from the sofa is the point of having it there.
type PowerAction string

const (
	PowerReboot   PowerAction = "reboot"
	PowerShutdown PowerAction = "shutdown"
	PowerSleep    PowerAction = "sleep"
)

// ParsePowerAction reads an action as the API spells it. Anything else is not
// an action, and must never reach a host.
func ParsePowerAction(s string) (PowerAction, bool) {
	switch a := PowerAction(s); a {
	case PowerReboot, PowerShutdown, PowerSleep:
		return a, true
	}
	return "", false
}

// PowerStateFile is where the kernel lists the sleep states it can enter.
// "mem" in it is suspend-to-RAM (or suspend-to-idle, where that is what the
// firmware offers), which is what an x86 PC has and a Raspberry Pi does not.
// The probe in internal/metrics reads the same file to tell the UI whether to
// offer sleep at all; the check here is the one that counts.
const PowerStateFile = "/sys/power/state"

// Exit statuses of powerScript that mean something other than "it failed".
const (
	powerExitNeedsRoot = 2
	powerExitNoSleep   = 3
)

// powerScript schedules the action in a detached process and returns
// immediately, so HostMan gets a clean answer instead of the connection dying
// mid-command and having to guess what that meant. The few seconds' delay is
// what buys that: sshd hangs up the remote command when its client goes away,
// so the command that takes the machine down must outlive the session it was
// asked from — the same reason a self-update runs detached.
//
// $1 is the action and $2 the kernel's list of sleep states. A machine that
// cannot sleep is told so before anything is scheduled, and before root is
// asked for, since no amount of root would change the answer.
//
// systemd first, then the older commands, so this works on a host without it.
const powerScript = `set -u
case "${1:-}" in
  reboot) c='systemctl reboot || shutdown -r now || reboot' ;;
  shutdown) c='systemctl poweroff || shutdown -h now || poweroff' ;;
  sleep)
    case " $(cat "$2" 2>/dev/null) " in
      *" mem "*) ;;
      *) printf 'this machine has no sleep state to enter\n' >&2; exit 3 ;;
    esac
    c="systemctl suspend || pm-suspend || echo mem > $2" ;;
  *) printf 'unknown power action\n' >&2; exit 64 ;;
esac
[ "$(id -u)" = 0 ] || { printf 'this needs root\n' >&2; exit 2; }
if command -v setsid >/dev/null 2>&1; then
  nohup setsid sh -c "sleep 3; $c" >/dev/null 2>&1 </dev/null &
else
  nohup sh -c "sleep 3; $c" >/dev/null 2>&1 </dev/null &
fi
printf 'scheduled\n'
`

// Power reboots a host, shuts it down or puts it to sleep. It returns once the
// host has accepted the request, which is a few seconds before the machine
// actually goes. Asking a machine that cannot sleep to sleep is ErrInvalid;
// lacking the privileges for any of them is ErrNeedsRoot.
func (s *Service) Power(ctx context.Context, h *store.Host, action PowerAction) error {
	if _, ok := ParsePowerAction(string(action)); !ok {
		return invalid("%q is not something a host's power can be asked to do", string(action))
	}
	res, err := s.run(ctx, h, elevate(powerScript, string(action), PowerStateFile), "")
	if err != nil {
		return err
	}
	switch res.ExitCode {
	case 0:
		return nil
	case powerExitNeedsRoot:
		return errNeedsRoot(h.Username, h.Name)
	case powerExitNoSleep:
		return invalid("%s cannot sleep: its kernel offers no suspend state", h.Name)
	}
	return failure(res, fmt.Sprintf("could not %s %s", action, h.Name))
}

// ErrNeedsRoot means the operation was refused for want of privileges, rather
// than having failed on its own terms.
var ErrNeedsRoot = errors.New("this needs root on the host")

func errNeedsRoot(user, host string) error {
	return fmt.Errorf("%w: %s needs passwordless sudo on %s to do that", ErrNeedsRoot, user, host)
}
