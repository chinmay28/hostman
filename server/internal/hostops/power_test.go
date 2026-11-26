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
	err := s.Power(context.Background(), &store.Host{Name: "pi"}, PowerAction("halt"))
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
