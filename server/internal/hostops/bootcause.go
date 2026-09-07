package hostops

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Turning a log into a guess.
//
// Everything here is a rule of thumb written down. There is no way to ask a
// machine why it restarted — the question is answered by what it did and did
// not say on the way out — so this is a table of the things Linux says when it
// is dying, a measure of how close each one was to the moment the machine came
// back, and an order of precedence between them.
//
// Two decisions shape all of it.
//
// The first is that *when* a line was logged matters as much as what it says. A
// machine that ran out of memory at nine and restarted at three did not restart
// because it ran out of memory. So every sign carries how many seconds before
// the restart it was written, and only the ones inside a couple of minutes of
// it are allowed to be the cause; the rest are kept, and shown, as the weather
// rather than the event.
//
// The second is that the most valuable finding is often an absence. A machine
// that was asked to restart says so, at length, in a dozen lines of systemd
// shutting things down. A machine that lost power says nothing at all: its log
// stops mid-sentence. That absence cannot be distinguished from a lock-up so
// complete the kernel never got to write about it, and HostMan says so in
// those words instead of picking the more dramatic of the two.

// Sign kinds. These group the patterns below; the verdict is decided on kinds,
// not on individual lines.
const (
	// SignShutdown is systemd taking the machine down in an orderly way.
	SignShutdown = "shutdown"
	// SignRequested is something naming itself as the one that asked.
	SignRequested = "requested"
	SignPanic     = "panic"
	SignLockup    = "lockup"
	SignOOM       = "oom"
	SignOverheat  = "overheat"
	// SignUndervoltage is the Raspberry Pi's supply sagging below what the
	// board needs.
	SignUndervoltage = "undervoltage"
	// SignStorage is the disk or the SD card erroring under the machine.
	SignStorage = "storage"
)

// nearWindow is how close to the restart a line has to be to be treated as part
// of it. Two minutes covers a machine that took its time dying — a thermal
// shutdown syncs its disks first, an out-of-memory kill can thrash for a while
// before the machine gives up — without letting the morning's warnings explain
// the afternoon's restart.
const nearWindow = 2 * time.Minute

// nearSkew allows a line to be stamped slightly after the machine came back
// without being thrown away. The stamps and the uptime come from the same host
// but not from the same clock read, and a log written across an NTP step can
// land the wrong side of the boot by a second or two.
const nearSkew = time.Minute

// nearLines is how many lines from the end of a chunk count as "near" for a
// line whose timestamp could not be read at all. It is a poor substitute for a
// clock and is only reached for on a log HostMan could not date.
const nearLines = 25

// maxSignLine is how much of a matching line to keep. Kernel lines carry
// register dumps that are meaningless on a phone and endless anywhere.
const maxSignLine = 300

// bootPattern is one well-known thing a dying Linux says.
type bootPattern struct {
	kind  string
	label string
	re    *regexp.Regexp
	// detail is the capture group worth pulling out on its own — the panic's
	// reason, the process the out-of-memory killer picked. 0 for none.
	detail int
}

