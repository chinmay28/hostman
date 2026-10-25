package hostops

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chinmay28/hostman/server/internal/store"
)

// Virtual machines are VirtualBox's, driven through VBoxManage on the host.
// HostMan lists the machines the SSH user owns and runs the handful of things
// anybody does to one from a phone: start it, shut it down, pause it, save it,
// pull the plug. Making a machine, or deleting one, is a laptop job and stays
// one — the screen manages what is there, and nothing here can create or
// destroy a disk.
//
// Three decisions shape this file.
//
// **VirtualBox is the host's to install.** Like deluge, and unlike everything
// HostMan fetches for itself, a hypervisor is a decision about what a machine
// is for, and it comes with kernel modules the host's own package manager
// should be the one to build. The probe records whether VBoxManage is there,
// and the host's page offers the screen only where it is.
//
// **The machines are the SSH user's.** VirtualBox keeps each user's registry in
// their own home, so the VMs root sees are not the VMs you see, and a screen
// that ran VBoxManage as root would list nothing on a host where somebody has
// been running their machines as themselves all along. So nothing here
// elevates: it asks as the connecting user, and says whose machines it shows.
//
// **Starting is always headless.** There is no display on the other end of an
// SSH session, and a phone could not show one if there were. A machine that
// needs a window is a machine for a laptop.

// vbmResolve finds VBoxManage however the host spells it. Linux installs both
// the cased name and a lowercase link; the first one found is used.
const vbmResolve = `vbm=$(command -v VBoxManage 2>/dev/null || command -v vboxmanage 2>/dev/null)`

// vmStatusScript lists the machines the connecting user owns. One pass over
// `list vms` for the registry, then showvminfo per machine for the state and
// the settings worth a card — kept to a grep so a description that spans lines
// cannot break the sections. The uuid heads each machine's section, so the
// parser pairs it with the registry line that carries the name even where the
// machine is inaccessible and showvminfo has nothing to say about it.
const vmStatusScript = vbmResolve + `
printf '@@have\n'
[ -n "$vbm" ] && printf '%s\n' "$vbm"
printf '@@user\n'
id -un 2>/dev/null
printf '@@version\n'
[ -n "$vbm" ] && "$vbm" --version 2>/dev/null | head -1
printf '@@list\n'
[ -n "$vbm" ] && "$vbm" list vms 2>/dev/null
[ -n "$vbm" ] && "$vbm" list vms 2>/dev/null | sed -n 's/.*{\([0-9a-fA-F-]*\)}[[:space:]]*$/\1/p' | while IFS= read -r id; do
  printf '@@vm %s\n' "$id"
  "$vbm" showvminfo "$id" --machinereadable 2>/dev/null | grep -E '^(name|VMState|VMStateChangeTime|ostype|cpus|memory)='
done
exit 0
`

// vmActionScript runs one verb on one machine. The uuid and the VBoxManage
// verb arrive as positional arguments; which verbs are allowed is decided in Go
// before anything connects, so the case here only says how each is spelled.
const vmActionScript = vbmResolve + `
[ -n "$vbm" ] || { echo 'VirtualBox is not installed on this host' >&2; exit 7; }
case "$2" in
  startvm) "$vbm" startvm "$1" --type headless ;;
  *) "$vbm" controlvm "$1" "$2" ;;
esac
`

// VM is one VirtualBox machine, as the registry and showvminfo describe it.
type VM struct {
	// ID is the uuid VirtualBox knows it by, and what an action names.
	ID   string `json:"id"`
	Name string `json:"name"`
	// State is VirtualBox's own word: running, poweroff, paused, saved,
	// aborted, starting, stopping, saving, restoring — or inaccessible, where
	// the registry names a machine whose files are gone.
	State string `json:"state"`
	// Since is when the state last changed, where VirtualBox said.
	Since *time.Time `json:"since,omitempty"`
	// OSType is the guest VirtualBox was told it runs, e.g. Ubuntu_64.
	OSType string `json:"osType,omitempty"`
	// CPUs and MemoryMB are what the machine was given, not what it is using.
	CPUs     int `json:"cpus,omitempty"`
	MemoryMB int `json:"memoryMb,omitempty"`
}

// VMList is what a host says about its virtual machines.
type VMList struct {
	// Installed is whether VBoxManage is on the host. The probe records the
	// same fact so the host page knows whether to offer the screen; this is
	// the live answer when the screen is open.
	Installed bool `json:"installed"`
	// Version is VirtualBox's own, e.g. 7.0.14r161095.
	Version string `json:"version,omitempty"`
	// User is whose machines these are — the SSH user, always.
	User string `json:"user"`
	VMs  []VM   `json:"vms"`
}

// VMActions are the verbs the screen offers, by the name a request uses, and
// the VBoxManage verb each one becomes. Everything is a controlvm verb except
// starting, which is its own command. Shutting down sends the ACPI power
// button and lets the guest shut itself down; powering off does not ask.
var VMActions = map[string]string{
	"start":    "startvm",
	"stop":     "acpipowerbutton",
	"poweroff": "poweroff",
	"reset":    "reset",
	"pause":    "pause",
	"resume":   "resume",
	"save":     "savestate",
}

