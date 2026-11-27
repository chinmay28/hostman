package hostops

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/chinmay28/hostman/server/internal/store"
)

// PowerAction is one of the three things HostMan will do to a host's power
// state. Only a reboot is something it can watch the machine come back from: a
// host that was shut down needs someone at the button, and one that is asleep
// needs a key pressed or a wake-on-LAN packet (internal/wol sends those, to the
// address the probe recorded). The API offers all three all the same, and the UI asks before each, because turning off the machine in
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

// armWakeScript switches wake-on-LAN on for the wired card carrying the
// default route — the one whose address the probe recorded — and says whether
// it is on afterwards: "armed" or "unarmed", always exiting 0, because this is
// a courtesy before the machine goes and never a reason for it not to.
//
// It is done at the last moment because the setting does not keep: a driver
// reload or a reboot puts most cards back to their default, which is often
// off. Where the route runs over a bridge the cards enslaved to it are the
// ones that listen. Wi-Fi is left alone; almost no card wakes a machine over
// it. What this cannot see or change is the firmware's own setting.
//
// $1 is the kernel's routing table and $2 its directory of interfaces.
const armWakeScript = `set -u
i=$(awk '$2 == "00000000" { print $1; exit }' "$1" 2>/dev/null)
if [ -z "$i" ] || [ -d "$2/$i/wireless" ] || ! command -v ethtool >/dev/null 2>&1; then
  echo unarmed; exit 0
fi
cards=$i
if [ -d "$2/$i/brif" ]; then cards=$(ls "$2/$i/brif" 2>/dev/null); fi
state=unarmed
for c in $cards; do
  ethtool -s "$c" wol g >/dev/null 2>&1
  if ethtool "$c" 2>/dev/null | grep -q '^[[:space:]]*Wake-on:.*g'; then state=armed; fi
done
echo $state
`

// Where armWakeScript looks on a real host.
const (
	routeFile = "/proc/net/route"
	netSysDir = "/sys/class/net"
)

// ArmWake switches wake-on-LAN on for a host's wired network card and reports
// whether it is on. False with a nil error means the host answered and it is
// not: no wired card, no ethtool, no root, or a card that cannot do it.
func (s *Service) ArmWake(ctx context.Context, h *store.Host) (bool, error) {
	res, err := s.run(ctx, h, elevate(armWakeScript, routeFile, netSysDir), "")
	if err != nil {
		return false, err
	}
	return wakeArmed(res.Stdout), nil
}

// wakeArmed reads armWakeScript's answer.
func wakeArmed(out string) bool { return strings.TrimSpace(out) == "armed" }

// Power reboots a host, shuts it down or puts it to sleep. It returns once the
// host has accepted the request, which is a few seconds before the machine
// actually goes. Asking a machine that cannot sleep to sleep is ErrInvalid;
// lacking the privileges for any of them is ErrNeedsRoot.
//
// A host about to sleep or shut down has wake-on-LAN switched on first, and
// wakeArmed says whether that took — which is not a promise that a wake packet
// will work, only that the card was left listening. Failing to arm never stops
// the action. A reboot arms nothing: it comes back by itself.
func (s *Service) Power(ctx context.Context, h *store.Host, action PowerAction) (wakeArmed bool, err error) {
	if _, ok := ParsePowerAction(string(action)); !ok {
		return false, invalid("%q is not something a host's power can be asked to do", string(action))
	}
	if action != PowerReboot {
		wakeArmed, _ = s.ArmWake(ctx, h)
	}
	res, err := s.run(ctx, h, elevate(powerScript, string(action), PowerStateFile), "")
	if err != nil {
		return false, err
	}
	switch res.ExitCode {
	case 0:
		return wakeArmed, nil
	case powerExitNeedsRoot:
		return false, errNeedsRoot(h.Username, h.Name)
	case powerExitNoSleep:
		return false, invalid("%s cannot sleep: its kernel offers no suspend state", h.Name)
	}
	return false, failure(res, fmt.Sprintf("could not %s %s", action, h.Name))
}

// ErrNeedsRoot means the operation was refused for want of privileges, rather
// than having failed on its own terms.
var ErrNeedsRoot = errors.New("this needs root on the host")

func errNeedsRoot(user, host string) error {
	return fmt.Errorf("%w: %s needs passwordless sudo on %s to do that", ErrNeedsRoot, user, host)
}