// bootPatterns is the table, most specific first within each kind. Every one of
// these is a message the kernel or systemd actually emits; where a message has
// changed wording across versions, the pattern covers both spellings rather
// than the newest, because the machine asking the question is usually the one
// running the older kernel.
var bootPatterns = []bootPattern{
	// --- it was asked to go ---
	{SignShutdown, "systemd shut the machine down", regexp.MustCompile(`systemd-shutdown\[\d+\]:\s*(Rebooting|Powering off|Halting|Syncing filesystems)`), 0},
	// systemd has written this target three ways across versions — "Reached
	// target Shutdown.", then "Reached target shutdown.target - System
	// Shutdown." — so the name is matched anywhere after the phrase rather than
	// in the position one version happened to put it.
	{SignShutdown, "systemd reached its shutdown target", regexp.MustCompile(`Reached target (?i:.*(shutdown|reboot|power-?off|final step|unmount all filesystems))`), 0},
	{SignShutdown, "systemd began shutting down", regexp.MustCompile(`systemd\[1\]:\s*Shutting down\.`), 0},
	{SignShutdown, "the journal was closed on the way out", regexp.MustCompile(`systemd-journald\[\d+\]:\s*Journal stopped`), 0},

	// --- and this is who asked ---
	{SignRequested, "logind announced the restart", regexp.MustCompile(`systemd-logind\[\d+\]:\s*(The system will (?:reboot|power off) now|System is (?:rebooting|powering down|shutting down))`), 1},
	{SignRequested, "someone ran a restart command", regexp.MustCompile(`sudo:.*COMMAND=(\S*(?:/reboot|/shutdown|/poweroff|/halt)\S*|\S*systemctl\s+(?:reboot|poweroff|halt))`), 1},
	{SignRequested, "shutdown was scheduled", regexp.MustCompile(`shutdown\[\d+\]:.*shutting down for system (\w+)`), 1},

	// --- the kernel stopped on purpose ---
	{SignPanic, "Kernel panic", regexp.MustCompile(`Kernel panic - not syncing:\s*(.*)`), 1},
	{SignPanic, "A crash was triggered deliberately", regexp.MustCompile(`sysrq:.*Trigger a crash`), 0},
	{SignPanic, "Kernel oops", regexp.MustCompile(`Internal error: Oops`), 0},
	{SignPanic, "Kernel oops", regexp.MustCompile(`\bOops:\s*[0-9a-fx]+\s*\[#\d+\]`), 0},
	{SignPanic, "The kernel dereferenced something it should not have", regexp.MustCompile(`(?:BUG: )?[Uu]nable to handle kernel (NULL pointer dereference|paging request)`), 1},
	// "Fatal exception" is deliberately not a pattern of its own. The kernel
	// only ever prints it alongside the oops it came from and quotes it back in
	// the panic line, so matching it would turn one death into three findings.

	// --- the kernel was alive and stuck, which is what a watchdog resets ---
	{SignLockup, "A CPU locked up", regexp.MustCompile(`(?:watchdog|NMI watchdog):\s*BUG: soft lockup - CPU#(\d+) stuck`), 1},
	{SignLockup, "A CPU locked up hard", regexp.MustCompile(`(?:watchdog:\s*)?(?:BUG: )?[Hh]ard LOCKUP`), 0},
	{SignLockup, "The kernel stalled", regexp.MustCompile(`INFO: rcu_\w+ (?:self-)?detected stalls?`), 0},
	{SignLockup, "A task hung", regexp.MustCompile(`INFO: task (\S+):\d+ blocked for more than \d+ seconds`), 1},

	// --- it ran out of memory ---
	{SignOOM, "The out-of-memory killer chose a process", regexp.MustCompile(`Out of memory: Kill(?:ed)? process \d+ \(([^)]+)\)`), 1},
	{SignOOM, "The out-of-memory killer chose a process", regexp.MustCompile(`Memory cgroup out of memory: Killed process \d+ \(([^)]+)\)`), 1},
	{SignOOM, "Nothing was left to kill for memory", regexp.MustCompile(`Out of memory and no killable processes`), 0},
	{SignOOM, "A process asked for memory that was not there", regexp.MustCompile(`(\S+) invoked oom-killer`), 1},
	{SignOOM, "A process asked for memory that was not there", regexp.MustCompile(`oom-kill:constraint=`), 0},

	// --- it got too hot ---
	{SignOverheat, "A critical temperature was reached", regexp.MustCompile(`[Cc]ritical temperature reached(?:\s*\((-?\d+)\s*C\))?`), 1},
	{SignOverheat, "The CPU was throttled for heat", regexp.MustCompile(`temperature above threshold, cpu clock throttled`), 0},

	// --- the supply sagged, which on a Pi is the usual answer ---
	{SignUndervoltage, "Under-voltage detected", regexp.MustCompile(`[Uu]nder-?voltage detected`), 0},
	{SignUndervoltage, "The supply came back up", regexp.MustCompile(`[Vv]oltage normali[sz]ed`), 0},

	// --- the storage went out from under it ---
	{SignStorage, "The SD card stopped answering", regexp.MustCompile(`mmc\d+: (Timeout waiting for hardware interrupt|cannot verify|error -\d+)`), 1},
	{SignStorage, "The SD card errored", regexp.MustCompile(`mmcblk\d+: (error|timed out)`), 1},
	{SignStorage, "A read or write failed", regexp.MustCompile(`(?:blk_update_request|end_request): (?:I/O|critical (?:medium|target)) error`), 0},
	{SignStorage, "A read or write failed", regexp.MustCompile(`Buffer I/O error on dev|I/O error, dev \S+, sector`), 0},
	{SignStorage, "The filesystem reported errors", regexp.MustCompile(`EXT4-fs error|XFS \(\S+\): (?:metadata I/O error|Corruption)`), 0},
	{SignStorage, "The filesystem went read-only", regexp.MustCompile(`Remounting filesystem read-only`), 0},
}

