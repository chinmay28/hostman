package hostops

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/chinmay28/deployer/server/internal/store"
)

// A torrent downloader is deluged running on the host, with HostMan driving it
// through deluge-console. Hand it a .torrent file or a magnet link from a phone
// and the files land on the host's own disk — which is the whole point: the
// machine with the disk does the downloading, and nothing goes through the
// phone.
//
// Four decisions shape the rest of this file.
//
// **Deluge is the host's to install, not HostMan's.** Everything else here is
// a package HostMan will fetch for you; this one is not, and deliberately.
// A BitTorrent client is a decision about what a machine does on a network, and
// on plenty of them it is a decision somebody else has already made — a seedbox
// with its own deluged, a distribution that packages it differently, a host
// where it should not be at all. So a host without it is told what to install
// rather than having it installed: `apt install deluged deluge-console`, in the
// screen's own words, and setup refuses until it is there.
//
// **The daemon HostMan runs is HostMan's own.** Its state lives in
// /var/lib/deployer-torrent and it answers on a port of its own rather than
// deluge's default, so a host that already runs deluged keeps running it,
// untouched, with its own torrents and its own client attached. Two daemons on
// one machine is not tidy, but it is honest — the alternative is guessing at
// somebody else's credentials and adding torrents to a session they did not
// expect.
//
// **It comes back after a reboot.** The unit is enabled, which is the opposite
// of the remote browser session's rule and for the opposite reason: a browser
// holding your logins should run for two minutes, and a download that takes six
// hours should survive the machine restarting in the middle of it. Deluge
// resumes what it was doing from its own state, so the reboot costs the
// download nothing.
//
// **The daemon's password never leaves the host.** deluged authenticates its
// clients out of an auth file; the password in it is generated on the host, and
// the scripts HostMan sends read it there. It is not stored by HostMan, not
// carried in an API response and not shown on a screen, because nothing here
// needs it — the only client is a command running on the same machine.
//
// Nothing on the host is sourced. The settings file is written as lines to be
// parsed, torrent ids are checked against a hash's alphabet before they reach a
// command line, and every value the caller supplied arrives as a quoted
// positional argument.

const (
	// TorrentUnit is the daemon's systemd unit. It is an ordinary unit file in
	// the administrator's own directory, so the Services screen lists it, shows
	// its journal and can stop it, like anything else installed by hand.
	TorrentUnit = "deployer-torrent.service"

	// torrentStateDir is deluged's config directory: its settings, its auth
	// file, and the state that lets it pick a half-finished download back up.
	// /var/lib is where a daemon's own state belongs.
	torrentStateDir = "/var/lib/deployer-torrent"

	// torrentPort is where deluged listens for its client. It is deluge's
	// default plus a hundred, on purpose: a host that already runs deluged is
	// using 58846, and HostMan starting a second daemon on it would leave
	// whichever lost the race dead in the journal. Both listen on loopback
	// only — deluged's allow_remote is off by default and HostMan never turns
	// it on.
	torrentPort = 58946

	// torrentAccount is the account HostMan's scripts authenticate as. deluged
	// keeps its own "localclient" alongside it, so a thin client somebody
	// already uses on this host carries on working.
	torrentAccount = "deployer"

	// MaxTorrentFileBytes is the largest .torrent file HostMan will carry to a
	// host. A torrent file is a list of hashes: a few kilobytes is ordinary and
	// a megabyte is a very large one, so anything past this is not a torrent
	// file that was picked by mistake.
	MaxTorrentFileBytes = 4 << 20

	// maxTorrents is how many the screen is told about. A phone is not where a
	// hundred-torrent queue is read, and every one of these crosses the wire.
	maxTorrents = 60
)

// TorrentDaemon is everything the screen needs in one round trip: what is
// installed, what HostMan has set up, whether it is running, and what it is
// downloading.
type TorrentDaemon struct {
	// Unit is the systemd unit, so start and stop go through the service API
	// that already knows how to wait for systemd.
	Unit string `json:"unit"`
	// Installed reports deluge being present on the host. It is the one thing
	// here HostMan will not install for you.
	Installed bool `json:"installed"`
	// Missing names the deluge commands that are not there, which is what the
	// screen turns into an apt line to run.
	Missing []string `json:"missing,omitempty"`
	// Version is deluged's own version, as it reports it.
	Version string `json:"version,omitempty"`
	// Configured reports HostMan having written the daemon onto this host.
	Configured bool `json:"configured"`
	// Ready means a torrent could be added right now: deluge installed, the
	// daemon written, and a unit systemd has loaded.
	Ready bool `json:"ready"`
	// Running reports whether the daemon is up, and Enabled whether it comes
	// back after a reboot.
	Running bool `json:"running"`
	Enabled bool `json:"enabled"`
	// Active and Sub are systemd's own words, for the times when "running" is
	// not the whole story: activating, failed, dead.
	Active string `json:"active,omitempty"`
	Sub    string `json:"sub,omitempty"`
	// Stale reports a daemon written by an older HostMan. Updating HostMan
	// does not rewrite what is on a host — setting it up again does.
	Stale bool `json:"stale,omitempty"`
	// User is the account the daemon runs as, and so the account that ends up
	// owning the files.
	User string `json:"user"`
	// Downloads is where finished files land. Before setup it is the folder
	// HostMan would use, so the screen has something to offer rather than an
	// empty field.
	Downloads string `json:"downloads"`
	// Free and Capacity are the disk behind that folder, in bytes. A torrent
	// that fills a Pi's card is the ordinary way this goes wrong, so the figure
	// belongs on the screen before the download rather than after it.
	Free     int64 `json:"free,omitempty"`
	Capacity int64 `json:"capacity,omitempty"`
	// Torrents is what the daemon is working on, newest first as deluge lists
	// them.
	Torrents []Torrent `json:"torrents"`
	// Asked reports that deluge itself answered this time. When it is false the
	// list is empty because nobody could be asked — the daemon is stopped, or it
	// did not answer — rather than because there is nothing to download, and a
	// screen that could not tell those apart would throw away a running download
	// the first time deluge was slow.
	Asked bool `json:"asked"`
	// Seeding is what deluge will do with a torrent once it has finished.
	Seeding TorrentSeeding `json:"seeding"`
	// ActiveLimit is how many torrents deluge works on at once — the rest wait
	// as Queued until a slot opens. It is -1 where there is no limit at all,
	// which is deluge's own way of saying so, and 0 where deluge could not be
	// asked.
	ActiveLimit int `json:"activeLimit,omitempty"`
	// Trouble is what deluge-console said when it could not be asked. It is a
	// state the screen reports rather than an error the request fails with:
	// everything else on this page is still true and still worth showing.
	Trouble string `json:"trouble,omitempty"`
}

// Torrent is one torrent, as deluge describes it.
type Torrent struct {
	// ID is the info hash deluge knows it by, and what an action names.
	ID string `json:"id"`
	// Name is the torrent's name — until the metadata for a magnet link
	// arrives, deluge answers with the hash instead.
	Name string `json:"name"`
	// State is deluge's own word: Downloading, Seeding, Paused, Queued,
	// Checking, Error.
	State string `json:"state"`
	// Progress is how far along it is, 0 to 100.
	Progress float64 `json:"progress"`
	// Done and Size are bytes: what has arrived, and what the whole thing is.
	Done int64 `json:"done"`
	Size int64 `json:"size"`
	// Down and Up are bytes per second.
	Down int64 `json:"down"`
	Up   int64 `json:"up"`
	// ETA is how long deluge thinks is left, in seconds. Zero where it will not
	// say — a paused torrent, or one it has no rate to guess from.
	ETA int64 `json:"eta,omitempty"`
	// ETAText is deluge's own words for the same thing, kept because "∞" says
	// something a zero cannot.
	ETAText string `json:"etaText,omitempty"`
	// Ratio is how much has been uploaded against how much came down.
	Ratio float64 `json:"ratio,omitempty"`
	// Folder is where this torrent's own files are going, which is the folder
	// the downloader had when it was added rather than the one it has now.
	Folder string `json:"folder,omitempty"`
	// Seeds and Peers are what this torrent is connected to, out of what it can
	// see — the two numbers deluge prints as "4 (23)".
	Seeds      int `json:"seeds"`
	SeedsTotal int `json:"seedsTotal"`
	Peers      int `json:"peers"`
	PeersTotal int `json:"peersTotal"`
}

