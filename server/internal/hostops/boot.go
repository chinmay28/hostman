package hostops

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/chinmay28/deployer/server/internal/store"
)

// Why did it restart?
//
// A machine that reboots on its own is the one question a phone is worst at
// answering, because the answer is never in one place. It is spread across
// wtmp, which remembers when the machine came up and whether anything asked it
// to go down; the previous boot's log, which holds the panic or the out-of-
// memory kill if there was one; and, on a Raspberry Pi, a firmware register
// that is the only record of the power supply sagging. Each of those is
// readable in one command, and none of them is worth typing three commands on a
// phone to get to.
//
// So HostMan reads all of them in a single SSH round trip and says what it
// thinks, with the evidence underneath. The verdict is a guess and is labelled
// as one — see bootcause.go, which is where the guessing happens. This file is
// only concerned with getting the evidence back intact.
//
// Two things shape the collection. The first is that the record may simply not
// exist: Debian, and so Raspberry Pi OS, leaves systemd's journal in memory
// unless /var/log/journal is there to write to, which means the log of the boot
// that crashed dies with the boot that crashed. That is the single most common
// reason this question cannot be answered, so it is detected, reported in those
// words, and offered a fix rather than being left as an empty screen. The
// second is that where the journal is volatile there may still be an rsyslog
// file, and the lines just before the last kernel banner in it are exactly the
// evidence the journal would have had.

const (
	// MaxBootLogBytes bounds each chunk of log carried back. The end of the
	// previous boot is what matters, so an over-long chunk keeps its end.
	MaxBootLogBytes = 64 << 10
	// bootTailLines is how much of the end of the previous boot to read at any
	// priority. The shutdown sequence, or the abrupt lack of one, is here.
	bootTailLines = 80
	// bootWarnLines is how far back to read that boot's complaints. Filtering
	// to warning and worse is what makes reading hours of log affordable: a
	// panic, an out-of-memory kill, a critical temperature and the Pi's
	// under-voltage warning are all logged above that line.
	bootWarnLines = 500
	// bootFileLines is how much of an rsyslog file to take from before the last
	// kernel banner, when the journal has nothing.
	bootFileLines = 300
	// maxRestarts bounds the restart history read out of wtmp.
	maxRestarts = 40
)

// Cause is what HostMan thinks took the machine down.
type Cause string

const (
	// CauseClean is a restart something asked for.
	CauseClean Cause = "clean"
	// CausePanic is the kernel stopping on purpose because it could not carry
	// on: a panic, an oops, a fatal exception.
	CausePanic Cause = "panic"
	// CauseLockup is the kernel alive but stuck — a soft lockup or an RCU
	// stall — which is what a watchdog exists to reset.
	CauseLockup Cause = "lockup"
	// CauseOOM is the out-of-memory killer.
	CauseOOM Cause = "oom"
	// CauseOverheat is a thermal shutdown.
	CauseOverheat Cause = "overheat"
	// CauseUndervoltage is the supply dipping below what the board needs, which
	// on a Raspberry Pi is the usual answer to "it restarts at random".
	CauseUndervoltage Cause = "undervoltage"
	// CausePower is losing power outright, which leaves no evidence of its own
	// and is inferred from the absence of everything else.
	CausePower Cause = "power"
	// CauseStorage is the disk or the SD card failing under the machine.
	CauseStorage Cause = "storage"
	// CauseUnknown is an honest answer, and a common one.
	CauseUnknown Cause = "unknown"
)

// Confidence levels. "certain" is only used where the machine said so in as
// many words; everything inferred is at best "likely".
const (
	ConfidenceCertain = "certain"
	ConfidenceLikely  = "likely"
	ConfidenceUnclear = "unclear"
)

// Where the evidence came from.
const (
	SourceJournal = "journal"
	SourceLogFile = "logfile"
	SourceNone    = "none"
)

