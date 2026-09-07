package hostops

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The verdict is the part of HostMan that guesses, so it is the part most
// worth pinning down. Every case here is built from log output of the shape the
// real thing produces — a Raspberry Pi's journal, an rsyslog file on a host
// that keeps no journal, the `last` output that is all there is on a host that
// keeps neither — and asserts both the cause and, where it matters, the
// wording, because "unknown" said confidently is a worse answer than "unknown"
// said plainly.

// hostNow is the host's clock in every fixture, and hostUp how long it has been
// running: the restart happened an hour before "now".
var (
	hostNow = time.Date(2025, 8, 11, 12, 0, 0, 0, time.UTC)
	hostUp  = int64(3600)
)

func bootedAt() time.Time { return hostNow.Add(-time.Duration(hostUp) * time.Second) }

// stamp writes a journal short-iso timestamp for a moment before the restart.
func stamp(before time.Duration) string {
	return bootedAt().Add(-before).Format("2006-01-02T15:04:05-0700")
}

// syslogStamp writes rsyslog's traditional stamp, which carries neither a year
// nor an offset — the two things that make dating one of those files awkward.
func syslogStamp(before time.Duration) string {
	return bootedAt().Add(-before).Format("Jan _2 15:04:05")
}

// journalLine is one line as `journalctl -o short-iso --no-hostname` writes it.
func journalLine(before time.Duration, text string) string {
	return stamp(before) + " " + text
}

// fixture builds the output of bootScript, so the parser and the verdict are
// exercised through exactly the bytes the host would have sent.
type fixture struct {
	uptime     int64
	tz         string
	kernel     string
	model      string
	lastMode   string
	last       []string
	journal    bool
	persistent bool
	boots      int
	source     string
	logFile    string
	tail       []string
	warnings   []string
	throttled  string
	temp       string
	watchdog   string
}

// defaults is a healthy Raspberry Pi with a persistent journal: everything
// present, nothing wrong. Each test changes only the part it is about.
func defaults() fixture {
	return fixture{
		uptime:     hostUp,
		tz:         "+0000",
		kernel:     "6.6.51+rpt-rpi-v8",
		model:      "Raspberry Pi 4 Model B Rev 1.4",
		lastMode:   "iso",
		journal:    true,
		persistent: true,
		boots:      4,
		source:     SourceJournal,
	}
}

func (f fixture) output() string {
	var b strings.Builder
	section := func(name string, lines ...string) {
		fmt.Fprintf(&b, "@@%s\n", name)
		for _, line := range lines {
			b.WriteString(line + "\n")
		}
	}
	yes := func(v bool) string {
		if v {
			return "yes"
		}
		return "no"
	}

	section("user", "root")
	section("now", fmt.Sprint(hostNow.Unix()))
	section("tz", f.tz)
	section("uptime", fmt.Sprintf("%d.42", f.uptime))
	section("kernel", f.kernel)
	section("model", f.model)
	section("lastmode", f.lastMode)
	section("last", f.last...)
	section("journal", yes(f.journal))
	section("persistent", yes(f.persistent))
	boots := []string{}
	for i := f.boots - 1; i >= 0; i-- {
		boots = append(boots, fmt.Sprintf("%3d 4a8b%dcd Sun 2025-08-10 22:03:11 UTC—Mon 2025-08-11 09:11:02 UTC", -i, i))
	}
	section("boots", boots...)
	section("source", f.source)
	section("logfile", f.logFile)

	tail := strings.Join(f.tail, "\n")
	if tail != "" {
		tail += "\n"
	}
	section("size", fmt.Sprint(len(tail)))
	section("tail", base64.StdEncoding.EncodeToString([]byte(tail)))
	warnings := strings.Join(f.warnings, "\n")
	if warnings != "" {
		warnings += "\n"
	}
	section("warnings", base64.StdEncoding.EncodeToString([]byte(warnings)))

	section("throttled", f.throttled)
	section("temp", f.temp)
	section("watchdog", f.watchdog)
	section("end")
	return b.String()
}