// collectSigns runs the table over every chunk of log that came back and keeps
// one sign per finding, the one closest to the moment the machine came back.
//
// A panic loud enough to be in the tail is also a warning and so appears in
// both chunks; keeping one sign per label is what stops that from reading as
// two separate findings. Where a sign genuinely happened more than once — an
// under-voltage warning usually has, many times — the count says so and the
// line kept is the last one.
func collectSigns(bootedAt time.Time, zone *time.Location, chunks ...string) []Sign {
	type candidate struct {
		sign  Sign
		timed bool
	}
	best := map[string]*candidate{}

	for _, chunk := range chunks {
		lines := strings.Split(strings.TrimSuffix(chunk, "\n"), "\n")
		for i, line := range lines {
			if strings.TrimSpace(line) == "" {
				continue
			}
			at, timed := lineTime(line, zone, bootedAt)
			// One line, one finding per label. Some things are matched two ways
			// on purpose — an oops is written differently on ARM and on x86 —
			// and a line that satisfies both is still one oops, not two.
			counted := map[string]bool{}
			for _, pattern := range bootPatterns {
				if counted[pattern.label] {
					continue
				}
				match := pattern.re.FindStringSubmatch(line)
				if match == nil {
					continue
				}
				counted[pattern.label] = true
				found := Sign{
					Kind:  pattern.kind,
					Label: pattern.label,
					Line:  trimLine(line),
					Count: 1,
				}
				if pattern.detail > 0 && pattern.detail < len(match) {
					found.Detail = strings.TrimSpace(match[pattern.detail])
				}
				if timed && !bootedAt.IsZero() {
					found.BeforeS = int64(bootedAt.Sub(at).Seconds())
					found.Near = at.Before(bootedAt.Add(nearSkew)) && bootedAt.Sub(at) <= nearWindow
				} else {
					// No clock to go on: only the very end of the chunk can be
					// claimed as part of the restart.
					found.Near = len(lines)-i <= nearLines
				}

				previous, seen := best[pattern.label]
				if !seen {
					best[pattern.label] = &candidate{sign: found, timed: timed}
					continue
				}
				previous.sign.Count++
				// Nearer the restart wins, and a dated line beats an undated
				// one whatever position it was in.
				switch {
				case timed && !previous.timed,
					timed && previous.timed && found.BeforeS < previous.sign.BeforeS,
					!timed && !previous.timed:
					count := previous.sign.Count
					previous.sign = found
					previous.sign.Count = count
					previous.timed = timed
				}
			}
		}
	}

	signs := make([]Sign, 0, len(best))
	for _, c := range best {
		signs = append(signs, c.sign)
	}
	// Nearest the restart first, because that is the reading order of the
	// screen: the thing that happened last is the thing that happened.
	sort.SliceStable(signs, func(i, j int) bool {
		if signs[i].Near != signs[j].Near {
			return signs[i].Near
		}
		if signs[i].BeforeS != signs[j].BeforeS {
			return signs[i].BeforeS < signs[j].BeforeS
		}
		return signs[i].Label < signs[j].Label
	})
	return signs
}

func trimLine(line string) string {
	line = strings.TrimSpace(line)
	if len(line) > maxSignLine {
		return line[:maxSignLine] + "…"
	}
	return line
}

// --- the verdict ---

// uncleanOrder is the precedence between causes when more than one sign sits
// close to the restart. It reads as a chain of consequence rather than a
// ranking of severity: heat and power are conditions the machine was in, and
// a panic or a lock-up is what those conditions did to it, so the condition is
// named ahead of its symptom. Storage comes before a plain lock-up because a
// card that has stopped answering is why the tasks hung, not the other way
// round.
var uncleanOrder = []string{
	SignOverheat,
	SignUndervoltage,
	SignOOM,
	SignPanic,
	SignStorage,
	SignLockup,
}