// Sign is one thing found in the record that bears on why the machine went
// down. It keeps the line it was found in, because a verdict nobody can check
// is not worth much on a screen this small.
type Sign struct {
	// Kind groups signs that mean the same thing; see bootcause.go.
	Kind string `json:"kind"`
	// Label is what to call it on screen.
	Label string `json:"label"`
	// Line is the last log line that matched, trimmed to something a phone can
	// hold.
	Line string `json:"line"`
	// Detail is the part of the line worth pulling out — the panic's reason,
	// the process the out-of-memory killer chose.
	Detail string `json:"detail,omitempty"`
	// BeforeS is how many seconds before the machine came back this was logged.
	// 0 where the line carried no time HostMan could read.
	BeforeS int64 `json:"beforeS,omitempty"`
	// Near marks a sign close enough to the restart to be part of it, rather
	// than something that also happened during that boot. It is the difference
	// between the cause and the weather.
	Near bool `json:"near"`
	// Count is how many lines matched; Line is the last of them.
	Count int `json:"count"`
}

// Restart is one time the machine came up, as wtmp remembers it.
type Restart struct {
	BootedAt time.Time `json:"bootedAt"`
	// UpS is how long that boot lasted, or how long the current one has been
	// running. 0 where the times could not be read.
	UpS int64 `json:"upS,omitempty"`
	// Clean is true where a shutdown was recorded before this boot — something
	// asked the machine to go. False means nothing did, which is what a crash
	// and a power cut both leave behind.
	Clean bool `json:"clean"`
	// Kernel is what it booted, which is how an update shows up as a restart.
	Kernel string `json:"kernel,omitempty"`
	// Current marks the boot that is still running.
	Current bool `json:"current,omitempty"`
	// Timed is false where the record was found but its time was not, which
	// happens on a `last` too old to print ISO timestamps. The restart is still
	// worth counting; it just cannot be placed.
	Timed bool `json:"timed"`
}

// Throttle is the Raspberry Pi firmware's own account of its power and heat,
// from `vcgencmd get_throttled`. It describes the boot that is running now, not
// the one that ended — which makes it corroboration rather than proof, and the
// wording built from it says so.
type Throttle struct {
	// Raw is the word from the firmware, e.g. "0x50005".
	Raw string `json:"raw"`

	// The "now" flags: what is true this instant.
	//
	// Which of these means heat and which means power is not obvious from the
	// names the firmware uses, and getting it backwards would send someone to
	// buy a fan for a power supply problem. Throttling is what the board does
	// about a sagging supply; capping the ARM frequency and the soft
	// temperature limit are what it does about heat.
	UnderVoltageNow bool `json:"underVoltageNow"`
	CappedNow       bool `json:"cappedNow"`
	ThrottledNow    bool `json:"throttledNow"`
	SoftTempNow     bool `json:"softTempNow"`

	// The "since boot" flags: what has happened at least once since the machine
	// came up. These are the interesting ones — a supply that sags under load
	// has usually recovered by the time anyone looks.
	UnderVoltage bool `json:"underVoltage"`
	Capped       bool `json:"capped"`
	Throttled    bool `json:"throttled"`
	SoftTemp     bool `json:"softTemp"`
}

// Watchdog is what the hardware watchdog says about the reset that started this
// boot. Not every driver fills it in, and on a Raspberry Pi an ordinary reboot
// goes through the watchdog too — so "the card reset the CPU" is reported as an
// observation and never as a verdict on its own.
type Watchdog struct {
	BootStatus int `json:"bootStatus"`
	// Flags names the bits that are set, in words.
	Flags []string `json:"flags,omitempty"`
}

