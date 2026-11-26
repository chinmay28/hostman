package hostops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chinmay28/hostman/server/internal/store"
)

// Only the branches that refuse are exercised here. The ones that succeed end
// in `systemctl reboot`, `poweroff` or `suspend`, and the machine running the
// tests is as capable of obeying those as any other.

// notRoot is a directory holding an `id` that answers as an ordinary user.
func notRoot(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "id"), []byte("#!/bin/sh\necho 1000\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// stateFile stands in for /sys/power/state with the given contents.
func stateFile(t *testing.T, states string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(path, []byte(states), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParsePowerAction(t *testing.T) {
	for _, s := range []string{"reboot", "shutdown", "sleep"} {
		if a, ok := ParsePowerAction(s); !ok || string(a) != s {
			t.Errorf("ParsePowerAction(%q) = %q, %v; want it accepted", s, a, ok)
		}
	}
	for _, s := range []string{"", "Reboot", "halt", "hibernate", "poweroff", "sleep; reboot"} {
		if a, ok := ParsePowerAction(s); ok {
			t.Errorf("ParsePowerAction(%q) = %q, accepted; want it refused", s, a)
		}
	}
}

// Without root there is nothing to do but say so, rather than scheduling a
// command that will fail silently three seconds after the reply was sent.
func TestPowerScriptNeedsRoot(t *testing.T) {
	bin := notRoot(t)
	state := stateFile(t, "freeze mem disk\n")
	for _, action := range []PowerAction{PowerReboot, PowerShutdown, PowerSleep} {
		out, code := runScript(t, asUser(powerScript, string(action), state), "", bin)
		if code != powerExitNeedsRoot {
			t.Errorf("%s: exit = %d (%q), want %d", action, code, out, powerExitNeedsRoot)
		}
	}
}

// A machine whose kernel lists no suspend state is told it cannot sleep, and
// that comes before the question of root: root would not change the answer.
func TestPowerScriptRefusesSleepWhereThereIsNone(t *testing.T) {
	bin := notRoot(t)
	states := map[string]string{
		"an empty list":       "\n",
		"hibernate only":      "disk\n",
		"a lookalike":         "memory\n",
		"no such file at all": "",
	}
	for name, content := range states {
		t.Run(name, func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "absent")
			if content != "" {
				state = stateFile(t, content)
			}
			out, code := runScript(t, asUser(powerScript, "sleep", state), "", bin)
			if code != powerExitNoSleep {
				t.Fatalf("exit = %d (%q), want %d", code, out, powerExitNoSleep)
			}
		})
	}
}

// What a machine cannot sleep through does not stop it rebooting or shutting
// down: those get as far as the question of root.
func TestPowerScriptSleepSupportOnlyMattersForSleep(t *testing.T) {
	bin := notRoot(t)
	state := stateFile(t, "\n")
	for _, action := range []PowerAction{PowerReboot, PowerShutdown} {
		if out, code := runScript(t, asUser(powerScript, string(action), state), "", bin); code != powerExitNeedsRoot {
			t.Errorf("%s: exit = %d (%q), want %d", action, code, out, powerExitNeedsRoot)
		}
	}
}

// The script schedules nothing it was not asked for by name.
func TestPowerScriptRefusesAnUnknownAction(t *testing.T) {
	bin := notRoot(t)
	state := stateFile(t, "mem\n")
	for _, action := range []string{"", "halt", "hibernate"} {
		if out, code := runScript(t, asUser(powerScript, action, state), "", bin); code != 64 {
			t.Errorf("%q: exit = %d (%q), want 64", action, code, out)
		}
	}
}

// An action that is not one of the three is refused before anything is
// dialled: the service here has no way to reach a host, and is never asked to.
func TestPowerRefusesAnUnknownActionWithoutConnecting(t *testing.T) {
	var s Service
	_, err := s.Power(context.Background(), &store.Host{Name: "pi"}, PowerAction("halt"))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

// Powering a host off is the power script's job and nobody else's: no other
// script here should be able to take a machine down.
func TestOnlyThePowerScriptTakesAHostDown(t *testing.T) {
	for _, script := range []string{listScript, writeScript, removeScript} {
		for _, word := range []string{"poweroff", "shutdown -h", "halt", "suspend"} {
			if strings.Contains(script, word) {
				t.Errorf("a script mentions %q; only the power script should", word)
			}
		}
	}
}

// fakeNet builds a routing table whose default route runs over iface, and an
// interface directory beside it, the way armWakeScript expects to find them.
func fakeNet(t *testing.T, iface string) (route, sys string) {
	t.Helper()
	dir := t.TempDir()
	route = filepath.Join(dir, "route")
	table := "Iface\tDestination\tGateway\tFlags\n" +
		iface + "\t0001A8C0\t00000000\t0001\n" +
		iface + "\t00000000\t0101A8C0\t0003\n"
	if err := os.WriteFile(route, []byte(table), 0o644); err != nil {
		t.Fatal(err)
	}
	sys = filepath.Join(dir, "net")
	if err := os.MkdirAll(filepath.Join(sys, iface), 0o755); err != nil {
		t.Fatal(err)
	}
	return route, sys
}

// fakeEthtool is an ethtool that remembers, per card, what it was told with
// `-s <card> wol <x>` and reports it back the way the real one does. Cards
// named in deaf ignore the setting, like one whose driver cannot wake.
func fakeEthtool(t *testing.T, deaf ...string) (bin, log string) {
	t.Helper()
	bin = t.TempDir()
	log = filepath.Join(bin, "calls")
	script := `#!/bin/sh
echo "$*" >> ` + log + `
state=` + bin + `/state
if [ "$1" = -s ]; then
  case " ` + strings.Join(deaf, " ") + ` " in *" $2 "*) exit 1 ;; esac
  echo "$4" > "$state.$2"; exit 0
fi
printf 'Settings for %s:\n\tSupports Wake-on: pumbg\n\tWake-on: %s\n' "$1" "$(cat "$state.$1" 2>/dev/null || echo d)"
`
	if err := os.WriteFile(filepath.Join(bin, "ethtool"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func TestArmWakeScriptSwitchesItOnForTheCardWithTheDefaultRoute(t *testing.T) {
	route, sys := fakeNet(t, "enp3s0")
	bin, log := fakeEthtool(t)
	out, code := runScript(t, asUser(armWakeScript, route, sys), "", bin)
	if code != 0 || !wakeArmed(out) {
		t.Fatalf("exit %d, %q; want armed", code, out)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "-s enp3s0 wol g") {
		t.Errorf("ethtool was called with %q, want the card set to wake on a magic packet", calls)
	}
}

// "Supports Wake-on: pumbg" has a g in it too. A card that can but was not
// switched on is not armed.
func TestArmWakeScriptIsNotFooledByWhatACardSupports(t *testing.T) {
	route, sys := fakeNet(t, "enp3s0")
	bin, _ := fakeEthtool(t, "enp3s0")
	out, code := runScript(t, asUser(armWakeScript, route, sys), "", bin)
	if code != 0 || wakeArmed(out) {
		t.Fatalf("exit %d, %q; want unarmed and exit 0", code, out)
	}
}

// Over a bridge it is the cards behind it that listen.
func TestArmWakeScriptArmsTheCardsBehindABridge(t *testing.T) {
	route, sys := fakeNet(t, "br0")
	for _, member := range []string{"enp3s0", "vnet0"} {
		if err := os.MkdirAll(filepath.Join(sys, "br0", "brif", member), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin, log := fakeEthtool(t, "vnet0")
	out, _ := runScript(t, asUser(armWakeScript, route, sys), "", bin)
	if !wakeArmed(out) {
		t.Fatalf("%q; want armed, one card behind the bridge took it", out)
	}
	calls, _ := os.ReadFile(log)
	if strings.Contains(string(calls), "-s br0") {
		t.Errorf("the bridge itself was asked: %q", calls)
	}
}

// Nothing here is a reason to stop a machine going down: every way of not
// arming answers "unarmed" and exits 0.
func TestArmWakeScriptNeverFails(t *testing.T) {
	t.Run("wi-fi", func(t *testing.T) {
		route, sys := fakeNet(t, "wlan0")
		if err := os.MkdirAll(filepath.Join(sys, "wlan0", "wireless"), 0o755); err != nil {
			t.Fatal(err)
		}
		bin, log := fakeEthtool(t)
		out, code := runScript(t, asUser(armWakeScript, route, sys), "", bin)
		if code != 0 || wakeArmed(out) {
			t.Errorf("exit %d, %q; want unarmed and exit 0", code, out)
		}
		if _, err := os.Stat(log); err == nil {
			t.Error("ethtool was run against a Wi-Fi card")
		}
	})
	t.Run("no default route", func(t *testing.T) {
		_, sys := fakeNet(t, "enp3s0")
		bin, _ := fakeEthtool(t)
		empty := stateFile(t, "Iface\tDestination\n")
		out, code := runScript(t, asUser(armWakeScript, empty, sys), "", bin)
		if code != 0 || wakeArmed(out) {
			t.Errorf("exit %d, %q; want unarmed and exit 0", code, out)
		}
	})
	t.Run("no routing table", func(t *testing.T) {
		_, sys := fakeNet(t, "enp3s0")
		bin, _ := fakeEthtool(t)
		out, code := runScript(t, asUser(armWakeScript, filepath.Join(sys, "absent"), sys), "", bin)
		if code != 0 || wakeArmed(out) {
			t.Errorf("exit %d, %q; want unarmed and exit 0", code, out)
		}
	})
}