// diagnose fills in the verdict, its confidence, and the reasoning that led to
// it. It is deliberately the only place any of that is decided: the API returns
// the report as it stands and the UI renders it, so there is one account of why
// HostMan thinks what it thinks rather than three that can drift apart.
func diagnose(r *BootReport) {
	r.Reasons = []string{}

	nearest := func(kind string) *Sign {
		for i := range r.Signs {
			if r.Signs[i].Kind == kind && r.Signs[i].Near {
				return &r.Signs[i]
			}
		}
		return nil
	}
	anywhere := func(kind string) *Sign {
		for i := range r.Signs {
			if r.Signs[i].Kind == kind {
				return &r.Signs[i]
			}
		}
		return nil
	}

	// What wtmp thinks. It is the one record that survives a host with no
	// persistent journal, so it gets asked first and is often all there is.
	//
	// It takes two records to have an opinion: whether this boot was asked for
	// is a question about what lies below it, and the oldest boot wtmp
	// remembers has nothing below it to answer with.
	wtmpKnown := r.CleanKnown && len(r.Restarts) >= 2
	wtmpClean := wtmpKnown && r.Restarts[0].Clean

	shutdown := nearest(SignShutdown)

	switch {
	case shutdown != nil || wtmpClean:
		diagnoseClean(r, shutdown, nearest(SignRequested), wtmpKnown, wtmpClean)
	default:
		diagnoseUnclean(r, nearest, anywhere, wtmpKnown)
	}

	appendCorroboration(r, anywhere)
	appendHistory(r)
	appendGaps(r)
}

// diagnoseClean covers a restart something asked for, which is most of them and
// the one worth ruling in quickly so that "it restarts at random" can mean what
// it says.
func diagnoseClean(r *BootReport, shutdown, requested *Sign, wtmpKnown, wtmpClean bool) {
	r.Cause = CauseClean
	r.Headline = "Something asked it to restart"
	r.Detail = "The machine shut its services down and unmounted its disks on the way out, " +
		"which is what a restart someone or something asked for looks like. Nothing here points at a fault."

	switch {
	case shutdown != nil && wtmpClean:
		r.Confidence = ConfidenceCertain
	case shutdown != nil && wtmpKnown && !wtmpClean:
		// The log says it began going down and wtmp has no record of it. The
		// log is the more specific of the two and wins, but the disagreement is
		// worth putting on screen rather than resolving quietly.
		r.Confidence = ConfidenceLikely
		r.Reasons = append(r.Reasons, "The log shows it shutting down, but no shutdown was recorded — it may not have finished.")
	default:
		r.Confidence = ConfidenceLikely
	}

	if shutdown != nil {
		r.Reasons = append(r.Reasons, fmt.Sprintf("%s, %s before it came back.", shutdown.Label, spell(shutdown.BeforeS)))
	}
	if wtmpClean {
		r.Reasons = append(r.Reasons, "A shutdown was recorded before this boot, so the machine was told to go.")
	}
	if requested != nil {
		who := requested.Label
		if requested.Detail != "" {
			who = fmt.Sprintf("%s: %s", requested.Label, requested.Detail)
		}
		r.Reasons = append(r.Reasons, capitalize(who)+".")
	}
	if r.PreviousKernel != "" {
		r.Headline = "It was updated and restarted"
		r.Detail = "It went down on one kernel and came back on another, which is a restart that finished an update rather than one that needs explaining."
		r.Confidence = ConfidenceCertain
		r.Reasons = append(r.Reasons, fmt.Sprintf("It came back on %s, having gone down on %s.", r.Kernel, r.PreviousKernel))
	}
}