// BootReport is HostMan's answer to "why did it restart?", with everything it
// looked at.
type BootReport struct {
	// The guess.
	Cause      Cause  `json:"cause"`
	Confidence string `json:"confidence"`
	// Headline is one line: what HostMan thinks happened.
	Headline string `json:"headline"`
	// Detail is a sentence or two saying what that means and what it does not.
	Detail string `json:"detail"`
	// Reasons are the steps to the verdict, in the order they mattered.
	Reasons []string `json:"reasons"`

	// When.
	BootedAt time.Time `json:"bootedAt"`
	UptimeS  int64     `json:"uptimeS"`
	// PreviousUpS is how long the boot before this one lasted. 0 where wtmp did
	// not say.
	PreviousUpS int64 `json:"previousUpS,omitempty"`

	// What the machine is.
	Model  string `json:"model,omitempty"`
	Kernel string `json:"kernel,omitempty"`
	// PreviousKernel is what it was running before, where that is known and
	// different — a restart that changed the kernel was an update.
	PreviousKernel string `json:"previousKernel,omitempty"`

	// The evidence.
	Signs    []Sign    `json:"signs"`
	Restarts []Restart `json:"restarts"`
	// Unclean counts the restarts above that nothing asked for.
	Unclean int `json:"unclean"`
	// CleanKnown is whether the restart history can tell a restart something
	// asked for from one it did not. It comes from whether `last` on this host
	// will show the shutdown records at all — busybox's will not — and a screen
	// that called every restart unexplained for want of that would be inventing
	// a fault rather than finding one.
	CleanKnown bool `json:"cleanKnown"`

	// Where the evidence came from, and what was missing.
	Source string `json:"source"`
	// Journal is whether the host has journalctl at all.
	Journal bool `json:"journal"`
	// Persistent is whether its journal survives a reboot. False is the reason
	// most of these screens have nothing to show.
	Persistent bool `json:"persistent"`
	// LogFile names the rsyslog file the evidence came out of, where the
	// journal had none.
	LogFile string `json:"logFile,omitempty"`
	// BootsKept is how many boots the journal still holds.
	BootsKept int `json:"bootsKept,omitempty"`

	// LogTail is the end of the previous boot's log — the last thing the
	// machine said before it went.
	LogTail string `json:"logTail"`
	// Truncated means the tail was longer than HostMan will carry and lost its
	// beginning.
	Truncated bool `json:"truncated"`

	Throttle *Throttle `json:"throttle,omitempty"`
	Watchdog *Watchdog `json:"watchdog,omitempty"`
	TempC    *float64  `json:"tempC,omitempty"`

	AsUser string `json:"asUser"`
}