func (f fixture) report(t *testing.T) *BootReport {
	t.Helper()
	report, err := parseBootReport(f.output())
	if err != nil {
		t.Fatalf("parseBootReport: %v", err)
	}
	return report
}

// cleanWtmp is what `last -x --time-format=iso reboot shutdown` prints for a
// machine that was asked to restart: a shutdown record sitting between the two
// boots, which is the whole of the evidence.
func cleanWtmp() []string {
	return []string{
		"reboot   system boot  6.6.51+rpt-rpi-v8 " + bootedAt().Format(time.RFC3339) + "   still running",
		"shutdown system down  6.6.51+rpt-rpi-v8 " + bootedAt().Add(-90*time.Second).Format(time.RFC3339) + " - " + bootedAt().Format(time.RFC3339) + "  (00:01)",
		"reboot   system boot  6.6.51+rpt-rpi-v8 " + bootedAt().Add(-72*time.Hour).Format(time.RFC3339) + " - " + bootedAt().Add(-90*time.Second).Format(time.RFC3339) + " (71:58)",
		"",
		"wtmp begins " + bootedAt().Add(-72*time.Hour).Format(time.RFC3339),
	}
}

// uncleanWtmp is the same machine with nothing between the two boots: one boot
// ends and the next begins, and nothing recorded asking for it.
func uncleanWtmp() []string {
	return []string{
		"reboot   system boot  6.6.51+rpt-rpi-v8 " + bootedAt().Format(time.RFC3339) + "   still running",
		"reboot   system boot  6.6.51+rpt-rpi-v8 " + bootedAt().Add(-72*time.Hour).Format(time.RFC3339) + " - " + bootedAt().Format(time.RFC3339) + " (72:00)",
		"reboot   system boot  6.6.51+rpt-rpi-v8 " + bootedAt().Add(-96*time.Hour).Format(time.RFC3339) + " - " + bootedAt().Add(-72*time.Hour).Format(time.RFC3339) + " (24:00)",
	}
}

// shutdownTail is the end of a boot that was asked to go: systemd stopping
// things, then the last line journald writes before it closes.
func shutdownTail() []string {
	return []string{
		journalLine(20*time.Second, "systemd[1]: Stopping User Manager for UID 1000..."),
		journalLine(12*time.Second, "systemd[1]: Reached target shutdown.target - System Shutdown."),
		journalLine(8*time.Second, "systemd-journald[212]: Journal stopped"),
		journalLine(5*time.Second, "systemd-shutdown[1]: Rebooting."),
	}
}

// busyTail is the end of a boot that was not: ordinary work, and then nothing.
func busyTail() []string {
	return []string{
		journalLine(70*time.Second, "CRON[4411]: (pi) CMD (/usr/local/bin/backup.sh)"),
		journalLine(40*time.Second, "dhcpcd[431]: wlan0: renewing lease of 192.168.2.51"),
		journalLine(9*time.Second, "countroster[1180]: served GET / in 3ms"),
	}
}

func mustSign(t *testing.T, r *BootReport, kind string) Sign {
	t.Helper()
	for _, sign := range r.Signs {
		if sign.Kind == kind {
			return sign
		}
	}
	t.Fatalf("no %s sign in %+v", kind, r.Signs)
	return Sign{}
}

func hasReason(r *BootReport, substr string) bool {
	for _, reason := range r.Reasons {
		if strings.Contains(reason, substr) {
			return true
		}
	}
	return false
}

// --- the ordinary case ---

func TestBootClean(t *testing.T) {
	f := defaults()
	f.last = cleanWtmp()
	f.tail = shutdownTail()
	f.warnings = []string{}

	r := f.report(t)
	if r.Cause != CauseClean {
		t.Errorf("cause = %q, want %q", r.Cause, CauseClean)
	}
	// The log and wtmp agree, which is the only combination worth calling
	// certain.
	if r.Confidence != ConfidenceCertain {
		t.Errorf("confidence = %q, want %q", r.Confidence, ConfidenceCertain)
	}
	if r.BootedAt != bootedAt() {
		t.Errorf("bootedAt = %v, want %v", r.BootedAt, bootedAt())
	}
	if r.UptimeS != hostUp {
		t.Errorf("uptimeS = %d, want %d", r.UptimeS, hostUp)
	}
	if !hasReason(r, "shutdown was recorded") {
		t.Errorf("reasons = %v, want wtmp's shutdown record named", r.Reasons)
	}
	if r.Unclean != 0 {
		t.Errorf("unclean = %d, want 0", r.Unclean)
	}
}