// diagnoseUnclean covers the case worth having this screen for: the machine
// went down without being asked, and what it said on the way out — or did not —
// is the only evidence there is.
func diagnoseUnclean(r *BootReport, nearest, anywhere func(string) *Sign, wtmpKnown bool) {
	if wtmpKnown {
		r.Reasons = append(r.Reasons, "Nothing recorded a shutdown before this boot, so the machine was not asked to go.")
	}

	for _, kind := range uncleanOrder {
		sign := nearest(kind)
		if sign == nil {
			continue
		}
		// A kind in the order above with no wording below it would leave the
		// verdict blank, so the wording is what decides whether it was handled
		// rather than the two lists being trusted to stay in step.
		if !explainSign(r, kind, sign) {
			continue
		}
		r.Reasons = append(r.Reasons, fmt.Sprintf("%s, %s before it came back.", withDetail(sign), spell(sign.BeforeS)))
		return
	}

	// Nothing near the restart. What is left is the absence itself, which still
	// separates two cases: a machine whose supply has been sagging all along,
	// and a machine that gave no warning of any kind.
	if r.Source == SourceNone {
		r.Cause = CauseUnknown
		r.Confidence = ConfidenceUnclear
		r.Headline = "There is no record of it to read"
		r.Detail = "Nothing on this machine kept a log of the boot that ended, so there is nothing to look through. " +
			"What is below is everything HostMan could find without one."
		return
	}

	if undervoltage := anywhere(SignUndervoltage); undervoltage != nil ||
		(r.Throttle != nil && (r.Throttle.UnderVoltage || r.Throttle.UnderVoltageNow)) {
		r.Cause = CausePower
		r.Confidence = ConfidenceLikely
		r.Headline = "It lost power, most likely"
		r.Detail = "The log stops mid-sentence: nothing asked the machine to go and nothing complained on the way out. " +
			"That is what losing power looks like from the inside — and this board has been running on a supply that " +
			"does not quite keep up, which is the usual reason a Raspberry Pi restarts at random."
		if undervoltage != nil {
			r.Reasons = append(r.Reasons, fmt.Sprintf("The supply had already dipped during that boot: %s.", withDetail(undervoltage)))
		}
		return
	}

	r.Cause = CauseUnknown
	r.Confidence = ConfidenceUnclear
	r.Headline = "It went down without saying why"
	r.Detail = "The log ends in the middle of ordinary work, with no panic, no shutdown and no complaint. " +
		"Losing power looks exactly like this, and so does a lock-up hard enough that the kernel never got to write " +
		"about it — HostMan will not pick between the two on no evidence."
	r.Reasons = append(r.Reasons, "The last thing it said was routine, so whatever happened left no trace.")
}

// explainSign turns the nearest sign into the verdict it implies, and reports
// whether it had anything to say. The wording is the point: each of these is a
// different thing to go and do next, and a screen that said "hardware fault"
// for all of them would send someone to buy the wrong part.
func explainSign(r *BootReport, kind string, sign *Sign) bool {
	switch kind {
	case SignOverheat:
		r.Cause = CauseOverheat
		r.Confidence = ConfidenceCertain
		r.Headline = "It got too hot and shut itself down"
		r.Detail = "The kernel reached a temperature it will not run through and took the machine down to protect it. " +
			"That is a case, a fan or a room, not a bug."

	case SignUndervoltage:
		r.Cause = CauseUndervoltage
		r.Confidence = ConfidenceLikely
		r.Headline = "The power supply dipped"
		r.Detail = "The board's firmware reported the supply below what it needs, moments before it went down. " +
			"On a Raspberry Pi this is the usual explanation for restarts that look random: a charger or a cable that " +
			"copes until the machine draws a little more."

	case SignOOM:
		r.Cause = CauseOOM
		r.Confidence = ConfidenceLikely
		r.Headline = "It ran out of memory"
		r.Detail = "The kernel was killing processes to stay alive just before the machine went. That usually costs one " +
			"program rather than the whole machine, so it is the best explanation here rather than a proven one — " +
			"unless what it killed was something the machine could not do without."

	case SignPanic:
		r.Cause = CausePanic
		r.Confidence = ConfidenceCertain
		r.Headline = "The kernel panicked"
		r.Detail = "The kernel hit something it could not carry on from and stopped where it stood. The line below is " +
			"the last thing it managed to write."

	case SignStorage:
		r.Cause = CauseStorage
		r.Confidence = ConfidenceLikely
		r.Headline = "The storage gave out under it"
		r.Detail = "The disk or the SD card stopped answering just before the machine went. A card that has started " +
			"erroring rarely stops, so this is worth acting on whether or not it is the whole story."

	case SignLockup:
		r.Cause = CauseLockup
		r.Confidence = ConfidenceLikely
		r.Headline = "The kernel locked up"
		r.Detail = "A CPU stopped making progress and said so. What restarts a machine in that state is usually the " +
			"hardware watchdog, which is working as intended — the question is what wedged it."

	default:
		return false
	}
	return true
}