// bootScript collects every record of the last restart in one round trip.
//
// The journal is asked for twice: once for the tail of the previous boot at any
// priority, which is where an orderly shutdown shows itself, and once for that
// boot's warnings and worse, which is where a panic, an out-of-memory kill, a
// critical temperature and the Pi's under-voltage warning all are. Filtering by
// priority is what makes reading hours of log affordable on a Pi's SD card.
//
// Both come back base64-encoded, like any other log HostMan carries, so no
// line of a log can be mistaken for one of the markers around it. --no-hostname
// drops a column that is the same on every line; it arrived in systemd 230, so
// a host that refuses it gets the same command without it rather than nothing.
//
// When the journal has no previous boot — the default on Debian, where nothing
// creates /var/log/journal — the same evidence is often still in rsyslog's
// files. The lines immediately before the last kernel banner in one of them are
// precisely the end of the previous boot, which is why the fallback looks for
// that banner rather than simply tailing the file.
const bootScript = `set -u
tailN=$1
warnN=$2
fileN=$3
cap=$4
lastN=$5
shift 5

say() { printf '@@%s\n' "$1"; }

say user
id -un 2>/dev/null || echo unknown
say now
date +%s 2>/dev/null || echo 0
say tz
date +%z 2>/dev/null || echo +0000
say uptime
cut -d' ' -f1 /proc/uptime 2>/dev/null || echo 0
say kernel
uname -r 2>/dev/null || echo unknown
say model
for m in /proc/device-tree/model /sys/firmware/devicetree/base/model; do
  if [ -r "$m" ]; then tr -d '\000' <"$m"; echo; break; fi
done

out=
mode=none
if command -v last >/dev/null 2>&1; then
  if out=$(last -x --time-format=iso -n "$lastN" reboot shutdown 2>/dev/null); then mode=iso
  elif out=$(last -x -n "$lastN" reboot shutdown 2>/dev/null); then mode=x
  elif out=$(last -n "$lastN" reboot 2>/dev/null); then mode=plain
  fi
fi
say lastmode
printf '%s\n' "$mode"
say last
if [ "$mode" != none ]; then printf '%s\n' "$out"; fi

say journal
if command -v journalctl >/dev/null 2>&1; then echo yes; else echo no; fi
say persistent
if [ -d /var/log/journal ]; then echo yes; else echo no; fi
say boots
if command -v journalctl >/dev/null 2>&1; then
  journalctl --list-boots --no-pager 2>/dev/null | tail -n 40 || true
fi

prev=$(mktemp /tmp/deployer-boot.XXXXXX) || { printf 'cannot write a temporary file\n' >&2; exit 2; }
warn=$(mktemp /tmp/deployer-boot.XXXXXX) || { printf 'cannot write a temporary file\n' >&2; exit 2; }
trap 'rm -f "$prev" "$warn"' EXIT

src=none
logfile=
if command -v journalctl >/dev/null 2>&1; then
  journalctl --no-pager --no-hostname -o short-iso -b -1 -n "$tailN" >"$prev" 2>/dev/null ||
    journalctl --no-pager -o short-iso -b -1 -n "$tailN" >"$prev" 2>/dev/null || true
  if [ -s "$prev" ]; then
    src=journal
    journalctl --no-pager --no-hostname -o short-iso -b -1 -p warning -n "$warnN" >"$warn" 2>/dev/null ||
      journalctl --no-pager -o short-iso -b -1 -p warning -n "$warnN" >"$warn" 2>/dev/null || true
  fi
fi

if [ "$src" = none ]; then
  for f in "$@"; do
    [ -r "$f" ] || continue
    n=$(grep -n -e 'Linux version' -e 'Booting Linux' "$f" 2>/dev/null | tail -n 1 | cut -d: -f1)
    [ -n "$n" ] || continue
    [ "$n" -gt 1 ] 2>/dev/null || continue
    start=$((n - fileN))
    if [ "$start" -lt 1 ]; then start=1; fi
    sed -n "${start},$((n - 1))p" "$f" >"$prev" 2>/dev/null || continue
    if [ -s "$prev" ]; then src=logfile; logfile=$f; break; fi
  done
fi

say source
printf '%s\n' "$src"
say logfile
printf '%s\n' "$logfile"
say size
wc -c <"$prev" | tr -d ' '
say tail
tail -c "$cap" "$prev" | base64
say warnings
tail -c "$cap" "$warn" | base64

say throttled
if command -v vcgencmd >/dev/null 2>&1; then vcgencmd get_throttled 2>/dev/null || true; fi
say temp
for z in /sys/class/thermal/thermal_zone*/temp; do
  if [ -r "$z" ]; then cat "$z"; break; fi
done
say watchdog
for b in /sys/class/watchdog/watchdog*/bootstatus; do
  if [ -r "$b" ]; then cat "$b"; break; fi
done
say end
exit 0
`

// bootLogFiles is where an rsyslog host keeps the log the journal did not.
// syslog comes first because it holds systemd's shutdown sequence as well as
// the kernel's messages, and kern.log only the latter. They are arguments to
// the script rather than text inside it so a test can point the same script at
// files it wrote.
var bootLogFiles = []string{"/var/log/syslog", "/var/log/messages", "/var/log/kern.log"}

// LastBoot reads everything the host remembers about its last restart and says
// what it thinks happened.
//
// It runs elevated where passwordless sudo is available, because most of this
// is unreadable otherwise: the system journal belongs to root, /var/log/syslog
// to the adm group, and the firmware's throttle register to whoever `vcgencmd`
// will talk to. Where sudo is not available the same script runs as the
// connecting user and returns whatever it could see, which is usually wtmp and
// little else — an honest partial answer rather than a refusal.
func (s *Service) LastBoot(ctx context.Context, h *store.Host) (*BootReport, error) {
	args := append([]string{
		strconv.Itoa(bootTailLines),
		strconv.Itoa(bootWarnLines),
		strconv.Itoa(bootFileLines),
		strconv.Itoa(MaxBootLogBytes),
		strconv.Itoa(maxRestarts),
	}, bootLogFiles...)
	cmd := elevate(bootScript, args...)
	res, err := s.run(ctx, h, cmd, "")
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, failure(res, "could not read the boot record on "+h.Name)
	}
	return parseBootReport(res.Stdout)
}