func TestBootCleanFromWtmpAlone(t *testing.T) {
	// A host with no journal at all still has wtmp, and wtmp alone is enough to
	// rule a restart in as asked-for — but not enough to be certain of it.
	f := defaults()
	f.journal = false
	f.persistent = false
	f.source = SourceNone
	f.last = cleanWtmp()

	r := f.report(t)
	if r.Cause != CauseClean {
		t.Errorf("cause = %q, want %q", r.Cause, CauseClean)
	}
	if r.Confidence != ConfidenceLikely {
		t.Errorf("confidence = %q, want %q on wtmp alone", r.Confidence, ConfidenceLikely)
	}
	if !hasReason(r, "no systemd journal") {
		t.Errorf("reasons = %v, want the missing journal named", r.Reasons)
	}
}

func TestBootNamesWhoAsked(t *testing.T) {
	f := defaults()
	f.last = cleanWtmp()
	f.tail = append(shutdownTail(),
		journalLine(25*time.Second, "sudo:       pi : TTY=pts/0 ; PWD=/home/pi ; USER=root ; COMMAND=/usr/sbin/reboot"))

	r := f.report(t)
	sign := mustSign(t, r, SignRequested)
	if !strings.Contains(sign.Detail, "reboot") {
		t.Errorf("detail = %q, want the command that was run", sign.Detail)
	}
	if !hasReason(r, "someone ran a restart command") && !hasReason(r, "Someone ran a restart command") {
		t.Errorf("reasons = %v, want the requester named", r.Reasons)
	}
}

func TestBootKernelUpdate(t *testing.T) {
	// It went down on one kernel and came back on another. That is an update
	// finishing, and saying so stops anyone hunting a fault that isn't there.
	f := defaults()
	f.tail = shutdownTail()
	f.last = []string{
		"reboot   system boot  6.6.51+rpt-rpi-v8 " + bootedAt().Format(time.RFC3339) + "   still running",
		"shutdown system down  6.6.31+rpt-rpi-v8 " + bootedAt().Add(-90*time.Second).Format(time.RFC3339) + " - " + bootedAt().Format(time.RFC3339) + "  (00:01)",
		"reboot   system boot  6.6.31+rpt-rpi-v8 " + bootedAt().Add(-72*time.Hour).Format(time.RFC3339) + " - " + bootedAt().Add(-90*time.Second).Format(time.RFC3339) + " (71:58)",
	}

	r := f.report(t)
	if r.Cause != CauseClean {
		t.Fatalf("cause = %q, want %q", r.Cause, CauseClean)
	}
	if r.PreviousKernel != "6.6.31+rpt-rpi-v8" {
		t.Errorf("previousKernel = %q, want the one it went down on", r.PreviousKernel)
	}
	if !strings.Contains(r.Headline, "updated") {
		t.Errorf("headline = %q, want it to say this was an update", r.Headline)
	}
}

// --- the failures ---

func TestBootPanic(t *testing.T) {
	f := defaults()
	f.last = uncleanWtmp()
	f.tail = busyTail()
	f.warnings = []string{
		journalLine(4*time.Second, "kernel: Unable to handle kernel NULL pointer dereference at virtual address 00000000"),
		journalLine(3*time.Second, "kernel: Internal error: Oops: 5 [#1] PREEMPT SMP ARM"),
		journalLine(2*time.Second, "kernel: Kernel panic - not syncing: Fatal exception in interrupt"),
	}

	r := f.report(t)
	if r.Cause != CausePanic {
		t.Errorf("cause = %q, want %q", r.Cause, CausePanic)
	}
	if r.Confidence != ConfidenceCertain {
		t.Errorf("confidence = %q, want %q", r.Confidence, ConfidenceCertain)
	}
	sign := mustSign(t, r, SignPanic)
	if sign.Detail != "Fatal exception in interrupt" {
		t.Errorf("detail = %q, want the panic's own reason", sign.Detail)
	}
	if sign.BeforeS != 2 {
		t.Errorf("beforeS = %d, want 2 seconds before it came back", sign.BeforeS)
	}
	if !sign.Near {
		t.Error("a panic two seconds before the restart should be near it")
	}
	if !hasReason(r, "not asked to go") {
		t.Errorf("reasons = %v, want wtmp's silence named", r.Reasons)
	}
}