// vmActionNames is VMActions' keys in the order a message lists them.
var vmActionNames = []string{"start", "stop", "poweroff", "reset", "pause", "resume", "save"}

// vmIDPattern is a VirtualBox uuid: eight-four-four-four-twelve hex digits.
var vmIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// VMStatus lists the virtual machines the SSH user owns on h.
func (s *Service) VMStatus(ctx context.Context, h *store.Host) (*VMList, error) {
	res, err := s.run(ctx, h, asUser(vmStatusScript), "")
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, failure(res, "could not list virtual machines")
	}
	return parseVMList(res.Stdout), nil
}

// VMAction runs one verb on one machine and answers with the list afterwards,
// because that is what the screen shows next. The verb is checked against
// VMActions and the id against the shape of a uuid before anything connects.
func (s *Service) VMAction(ctx context.Context, h *store.Host, id, action string) (*VMList, error) {
	verb, ok := VMActions[action]
	if !ok {
		return nil, invalid("a virtual machine can be %s", joinNames(vmActionNames))
	}
	if !vmIDPattern.MatchString(id) {
		return nil, invalid("that is not a virtual machine id")
	}
	client, err := s.conn.Connect(ctx, h)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	res, err := client.Run(ctx, asUser(vmActionScript, id, verb))
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, vmFailure(res.Stderr, res.Stdout, action)
	}
	after, err := client.Run(ctx, asUser(vmStatusScript))
	if err != nil {
		return nil, err
	}
	return parseVMList(after.Stdout), nil
}

// parseVMList turns the status script's output into a VMList. The registry
// line carries the name for every machine, including one showvminfo could not
// open; the per-machine section fills in the rest where there is one.
func parseVMList(out string) *VMList {
	got := sections(out)
	list := &VMList{
		Installed: first(got["have"]) != "",
		Version:   first(got["version"]),
		User:      first(got["user"]),
		VMs:       []VM{},
	}
	for _, line := range got["list"] {
		name, id, ok := parseRegistryLine(line)
		if !ok {
			continue
		}
		vm := VM{ID: id, Name: name, State: "inaccessible"}
		if name == "<inaccessible>" {
			vm.Name = id
		}
		for _, kv := range got["vm "+id] {
			key, value, ok := strings.Cut(kv, "=")
			if !ok {
				continue
			}
			value = unquoteVBox(value)
			switch key {
			case "name":
				if value != "" {
					vm.Name = value
				}
			case "VMState":
				vm.State = value
			case "VMStateChangeTime":
				if t, err := time.Parse("2006-01-02T15:04:05.000000000", value); err == nil {
					t = t.UTC()
					vm.Since = &t
				}
			case "ostype":
				vm.OSType = value
			case "cpus":
				vm.CPUs, _ = strconv.Atoi(value)
			case "memory":
				vm.MemoryMB, _ = strconv.Atoi(value)
			}
		}
		list.VMs = append(list.VMs, vm)
	}
	// The registry's order is the order machines were made in, which is nobody's
	// idea of where to look for one.
	sort.SliceStable(list.VMs, func(i, j int) bool {
		return strings.ToLower(list.VMs[i].Name) < strings.ToLower(list.VMs[j].Name)
	})
	return list
}

// parseRegistryLine reads one line of `VBoxManage list vms`:
//
//	"ubuntu server" {6c0a3a1e-5a7f-4c1c-9d3a-1f0b2c3d4e5f}
//
// The name is whatever sits between the first quote and the ` {` before the
// uuid, so a name with quotes in it still comes through whole.
func parseRegistryLine(line string) (name, id string, ok bool) {
	line = strings.TrimSpace(line)
	open := strings.LastIndex(line, "{")
	if open < 0 || !strings.HasSuffix(line, "}") {
		return "", "", false
	}
	id = line[open+1 : len(line)-1]
	if !vmIDPattern.MatchString(id) {
		return "", "", false
	}
	name = strings.TrimSpace(line[:open])
	name = strings.TrimSuffix(strings.TrimPrefix(name, `"`), `"`)
	if name == "" {
		name = id
	}
	return name, id, true
}

// unquoteVBox undoes --machinereadable's quoting: a string value sits in
// double quotes with any quote of its own escaped; a number stands bare.
func unquoteVBox(v string) string {
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		v = v[1 : len(v)-1]
	}
	return strings.ReplaceAll(v, `\"`, `"`)
}

// vmFailure turns what VBoxManage printed into one sentence. Its errors run to
// several lines — the message, then "Details:" with component and interface
// names — and the first is the one a person can act on.
func vmFailure(stderr, stdout, action string) error {
	for _, stream := range []string{stderr, stdout} {
		for _, line := range strings.Split(stream, "\n") {
			line = strings.TrimSpace(line)
			if msg, ok := strings.CutPrefix(line, "VBoxManage: error: "); ok {
				return errors.New(msg)
			}
		}
	}
	if msg := strings.TrimSpace(stderr + "\n" + stdout); msg != "" {
		lines := strings.Split(msg, "\n")
		return errors.New(strings.TrimSpace(lines[len(lines)-1]))
	}
	return errors.New("could not " + action + " the virtual machine")
}

// joinNames renders a list as "a, b, c or d".
func joinNames(names []string) string {
	if len(names) == 0 {
		return ""
	}
	if len(names) == 1 {
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}