// parseBootReport turns the script's output into a report and then asks
// bootcause.go what it means.
func parseBootReport(out string) (*BootReport, error) {
	found := sections(out)
	// The end marker is what separates a truncated answer from a machine that
	// genuinely had nothing to say. Without it the sections that are missing
	// are missing because the connection died, and a verdict built on that
	// would be a verdict built on nothing.
	if _, ok := found["end"]; !ok {
		return nil, fmt.Errorf("the host stopped part way through reading its boot record")
	}

	report := &BootReport{
		AsUser:     first(found["user"]),
		Kernel:     first(found["kernel"]),
		Model:      first(found["model"]),
		Source:     first(found["source"]),
		LogFile:    first(found["logfile"]),
		Journal:    first(found["journal"]) == "yes",
		Persistent: first(found["persistent"]) == "yes",
		BootsKept:  countBoots(found["boots"]),
		Signs:      []Sign{},
		Restarts:   []Restart{},
		Reasons:    []string{},
	}
	if report.Source == "" {
		report.Source = SourceNone
	}

	// The host's own clock, and its offset, because every age on this screen is
	// measured against when this machine came up and the two boxes need not
	// agree about what time it is.
	now := time.Unix(int64(parseSeconds(first(found["now"]))), 0).UTC()
	report.UptimeS = int64(parseSeconds(first(found["uptime"])))
	if now.Unix() > 0 && report.UptimeS > 0 {
		report.BootedAt = now.Add(-time.Duration(report.UptimeS) * time.Second)
	}
	zone := parseZone(first(found["tz"]))

	// `last` is asked for three ways and the host answers with whichever it
	// understood; only the two that carry `-x` show the shutdown records that
	// say whether a restart was asked for.
	mode := first(found["lastmode"])
	report.CleanKnown = mode == "iso" || mode == "x"
	report.Restarts, report.Unclean = parseRestarts(found["last"], zone, now)
	if !report.CleanKnown {
		report.Unclean = 0
	}
	report.PreviousUpS, report.PreviousKernel = previousBoot(report.Restarts, report.Kernel)

	tail, truncated := decodeLog(found["tail"], first(found["size"]))
	report.LogTail = tail
	report.Truncated = truncated
	warnings, _ := decodeLog(found["warnings"], "")

	report.Throttle = parseThrottle(first(found["throttled"]))
	report.Watchdog = parseWatchdog(first(found["watchdog"]))
	report.TempC = parseMilliCelsius(first(found["temp"]))

	// Both chunks go through the same matcher. They overlap — a panic loud
	// enough to be in the tail is also a warning — and collectSigns keeps one
	// sign per kind, so saying a thing twice does not make it two findings.
	report.Signs = collectSigns(report.BootedAt, zone, tail, warnings)

	diagnose(report)
	return report, nil
}

// JournalStorage is the state of persistent logging on a host, and what came of
// turning it on.
type JournalStorage struct {
	// Enabled is whether the journal now survives a restart.
	Enabled bool `json:"enabled"`
	// Already is true where it was on before HostMan touched anything, which
	// makes this a no-op rather than a change.
	Already bool `json:"already"`
	// Configured is what journald.conf says about Storage=, where it says
	// anything at all. "volatile" or "none" there beats the directory and is
	// the one case this cannot fix from here.
	Configured string `json:"configured,omitempty"`
	// Blocked says why the journal still will not survive a restart, on the
	// hosts where creating the directory was not enough. Empty otherwise.
	Blocked string `json:"blocked,omitempty"`
}

