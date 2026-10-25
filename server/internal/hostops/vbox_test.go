package hostops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/hostman/server/internal/sshx"
	"github.com/chinmay28/hostman/server/internal/store"
)

// The uuids three machines on a pretend host go by.
const (
	vmUbuntu = "6c0a3a1e-5a7f-4c1c-9d3a-1f0b2c3d4e5f"
	vmWin    = "0b6d2d2e-0f0a-4a5a-9b1b-2c3d4e5f6a7b"
	vmGone   = "00000000-0000-4000-8000-000000000002"
)

// vboxStub is a VBoxManage that answers the way the real one does for the
// commands the scripts use, and writes every call to $VBOX_LOG so a test can
// see what it was asked.
const vboxStub = `
[ -n "$VBOX_LOG" ] && printf '%s\n' "$*" >> "$VBOX_LOG"
case "$1" in
  --version) echo "7.0.14r161095" ;;
  list)
    printf '"ubuntu server" {6c0a3a1e-5a7f-4c1c-9d3a-1f0b2c3d4e5f}\n'
    printf '"<inaccessible>" {00000000-0000-4000-8000-000000000002}\n'
    printf "\"chinmay's box\" {0b6d2d2e-0f0a-4a5a-9b1b-2c3d4e5f6a7b}\n"
    ;;
  showvminfo)
    case "$2" in
      6c0a*)
        printf 'name="ubuntu server"\nostype="Ubuntu_64"\nmemory=2048\ncpus=2\n'
        printf 'description="first line\nsecond line"\n'
        printf 'VMState="running"\nVMStateChangeTime="2026-10-25T09:30:00.123456000"\n'
        ;;
      0b6d*)
        printf 'name="chinmay'"'"'s box"\nostype="Windows11_64"\nmemory=8192\ncpus=4\n'
        printf 'VMState="poweroff"\nVMStateChangeTime="2026-10-20T08:00:00.000000000"\n'
        ;;
      *) echo "VBoxManage: error: Could not find a registered machine named '$2'" >&2; exit 1 ;;
    esac
    ;;
  startvm) echo "Waiting for VM \"$2\" to power on..."; echo "VM \"$2\" has been successfully started." ;;
  controlvm)
    if [ "$3" = pause ]; then
      echo "VBoxManage: error: Machine '$2' is not currently running" >&2
      echo "VBoxManage: error: Details: code VBOX_E_INVALID_VM_STATE (0x80bb0002), component MachineWrap, interface IMachine, callee nsISupports" >&2
      exit 1
    fi
    ;;
  *) echo "VBoxManage: error: unknown command $1" >&2; exit 1 ;;
esac
`

// vboxBin is a PATH entry holding the stub, logging to a file the test reads.
func vboxBin(t *testing.T) (bin, log string) {
	t.Helper()
	log = filepath.Join(t.TempDir(), "calls")
	t.Setenv("VBOX_LOG", log)
	return stubBin(t, map[string]string{"VBoxManage": vboxStub}), log
}

func calls(t *testing.T, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// A generated script has to be a script before it can be a correct one.
func TestVMScriptsParse(t *testing.T) {
	for name, script := range map[string]string{"status": vmStatusScript, "action": vmActionScript} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
				t.Fatal(err)
			}
			if out, code := runScript(t, "sh -n "+path, ""); code != 0 {
				t.Fatalf("sh -n %s exited %d: %s", name, code, out)
			}
		})
	}
}

