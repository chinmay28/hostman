package metrics

import (
	"math"
	"strings"
	"testing"
)

// sampleOutput mirrors what probeScript prints on a Raspberry Pi.
const sampleOutput = `@@facts
nakedpi
6.6.31+rpt-rpi-v8
aarch64
Debian GNU/Linux 12 (bookworm)
@@sudo
yes
@@tools
virtualbox
@@pagesize
4096
@@cpu1
cpu  1000 20 300 5000 100 0 10 0 0 0
@@proc1
1 400 2000 systemd
219 1000 60000 python3
@@uptime
104523.12 830192.44
@@loadavg
0.52 0.61 0.58 1/210 4821
@@meminfo
MemTotal:        8000000 kB
MemAvailable:    6000000 kB
MemFree:         5000000 kB
SwapTotal:        200000 kB
SwapFree:         150000 kB
@@df
Filesystem     1024-blocks     Used Available Capacity Mounted on
/dev/mmcblk0p2    60000000 20000000  37000000      36% /
/dev/mmcblk0p1      520000    60000    460000      12% /boot/firmware
tmpfs               400000        0    400000       0% /dev/shm
/dev/loop0           50000    50000         0     100% /snap/core
@@temp
48123
@@cpu2
cpu  1100 20 340 5400 120 0 12 0 0 0
@@proc2
1 402 2000 systemd
219 1071 61000 python3
`

func TestParseProbe(t *testing.T) {
	p, err := parseProbe(sampleOutput)
	if err != nil {
		t.Fatalf("parseProbe: %v", err)
	}

	if p.Facts.Hostname != "nakedpi" {
		t.Errorf("hostname = %q, want nakedpi", p.Facts.Hostname)
	}
	if p.Facts.OS != "Debian GNU/Linux 12 (bookworm)" {
		t.Errorf("os = %q", p.Facts.OS)
	}
	if p.Facts.Arch != "aarch64" {
		t.Errorf("arch = %q, want aarch64", p.Facts.Arch)
	}
	if !p.Facts.SudoOK {
		t.Error("sudoOk = false, want true")
	}
	if !p.Facts.VirtualBox {
		t.Error("virtualbox = false, want true: the tools section named it")
	}

	// busy delta 142 of 562 total jiffies.
	if want := 142.0 / 562.0 * 100; !closeTo(p.Sample.CPUPct, want, 0.01) {
		t.Errorf("cpuPct = %.3f, want %.3f", p.Sample.CPUPct, want)
	}
	if want := int64(2000000 * 1024); p.Sample.MemUsed != want {
		t.Errorf("memUsed = %d, want %d", p.Sample.MemUsed, want)
	}
	if want := int64(8000000 * 1024); p.Sample.MemTotal != want {
		t.Errorf("memTotal = %d, want %d", p.Sample.MemTotal, want)
	}
	if want := int64(50000 * 1024); p.Sample.SwapUsed != want {
		t.Errorf("swapUsed = %d, want %d", p.Sample.SwapUsed, want)
	}
	if p.Sample.Load1 != 0.52 {
		t.Errorf("load1 = %v, want 0.52", p.Sample.Load1)
	}
	if p.Sample.UptimeS != 104523 {
		t.Errorf("uptimeS = %d, want 104523", p.Sample.UptimeS)
	}
	if p.Sample.TempC == nil || !closeTo(*p.Sample.TempC, 48.123, 0.001) {
		t.Errorf("tempC = %v, want 48.123", p.Sample.TempC)
	}

	// tmpfs and loop devices are noise and must not appear.
	if len(p.Sample.Disks) != 2 {
		t.Fatalf("disks = %+v, want 2 real filesystems", p.Sample.Disks)
	}
	root := p.Sample.Disks[0]
	if root.Mount != "/" || root.Device != "/dev/mmcblk0p2" {
		t.Errorf("first disk = %+v, want / on /dev/mmcblk0p2", root)
	}
	if want := int64(20000000 * 1024); root.UsedBytes != want {
		t.Errorf("root usedBytes = %d, want %d", root.UsedBytes, want)
	}
	if p.Sample.Disks[1].Mount != "/boot/firmware" {
		t.Errorf("second disk = %+v, want /boot/firmware", p.Sample.Disks[1])
	}

	// The two /proc walks bracket the same second as the two /proc/stat reads,
	// so a process's share is measured against the machine's own 562 jiffies:
	// python3 gained 71 of them.
	if p.Processes == nil {
		t.Fatal("processes = nil, want the top consumers of the same interval")
	}
	if len(p.Processes.TopCPU) != 2 || p.Processes.TopCPU[0].Name != "python3" {
		t.Fatalf("top cpu = %+v, want python3 first", p.Processes.TopCPU)
	}
	if want := 71.0 / 562.0 * 100; !closeTo(p.Processes.TopCPU[0].CPUPct, want, 0.01) {
		t.Errorf("python3 cpuPct = %.3f, want %.3f", p.Processes.TopCPU[0].CPUPct, want)
	}
	if want := int64(61000 * 4096); p.Processes.TopMem[0].MemBytes != want {
		t.Errorf("top memory = %+v, want python3 at %d bytes", p.Processes.TopMem[0], want)
	}
}