// persistScript turns on the persistent journal, which is the difference
// between a machine that can explain its last restart and one that cannot.
//
// There is nothing to install and no setting to change: journald's default
// Storage=auto means "write to /var/log/journal if it is there", so creating
// the directory is the whole of it. systemd-tmpfiles is what gives it the right
// owner and mode, and the flush is what moves the log that is already in memory
// into it so this boot is not lost either.
//
// Storage= set explicitly to volatile or none overrides all of that, so it is
// read and reported rather than being edited underneath whoever set it.
const persistScript = `set -u
command -v journalctl >/dev/null 2>&1 || { printf 'this host has no systemd journal\n' >&2; exit 2; }
[ "$(id -u)" = 0 ] || { printf 'this needs root\n' >&2; exit 3; }

say() { printf '@@%s\n' "$1"; }

say configured
grep -h '^[[:space:]]*Storage=' /etc/systemd/journald.conf /etc/systemd/journald.conf.d/*.conf 2>/dev/null |
  tail -n 1 | sed 's/.*=//' | tr -d '[:space:]'
echo
say already
if [ -d /var/log/journal ]; then echo yes; else echo no; fi

if [ ! -d /var/log/journal ]; then
  mkdir -p /var/log/journal || { printf 'could not create /var/log/journal\n' >&2; exit 4; }
  systemd-tmpfiles --create --prefix /var/log/journal >/dev/null 2>&1 || true
fi
journalctl --flush >/dev/null 2>&1 ||
  systemctl kill --kill-who=main --signal=SIGUSR1 systemd-journald >/dev/null 2>&1 || true

say enabled
if [ -d /var/log/journal ]; then echo yes; else echo no; fi
say end
exit 0
`

// KeepJournal makes the host's journal survive a restart, so that the next
// unexplained one can be explained. It is idempotent: a host that already keeps
// its journal is left alone and says so.
func (s *Service) KeepJournal(ctx context.Context, h *store.Host) (*JournalStorage, error) {
	res, err := s.run(ctx, h, elevate(persistScript), "")
	if err != nil {
		return nil, err
	}
	if res.ExitCode == 3 {
		return nil, errNeedsRoot(h.Username, h.Name)
	}
	if res.ExitCode != 0 {
		return nil, failure(res, "could not turn on persistent logging on "+h.Name)
	}
	found := sections(res.Stdout)
	storage := &JournalStorage{
		Enabled:    first(found["enabled"]) == "yes",
		Already:    first(found["already"]) == "yes",
		Configured: first(found["configured"]),
	}
	if !storage.Enabled {
		return nil, fmt.Errorf("/var/log/journal was created but the journal is still not persistent")
	}
	// The directory is there and systemd will ignore it, because it was told
	// to. Reporting that plainly beats reporting a success that changes
	// nothing — and the fix is a one-line edit the file browser can make.
	switch strings.ToLower(storage.Configured) {
	case "volatile", "none":
		storage.Enabled = false
		storage.Blocked = "journald.conf on this host sets Storage=" + storage.Configured +
			", which keeps the journal in memory whatever /var/log/journal says."
	}
	return storage, nil
}

// decodeLog turns a base64 section back into text and says whether its
// beginning was dropped on the way. Taking the last bytes cuts the oldest line
// in half, and half a line at the top reads as corruption, so it goes.
func decodeLog(lines []string, size string) (string, bool) {
	raw, err := base64.StdEncoding.DecodeString(strings.Join(lines, ""))
	if err != nil {
		return "", false
	}
	body := string(raw)
	whole, err := strconv.ParseInt(strings.TrimSpace(size), 10, 64)
	if err != nil || whole <= int64(len(raw)) {
		return body, false
	}
	if cut := strings.IndexByte(body, '\n'); cut >= 0 {
		body = body[cut+1:]
	}
	return body, true
}

// countBoots counts the entries in `journalctl --list-boots`, each of which
// begins with an index: 0 for the boot that is running, negative for the ones
// before it. Formats have varied across systemd versions and this needs only
// the count, so it counts leading indices rather than parsing the rest.
func countBoots(lines []string) int {
	n := 0
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if _, err := strconv.Atoi(fields[0]); err == nil {
			n++
		}
	}
	return n
}