// TorrentSeeding is what deluge does with a torrent once it has finished
// downloading. Left alone it seeds for ever, which is polite and is also how a
// list nobody is watching fills up with things that finished last month.
type TorrentSeeding struct {
	// Ratio is what a torrent has to have uploaded, against what it downloaded,
	// before deluge stops seeding it. Zero means never stop.
	Ratio float64 `json:"ratio"`
	// Remove takes the entry out of deluge when it stops, rather than leaving it
	// paused in the list. The files are never touched — deluge removes the
	// torrent, not the download.
	Remove bool `json:"remove"`
}

// settingKeys are the daemon settings HostMan reads back, by deluge's own
// names: the three that make up the seeding rule, and the queue's limit on how
// many torrents are worked on at once.
var settingKeys = []string{"stop_seed_at_ratio", "stop_seed_ratio", "remove_seed_at_ratio", "max_active_limit"}

// settingLine is one of them coming back: "stop_seed_ratio: 2.0".
var settingLine = regexp.MustCompile(`^(stop_seed_at_ratio|stop_seed_ratio|remove_seed_at_ratio|max_active_limit):\s*(\S+)\s*$`)

// MaxSeedRatio is as far as this will go. A ratio is a multiple of what was
// downloaded, so ten is a great deal of uploading and a hundred is a typing
// mistake.
const MaxSeedRatio = 100

// MaxActiveTorrents is as many as HostMan will ask deluge to work on at once.
// Past a couple of hundred a limit is not limiting anything, so a bigger
// number is a typing mistake — and "no limit at all" is asked for as -1, which
// is deluge's own word for it.
const MaxActiveTorrents = 200

// torrentPieces is what a host needs before any of this works: the daemon, and
// the client HostMan drives it with. Both come from deluge's own packages, and
// neither is installed by HostMan.
var torrentPieces = []string{"deluged", "deluge-console"}

// TorrentPackages is the apt line a host without deluge needs, so the screen
// and the README say the same thing.
const TorrentPackages = "deluged deluge-console"

// torrentIDPattern is an info hash: forty hex characters and nothing else. It
// is checked before it reaches a command line, and it is the only thing here
// that names a torrent — deluge will also accept a partial id or a name, which
// is convenient at a prompt and far too loose for an API.
var torrentIDPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// torrentActions is every action HostMan will take on one torrent.
var torrentActions = map[string]bool{"pause": true, "resume": true, "remove": true}

// ansiPattern strips the colour deluge-console adds when it thinks something is
// watching. Nothing should be reading these bytes as text before they are gone.
var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// torrentConsole is the one way HostMan talks to deluged, shared by every
// script that needs it so there is a single place where a connection is made.
//
// Four things about it are deluge's doing rather than choices:
//
//   - **The daemon is named with options, not with a `connect` command.**
//     `deluge-console "connect host user pass; info"` is the recipe every guide
//     gives and it is broken in deluge 2.1: the console dies inside its own
//     startup with an AttributeError and prints nothing anybody asked for. The
//     -d/-p/-U/-P options do the same job and work.
//
//   - **The command is one argument with its own quoting inside it.** The
//     console joins whatever it is given back into a string and splits it again
//     itself, so a folder with a space in it survives only if the quotes are
//     part of the value. This is also what keeps the subcommand's own -p (a
//     path) from being read as the console's -p (a port).
//
//   - **It is given a deadline.** A deluge-console that has finished its work
//     does not always exit — removing a torrent in 2.1 leaves it running
//     forever, and so does any command that errors — and an SSH session held
//     open by a hung client would be a phone waiting on nothing.
//
//   - **The password is read out of deluged's own auth file on the host.** That
//     is what keeps it there: HostMan never learns it, never stores it and
//     never sends it. It does reach the daemon as a command-line argument,
//     which another user on the host could read out of `ps` — the same trade
//     the `connect` form makes, on a daemon that only listens on loopback, and
//     the alternative is HostMan holding the password instead.
const torrentConsole = `console() {
  pw=$(awk -F: '$1 == "%[1]s" { print $2; exit }' "$conf/auth" 2>/dev/null)
  if [ -z "$pw" ]; then
    printf 'this host has no deluge account for HostMan yet\n' >&2
    return 3
  fi
  if command -v timeout >/dev/null 2>&1; then
    timeout "${console_wait:-15}" deluge-console -c "$conf" \
      -d 127.0.0.1 -p %[2]d -U %[1]s -P "$pw" "$1" 2>&1
  else
    deluge-console -c "$conf" -d 127.0.0.1 -p %[2]d -U %[1]s -P "$pw" "$1" 2>&1
  fi
}
`

// consoleScript renders the shared console helper for a script to embed.
func consoleScript() string {
	return fmt.Sprintf(torrentConsole, torrentAccount, torrentPort)
}

// torrentStatusScript answers everything about the downloader in one round
// trip: what deluge is installed, what HostMan has written, what systemd makes
// of the unit, how much disk is left, what the daemon is working on, and what
// it has been told to do with a torrent that has finished.
//
// It runs as the SSH user rather than as root, which is the exception on this
// screen and the correct one: the daemon runs as that user, its config
// directory is that user's, and deluge-console leaves files of its own in
// there. A status probe that ran as root would leave root-owned files in a
// directory the daemon then cannot write, which is a fault HostMan would have
// caused rather than found.
//
// It says which of the four things it did — asked, found the daemon stopped,
// found no console, found no downloader — because an empty listing means
// something different in each case, and a screen that could not tell them apart
// would replace a running download with "nothing is downloading" the first time
// deluge was slow to answer.
//
// The seeding policy rides along in the same console rather than a second one.
// Starting deluge-console is a second or two of python, and this is the one
// request on this screen that is asked again every few seconds.
//
// Nothing here writes, so it is safe to ask as often as a screen wants to.
const torrentStatusScript = `set -u
r=$1
u=$2
conf="$r%[1]s"
%[2]s

printf '@@have\n'
for b in %[3]s; do
  command -v "$b" >/dev/null 2>&1 && printf '%%s\n' "$b"
done

printf '@@version\n'
if command -v deluged >/dev/null 2>&1; then
  deluged --version 2>/dev/null | head -1
fi

printf '@@config\n'
cat "$conf/deployer.conf" 2>/dev/null

printf '@@home\n'
home=$(getent passwd "$u" 2>/dev/null | cut -d: -f6)
[ -n "$home" ] || home="/home/$u"
printf '%%s\n' "$home"

printf '@@unit\n'
if command -v systemctl >/dev/null 2>&1; then
  systemctl show --no-pager -p LoadState -p ActiveState -p SubState -p UnitFileState -- %[4]s 2>/dev/null
fi

dl=$(sed -n 's/^DOWNLOADS=//p' "$conf/deployer.conf" 2>/dev/null | head -1)
[ -n "$dl" ] || dl="$home/Downloads/torrents"
printf '@@downloads\n%%s\n' "$dl"

printf '@@disk\n'
# The folder itself may not exist yet, in which case the disk behind the place
# it would go is the answer worth having.
d="$r$dl"
while [ -n "$d" ] && [ ! -d "$d" ]; do d=$(dirname -- "$d"); [ "$d" = / ] && break; done
df -Pk -- "$d" 2>/dev/null | tail -1

# Asking a daemon that is not running is a slow way of being told it is not
# running, and systemd already knows.
list=""
ask="not-set-up"
if [ -f "$conf/deployer.conf" ]; then
  if ! command -v deluge-console >/dev/null 2>&1; then
    ask="no-console"
  elif command -v systemctl >/dev/null 2>&1 && ! systemctl is-active --quiet -- %[4]s 2>/dev/null; then
    ask="stopped"
  else
    list=$(console "info --verbose; config %[6]s")
    ask="asked:$?"
  fi
fi
printf '@@ask\n%%s\n' "$ask"
printf '@@torrents\n'
printf '%%s\n' "$list" | head -c %[5]d

# Reading the state of the downloader never fails: every part of it is
# optional, and a piece that is not there is an answer rather than an error.
# What comes back from a host with none of it is "deluge is not installed",
# which is exactly what the screen offers to fix.
exit 0
`

