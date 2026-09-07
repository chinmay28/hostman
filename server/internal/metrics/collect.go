// Package metrics collects host telemetry over SSH, with no agent installed on
// the host: it reads /proc, /sys and df through a single shell command.
package metrics

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chinmay28/hostman/server/internal/sshx"
	"github.com/chinmay28/hostman/server/internal/store"
)

// probeScript writes marker-delimited sections so one round trip yields
// everything. It reads /proc/stat twice a second apart so CPU usage is a real
// interval average rather than an average since boot, and walks /proc at both
// ends of that same second so every process gets its usage measured over the
// interval too — see processes.go.
const probeScript = `
walk='{
  o = index($0, "(")
  c = 0
  for (i = o + 16; i > o; i--) if (substr($0, i, 1) == ")") { c = i; break }
  if (c == 0) next
  n = split(substr($0, c + 2), f, " ")
  if (n < 22) next
  split(FILENAME, path, "/")
  print path[3], f[12] + f[13], f[22], substr($0, o + 1, c - o - 1)
}'
printf '@@facts\n'
hostname 2>/dev/null || echo unknown
uname -r 2>/dev/null || echo unknown
uname -m 2>/dev/null || echo unknown
(. /etc/os-release 2>/dev/null; echo "${PRETTY_NAME:-${NAME:-unknown}}")
printf '@@sudo\n'
if sudo -n true 2>/dev/null; then echo yes; else echo no; fi
printf '@@machineid\n'
cat /etc/machine-id 2>/dev/null || cat /var/lib/dbus/machine-id 2>/dev/null || true
printf '@@pagesize\n'
getconf PAGESIZE 2>/dev/null || echo 4096
printf '@@cpu1\n'
grep '^cpu ' /proc/stat
printf '@@proc1\n'
awk "$walk" /proc/[0-9]*/stat 2>/dev/null
printf '@@uptime\n'
cat /proc/uptime
printf '@@loadavg\n'
cat /proc/loadavg
printf '@@meminfo\n'
grep -E '^(MemTotal|MemAvailable|MemFree|Buffers|Cached|SwapTotal|SwapFree):' /proc/meminfo
printf '@@df\n'
df -P -k -x tmpfs -x devtmpfs -x squashfs -x overlay 2>/dev/null || df -P -k
printf '@@temp\n'
for z in /sys/class/thermal/thermal_zone*/temp; do
  if [ -r "$z" ]; then cat "$z"; break; fi
done
sleep 1
printf '@@cpu2\n'
grep '^cpu ' /proc/stat
printf '@@proc2\n'
awk "$walk" /proc/[0-9]*/stat 2>/dev/null
`

// Probe is one full read of a host.
type Probe struct {
	Facts  store.HostFacts
	Sample store.Sample
	// Processes is what the machine was busy with during the sample's second.
	// It is nil where the host would not say — an older or stripped-down
	// userland without awk, which costs the caller the list and nothing else.
	Processes *Processes
}