// parseRestarts reads `last -x reboot shutdown`, newest first, into the history
// of times this machine came up.
//
// The cleanliness of a restart is a question about what is *next to* a record
// rather than what is in it: a machine that was asked to go down writes a
// shutdown record on its way out and a reboot record on its way back, so a
// reboot with a shutdown immediately below it in `last` was asked for, and a
// reboot with another reboot below it was not. That single adjacency is the
// most valuable thing here, and the only one that survives on a host whose
// journal keeps nothing.
//
// It returns the restarts and how many of them nothing asked for.
func parseRestarts(lines []string, zone *time.Location, now time.Time) ([]Restart, int) {
	type record struct {
		kind   string
		at     time.Time
		kernel string
	}
	var records []record
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		kind := fields[0]
		if kind != "reboot" && kind != "shutdown" {
			continue
		}
		rec := record{kind: kind}
		// `last -x` writes "reboot system boot <kernel> <time...>". The kernel
		// column is where a version lands whether or not the time after it is
		// one this parser can read.
		if fields[1] == "system" && len(fields) >= 4 {
			rec.kernel = fields[3]
		}
		for _, field := range fields[1:] {
			if at, ok := parseLogTime(field, zone, time.Time{}); ok {
				rec.at = at
				break
			}
		}
		records = append(records, rec)
	}

	restarts := []Restart{}
	unclean := 0
	for i, rec := range records {
		if rec.kind != "reboot" {
			continue
		}
		restart := Restart{
			BootedAt: rec.at,
			Kernel:   rec.kernel,
			Timed:    !rec.at.IsZero(),
			// The record below this one in `last` is what happened immediately
			// before this boot. A shutdown there means something asked.
			Clean:   i+1 < len(records) && records[i+1].kind == "shutdown",
			Current: len(restarts) == 0,
		}
		// How long that boot lasted: until whatever is above it happened, or
		// until now for the one still running.
		end := now
		if i > 0 && !records[i-1].at.IsZero() {
			end = records[i-1].at
		}
		if !rec.at.IsZero() && !end.IsZero() && end.After(rec.at) {
			restart.UpS = int64(end.Sub(rec.at).Seconds())
		}
		// The oldest record in wtmp has nothing below it, so whether anything
		// shut the machine down before it is unknowable rather than false. It
		// is not counted either way.
		if !restart.Clean && i+1 < len(records) {
			unclean++
		}
		restarts = append(restarts, restart)
	}
	return restarts, unclean
}

// previousBoot reports how long the boot before this one lasted, and what
// kernel it was running where that differs from the one running now. A restart
// that changed the kernel was an update, which is worth saying out loud before
// anyone goes looking for a fault.
func previousBoot(restarts []Restart, kernel string) (upS int64, previousKernel string) {
	if len(restarts) < 2 {
		return 0, ""
	}
	previous := restarts[1]
	if previous.Kernel != "" && kernel != "" && previous.Kernel != kernel {
		previousKernel = previous.Kernel
	}
	return previous.UpS, previousKernel
}

// parseThrottle reads `vcgencmd get_throttled`, which answers "throttled=0x0"
// and is the only place a Raspberry Pi records its supply sagging. The low bits
// are what is true now; the bits from 16 up are what has happened at least once
// since the machine came up, and those are the interesting ones — a supply that
// dips under load has usually recovered by the time anyone looks.
func parseThrottle(line string) *Throttle {
	_, value, ok := strings.Cut(strings.TrimSpace(line), "=")
	if !ok {
		return nil
	}
	value = strings.TrimSpace(value)
	bits, err := strconv.ParseUint(strings.TrimPrefix(strings.TrimPrefix(value, "0x"), "0X"), 16, 64)
	if err != nil {
		return nil
	}
	return &Throttle{
		Raw:             value,
		UnderVoltageNow: bits&(1<<0) != 0,
		CappedNow:       bits&(1<<1) != 0,
		ThrottledNow:    bits&(1<<2) != 0,
		SoftTempNow:     bits&(1<<3) != 0,
		UnderVoltage:    bits&(1<<16) != 0,
		Capped:          bits&(1<<17) != 0,
		Throttled:       bits&(1<<18) != 0,
		SoftTemp:        bits&(1<<19) != 0,
	}
}

// watchdogFlags are the bits of the Linux watchdog API's bootstatus, which is
// the driver's account of what reset the machine. Most drivers report 0 for
// everything; the ones that do fill it in are worth reading.
var watchdogFlags = []struct {
	bit   int
	label string
}{
	{0x0001, "reset after the CPU overheated"},
	{0x0002, "a fan failed"},
	{0x0004, "an external relay tripped"},
	{0x0008, "a second external relay tripped"},
	{0x0010, "the power went bad"},
	{0x0020, "the watchdog reset the CPU"},
	{0x0040, "the supply went over voltage"},
}