func TestBootOOM(t *testing.T) {
	f := defaults()
	f.last = uncleanWtmp()
	f.tail = busyTail()
	f.warnings = []string{
		journalLine(30*time.Second, "kernel: chromium invoked oom-killer: gfp_mask=0x140dca(GFP_HIGHUSER_MOVABLE|__GFP_COMP|__GFP_ZERO), order=0"),
		journalLine(25*time.Second, "kernel: Out of memory: Killed process 1180 (chromium) total-vm:1802340kB, anon-rss:742900kB"),
	}

	r := f.report(t)
	if r.Cause != CauseOOM {
		t.Errorf("cause = %q, want %q", r.Cause, CauseOOM)
	}
	// An out-of-memory kill usually costs one program rather than the machine,
	// so it is never more than the best explanation available.
	if r.Confidence != ConfidenceLikely {
		t.Errorf("confidence = %q, want %q", r.Confidence, ConfidenceLikely)
	}
	if sign := mustSign(t, r, SignOOM); sign.Detail != "chromium" {
		t.Errorf("detail = %q, want the process it killed", sign.Detail)
	}
}

func TestBootOverheat(t *testing.T) {
	f := defaults()
	f.last = uncleanWtmp()
	f.tail = busyTail()
	f.warnings = []string{
		journalLine(20*time.Second, "kernel: thermal thermal_zone0: critical temperature reached (85 C), shutting down"),
	}
	f.temp = "48312"

	r := f.report(t)
	if r.Cause != CauseOverheat {
		t.Errorf("cause = %q, want %q", r.Cause, CauseOverheat)
	}
	if r.Confidence != ConfidenceCertain {
		t.Errorf("confidence = %q, want %q", r.Confidence, ConfidenceCertain)
	}
	if sign := mustSign(t, r, SignOverheat); sign.Detail != "85" {
		t.Errorf("detail = %q, want the temperature it stopped at", sign.Detail)
	}
	if r.TempC == nil || *r.TempC < 48 || *r.TempC > 49 {
		t.Errorf("tempC = %v, want the thermal zone read as degrees", r.TempC)
	}
}

func TestBootUndervoltage(t *testing.T) {
	f := defaults()
	f.last = uncleanWtmp()
	f.tail = busyTail()
	f.warnings = []string{
		journalLine(6*time.Second, "kernel: hwmon hwmon1: Undervoltage detected!"),
	}
	f.throttled = "throttled=0x50005"

	r := f.report(t)
	if r.Cause != CauseUndervoltage {
		t.Errorf("cause = %q, want %q", r.Cause, CauseUndervoltage)
	}
	if r.Throttle == nil || !r.Throttle.UnderVoltageNow || !r.Throttle.UnderVoltage {
		t.Fatalf("throttle = %+v, want under-voltage now and since boot", r.Throttle)
	}
	if !hasReason(r, "still happening") && !hasReason(r, "right now") {
		t.Errorf("reasons = %v, want the firmware's own flags reported", r.Reasons)
	}
}

func TestBootStorage(t *testing.T) {
	f := defaults()
	f.last = uncleanWtmp()
	f.tail = busyTail()
	f.warnings = []string{
		journalLine(12*time.Second, "kernel: mmc0: Timeout waiting for hardware interrupt."),
		journalLine(10*time.Second, "kernel: EXT4-fs error (device mmcblk0p2): __ext4_find_entry:1660: inode #2: comm systemd: reading directory lblock 0"),
		journalLine(9*time.Second, "kernel: EXT4-fs (mmcblk0p2): Remounting filesystem read-only"),
	}

	r := f.report(t)
	if r.Cause != CauseStorage {
		t.Errorf("cause = %q, want %q", r.Cause, CauseStorage)
	}
}