// appendCorroboration adds what the machine says about itself now. None of it
// describes the boot that ended — the firmware's flags and the thermometer were
// both reset by the restart — so it is worded as a thing that is also true
// rather than as the finding, however tempting the opposite is.
func appendCorroboration(r *BootReport, anywhere func(string) *Sign) {
	if t := r.Throttle; t != nil {
		switch {
		case t.UnderVoltageNow:
			r.Reasons = append(r.Reasons, "The firmware says the supply is below spec right now.")
		case t.UnderVoltage:
			r.Reasons = append(r.Reasons, "The firmware says the supply has dipped below spec since this boot too, so it is still happening.")
		}
		// Capping the ARM frequency and the soft temperature limit are the
		// board's answer to heat; plain throttling is its answer to a sagging
		// supply, so saying that too would only repeat the line above.
		switch {
		case t.SoftTemp || t.SoftTempNow || t.Capped || t.CappedNow:
			r.Reasons = append(r.Reasons, "The firmware has pulled the clocks back for heat since this boot.")
		case (t.Throttled || t.ThrottledNow) && !t.UnderVoltage && !t.UnderVoltageNow:
			r.Reasons = append(r.Reasons, "The firmware has throttled the board since this boot.")
		}
	}
	if r.Watchdog != nil && len(r.Watchdog.Flags) > 0 {
		// Ordinary reboots on a Raspberry Pi go through the watchdog too, so
		// this is reported and never relied on.
		r.Reasons = append(r.Reasons, "The watchdog driver says: "+strings.Join(r.Watchdog.Flags, ", ")+".")
	}
	if sign := anywhere(SignStorage); sign != nil && r.Cause != CauseStorage {
		r.Reasons = append(r.Reasons, "The storage errored during that boot, whether or not it is what ended it.")
	}
	// A restart that changed the kernel is already the headline when something
	// asked for it. When nothing did, it is a different and more alarming fact
	// — the machine went down around an update — and still worth naming.
	if r.PreviousKernel != "" && r.Cause != CauseClean {
		r.Reasons = append(r.Reasons, fmt.Sprintf(
			"It came back on %s, having gone down on %s, so an update was in progress around this restart.",
			r.Kernel, r.PreviousKernel))
	}
}

// appendHistory says how often this has happened, which is the question behind
// the question: one unexplained restart is bad luck and a pattern is a fault.
func appendHistory(r *BootReport) {
	if !r.CleanKnown || len(r.Restarts) < 3 {
		return
	}
	if r.Unclean < 2 {
		return
	}
	r.Reasons = append(r.Reasons, fmt.Sprintf(
		"Of the last %d restarts this machine remembers, %d were not asked for.", len(r.Restarts), r.Unclean))
}

// appendGaps says what HostMan could not see, and is the most useful line on
// the screen when the verdict is "unknown". A journal kept in memory is the
// single commonest reason this question has no answer, and it is one command
// away from being fixed — which is what the button on this screen does.
func appendGaps(r *BootReport) {
	switch {
	case !r.Journal:
		r.Reasons = append(r.Reasons, "This host has no systemd journal, so the only log HostMan could look for was rsyslog's.")
	case r.Source == SourceNone && !r.Persistent:
		r.Reasons = append(r.Reasons, "This host's journal is kept in memory and does not survive a restart, so the log of the boot that ended is gone.")
	case r.Source == SourceLogFile:
		r.Reasons = append(r.Reasons, "The journal keeps nothing across restarts here, so this came out of "+r.LogFile+" instead.")
	case r.Source == SourceNone:
		r.Reasons = append(r.Reasons, "The journal is persistent here but has no record of a previous boot yet.")
	}
}

// withDetail writes a sign as a phrase, with the part worth quoting where there
// is one: "Kernel panic: Attempted to kill init!".
func withDetail(s *Sign) string {
	if s.Detail == "" {
		return s.Label
	}
	return s.Label + ": " + s.Detail
}

// spell writes a gap in seconds the way someone reading it would say it. It is
// only ever used for the distance between a log line and the restart, so it
// stops at hours.
func spell(seconds int64) string {
	switch {
	case seconds <= 0:
		return "moments"
	case seconds == 1:
		return "a second"
	case seconds < 60:
		return fmt.Sprintf("%d seconds", seconds)
	case seconds < 120:
		return "a minute"
	case seconds < 3600:
		return fmt.Sprintf("%d minutes", seconds/60)
	case seconds < 7200:
		return "an hour"
	default:
		return fmt.Sprintf("%d hours", seconds/3600)
	}
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