// parseWatchdog reads /sys/class/watchdog/watchdog0/bootstatus. Zero is the
// common answer and means nothing was reported, not that nothing happened.
func parseWatchdog(line string) *Watchdog {
	value := strings.TrimSpace(line)
	if value == "" {
		return nil
	}
	status, err := strconv.Atoi(value)
	if err != nil || status == 0 {
		return nil
	}
	w := &Watchdog{BootStatus: status}
	for _, flag := range watchdogFlags {
		if status&flag.bit != 0 {
			w.Flags = append(w.Flags, flag.label)
		}
	}
	return w
}

// parseMilliCelsius reads a thermal zone, which is in thousandths of a degree.
func parseMilliCelsius(line string) *float64 {
	milli, err := strconv.ParseFloat(strings.TrimSpace(line), 64)
	if err != nil || milli <= 0 {
		return nil
	}
	c := milli / 1000
	return &c
}

// parseZone turns `date +%z` into the offset the host's own log timestamps are
// written in. rsyslog's traditional format carries no offset at all, so without
// this a line that says 09:11 would be read as 09:11 UTC and land an hour or
// several from where it belongs.
func parseZone(value string) *time.Location {
	value = strings.TrimSpace(value)
	if len(value) != 5 {
		return time.UTC
	}
	sign := 1
	switch value[0] {
	case '+':
	case '-':
		sign = -1
	default:
		return time.UTC
	}
	hours, err1 := strconv.Atoi(value[1:3])
	minutes, err2 := strconv.Atoi(value[3:5])
	if err1 != nil || err2 != nil {
		return time.UTC
	}
	return time.FixedZone("host", sign*(hours*3600+minutes*60))
}

// isoLayouts are the timestamp formats a log line may open with: the journal's
// short-iso, which gained the colon in its offset in later systemd versions,
// and `last --time-format=iso`. The leading Z in each layout is what lets the
// same one read both a numeric offset and the bare "Z" a host running on UTC
// may write instead.
var isoLayouts = []string{
	"2006-01-02T15:04:05Z0700",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04:05.999999Z0700",
	"2006-01-02T15:04:05.999999Z07:00",
}

// syslogLayouts are rsyslog's traditional stamp, which has no year and no
// offset: "Aug  9 21:14:02". Both spellings of the day appear depending on
// whether it needs two digits.
var syslogLayouts = []string{
	"Jan _2 15:04:05",
	"Jan 2 15:04:05",
}

// parseLogTime reads the timestamp a log line opens with.
//
// A traditional syslog stamp names no year, so one has to be supplied: the year
// the machine came up, moved back by one where that would put the line in the
// future, which is what happens to the last December lines of a log read in
// January. near being zero means the caller has no reference — the stamp is
// then read in the year zero and rejected by the caller as untimed rather than
// silently landing in 1970.
func parseLogTime(value string, zone *time.Location, near time.Time) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range isoLayouts {
		if at, err := time.Parse(layout, value); err == nil {
			return at.UTC(), true
		}
	}
	if near.IsZero() {
		return time.Time{}, false
	}
	for _, layout := range syslogLayouts {
		at, err := time.ParseInLocation(layout, value, zone)
		if err != nil {
			continue
		}
		at = time.Date(near.Year(), at.Month(), at.Day(), at.Hour(), at.Minute(), at.Second(), 0, zone)
		if at.After(near.Add(24 * time.Hour)) {
			at = at.AddDate(-1, 0, 0)
		}
		return at.UTC(), true
	}
	return time.Time{}, false
}

// lineTime pulls the timestamp off the front of a log line, whichever of the
// two forms it is in. A syslog stamp is three fields wide and an ISO one is a
// single field, so both are tried rather than guessed at from the source.
func lineTime(line string, zone *time.Location, near time.Time) (time.Time, bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return time.Time{}, false
	}
	if at, ok := parseLogTime(fields[0], zone, near); ok {
		return at, true
	}
	if len(fields) >= 3 {
		if at, ok := parseLogTime(strings.Join(fields[:3], " "), zone, near); ok {
			return at, true
		}
	}
	return time.Time{}, false
}