func TestBootLockup(t *testing.T) {
	f := defaults()
	f.last = uncleanWtmp()
	f.tail = busyTail()
	f.warnings = []string{
		journalLine(15*time.Second, "kernel: watchdog: BUG: soft lockup - CPU#2 stuck for 23s! [kworker/2:1:87]"),
	}
	f.watchdog = "32"

	r := f.report(t)
	if r.Cause != CauseLockup {
		t.Errorf("cause = %q, want %q", r.Cause, CauseLockup)
	}
	if sign := mustSign(t, r, SignLockup); sign.Detail != "2" {
		t.Errorf("detail = %q, want the CPU that stuck", sign.Detail)
	}
	if r.Watchdog == nil || len(r.Watchdog.Flags) == 0 {
		t.Fatalf("watchdog = %+v, want the bootstatus bits in words", r.Watchdog)
	}
	if !hasReason(r, "watchdog driver says") {
		t.Errorf("reasons = %v, want the watchdog reported", r.Reasons)
	}
}

// --- the absences, which are the interesting half ---

func TestBootPowerLoss(t *testing.T) {
	// Nothing near the restart at all, but the supply had been sagging during
	// that boot and is still sagging now. That is as close to proof of a power
	// cut as this question gets.
	f := defaults()
	f.last = uncleanWtmp()
	f.tail = busyTail()
	f.warnings = []string{
		journalLine(4*time.Hour, "kernel: hwmon hwmon1: Undervoltage detected!"),
		journalLine(4*time.Hour-30*time.Second, "kernel: hwmon hwmon1: Voltage normalised"),
	}
	f.throttled = "throttled=0x50000"

	r := f.report(t)
	if r.Cause != CausePower {
		t.Errorf("cause = %q, want %q", r.Cause, CausePower)
	}
	if r.Confidence != ConfidenceLikely {
		t.Errorf("confidence = %q, want %q", r.Confidence, ConfidenceLikely)
	}
	// The under-voltage was hours before the restart, so it explains the
	// machine rather than the moment — and must not be presented as the cause.
	sign := mustSign(t, r, SignUndervoltage)
	if sign.Near {
		t.Error("an under-voltage four hours earlier is not near the restart")
	}
}

func TestBootUnknown(t *testing.T) {
	f := defaults()
	f.last = uncleanWtmp()
	f.tail = busyTail()

	r := f.report(t)
	if r.Cause != CauseUnknown {
		t.Errorf("cause = %q, want %q", r.Cause, CauseUnknown)
	}
	if r.Confidence != ConfidenceUnclear {
		t.Errorf("confidence = %q, want %q", r.Confidence, ConfidenceUnclear)
	}
	// Naming both possibilities is the point: picking one on no evidence would
	// send someone to buy a power supply for a software fault.
	if !strings.Contains(r.Detail, "power") || !strings.Contains(r.Detail, "lock-up") {
		t.Errorf("detail = %q, want both readings of an abrupt end named", r.Detail)
	}
}

func TestBootDistantSignIsNotTheCause(t *testing.T) {
	// A machine that ran out of memory in the morning and restarted in the
	// afternoon did not restart because it ran out of memory.
	f := defaults()
	f.last = uncleanWtmp()
	f.tail = busyTail()
	f.warnings = []string{
		journalLine(6*time.Hour, "kernel: Out of memory: Killed process 900 (node) total-vm:900000kB"),
	}

	r := f.report(t)
	if r.Cause == CauseOOM {
		t.Error("an out-of-memory kill six hours earlier should not be the verdict")
	}
	if r.Cause != CauseUnknown {
		t.Errorf("cause = %q, want %q", r.Cause, CauseUnknown)
	}
	// It is still worth showing, as the weather rather than the event.
	if sign := mustSign(t, r, SignOOM); sign.Near {
		t.Error("it should not be marked near the restart")
	}
}