// MaxTorrentListBytes is how much of deluge's own listing comes back. Seven
// lines a torrent, sixty torrents, and room for a daemon in a bad mood.
const MaxTorrentListBytes = 64 << 10

// TorrentStatus reports on a host's downloader: what is installed, what is set
// up, and what it is working on.
func (s *Service) TorrentStatus(ctx context.Context, h *store.Host) (*TorrentDaemon, error) {
	if !userPattern.MatchString(h.Username) {
		return nil, invalid("%q is not a user a downloader can run as", h.Username)
	}
	script := fmt.Sprintf(torrentStatusScript,
		torrentStateDir, consoleScript(), strings.Join(torrentPieces, " "),
		TorrentUnit, MaxTorrentListBytes, strings.Join(settingKeys, " "))
	res, err := s.run(ctx, h, asUser(script, "", h.Username), "")
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, failure(res, "could not read the downloader on "+h.Name)
	}
	return parseTorrentStatus(res.Stdout, h.Username), nil
}

// parseTorrentStatus turns the script's output into the one answer the screen
// works from. A host with nothing installed is not an error: it reads as a
// downloader that is absent, which is what the screen explains how to fix.
func parseTorrentStatus(out, user string) *TorrentDaemon {
	found := sections(out)
	daemon := &TorrentDaemon{
		Unit:      TorrentUnit,
		User:      user,
		Downloads: first(found["downloads"]),
		Torrents:  []Torrent{},
	}

	have := map[string]bool{}
	for _, line := range found["have"] {
		have[strings.TrimSpace(line)] = true
	}
	for _, piece := range torrentPieces {
		if !have[piece] {
			daemon.Missing = append(daemon.Missing, piece)
		}
	}
	daemon.Installed = len(daemon.Missing) == 0
	// "deluged: 2.0.3", or "deluged 2.0.3" depending on the version doing the
	// answering. The number is the part anybody wanted.
	daemon.Version = strings.TrimSpace(strings.TrimPrefix(first(found["version"]), "deluged:"))

	// A settings file with anything in it is a host HostMan has written the
	// downloader onto, whether or not every line of it still parses.
	daemon.Configured = len(found["config"]) > 0
	revision := ""
	for _, line := range found["config"] {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "DOWNLOADS":
			if value != "" {
				daemon.Downloads = value
			}
		case "REVISION":
			revision = value
		}
	}
	// A host that has a downloader at all, running a unit this build did not
	// write, is a host where setting up again is the whole of the fix.
	daemon.Stale = revision != "" && revision != torrentRevision()

	props := map[string]string{}
	for _, line := range found["unit"] {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			props[key] = value
		}
	}
	daemon.Active = props["ActiveState"]
	daemon.Sub = props["SubState"]
	daemon.Running = daemon.Active == "active" || daemon.Active == "activating"
	daemon.Enabled = props["UnitFileState"] == "enabled"
	daemon.Ready = daemon.Installed && daemon.Configured && props["LoadState"] == "loaded"

	daemon.Free, daemon.Capacity = parseDiskFree(first(found["disk"]))

	// The listing and the daemon's settings come back from one console, so
	// they arrive in one section and the settings are taken out of it first.
	listing, seeding, limit := splitSettings(strings.Join(found["torrents"], "\n"))
	daemon.Seeding = seeding
	daemon.ActiveLimit = limit
	daemon.Torrents, daemon.Trouble = parseTorrentList(listing)

	// What the probe managed to do decides what an empty list means.
	ask := first(found["ask"])
	if code, ok := strings.CutPrefix(ask, "asked:"); ok {
		daemon.Asked = strings.TrimSpace(code) == "0"
		if !daemon.Asked && daemon.Trouble == "" {
			daemon.Trouble = "deluge did not answer (exit " + strings.TrimSpace(code) + ")"
		}
	}
	// A daemon that is stopped is not trouble: the screen says so already, and
	// says it in a place with a button under it.
	if ask == "stopped" {
		daemon.Trouble = ""
	}
	return daemon
}

// splitSettings takes the settings lines out of what the console printed and
// hands back the rest, which is the torrent listing.
func splitSettings(out string) (string, TorrentSeeding, int) {
	seeding := TorrentSeeding{}
	stop := false
	limit := 0
	lines := strings.Split(out, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		match := settingLine.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			kept = append(kept, line)
			continue
		}
		switch match[1] {
		case "stop_seed_at_ratio":
			stop = strings.EqualFold(match[2], "true")
		case "stop_seed_ratio":
			seeding.Ratio, _ = strconv.ParseFloat(match[2], 64)
		case "remove_seed_at_ratio":
			seeding.Remove = strings.EqualFold(match[2], "true")
		case "max_active_limit":
			limit, _ = strconv.Atoi(match[2])
		}
	}
	// Deluge keeps a ratio whether or not it is using one, so the switch is
	// what decides: not stopping at all is a ratio of nothing.
	if !stop {
		seeding.Ratio = 0
	}
	// Anything below -1 is deluge being asked something it never was; it reads
	// as not knowing rather than as a limit.
	if limit < -1 {
		limit = 0
	}
	return strings.Join(kept, "\n"), seeding, limit
}

// parseDiskFree reads one `df -Pk` line: the filesystem, its size, what is
// used, what is available. POSIX output is fixed at those columns, which is
// what -P is for.
func parseDiskFree(line string) (free, capacity int64) {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return 0, 0
	}
	total, _ := strconv.ParseInt(fields[1], 10, 64)
	available, _ := strconv.ParseInt(fields[3], 10, 64)
	return available * 1024, total * 1024
}