// The status script asks the registry once and each machine once, and the
// parser puts the two together — including for a machine the registry still
// names but VirtualBox can no longer open.
func TestVMStatusScriptListsTheUsersMachines(t *testing.T) {
	bin, log := vboxBin(t)
	out, code := runScript(t, asUser(vmStatusScript), "", bin)
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, out)
	}
	list := parseVMList(out)

	if !list.Installed {
		t.Error("installed = false with VBoxManage on the PATH")
	}
	if list.Version != "7.0.14r161095" {
		t.Errorf("version = %q", list.Version)
	}
	if list.User == "" {
		t.Error("the list should say whose machines these are")
	}
	if len(list.VMs) != 3 {
		t.Fatalf("got %d machines, want 3: %+v", len(list.VMs), list.VMs)
	}

	// Sorted by name, case aside, rather than by the registry's own order.
	// The inaccessible one has only its uuid for a name, which sorts first.
	gone, box, ubuntu := list.VMs[0], list.VMs[1], list.VMs[2]
	if gone.ID != vmGone || gone.Name != vmGone || gone.State != "inaccessible" {
		t.Errorf("inaccessible machine = %+v", gone)
	}
	if box.Name != "chinmay's box" || box.State != "poweroff" || box.CPUs != 4 || box.MemoryMB != 8192 {
		t.Errorf("second machine = %+v", box)
	}
	if ubuntu.ID != vmUbuntu || ubuntu.Name != "ubuntu server" || ubuntu.State != "running" {
		t.Errorf("ubuntu = %+v", ubuntu)
	}
	if ubuntu.OSType != "Ubuntu_64" || ubuntu.CPUs != 2 || ubuntu.MemoryMB != 2048 {
		t.Errorf("ubuntu's settings = %+v", ubuntu)
	}
	want := time.Date(2026, 10, 25, 9, 30, 0, 123456000, time.UTC)
	if ubuntu.Since == nil || !ubuntu.Since.Equal(want) {
		t.Errorf("ubuntu since = %v, want %v", ubuntu.Since, want)
	}

	// The registry was read, and each machine asked about, as the connecting
	// user: nothing here goes through sudo.
	for _, call := range calls(t, log) {
		if strings.HasPrefix(call, "showvminfo") && !strings.Contains(call, "--machinereadable") {
			t.Errorf("showvminfo without --machinereadable: %q", call)
		}
	}
	if strings.Contains(asUser(vmStatusScript), "sudo") {
		t.Error("the machines are the SSH user's; the script must not elevate")
	}
}

// A host without VirtualBox is a PATH without it, and the status says so with
// a clean exit rather than a failure — not having a hypervisor is a fact about
// the host, not something that went wrong.
func TestVMStatusScriptOnAHostWithoutVirtualBox(t *testing.T) {
	bin := bareBin(t, map[string]string{"id": "echo pi"})
	out, code := runBare(t, asUser(vmStatusScript), bin)
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, out)
	}
	list := parseVMList(out)
	if list.Installed {
		t.Error("installed = true on a PATH with no VBoxManage")
	}
	if list.User != "pi" {
		t.Errorf("user = %q, want pi", list.User)
	}
	if len(list.VMs) != 0 {
		t.Errorf("machines = %+v, want none", list.VMs)
	}
}

// Starting is its own VBoxManage command and is always headless; every other
// verb is controlvm. What VBoxManage says when it refuses comes back as the one
// line a person can act on, not the "Details:" that follow it.
func TestVMActionScriptStartsHeadlessAndControlsTheRest(t *testing.T) {
	bin, log := vboxBin(t)

	if out, code := runScript(t, asUser(vmActionScript, vmUbuntu, "startvm"), "", bin); code != 0 {
		t.Fatalf("start exited %d: %s", code, out)
	}
	if out, code := runScript(t, asUser(vmActionScript, vmUbuntu, "acpipowerbutton"), "", bin); code != 0 {
		t.Fatalf("stop exited %d: %s", code, out)
	}
	got := calls(t, log)
	want := []string{
		"startvm " + vmUbuntu + " --type headless",
		"controlvm " + vmUbuntu + " acpipowerbutton",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("VBoxManage was asked %q, want %q", got, want)
	}

	// The stub refuses to pause: the machine is not running.
	path := filepath.Join(t.TempDir(), "err")
	cmd := asUser(vmActionScript, vmWin, "pause") + " 2>" + sshx.Quote(path)
	out, code := runScript(t, cmd, "", bin)
	if code == 0 {
		t.Fatal("pausing a stopped machine should fail")
	}
	stderr, _ := os.ReadFile(path)
	err := vmFailure(string(stderr), out, "pause")
	if want := "Machine '" + vmWin + "' is not currently running"; err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}

func TestVMActionScriptRefusesAHostWithoutVirtualBox(t *testing.T) {
	bin := bareBin(t, nil)
	if out, code := runBare(t, asUser(vmActionScript, vmUbuntu, "startvm"), bin); code != 7 {
		t.Fatalf("action exited %d, want 7: %s", code, out)
	}
}