// A restart nothing asked for that also changed the kernel is a different and
// more alarming fact than an update finishing: the machine went down around an
// update. It must not be quietly filed as one.
func TestBootUncleanDuringAnUpdate(t *testing.T) {
	f := defaults()
	f.tail = busyTail()
	f.last = []string{
		"reboot   system boot  6.6.51+rpt-rpi-v8 " + bootedAt().Format(time.RFC3339) + "   still running",
		"reboot   system boot  6.6.31+rpt-rpi-v8 " + bootedAt().Add(-72*time.Hour).Format(time.RFC3339) + " - " + bootedAt().Format(time.RFC3339) + " (72:00)",
		"reboot   system boot  6.6.31+rpt-rpi-v8 " + bootedAt().Add(-96*time.Hour).Format(time.RFC3339) + " - " + bootedAt().Add(-72*time.Hour).Format(time.RFC3339) + " (24:00)",
	}

	r := f.report(t)
	if r.Cause == CauseClean {
		t.Error("nothing recorded a shutdown, so this was not a restart something asked for")
	}
	if !strings.Contains(r.Headline, "without saying why") {
		t.Errorf("headline = %q, want the unexplained verdict", r.Headline)
	}
	if !hasReason(r, "an update was in progress") {
		t.Errorf("reasons = %v, want the kernel change named", r.Reasons)
	}
}

func TestBootNoRecordAtAll(t *testing.T) {
	// The default on Debian, and so on Raspberry Pi OS: the journal is kept in
	// memory, so the log of the boot that ended died with it. Saying that, and
	// saying it is fixable, is the whole value of the screen in this state.
	f := defaults()
	f.persistent = false
	f.boots = 1
	f.source = SourceNone
	f.last = uncleanWtmp()

	r := f.report(t)
	if r.Cause != CauseUnknown || r.Confidence != ConfidenceUnclear {
		t.Errorf("cause = %q/%q, want an unclear unknown", r.Cause, r.Confidence)
	}
	if !hasReason(r, "does not survive a restart") {
		t.Errorf("reasons = %v, want the volatile journal named", r.Reasons)
	}
	// wtmp survives even that, and still says nothing asked the machine to go.
	if !hasReason(r, "not asked to go") {
		t.Errorf("reasons = %v, want wtmp's evidence kept", r.Reasons)
	}
}

func TestBootFromASyslogFile(t *testing.T) {
	// No journal worth reading, but rsyslog kept the lines from before the last
	// kernel banner. They carry no year and no offset, which is the whole
	// difficulty: dated wrongly they would all look hours from the restart and
	// nothing would ever be near it.
	f := defaults()
	f.persistent = false
	f.source = SourceLogFile
	f.logFile = "/var/log/syslog"
	f.last = uncleanWtmp()
	f.tail = []string{
		syslogStamp(30*time.Second) + " nakedpi kernel: [86400.112233] mmc0: Timeout waiting for hardware interrupt.",
		syslogStamp(7*time.Second) + " nakedpi kernel: [86423.998877] Kernel panic - not syncing: Attempted to kill init! exitcode=0x00000100",
	}

	r := f.report(t)
	if r.Cause != CausePanic {
		t.Fatalf("cause = %q, want %q", r.Cause, CausePanic)
	}
	sign := mustSign(t, r, SignPanic)
	if sign.BeforeS != 7 {
		t.Errorf("beforeS = %d, want the syslog stamp dated against the boot", sign.BeforeS)
	}
	if !hasReason(r, "/var/log/syslog") {
		t.Errorf("reasons = %v, want the file it came out of named", r.Reasons)
	}
}

// A log written in the host's own timezone must be read in it. Getting this
// wrong moves every line by the offset, which turns the panic that ended the
// machine into one that happened an hour before anything else.
func TestBootSyslogRespectsTheHostTimezone(t *testing.T) {
	f := defaults()
	f.tz = "+0530"
	f.source = SourceLogFile
	f.logFile = "/var/log/syslog"
	f.last = uncleanWtmp()
	local := bootedAt().Add(-10 * time.Second).In(time.FixedZone("host", 5*3600+30*60))
	f.tail = []string{
		local.Format("Jan _2 15:04:05") + " pi kernel: Kernel panic - not syncing: VFS: Unable to mount root fs",
	}

	r := f.report(t)
	if sign := mustSign(t, r, SignPanic); sign.BeforeS != 10 {
		t.Errorf("beforeS = %d, want 10 — the stamp read in the host's own zone", sign.BeforeS)
	}
}