func TestParseProbeMissingSections(t *testing.T) {
	// A minimal host: no thermal zone, no swap, sudo denied.
	out := `@@facts
box
5.15.0
x86_64
Ubuntu 22.04.4 LTS
@@sudo
no
@@cpu1
cpu  10 0 5 100 0 0 0 0 0 0
@@uptime
50.0 100.0
@@loadavg
0.00 0.01 0.05 1/100 200
@@meminfo
MemTotal:        1000000 kB
MemFree:          400000 kB
Buffers:           50000 kB
Cached:           150000 kB
@@df
Filesystem     1024-blocks     Used Available Capacity Mounted on
/dev/sda1         10000000  4000000   6000000      40% /
@@temp
@@cpu2
cpu  10 0 5 100 0 0 0 0 0 0
`
	p, err := parseProbe(out)
	if err != nil {
		t.Fatalf("parseProbe: %v", err)
	}
	if p.Facts.SudoOK {
		t.Error("sudoOk = true, want false")
	}
	if p.Facts.VirtualBox {
		t.Error("virtualbox = true on a host whose probe had no tools section")
	}
	if p.Sample.TempC != nil {
		t.Errorf("tempC = %v, want nil when no thermal zone", *p.Sample.TempC)
	}
	// No MemAvailable: fall back to free + buffers + cached.
	if want := int64(400000 * 1024); p.Sample.MemUsed != want {
		t.Errorf("memUsed = %d, want %d", p.Sample.MemUsed, want)
	}
	if p.Sample.SwapTotal != 0 || p.Sample.SwapUsed != 0 {
		t.Errorf("swap = %d/%d, want 0/0", p.Sample.SwapUsed, p.Sample.SwapTotal)
	}
	// Identical /proc/stat reads mean no elapsed jiffies, not 100% busy.
	if p.Sample.CPUPct != 0 {
		t.Errorf("cpuPct = %v, want 0 for a zero-length interval", p.Sample.CPUPct)
	}
	// A host that did not walk /proc leaves the question unanswered.
	if p.Processes != nil {
		t.Errorf("processes = %+v, want nil where the host listed none", p.Processes)
	}
}

func TestParseProbeGarbage(t *testing.T) {
	if _, err := parseProbe("bash: /proc/stat: Permission denied\n"); err == nil {
		t.Error("expected an error when no sections are present")
	}
}

func TestParseDFPrimaryFirst(t *testing.T) {
	// A UEFI host lists efivarfs — a few hundred kilobytes of NVRAM — ahead of
	// the root filesystem, and df keeps the mount table's order. The caller
	// showing one disk means the root one, so it has to come out first.
	lines := []string{
		"Filesystem     1024-blocks     Used Available Capacity Mounted on",
		"efivarfs               512      271       241      54% /sys/firmware/efi/efivars",
		"/dev/vda2         41000000 12000000  27000000      31% /",
		"/dev/vda1           500000   100000    400000      20% /boot",
		"/dev/vdb1        200000000 10000000 190000000       5% /mnt/data",
	}
	disks := parseDF(lines)
	if len(disks) != 3 {
		t.Fatalf("disks = %+v, want the 3 real filesystems", disks)
	}
	if disks[0].Mount != "/" || disks[0].Device != "/dev/vda2" {
		t.Errorf("primary disk = %+v, want / on /dev/vda2", disks[0])
	}
	// Behind root, the biggest store leads.
	if disks[1].Mount != "/mnt/data" || disks[2].Mount != "/boot" {
		t.Errorf("disks after root = %+v, want /mnt/data then /boot", disks[1:])
	}
	for _, d := range disks {
		if strings.HasPrefix(d.Mount, "/sys") {
			t.Errorf("kernel interface %+v reported as storage", d)
		}
	}
}

func TestParseDFNoRoot(t *testing.T) {
	// Without a root line the largest real filesystem stands in, rather than
	// whichever one df happened to print first.
	lines := []string{
		"Filesystem     1024-blocks     Used Available Capacity Mounted on",
		"/dev/sda1           500000   100000    400000      20% /boot",
		"/dev/sdb1        200000000 10000000 190000000       5% /srv",
	}
	disks := parseDF(lines)
	if len(disks) != 2 || disks[0].Mount != "/srv" {
		t.Errorf("disks = %+v, want /srv first", disks)
	}
}

func closeTo(got, want, tol float64) bool { return math.Abs(got-want) <= tol }