// refuseConnector fails the test if anything tries to reach a host: these
// requests are meant to be refused before a connection is opened.
type refuseConnector struct{ t *testing.T }

func (c refuseConnector) Connect(context.Context, *store.Host) (*sshx.Client, error) {
	c.t.Fatal("connected to the host for a request that should have been refused first")
	return nil, nil
}

func TestVMActionRefusesBeforeConnecting(t *testing.T) {
	svc := NewService(refuseConnector{t})
	h := &store.Host{Name: "pi"}
	cases := []struct{ name, id, action, wantMsg string }{
		{"a verb nothing offers", vmUbuntu, "destroy", "start, stop, poweroff, reset, pause, resume or save"},
		{"the VBoxManage verb rather than ours", vmUbuntu, "acpipowerbutton", "can be"},
		{"no verb", vmUbuntu, "", "can be"},
		{"a name instead of an id", "ubuntu server", "start", "not a virtual machine id"},
		{"an id carrying a command", vmUbuntu + "; reboot", "start", "not a virtual machine id"},
		{"no id", "", "start", "not a virtual machine id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.VMAction(context.Background(), h, tc.id, tc.action)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want an invalid request", err)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("err = %q, want it to say %q", err, tc.wantMsg)
			}
		})
	}
	// Every action the screen offers maps to a verb, and only those.
	if len(VMActions) != len(vmActionNames) {
		t.Errorf("VMActions has %d verbs but the message lists %d", len(VMActions), len(vmActionNames))
	}
	for _, name := range vmActionNames {
		if _, ok := VMActions[name]; !ok {
			t.Errorf("the message offers %q, which is not an action", name)
		}
	}
}

func TestParseRegistryLine(t *testing.T) {
	cases := []struct {
		line       string
		name, id   string
		wantParsed bool
	}{
		{`"ubuntu server" {` + vmUbuntu + `}`, "ubuntu server", vmUbuntu, true},
		{`"<inaccessible>" {` + vmGone + `}`, "<inaccessible>", vmGone, true},
		{`"with {braces} inside" {` + vmWin + `}`, "with {braces} inside", vmWin, true},
		{`  "trailing space" {` + vmWin + `}  `, "trailing space", vmWin, true},
		{`"" {` + vmWin + `}`, vmWin, vmWin, true},
		{`"no uuid" {not-a-uuid}`, "", "", false},
		{`Oracle VM VirtualBox Command Line Management Interface Version 7.0.14`, "", "", false},
		{``, "", "", false},
	}
	for _, tc := range cases {
		name, id, ok := parseRegistryLine(tc.line)
		if ok != tc.wantParsed || name != tc.name || id != tc.id {
			t.Errorf("parseRegistryLine(%q) = %q, %q, %v; want %q, %q, %v",
				tc.line, name, id, ok, tc.name, tc.id, tc.wantParsed)
		}
	}
}

// A machine's name may carry a quote, which --machinereadable escapes.
func TestParseVMListUnquotesNames(t *testing.T) {
	out := "@@have\n/usr/bin/VBoxManage\n@@user\npi\n@@version\n7.0.14r161095\n@@list\n" +
		`"say \"hi\"" {` + vmWin + "}\n@@vm " + vmWin + "\n" +
		`name="say \"hi\""` + "\nVMState=\"saved\"\nmemory=512\ncpus=1\n"
	list := parseVMList(out)
	if len(list.VMs) != 1 {
		t.Fatalf("machines = %+v, want one", list.VMs)
	}
	if vm := list.VMs[0]; vm.Name != `say "hi"` || vm.State != "saved" || vm.MemoryMB != 512 || vm.CPUs != 1 {
		t.Errorf("machine = %+v", vm)
	}
	if list.VMs[0].Since != nil {
		t.Error("a machine whose state time was not said should not have one")
	}
}

func TestVMFailureFallsBackToTheLastLine(t *testing.T) {
	if got := vmFailure("", "something odd\nhappened here\n", "start").Error(); got != "happened here" {
		t.Errorf("fallback = %q, want the last line", got)
	}
	if got := vmFailure("", "", "start").Error(); got != "could not start the virtual machine" {
		t.Errorf("empty output = %q", got)
	}
}