// --- the record itself ---

func TestParseRestarts(t *testing.T) {
	f := defaults()
	f.last = cleanWtmp()
	r := f.report(t)

	if len(r.Restarts) != 2 {
		t.Fatalf("restarts = %+v, want the two boots wtmp names", r.Restarts)
	}
	current := r.Restarts[0]
	if !current.Current || !current.Clean {
		t.Errorf("current = %+v, want the running boot, asked for", current)
	}
	if current.UpS != hostUp {
		t.Errorf("upS = %d, want %d — how long it has been up", current.UpS, hostUp)
	}
	if current.BootedAt != bootedAt() {
		t.Errorf("bootedAt = %v, want %v", current.BootedAt, bootedAt())
	}
	// The older boot has nothing below it in wtmp, so whether anything shut it
	// down is unknowable rather than false, and it is not counted either way.
	if r.Unclean != 0 {
		t.Errorf("unclean = %d, want 0", r.Unclean)
	}
	if previous := r.Restarts[1]; previous.UpS != 72*3600-90 {
		t.Errorf("previous upS = %d, want the boot's length up to the shutdown", previous.UpS)
	}
}

func TestParseRestartsCountsTheUnaskedFor(t *testing.T) {
	f := defaults()
	f.last = uncleanWtmp()
	f.tail = busyTail()
	r := f.report(t)

	if r.Unclean != 2 {
		t.Errorf("unclean = %d, want 2 of the three boots — the oldest cannot be judged", r.Unclean)
	}
	if !hasReason(r, "were not asked for") {
		t.Errorf("reasons = %v, want the pattern named", r.Reasons)
	}
}

// busybox's `last` shows no shutdown records at all, so it cannot say whether a
// restart was asked for. Reading that as "nothing ever asks" would report a
// fault on every host that has one.
func TestBootWithoutShutdownRecords(t *testing.T) {
	f := defaults()
	f.lastMode = "plain"
	f.last = []string{
		"reboot   system boot  6.6.51+rpt-rpi-v8 Mon Aug 11 11:00   still running",
		"reboot   system boot  6.6.51+rpt-rpi-v8 Fri Aug  8 11:00 - 11:00 (72:00)",
	}
	f.tail = busyTail()

	r := f.report(t)
	if r.CleanKnown {
		t.Error("cleanKnown should be false where `last` cannot show shutdowns")
	}
	if r.Unclean != 0 {
		t.Errorf("unclean = %d, want 0 — it is unknown, not zero-known", r.Unclean)
	}
	if hasReason(r, "not asked to go") {
		t.Errorf("reasons = %v, must not claim wtmp's silence as evidence", r.Reasons)
	}
	if r.Restarts[0].Timed {
		t.Error("a `last` without ISO stamps has no times to report")
	}
}

// --- the small parsers ---

func TestParseThrottle(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want Throttle
	}{
		{"throttled=0x0", Throttle{Raw: "0x0"}},
		{"throttled=0x50005", Throttle{Raw: "0x50005", UnderVoltageNow: true, ThrottledNow: true, UnderVoltage: true, Throttled: true}},
		{"throttled=0x80008", Throttle{Raw: "0x80008", SoftTempNow: true, SoftTemp: true}},
		{"throttled=0x20000", Throttle{Raw: "0x20000", Capped: true}},
	} {
		got := parseThrottle(tc.raw)
		if got == nil {
			t.Fatalf("parseThrottle(%q) = nil", tc.raw)
		}
		if *got != tc.want {
			t.Errorf("parseThrottle(%q) = %+v, want %+v", tc.raw, *got, tc.want)
		}
	}
	// A host that is not a Raspberry Pi has no vcgencmd and says nothing.
	if got := parseThrottle(""); got != nil {
		t.Errorf("parseThrottle(\"\") = %+v, want nothing at all", got)
	}
	if got := parseThrottle("bash: vcgencmd: command not found"); got != nil {
		t.Errorf("parseThrottle(noise) = %+v, want nothing", got)
	}
}