// Collect runs the probe script over an existing connection and parses it.
func Collect(ctx context.Context, c *sshx.Client) (*Probe, error) {
	res, err := c.Run(ctx, probeScript)
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 && strings.TrimSpace(res.Stdout) == "" {
		return nil, fmt.Errorf("probe failed (exit %d): %s", res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return parseProbe(res.Stdout)
}

func parseProbe(out string) (*Probe, error) {
	sections := splitSections(out)
	if len(sections) == 0 {
		return nil, fmt.Errorf("probe returned no recognizable output")
	}

	p := &Probe{}
	p.Sample.TakenAt = time.Now().UTC()
	p.Sample.Disks = []store.Disk{}

	if facts := sections["facts"]; len(facts) >= 4 {
		p.Facts.Hostname = facts[0]
		p.Facts.Kernel = facts[1]
		p.Facts.Arch = facts[2]
		p.Facts.OS = strings.Trim(facts[3], `"`)
	}
	p.Facts.SudoOK = len(sections["sudo"]) > 0 && sections["sudo"][0] == "yes"
	if id := sections["machineid"]; len(id) > 0 {
		p.Facts.MachineID = strings.TrimSpace(id[0])
	}

	// The jiffies the whole machine spent in the interval are the denominator
	// for one process's share of it as much as for the host's own figure.
	var totalDelta int64
	if cpu1, cpu2 := sections["cpu1"], sections["cpu2"]; len(cpu1) > 0 && len(cpu2) > 0 {
		p.Sample.CPUPct = cpuUsage(cpu1[0], cpu2[0])
		t1, _, ok1 := cpuTotals(cpu1[0])
		t2, _, ok2 := cpuTotals(cpu2[0])
		if ok1 && ok2 && t2 > t1 {
			totalDelta = t2 - t1
		}
	}
	if up := sections["uptime"]; len(up) > 0 {
		if f, err := strconv.ParseFloat(field(up[0], 0), 64); err == nil {
			p.Sample.UptimeS = int64(f)
		}
	}
	if la := sections["loadavg"]; len(la) > 0 {
		p.Sample.Load1, _ = strconv.ParseFloat(field(la[0], 0), 64)
	}
	parseMeminfo(sections["meminfo"], &p.Sample)
	p.Sample.Disks = parseDF(sections["df"])
	if t := sections["temp"]; len(t) > 0 {
		if milli, err := strconv.ParseFloat(strings.TrimSpace(t[0]), 64); err == nil && milli > 0 {
			c := milli / 1000
			p.Sample.TempC = &c
		}
	}
	p.Processes = topConsumers(sections["proc1"], sections["proc2"], usage{
		cpuDelta: totalDelta,
		pageSize: pageSize(sections["pagesize"]),
		memTotal: p.Sample.MemTotal,
		takenAt:  p.Sample.TakenAt,
	})
	return p, nil
}

func splitSections(out string) map[string][]string {
	sections := map[string][]string{}
	current := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if name, ok := strings.CutPrefix(line, "@@"); ok {
			current = strings.TrimSpace(name)
			sections[current] = []string{}
			continue
		}
		if current == "" || strings.TrimSpace(line) == "" {
			continue
		}
		sections[current] = append(sections[current], strings.TrimSpace(line))
	}
	return sections
}

// cpuUsage turns two /proc/stat "cpu" lines into a busy percentage for the
// interval between them.
func cpuUsage(before, after string) float64 {
	t1, i1, ok1 := cpuTotals(before)
	t2, i2, ok2 := cpuTotals(after)
	if !ok1 || !ok2 || t2 <= t1 {
		return 0
	}
	busy := (t2 - t1) - (i2 - i1)
	pct := float64(busy) / float64(t2-t1) * 100
	return clamp(pct, 0, 100)
}

// cpuTotals returns total jiffies and idle jiffies (idle + iowait).
func cpuTotals(line string) (total, idle int64, ok bool) {
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	for i, f := range fields[1:] {
		v, err := strconv.ParseInt(f, 10, 64)
		if err != nil {
			continue
		}
		// Fields after softirq (steal, guest...) are counted in total too, but
		// guest time is already included in user, so stop at steal.
		if i < 8 {
			total += v
		}
		if i == 3 || i == 4 { // idle, iowait
			idle += v
		}
	}
	return total, idle, total > 0
}

func parseMeminfo(lines []string, s *store.Sample) {
	vals := map[string]int64{}
	for _, line := range lines {
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		// Values are in kB.
		kb, err := strconv.ParseInt(field(strings.TrimSpace(rest), 0), 10, 64)
		if err != nil {
			continue
		}
		vals[key] = kb * 1024
	}
	s.MemTotal = vals["MemTotal"]
	if avail, ok := vals["MemAvailable"]; ok {
		s.MemUsed = s.MemTotal - avail
	} else {
		s.MemUsed = s.MemTotal - vals["MemFree"] - vals["Buffers"] - vals["Cached"]
	}
	if s.MemUsed < 0 {
		s.MemUsed = 0
	}
	s.SwapTotal = vals["SwapTotal"]
	s.SwapUsed = s.SwapTotal - vals["SwapFree"]
	if s.SwapUsed < 0 {
		s.SwapUsed = 0
	}
}

// pseudoFS are filesystems that are not real storage and only add noise.
var pseudoFS = map[string]bool{
	"tmpfs": true, "devtmpfs": true, "none": true, "overlay": true,
	"squashfs": true, "udev": true, "run": true,
	// Kernel interfaces df happily reports a size for. efivarfs is the one
	// that bites: a few hundred kilobytes of NVRAM that df lists before the
	// root filesystem on a UEFI host, which is not a disk in any sense.
	"efivarfs": true, "sysfs": true, "proc": true, "devpts": true,
	"securityfs": true, "cgroup": true, "cgroup2": true, "pstore": true,
	"bpf": true, "configfs": true, "debugfs": true, "tracefs": true,
	"fusectl": true, "hugetlbfs": true, "mqueue": true, "nsfs": true,
	"ramfs": true, "binfmt_misc": true, "systemd-1": true,
}

// pseudoMounts are the trees a real disk is never mounted under. Filtering by
// mount as well as by device catches the same kernel interfaces under a
// filesystem name df reports differently across distributions.
var pseudoMounts = []string{"/proc", "/sys", "/dev", "/run"}

func isPseudoMount(mount string) bool {
	for _, prefix := range pseudoMounts {
		if mount == prefix || strings.HasPrefix(mount, prefix+"/") {
			return true
		}
	}
	return false
}

func parseDF(lines []string) []store.Disk {
	disks := []store.Disk{}
	seen := map[string]bool{}
	for _, line := range lines {
		f := strings.Fields(line)
		// Filesystem 1024-blocks Used Available Capacity Mounted-on
		if len(f) < 6 || f[0] == "Filesystem" {
			continue
		}
		device := f[0]
		if pseudoFS[device] || strings.HasPrefix(device, "/dev/loop") {
			continue
		}
		mount := strings.Join(f[5:], " ")
		if seen[mount] || isPseudoMount(mount) {
			continue
		}
		total, err1 := strconv.ParseInt(f[1], 10, 64)
		used, err2 := strconv.ParseInt(f[2], 10, 64)
		if err1 != nil || err2 != nil || total <= 0 {
			continue
		}
		seen[mount] = true
		disks = append(disks, store.Disk{
			Mount:      mount,
			Device:     device,
			TotalBytes: total * 1024,
			UsedBytes:  used * 1024,
		})
	}
	sortDisks(disks)
	return disks
}

// sortDisks puts the host's primary filesystem first, because that is the one
// a caller showing a single disk means. df's own order is the mount table's,
// which on some hosts starts somewhere other than the root filesystem. Root
// wins outright; the rest fall in by size, largest first, so a host without a
// reported root still leads with its main store.
func sortDisks(disks []store.Disk) {
	sort.SliceStable(disks, func(i, j int) bool {
		a, b := disks[i], disks[j]
		if (a.Mount == "/") != (b.Mount == "/") {
			return a.Mount == "/"
		}
		if a.TotalBytes != b.TotalBytes {
			return a.TotalBytes > b.TotalBytes
		}
		return a.Mount < b.Mount
	})
}

func field(line string, i int) string {
	f := strings.Fields(line)
	if i >= len(f) {
		return ""
	}
	return f[i]
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