// The lines deluge prints for one torrent, as `info --verbose` writes them:
//
//	Name: ubuntu-24.04.1-desktop-amd64.iso
//	ID: 8cbd1d8a2cbd0f6c9b1b3b0e5b3b3b3b3b3b3b3b
//	State: Downloading Down Speed: 1.2 M/s Up Speed: 24.0 K/s
//	Seeds: 12 (145) Peers: 3 (58) Availability: 2.31 Seed Rank: -
//	Size: 512.0 M/5.7 G Downloaded: 512.0 M Uploaded: 0 B Share Ratio: -1.00
//	ETA: 12m 30s Seeding: - Active: 4m 2s
//	Tracker status: releases.ubuntu.com: Announce OK
//	Progress: 8.77% [##----------------]
//	Download Folder: /home/pi/Downloads/torrents
//
// Reading it with a regexp per fact is not elegance for its own sake. Deluge
// puts several of them on one line, moves them between lines by version, and
// leaves out the ones it has nothing to say about — a paused torrent has no
// speeds and no swarm, a finished one has no progress bar and one size instead
// of two, and a magnet has almost nothing until its metadata arrives. Asking
// each question separately means a listing that answers six of them answers
// six, where a line-shaped parser would answer none.
var (
	torrentNameLine   = regexp.MustCompile(`^Name:\s*(.+?)\s*$`)
	torrentIDLine     = regexp.MustCompile(`^ID:\s*([0-9a-fA-F]{40})`)
	torrentFolderAt   = regexp.MustCompile(`Download Folder:\s*(.+?)\s*$`)
	torrentStateAt    = regexp.MustCompile(`State:\s*([A-Za-z ]+?)(?:\s+(?:Down Speed|Up Speed|ETA|Seeds|Peers|Progress|Size|Ratio):|\s*$)`)
	torrentDownAt     = regexp.MustCompile(`Down Speed:\s*([0-9.]+\s*[A-Za-z]*)/s`)
	torrentUpAt       = regexp.MustCompile(`Up Speed:\s*([0-9.]+\s*[A-Za-z]*)/s`)
	torrentETAAt      = regexp.MustCompile(`ETA:\s*(.*?)(?:\s+[A-Za-z][A-Za-z ]*:|\s*$)`)
	torrentSizePair   = regexp.MustCompile(`Size:\s*([0-9.]+\s*[A-Za-z]*)/([0-9.]+\s*[A-Za-z]*)`)
	torrentSizeOne    = regexp.MustCompile(`Size:\s*([0-9.]+\s*[A-Za-z]*)(?:\s+[A-Za-z][A-Za-z ]*:|\s*$)`)
	torrentRatioAt    = regexp.MustCompile(`Ratio:\s*(-?[0-9.]+)`)
	torrentSeedsAt    = regexp.MustCompile(`Seeds:\s*(-?[0-9]+)\s*\((-?[0-9]+)\)`)
	torrentPeersAt    = regexp.MustCompile(`Peers:\s*(-?[0-9]+)\s*\((-?[0-9]+)\)`)
	torrentProgressAt = regexp.MustCompile(`Progress:\s*([0-9.]+)%`)
)

// parseTorrentList reads what `deluge-console info` printed. Anything that is
// not a torrent — a daemon refusing a connection, a python traceback — comes
// back as trouble to report rather than as an empty list, because "nothing is
// downloading" and "nobody could be asked" are different answers.
func parseTorrentList(out string) ([]Torrent, string) {
	out = ansiPattern.ReplaceAllString(out, "")
	torrents := []Torrent{}
	var current *Torrent
	var noise []string

	flush := func() {
		if current != nil && current.ID != "" && len(torrents) < maxTorrents {
			// Deluge leaves the progress bar out entirely once a torrent is
			// finished — there is nothing left to draw — so a torrent with no
			// bar and everything downloaded is at 100% rather than at nought.
			if current.Progress == 0 && current.Size > 0 && current.Done >= current.Size {
				current.Progress = 100
			}
			torrents = append(torrents, *current)
		}
		current = nil
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSuffix(strings.TrimSpace(line), "\r")
		if line == "" {
			continue
		}
		if match := torrentNameLine.FindStringSubmatch(line); match != nil {
			flush()
			current = &Torrent{Name: match[1]}
			continue
		}
		if current == nil {
			noise = append(noise, line)
			continue
		}
		applyTorrentLine(current, line)
	}
	flush()

	if len(torrents) > 0 {
		return torrents, ""
	}
	return torrents, torrentTrouble(noise)
}

// applyTorrentLine reads every fact a line happens to carry into the torrent
// being assembled.
func applyTorrentLine(t *Torrent, line string) {
	if match := torrentIDLine.FindStringSubmatch(line); match != nil {
		t.ID = strings.ToLower(match[1])
	}
	if match := torrentStateAt.FindStringSubmatch(line); match != nil {
		t.State = strings.TrimSpace(match[1])
	}
	if match := torrentDownAt.FindStringSubmatch(line); match != nil {
		t.Down = parseByteSize(match[1])
	}
	if match := torrentUpAt.FindStringSubmatch(line); match != nil {
		t.Up = parseByteSize(match[1])
	}
	// A torrent with everything downloaded is given one size rather than the
	// same figure twice, which is deluge being tidy and would otherwise read as
	// a torrent of no size at all.
	if match := torrentSizePair.FindStringSubmatch(line); match != nil {
		t.Done = parseByteSize(match[1])
		t.Size = parseByteSize(match[2])
	} else if match := torrentSizeOne.FindStringSubmatch(line); match != nil {
		t.Size = parseByteSize(match[1])
		t.Done = t.Size
	}
	// "Share Ratio: -1.00" is deluge for "nothing has been uploaded yet", not a
	// ratio below zero.
	if match := torrentRatioAt.FindStringSubmatch(line); match != nil {
		if ratio, err := strconv.ParseFloat(match[1], 64); err == nil && ratio >= 0 {
			t.Ratio = ratio
		}
	}
	// Likewise "(-1)": deluge cannot see how many seeds a torrent has until a
	// tracker has told it, and a negative count on a screen is worse than none.
	if match := torrentSeedsAt.FindStringSubmatch(line); match != nil {
		t.Seeds = atLeastZero(match[1])
		t.SeedsTotal = atLeastZero(match[2])
	}
	if match := torrentPeersAt.FindStringSubmatch(line); match != nil {
		t.Peers = atLeastZero(match[1])
		t.PeersTotal = atLeastZero(match[2])
	}
	if match := torrentProgressAt.FindStringSubmatch(line); match != nil {
		t.Progress, _ = strconv.ParseFloat(match[1], 64)
	}
	if match := torrentFolderAt.FindStringSubmatch(line); match != nil {
		t.Folder = match[1]
	}
	// The ETA shares its line with two other times, so it is read only where
	// the line says "ETA" and only as far as the next label.
	if strings.Contains(line, "ETA:") {
		if match := torrentETAAt.FindStringSubmatch(line); match != nil {
			text := strings.TrimSpace(match[1])
			// "-" is deluge for nothing to say, and saying nothing is better.
			if text != "-" {
				t.ETAText = text
			}
			t.ETA = parseTorrentETA(text)
		}
	}
}