func TestParseWatchdog(t *testing.T) {
	// Zero is what almost every driver reports, and means nothing was said —
	// not that nothing happened.
	if got := parseWatchdog("0"); got != nil {
		t.Errorf("parseWatchdog(0) = %+v, want nothing", got)
	}
	if got := parseWatchdog(""); got != nil {
		t.Errorf("parseWatchdog(empty) = %+v, want nothing", got)
	}
	got := parseWatchdog("48")
	if got == nil || len(got.Flags) != 2 {
		t.Fatalf("parseWatchdog(48) = %+v, want the reset and the over-voltage bits", got)
	}
}

func TestParseZone(t *testing.T) {
	for _, tc := range []struct {
		value  string
		offset int
	}{
		{"+0000", 0},
		{"+0100", 3600},
		{"-0800", -8 * 3600},
		{"+0530", 5*3600 + 30*60},
		{"nonsense", 0},
		{"", 0},
	} {
		_, offset := time.Date(2025, 1, 1, 0, 0, 0, 0, parseZone(tc.value)).Zone()
		if offset != tc.offset {
			t.Errorf("parseZone(%q) offset = %d, want %d", tc.value, offset, tc.offset)
		}
	}
}

// A report is only worth reading if it is complete. An answer cut off part way
// — a dropped connection, a killed session — has to fail rather than become a
// verdict built on the half that arrived.
func TestBootRefusesATruncatedAnswer(t *testing.T) {
	f := defaults()
	f.last = cleanWtmp()
	full := f.output()
	cut := strings.Index(full, "@@end")
	if cut < 0 {
		t.Fatal("the fixture has no end marker")
	}
	if _, err := parseBootReport(full[:cut]); err == nil {
		t.Error("a truncated answer should not produce a verdict")
	}
	if _, err := parseBootReport(full); err != nil {
		t.Errorf("the whole answer should parse: %v", err)
	}
}

func TestBootKeepsTheLogTail(t *testing.T) {
	f := defaults()
	f.last = uncleanWtmp()
	f.tail = busyTail()
	r := f.report(t)

	if !strings.Contains(r.LogTail, "served GET /") {
		t.Errorf("logTail = %q, want the last thing the machine said", r.LogTail)
	}
	if r.Truncated {
		t.Error("a tail that fits should not be marked truncated")
	}
	if r.Model != "Raspberry Pi 4 Model B Rev 1.4" {
		t.Errorf("model = %q, want the board it is running on", r.Model)
	}
	if r.BootsKept != 4 {
		t.Errorf("bootsKept = %d, want the four boots the journal lists", r.BootsKept)
	}
}

// The tail comes back as the last bytes of a longer log, which cuts the oldest
// line in half; half a line at the top reads as corruption, so it goes.
func TestBootTailDropsAHalfLine(t *testing.T) {
	f := defaults()
	f.last = uncleanWtmp()
	f.tail = busyTail()
	out := f.output()
	// Claim the log on the host was larger than what came back.
	out = strings.Replace(out, "@@size\n"+fmt.Sprint(len(strings.Join(f.tail, "\n"))+1), "@@size\n999999", 1)

	report, err := parseBootReport(out)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Truncated {
		t.Fatal("a tail that lost its beginning should say so")
	}
	if strings.Contains(report.LogTail, "CRON") {
		t.Error("the first, half-cut line should have been dropped")
	}
}

func TestSpell(t *testing.T) {
	for _, tc := range []struct {
		seconds int64
		want    string
	}{
		{0, "moments"},
		{1, "a second"},
		{45, "45 seconds"},
		{90, "a minute"},
		{600, "10 minutes"},
		{4000, "an hour"},
		{21600, "6 hours"},
	} {
		if got := spell(tc.seconds); got != tc.want {
			t.Errorf("spell(%d) = %q, want %q", tc.seconds, got, tc.want)
		}
	}
}