// atLeastZero reads a count deluge may have written as -1, meaning it does not
// know yet.
func atLeastZero(value string) int {
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// troubleMarkers are what deluge says when it could not do the thing rather
// than when it had nothing to do.
var troubleMarkers = []string{
	"failed", "error", "refused", "traceback", "unable", "could not",
	"does not exist", "no such", "timed out", "denied",
}

// torrentTrouble turns whatever deluge-console said instead of a torrent into
// one sentence. Its own line is almost always the useful one — "Failed to
// connect to 127.0.0.1:58946" says the whole thing.
//
// Only a line that reads like a failure counts. A deluge that prints a
// deprecation notice, or a blank line, has still answered, and an empty list
// with a red banner over it would be a screen inventing a problem.
func torrentTrouble(lines []string) string {
	for _, line := range lines {
		lower := strings.ToLower(line)
		for _, marker := range troubleMarkers {
			if !strings.Contains(lower, marker) {
				continue
			}
			if len(line) > 300 {
				line = line[:300]
			}
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// byteUnits are the suffixes deluge prints. It uses the binary ones; the
// decimal spellings are here because a version that changes its mind should not
// turn a size into a zero.
var byteUnits = map[string]int64{
	"b":  1,
	"kb": 1 << 10, "kib": 1 << 10, "k": 1 << 10,
	"mb": 1 << 20, "mib": 1 << 20, "m": 1 << 20,
	"gb": 1 << 30, "gib": 1 << 30, "g": 1 << 30,
	"tb": 1 << 40, "tib": 1 << 40, "t": 1 << 40,
}

// parseByteSize turns "269.8 MiB" into bytes.
func parseByteSize(value string) int64 {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 0 {
		return 0
	}
	number, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || number < 0 {
		return 0
	}
	unit := int64(1)
	if len(fields) > 1 {
		scale, ok := byteUnits[strings.ToLower(fields[1])]
		if !ok {
			return 0
		}
		unit = scale
	}
	return int64(number * float64(unit))
}

// etaPattern is one piece of deluge's own way of saying how long: "1d 4h",
// "5m 30s", "2h".
var etaPattern = regexp.MustCompile(`([0-9]+)\s*([ywdhms])`)

// etaScale is what each of those pieces is worth in seconds.
var etaScale = map[string]int64{
	// A year and a week as deluge counts them, so "3y 9w" comes back as the
	// number deluge was rendering.
	"y": 31449600, "w": 604800, "d": 86400, "h": 3600, "m": 60, "s": 1,
}

// parseTorrentETA turns deluge's words into seconds. An answer it will not give
// — "∞", or nothing at all — is zero, which the screen reads as "it isn't
// saying" rather than as "any moment now".
func parseTorrentETA(value string) int64 {
	var total int64
	for _, match := range etaPattern.FindAllStringSubmatch(strings.ToLower(value), -1) {
		amount, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			continue
		}
		total += amount * etaScale[match[2]]
	}
	return total
}

// TorrentSetup is what a caller may choose about the downloader. Everything
// else — the port, the state directory, the account the scripts authenticate
// as — is HostMan's business and is not worth a field on a phone.
type TorrentSetup struct {
	// Downloads is where the files land. Empty means the SSH user's
	// ~/Downloads/torrents, which only the host can work out.
	Downloads string `json:"downloads"`
	// Reset throws away the stored daemon password and makes a new one.
	Reset bool `json:"reset"`
}

// torrentSetupScript writes the daemon onto the host: its state directory, its
// password, its settings and its unit.
//
// All of it is deterministic and none of it is slow — there is no apt here, and
// so no detached job and no progress to follow. That is the whole difference
// between this and the remote browser session: the minutes of installing are
// somebody else's, done once, before any of this.
//
// It refuses a host without deluge rather than installing it. A BitTorrent
// client is a decision about what a machine does on a network, and the message
// says exactly which packages to install.
const torrentSetupScript = `set -u
r=$1
u=$2
dl=$3
reset=$4
rev=$5

if [ "$(id -u 2>/dev/null || echo 1)" != 0 ]; then
  printf 'setting up the downloader needs root\n' >&2
  exit 3
fi
for b in %[3]s; do
  command -v "$b" >/dev/null 2>&1 || {
    printf 'this host has no %%s — install deluge first: apt install %[6]s\n' "$b" >&2
    exit 8
  }
done
deluged=$(command -v deluged) || exit 8

home=$(getent passwd "$u" 2>/dev/null | cut -d: -f6)
[ -n "$home" ] || home="/home/$u"
[ -n "$dl" ] || dl="$home/Downloads/torrents"

conf="$r%[1]s"
units="$r/etc/systemd/system"
mkdir -p "$conf" "$units" "$r$dl" || { printf 'could not write to %%s\n' "$conf" >&2; exit 4; }
# The daemon runs as the SSH user: the directory holding its password and its
# state is that user's, and so is the folder the files land in.
chown "$u" "$conf" "$r$dl" 2>/dev/null || true
chmod 750 "$conf" 2>/dev/null || true

# deluged authenticates its clients out of this file. The password is made here
# and stays here — HostMan never reads it back, and the scripts that need it
# read it on the host.
#
# What is asked is whether HostMan's own account is in the file, not whether
# the file exists: deluged writes one of these the first time it starts, with a
# localclient account in it and nothing else, and a host where that had already
# happened would otherwise end up with a downloader nothing could log in to.
have=$(awk -F: '$1 == "%[5]s" { print $2; exit }' "$conf/auth" 2>/dev/null)
if [ "$reset" = 1 ] || [ -z "$have" ]; then
  pw=$(tr -dc 'abcdef0123456789' < /dev/urandom 2>/dev/null | head -c 32)
  [ ${#pw} -eq 32 ] || { printf 'could not generate a password\n' >&2; exit 9; }
  # Anything else already in the file stays: deluged keeps its own localclient
  # account in here, and a thin client somebody uses on this host authenticates
  # with it.
  if [ -s "$conf/auth" ]; then
    grep -v '^%[5]s:' "$conf/auth" > "$conf/auth.new" 2>/dev/null || :
  else
    : > "$conf/auth.new"
  fi
  printf '%[5]s:%%s:10\n' "$pw" >> "$conf/auth.new" || exit 6
  mv -f "$conf/auth.new" "$conf/auth" || exit 6
fi
chmod 600 "$conf/auth" 2>/dev/null || true
chown "$u" "$conf/auth" 2>/dev/null || true

# Settings are written as lines to be read, never as a file to be sourced.
{
  printf 'PORT=%%s\n' %[2]d
  printf 'DOWNLOADS=%%s\n' "$dl"
  printf 'USER=%%s\n' "$u"
  printf 'REVISION=%%s\n' "$rev"
} > "$conf/deployer.conf" || exit 6
chown "$u" "$conf/deployer.conf" 2>/dev/null || true

# Unlike the remote browser session, this unit has an [Install] section and is
# enabled. A browser holding your logins should run for the two minutes you are
# using it; a download that takes six hours should survive the machine
# restarting in the middle of it, and deluge picks up from its own state.
cat > "$units/%[4]s" <<UNIT
[Unit]
Description=HostMan torrent downloader (deluged)
Documentation=https://deluge.readthedocs.io
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$u
UMask=0022
ExecStart=$deluged --do-not-daemonize --config $conf --port %[2]d --loglevel warning
Restart=on-failure
RestartSec=5
TimeoutStopSec=30

[Install]
WantedBy=multi-user.target
UNIT
[ $? -eq 0 ] || exit 6

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
  systemctl enable -- %[4]s >/dev/null 2>&1 || true
fi
printf 'written\n'
`

// renderTorrentSetup builds the script that writes the daemon onto a host.
func renderTorrentSetup() string {
	return fmt.Sprintf(torrentSetupScript,
		torrentStateDir, torrentPort, strings.Join(torrentPieces, " "),
		TorrentUnit, torrentAccount, TorrentPackages)
}

// torrentRevision names the unit and settings this build writes, as a hash of
// them. A host keeps what it was given — updating HostMan does not reach back
// and rewrite a unit somebody's download is running under — so hashing is what
// lets the screen say a host is behind rather than leaving a fix that changed
// nothing to be discovered.
func torrentRevision() string {
	sum := sha256.Sum256([]byte(renderTorrentSetup()))
	return hex.EncodeToString(sum[:])[:12]
}

// SetupTorrent writes the downloader onto a host, or rewrites the one already
// there. It is idempotent: the password, the state and everything already
// downloading are kept unless the password is explicitly asked to be replaced.
func (s *Service) SetupTorrent(ctx context.Context, h *store.Host, opts TorrentSetup) (*TorrentDaemon, error) {
	if !userPattern.MatchString(h.Username) {
		return nil, invalid("%q is not a user a downloader can run as", h.Username)
	}
	downloads, err := cleanDownloads(opts.Downloads)
	if err != nil {
		return nil, err
	}
	res, err := s.run(ctx, h, elevate(renderTorrentSetup(),
		"", h.Username, downloads, boolArg(opts.Reset), torrentRevision()), "")
	if err != nil {
		return nil, err
	}
	switch res.ExitCode {
	case 0:
	case 3:
		return nil, errNeedsRoot(h.Username, h.Name)
	case 8:
		return nil, invalid("deluge is not installed on %s. Install it there first: apt install %s",
			h.Name, TorrentPackages)
	default:
		return nil, failure(res, "could not set up the downloader on "+h.Name)
	}
	return s.TorrentStatus(ctx, h)
}

// torrentRemoveScript takes the downloader back off the host: the unit first,
// so nothing is left pointing at a directory that has gone, then deluge's state
// and HostMan's settings.
//
// Two things it does not touch. Deluge itself stays, because the host installed
// it and something else may be using it. The downloaded files stay, always and
// without being asked — they are the entire reason any of this ran, and a
// remove button that deleted a week of downloading would be a trap.
const torrentRemoveScript = `set -u
r=$1
conf="$r%[1]s"
unit="$r/etc/systemd/system/%[2]s"

if command -v systemctl >/dev/null 2>&1; then
  systemctl disable --now -- %[2]s >/dev/null 2>&1 || true
fi
rm -f -- "$unit" || exit 3
rm -rf -- "$conf" || exit 3
if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload >/dev/null 2>&1 || true
  systemctl reset-failed -- %[2]s >/dev/null 2>&1 || true
fi
printf 'removed\n'
`

// RemoveTorrent deletes the downloader from a host. Deluge and everything
// already downloaded stay where they are.
func (s *Service) RemoveTorrent(ctx context.Context, h *store.Host) error {
	script := fmt.Sprintf(torrentRemoveScript, torrentStateDir, TorrentUnit)
	res, err := s.run(ctx, h, elevate(script, ""), "")
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return failure(res, "could not remove the downloader from "+h.Name)
	}
	return nil
}

// TorrentAdd is a torrent to start downloading, in one of the two forms a phone
// has: a link that was copied, or a file that was picked.
type TorrentAdd struct {
	// Source is a magnet link, or the address of a .torrent file for the host
	// to fetch itself.
	Source string `json:"source"`
	// File is a .torrent file's own bytes, base64, when one was picked on the
	// phone rather than linked to.
	File string `json:"file"`
	// Name is what that file was called, which is only ever used to name the
	// copy on the host — deluge takes the torrent's real name from inside it.
	Name string `json:"name"`
	// Path is where this torrent's files should land, overriding the folder the
	// downloader was set up with.
	Path string `json:"path"`
}

// torrentAddScript hands one torrent to the daemon.
//
// A .torrent file arrives base64 on stdin and is written into the state
// directory, because deluged reads it as itself and the /tmp of a systemd
// service is not the /tmp of an SSH session — PrivateTmp turns a path that
// exists into one that does not. It is removed again as soon as deluge has
// taken it: deluge keeps its own copy, and a directory of stale torrent files
// is a thing nobody asked HostMan to look after.
const torrentAddScript = `set -u
r=$1
name=$2
src=$3
path=$4
conf="$r%[1]s"
%[2]s

[ -f "$conf/deployer.conf" ] || { printf 'this host has no downloader set up yet\n' >&2; exit 3; }
command -v deluge-console >/dev/null 2>&1 || { printf 'deluge is not installed on this host\n' >&2; exit 8; }

if [ -z "$path" ]; then
  path=$(sed -n 's/^DOWNLOADS=//p' "$conf/deployer.conf" | head -1)
fi
[ -n "$path" ] || { printf 'the downloader has no folder to download into\n' >&2; exit 3; }

# A daemon that is not running cannot be told anything, and starting it needs
# root — which this script does not have. HostMan is told so it can start the
# service and ask again, which is one round trip rather than a screen saying no.
if command -v systemctl >/dev/null 2>&1; then
  systemctl is-active --quiet -- %[3]s 2>/dev/null || exit 4
fi

target=$src
if [ -n "$name" ]; then
  mkdir -p "$conf/incoming" || exit 5
  target="$conf/incoming/$name"
  base64 -d > "$target" || { rm -f "$target"; printf 'could not decode the torrent file\n' >&2; exit 6; }
fi

mkdir -p "$r$path" 2>/dev/null || true

# systemd calls a Type=simple service started the moment it has forked, which
# is a second or so before deluged is listening. Since HostMan starts a
# stopped daemon and immediately says this, the first attempt can arrive before
# anybody is there to hear it — so a refused connection is waited out rather
# than reported. Anything else, including a torrent deluge will not take, is
# the answer and is returned at once.
add_torrent() {
  tries=0
  while :; do
    out=$(console "add -p \"$path\" \"$target\"")
    code=$?
    case "$out" in
      *"Failed to connect"*|*"Could not connect"*|*"refused"*)
        tries=$((tries + 1))
        [ "$tries" -ge 5 ] && return
        sleep 1
        ;;
      *) return ;;
    esac
  done
}
add_torrent
[ -n "$name" ] && rm -f "$target"

printf '@@code\n%%s\n' "$code"
printf '@@out\n%%s\n' "$out"
exit 0
`

// AddTorrent starts one torrent downloading on the host.
//
// A daemon that is not running is started rather than refused: somebody who has
// just handed HostMan a torrent has said what they want clearly enough, and
// "the service is stopped" is an answer nobody needed to be given. That is the
// one thing here that needs root, and it goes through the same systemctl path
// every other service on this host does.
func (s *Service) AddTorrent(ctx context.Context, h *store.Host, in TorrentAdd) (*TorrentDaemon, error) {
	source, name, body, err := cleanTorrentAdd(in)
	if err != nil {
		return nil, err
	}
	path := ""
	if strings.TrimSpace(in.Path) != "" {
		if path, err = cleanDownloads(in.Path); err != nil {
			return nil, err
		}
	}
	script := fmt.Sprintf(torrentAddScript, torrentStateDir, consoleScript(), TorrentUnit)
	cmd := asUser(script, "", name, source, path)

	res, err := s.run(ctx, h, cmd, body)
	if err != nil {
		return nil, err
	}
	if res.ExitCode == 4 {
		// The daemon is stopped. Start it, then say the same thing again.
		if err := s.Act(ctx, h, TorrentUnit, "start"); err != nil {
			return nil, err
		}
		if res, err = s.run(ctx, h, cmd, body); err != nil {
			return nil, err
		}
	}
	switch res.ExitCode {
	case 0:
	case 3:
		return nil, invalid("this host has no downloader set up yet")
	case 8:
		return nil, invalid("deluge is not installed on %s. Install it there first: apt install %s",
			h.Name, TorrentPackages)
	default:
		return nil, failure(res, "could not add the torrent on "+h.Name)
	}
	if err := torrentConsoleError(res.Stdout); err != nil {
		return nil, err
	}
	return s.TorrentStatus(ctx, h)
}

// torrentActionScript pauses, resumes or removes one torrent.
//
// Removing is where deluge is at its most awkward, and both halves of this are
// its doing. It wants a --confirm that 2.0 has never heard of, so the older
// spelling is tried when the newer one is not understood. And having removed
// the torrent, deluge 2.1's console never exits — so the command is given a
// short deadline, and being cut short is not read as having failed. What
// actually happened is answered by asking for the list again, which is what
// HostMan does next in any case.
const torrentActionScript = `set -u
r=$1
id=$2
action=$3
data=$4
conf="$r%[1]s"
%[2]s

[ -f "$conf/deployer.conf" ] || { printf 'this host has no downloader set up yet\n' >&2; exit 3; }

flag=""
case "$action" in
  pause)  cmd="pause $id" ;;
  resume) cmd="resume $id" ;;
  remove)
    [ "$data" = 1 ] && flag=" --remove_data"
    cmd="rm --confirm$flag $id"
    console_wait=8
    ;;
  *) printf 'unknown action\n' >&2; exit 5 ;;
esac

out=$(console "$cmd")
code=$?
case "$out" in
  *"unrecognized arguments"*|*"no such option"*|*"invalid choice"*)
    if [ "$action" = remove ]; then
      out=$(console "rm$flag $id")
      code=$?
    fi
    ;;
esac

printf '@@code\n%%s\n' "$code"
printf '@@out\n%%s\n' "$out"
exit 0
`

// TorrentAction pauses, resumes or removes one torrent. Removing takes the
// files with it only when asked: a torrent removed from the list and a download
// deleted off the disk are different things, and the second one cannot be
// undone.
//
// A remove is checked rather than believed, because deluge's own console cannot
// be believed here — it does the work and then hangs, so being cut short says
// nothing either way. The list that comes back afterwards is the answer.
func (s *Service) TorrentAction(ctx context.Context, h *store.Host, id, action string, withData bool) (*TorrentDaemon, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	if !torrentIDPattern.MatchString(id) {
		return nil, invalid("that is not a torrent id")
	}
	if !torrentActions[action] {
		return nil, invalid("%q is not something HostMan will do to a torrent", action)
	}
	script := fmt.Sprintf(torrentActionScript, torrentStateDir, consoleScript())
	res, err := s.run(ctx, h, asUser(script, "", id, action, boolArg(withData)), "")
	if err != nil {
		return nil, err
	}
	if res.ExitCode == 3 {
		return nil, invalid("this host has no downloader set up yet")
	}
	if res.ExitCode != 0 {
		return nil, failure(res, "could not "+action+" that torrent on "+h.Name)
	}
	if err := torrentConsoleError(res.Stdout); err != nil && action != "remove" {
		return nil, err
	}

	daemon, err := s.TorrentStatus(ctx, h)
	if err != nil {
		return nil, err
	}
	if action == "remove" {
		for _, torrent := range daemon.Torrents {
			if torrent.ID == id {
				return nil, failure(res, "deluge did not remove that torrent on "+h.Name)
			}
		}
	}
	return daemon, nil
}

// torrentSeedingScript tells deluge what to do with a torrent that has
// finished. All three settings go in one console, because starting one is a
// second of python and three of them would be three.
const torrentSeedingScript = `set -u
r=$1
stop=$2
ratio=$3
remove=$4
conf="$r%[1]s"
%[2]s

[ -f "$conf/deployer.conf" ] || { printf 'this host has no downloader set up yet\n' >&2; exit 3; }
if command -v systemctl >/dev/null 2>&1; then
  systemctl is-active --quiet -- %[3]s 2>/dev/null || exit 4
fi

out=$(console "config --set stop_seed_at_ratio $stop; config --set stop_seed_ratio $ratio; config --set remove_seed_at_ratio $remove")
code=$?
printf '@@code\n%%s\n' "$code"
printf '@@out\n%%s\n' "$out"
exit 0
`

// SetTorrentSeeding decides what becomes of a torrent once it has finished
// downloading: how long it keeps seeding, and whether its entry is then taken
// out of the list.
//
// It is deluge's own setting rather than a rule HostMan enforces, which is
// what makes it hold when nobody is looking at the screen — the daemon applies
// it to torrents that finish at three in the morning, which is when they
// finish.
//
// The daemon has to be running to be told, so a stopped one is started first,
// the same way adding a torrent does it.
func (s *Service) SetTorrentSeeding(ctx context.Context, h *store.Host, in TorrentSeeding) (*TorrentDaemon, error) {
	ratio, err := cleanSeedRatio(in.Ratio)
	if err != nil {
		return nil, err
	}
	// Removing an entry only ever happens where seeding stops, so asking for
	// one without the other is a setting that would never fire.
	if in.Remove && ratio == 0 {
		return nil, invalid("deluge can only remove a torrent it has stopped seeding, so give it a ratio")
	}
	script := fmt.Sprintf(torrentSeedingScript, torrentStateDir, consoleScript(), TorrentUnit)
	cmd := asUser(script, "",
		delugeBool(ratio > 0), strconv.FormatFloat(ratio, 'f', 2, 64), delugeBool(in.Remove))

	res, err := s.run(ctx, h, cmd, "")
	if err != nil {
		return nil, err
	}
	if res.ExitCode == 4 {
		// Nothing can be told to a daemon that is not running.
		if err := s.Act(ctx, h, TorrentUnit, "start"); err != nil {
			return nil, err
		}
		if res, err = s.run(ctx, h, cmd, ""); err != nil {
			return nil, err
		}
	}
	if res.ExitCode == 3 {
		return nil, invalid("this host has no downloader set up yet")
	}
	if res.ExitCode != 0 {
		return nil, failure(res, "could not change the seeding rule on "+h.Name)
	}
	if err := torrentConsoleError(res.Stdout); err != nil {
		return nil, err
	}
	return s.TorrentStatus(ctx, h)
}

// cleanSeedRatio checks a ratio. Zero is "never stop", which is deluge's own
// default and a perfectly good answer.
func cleanSeedRatio(ratio float64) (float64, error) {
	if ratio == 0 {
		return 0, nil
	}
	if ratio < 0.1 || ratio > MaxSeedRatio {
		return 0, invalid("a ratio between 0.1 and %d, or none at all to keep seeding", MaxSeedRatio)
	}
	return ratio, nil
}

// torrentLimitScript tells deluge how many torrents to work on at once.
//
// Deluge keeps three limits, and setting only the headline one would be a
// setting that lies: max_active_limit caps the total, but its own defaults —
// three downloading, five seeding — would still hold the queue under the
// number that was chosen, and a sixth torrent would sit at 0% under a screen
// saying ten are allowed. So all three are set to the same number, and the one
// knob means what it says: this many at once, whatever they are doing.
const torrentLimitScript = `set -u
r=$1
limit=$2
conf="$r%[1]s"
%[2]s

[ -f "$conf/deployer.conf" ] || { printf 'this host has no downloader set up yet\n' >&2; exit 3; }
if command -v systemctl >/dev/null 2>&1; then
  systemctl is-active --quiet -- %[3]s 2>/dev/null || exit 4
fi

out=$(console "config --set max_active_limit $limit; config --set max_active_downloading $limit; config --set max_active_seeding $limit")
code=$?
printf '@@code\n%%s\n' "$code"
printf '@@out\n%%s\n' "$out"
exit 0
`

// SetTorrentActiveLimit decides how many torrents deluge works on at once.
// Everything past the limit waits as Queued until a slot opens, which is
// deluge's own queue doing the waiting — so, like the seeding rule, it holds
// when nobody is looking at the screen.
//
// The daemon has to be running to be told, so a stopped one is started first,
// the same way adding a torrent does it.
func (s *Service) SetTorrentActiveLimit(ctx context.Context, h *store.Host, limit int) (*TorrentDaemon, error) {
	limit, err := cleanActiveLimit(limit)
	if err != nil {
		return nil, err
	}
	script := fmt.Sprintf(torrentLimitScript, torrentStateDir, consoleScript(), TorrentUnit)
	cmd := asUser(script, "", strconv.Itoa(limit))

	res, err := s.run(ctx, h, cmd, "")
	if err != nil {
		return nil, err
	}
	if res.ExitCode == 4 {
		// Nothing can be told to a daemon that is not running.
		if err := s.Act(ctx, h, TorrentUnit, "start"); err != nil {
			return nil, err
		}
		if res, err = s.run(ctx, h, cmd, ""); err != nil {
			return nil, err
		}
	}
	if res.ExitCode == 3 {
		return nil, invalid("this host has no downloader set up yet")
	}
	if res.ExitCode != 0 {
		return nil, failure(res, "could not change the torrent limit on "+h.Name)
	}
	if err := torrentConsoleError(res.Stdout); err != nil {
		return nil, err
	}
	return s.TorrentStatus(ctx, h)
}

// cleanActiveLimit checks a limit. It becomes an argument on a command line,
// so only a number deluge would read as one gets there: a count worth having,
// or -1, which is deluge's own way of asking for no limit at all.
func cleanActiveLimit(limit int) (int, error) {
	if limit == -1 {
		return -1, nil
	}
	if limit < 1 || limit > MaxActiveTorrents {
		return 0, invalid("a number of torrents between 1 and %d, or -1 for no limit at all", MaxActiveTorrents)
	}
	return limit, nil
}

// delugeBool renders a flag the way deluge's own config command reads it.
func delugeBool(on bool) string {
	if on {
		return "True"
	}
	return "False"
}

// StartTorrent and StopTorrent run the daemon. They are systemctl like
// everything else on this host, so a daemon that takes its time coming up takes
// its time here too.
func (s *Service) StartTorrent(ctx context.Context, h *store.Host) (*TorrentDaemon, error) {
	if err := s.Act(ctx, h, TorrentUnit, "start"); err != nil {
		return nil, err
	}
	return s.TorrentStatus(ctx, h)
}

// StopTorrent stops the daemon. Nothing is lost by it: deluge writes what each
// torrent had got to, and starting it again carries on from there.
func (s *Service) StopTorrent(ctx context.Context, h *store.Host) (*TorrentDaemon, error) {
	if err := s.Act(ctx, h, TorrentUnit, "stop"); err != nil {
		return nil, err
	}
	return s.TorrentStatus(ctx, h)
}

// torrentConsoleError reads what deluge-console said about a command.
//
// It is not enough to look at the exit status: deluge-console reports a torrent
// it would not take by printing a line and exiting 0, so a screen that trusted
// the status would say a torrent was added and then never show it.
func torrentConsoleError(out string) error {
	found := sections(out)
	message := strings.TrimSpace(ansiPattern.ReplaceAllString(strings.Join(found["out"], "\n"), ""))
	lower := strings.ToLower(message)

	// Deluge's own words for the one refusal that is not a failure: the torrent
	// the caller asked for is there, which is what they wanted.
	if strings.Contains(lower, "already in session") {
		return nil
	}
	if code := first(found["code"]); code != "" && code != "0" {
		if message == "" {
			message = "deluge would not take that (exit " + code + ")"
		}
		return invalid("%s", tellingLine(message))
	}
	for _, marker := range troubleMarkers {
		if strings.Contains(lower, marker) {
			return invalid("%s", tellingLine(message))
		}
	}
	return nil
}

// tellingLine picks the line worth showing out of what deluge printed. Its
// first line is usually "Attempting to add torrent: ..." and its tenth is a
// base64 dump of the file, so the line that says what went wrong is the one
// that names the trouble.
func tellingLine(message string) string {
	lines := strings.Split(strings.TrimSpace(message), "\n")
	best := strings.TrimSpace(lines[0])
	for _, line := range lines {
		lower := strings.ToLower(line)
		for _, marker := range troubleMarkers {
			if strings.Contains(lower, marker) {
				best = strings.TrimSpace(line)
				// A deluge traceback ends with the exception, which says more
				// than the "[ERROR]" line that started it — so the search
				// carries on rather than stopping at the first match.
				if strings.Contains(lower, "error:") || strings.Contains(lower, "does not exist") {
					return trim(best)
				}
			}
		}
	}
	return trim(best)
}

// trim keeps a message to the part somebody reads.
func trim(line string) string {
	if len(line) > 300 {
		line = line[:300]
	}
	return line
}

// magnetPattern is a magnet link with a BitTorrent hash in it. Deluge would
// accept other kinds; this is the one that names a torrent.
var magnetPattern = regexp.MustCompile(`(?i)^magnet:\?.*xt=urn:btih:[0-9a-z]{32,40}`)

// torrentNameSafe is what is left of a picked file's name once it can only be a
// filename: no directory, no dot-file, nothing a shell or a path would read as
// anything but characters.
var torrentNameSafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// cleanTorrentAdd checks what was asked for and returns the pieces the script
// takes: the source to hand deluge, the name to write a picked file under, and
// the file's bytes base64 for stdin.
func cleanTorrentAdd(in TorrentAdd) (source, name, body string, err error) {
	source = strings.TrimSpace(in.Source)
	hasFile := strings.TrimSpace(in.File) != ""
	if source == "" && !hasFile {
		return "", "", "", invalid("a magnet link, a link to a .torrent file, or the file itself")
	}
	if source != "" && hasFile {
		return "", "", "", invalid("one torrent at a time: a link or a file, not both")
	}

	if source != "" {
		clean, err := CleanTorrentSource(source)
		return clean, "", "", err
	}

	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(in.File))
	if err != nil {
		return "", "", "", invalid("that file did not arrive intact")
	}
	if len(raw) == 0 {
		return "", "", "", invalid("that file is empty")
	}
	if len(raw) > MaxTorrentFileBytes {
		return "", "", "", invalid("a .torrent file over %d MB is not a .torrent file", MaxTorrentFileBytes>>20)
	}
	// A torrent file is a bencoded dictionary with an info dictionary in it.
	// Checking that much is what stops a photo picked by mistake from being
	// carried to a host and handed to a daemon that will only refuse it.
	if raw[0] != 'd' || !strings.Contains(string(raw), "4:info") {
		return "", "", "", invalid("that is not a .torrent file")
	}
	return "", torrentFileName(in.Name), base64.StdEncoding.EncodeToString(raw), nil
}

// torrentFileName reduces a picked file's name to something that can only be a
// filename. Deluge takes the torrent's real name from inside the file, so this
// one is never seen by anybody — it only has to be safe and unambiguous.
func torrentFileName(name string) string {
	// The suffix comes off first, while the name is still the one that was
	// picked: taking it off afterwards would leave the dash that replaced the
	// dot before it.
	name = strings.TrimSuffix(strings.TrimSpace(name), ".torrent")
	name = strings.Trim(torrentNameSafe.ReplaceAllString(name, "-"), ".-")
	if len(name) > 80 {
		name = name[:80]
	}
	if name == "" {
		name = "picked"
	}
	return name + ".torrent"
}

// CleanTorrentSource checks a link. It reaches deluge as an argument, so a
// value that could be read as an option, a local path or anything but a torrent
// is not a link somebody asked for.
func CleanTorrentSource(source string) (string, error) {
	source = strings.TrimSpace(source)
	if len(source) > 4096 {
		return "", invalid("that link is too long")
	}
	if strings.ContainsAny(source, " \t\r\n\"'`\\") || strings.HasPrefix(source, "-") {
		return "", invalid("that does not look like a magnet link or a .torrent address")
	}
	if magnetPattern.MatchString(source) {
		return source, nil
	}
	parsed, err := url.Parse(source)
	if err != nil || parsed.Host == "" {
		return "", invalid("that does not look like a magnet link or a .torrent address")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", invalid("a magnet link, or an http:// or https:// address of a .torrent file")
	}
	return source, nil
}

// cleanDownloads checks the folder the files land in. Empty is allowed and
// means the SSH user's own ~/Downloads/torrents, which only the host can work
// out — so the host is the one that fills it in.
func cleanDownloads(folder string) (string, error) {
	folder = strings.TrimSpace(folder)
	if folder == "" {
		return "", nil
	}
	clean, err := CleanPath(folder)
	if err != nil {
		return "", err
	}
	// It becomes an argument to deluge and a directory to create. A quote or a
	// backslash in it is not a folder anybody meant.
	if strings.ContainsAny(clean, `"'`+"`\\") {
		return "", invalid("that folder name has a character HostMan will not send")
	}
	if clean == "/" {
		return "", invalid("downloading into / is not a folder, it is a mistake")
	}
	return clean, nil
}
