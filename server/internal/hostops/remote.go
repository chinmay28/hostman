package hostops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/chinmay28/deployer/server/internal/store"
)

// A remote session is a browser running on the host that you drive from your
// phone: a virtual screen with nothing on it but a browser, offered over VNC
// and reached through noVNC in an ordinary browser tab.
//
// It exists because some things can only be done by a person in front of a
// browser — signing in, clicking through a consent page, taking a file from
// behind a login — and the machine that should end up holding the file is the
// host, not the phone. A browser on the host downloads into the host's own
// Downloads directory, which is the whole point: no transfer, no second copy,
// no phone in the middle.
//
// Three decisions shape the rest of this file.
//
// **The session gets its own screen rather than the host's.** x11vnc attaching
// to :0 is the familiar recipe and it is the wrong one here: it needs a desktop
// to already be running, it cannot attach to a Wayland session at all — which
// is what Raspberry Pi OS has run by default since Bookworm — and what it shows
// is whatever the machine is showing, to anybody who walks past it. A private
// Xvfb screen works the same on a headless Pi as on one with a monitor, and
// nothing on the real display changes while you use it.
//
// **It is off unless you turn it on.** The unit has no [Install] section, so
// systemd calls it static: it cannot be enabled, and it does not come back
// after a reboot. A logged-in browser is a bearer of every credential you typed
// into it, and one that runs all week is a worse thing to own than one that
// runs for the two minutes it takes to fetch a file.
//
// **The profile survives.** The browser keeps its profile in the SSH user's
// home, so the site you signed into last week is still signed in this week.
// That is what makes the second download a tap rather than another round with
// an authenticator app — and it is also why removing the session offers to take
// the profile with it.
//
// Nothing here is sourced. Values HostMan writes to the host are read back by
// parsing, and the session script is handed its settings as arguments by
// systemd, so no file on the host is ever fed to a shell that would run what is
// in it.

const (
	// RemoteUnit is the session's systemd unit. It is an ordinary unit file in
	// the administrator's own directory, so the Services screen lists it, shows
	// its journal and can stop it, like anything else installed by hand.
	RemoteUnit = "deployer-remote.service"

	// remoteConfDir holds everything HostMan writes that is not a unit file:
	// the VNC password, the settings, the page to open, and the setup log.
	remoteConfDir = "/etc/deployer-remote"
	// remoteLibDir holds the two generated scripts. /usr/local is where things
	// that are not the package manager's belong.
	remoteLibDir = "/usr/local/lib/deployer"

	// DefaultRemoteGeometry is the virtual screen's size. A desktop-width
	// screen is what makes sites lay out the way their author meant, and noVNC
	// scales it down to whatever the phone actually has.
	DefaultRemoteGeometry = "1280x800"
	// DefaultRemotePort is where noVNC answers. 6080 is websockify's own
	// convention and is unlikely to collide with anything on a home server.
	DefaultRemotePort = 6080
	// remoteVNCPort is where x11vnc listens, bound to localhost so the only
	// door into the session is the one noVNC opens.
	remoteVNCPort = 5999
	// remoteDisplay is the X display the session runs on. :99 is high enough to
	// stay out of the way of a real desktop on :0.
	remoteDisplay = 99
)

// RemoteSession is everything the screen needs to know in one round trip.
type RemoteSession struct {
	// Unit is the systemd unit, so start and stop go through the service API
	// that already knows how to wait for systemd.
	Unit string `json:"unit"`
	// Setup is where the install got to: "absent" (never run), "running",
	// "ok", or "failed".
	Setup string `json:"setup"`
	// SetupExit is the exit status behind a failed setup, for the log to be
	// read against.
	SetupExit int `json:"setupExit,omitempty"`
	// SetupLog is the tail of the install log — the answer to "what is it
	// doing?" while apt works, and to "why not?" afterwards.
	SetupLog string `json:"setupLog,omitempty"`
	// Ready reports whether a session could be started right now.
	Ready bool `json:"ready"`
	// Missing names the pieces that are not installed yet, where they are what
	// stands between the host and a session.
	Missing []string `json:"missing,omitempty"`
	// Browser is the browser the session will run, as the host names it.
	Browser string `json:"browser,omitempty"`
	// Running reports whether the session is up right now.
	Running bool `json:"running"`
	// Active and Sub are systemd's own words for it, for the times when
	// "running" is not the whole story: activating, failed, dead.
	Active string `json:"active,omitempty"`
	Sub    string `json:"sub,omitempty"`
	// Port is where noVNC answers, and Geometry the size of the virtual screen.
	Port     int    `json:"port"`
	Geometry string `json:"geometry"`
	// Password is the VNC password. It is generated on the host and kept there;
	// HostMan reads it back so the screen can show it and put it in the link,
	// rather than asking somebody to type eight random characters on a phone.
	Password string `json:"password,omitempty"`
	// Homepage is the page the session opens when it starts.
	Homepage string `json:"homepage,omitempty"`
	// SnapBrowser names the browsers this host has only as snaps, which are no
	// use here. It is the difference between "no browser" and "a browser that
	// looks installed and cannot run", which are fixed by different things.
	SnapBrowser string `json:"snapBrowser,omitempty"`
	// BrokenBrowser names browsers that are installed and will not run — they
	// cannot even report their own version. Same fix as a snap, different
	// sentence, and a host deserves to be told which it has.
	BrokenBrowser string `json:"brokenBrowser,omitempty"`
	// NoSandbox reports a browser running here without its own sandbox, because
	// this host would not give it one. It is a weaker browser than the one on
	// the phone, and whoever signs into something with it should be told.
	NoSandbox bool `json:"noSandbox,omitempty"`
	// Stale reports a session written by an older HostMan. Updating HostMan
	// does not rewrite what is on the host — setting it up again does — so a
	// host still running the old scripts says so rather than leaving somebody
	// to wonder why a fix changed nothing.
	Stale bool `json:"stale,omitempty"`
	// User is the account the session runs as — whose home the profile lives
	// in, and whose Downloads the files land in.
	User string `json:"user"`
	// Downloads is the directory a download ends up in, and Profile is where
	// the browser keeps the logins that make the next visit a tap.
	Downloads string `json:"downloads,omitempty"`
	Profile   string `json:"profile,omitempty"`
	// Files is the newest handful of downloads, so the screen can say the file
	// arrived rather than leaving somebody to go and look.
	Files []RemoteFile `json:"files"`
}

// RemoteFile is one file in the downloads directory.
type RemoteFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	// AgeS is how long ago it was written, in seconds, as the host counts.
	AgeS int64 `json:"ageS"`
}

// remotePieces is what a session needs on the host. openbox is deliberately not
// among them: without a window manager the browser still runs, its dialogs are
// just harder to move, and a missing package is no reason to refuse to start.
var remotePieces = []string{"Xvfb", "x11vnc", "websockify"}

// remoteBrowsers are the browsers a session will use, best first. Chromium is
// first because it is what a Raspberry Pi already has, and because its download
// directory can be set without a person clicking through a settings screen over
// VNC.
var remoteBrowsers = []string{"chromium", "chromium-browser", "google-chrome", "firefox-esr", "firefox"}

// geometryPattern is a screen size, checked before it reaches a command line.
var geometryPattern = regexp.MustCompile(`^([0-9]{3,4})x([0-9]{3,4})$`)

// MaxRemoteLogBytes is how much of the setup log comes back. apt is chatty and
// the end is the part worth reading.
const MaxRemoteLogBytes = 16 << 10

// remotePickBrowser is the same question asked in three places — the installer,
// the session and the status probe — so it is written once and rendered into
// each of them. It sets three variables rather than printing, because a command
// substitution would run it in a subshell and lose the two that say why a
// browser was turned down.
//
// Three things disqualify a browser, and the last one is the only rule that
// catches everything:
//
//   - a path that resolves into /snap. Snap confinement walls a browser out of
//     the hidden profile directory in the home it is otherwise allowed into,
//     and a system service has no user runtime directory for snapd to work in.
//
//   - a wrapper script that calls out to a snap. Ubuntu's chromium-browser is
//     exactly this and is not a symlink into /snap, so the path alone does not
//     give it away — it has to be read.
//
//   - not being able to say its own version. Whatever the reason — a wrapper
//     for something that is not installed, a missing library, a half-finished
//     package — a browser that cannot answer that is not going to render a
//     page either, and asking costs one exec.
const remotePickBrowser = `pick_browser() {
  browser=""
  snap_browsers=""
  broken_browsers=""
  for b in %s; do
    p=$(command -v "$b" 2>/dev/null) || continue
    [ -n "$p" ] || continue
    real=$(readlink -f "$p" 2>/dev/null) || real="$p"
    case "$p$real" in
      */snap/*)
        snap_browsers="${snap_browsers:+$snap_browsers }$b"
        continue
        ;;
    esac
    if [ "$(head -c 2 "$real" 2>/dev/null)" = '#!' ] && grep -qi snap "$real" 2>/dev/null; then
      snap_browsers="${snap_browsers:+$snap_browsers }$b"
      continue
    fi
    if command -v timeout >/dev/null 2>&1; then
      timeout 20 "$b" --version >/dev/null 2>&1 || {
        broken_browsers="${broken_browsers:+$broken_browsers }$b"
        continue
      }
    else
      "$b" --version >/dev/null 2>&1 || {
        broken_browsers="${broken_browsers:+$broken_browsers }$b"
        continue
      }
    fi
    browser="$b"
    return 0
  done
  return 1
}
`

// remoteStatusScript answers everything about a session in one round trip:
// where setup got to, which pieces are installed, what the settings say, what
// systemd makes of the unit, and what is in the downloads directory.
//
// Nothing here writes, so it is safe to ask as often as a screen wants to.
const remoteStatusScript = `set -u
r=$1
u=$2
%[2]s
etc="$r` + remoteConfDir + `"

printf '@@state\n'
cat "$etc/setup.state" 2>/dev/null || printf 'absent\n'

printf '@@logsize\n'
wc -c < "$etc/setup.log" 2>/dev/null || printf '0\n'

printf '@@log\n'
tail -c %[1]d "$etc/setup.log" 2>/dev/null | base64 2>/dev/null

printf '@@config\n'
cat "$etc/config" 2>/dev/null

printf '@@password\n'
cat "$etc/password" 2>/dev/null

printf '@@homepage\n'
cat "$etc/homepage" 2>/dev/null

printf '@@degraded\n'
cat "$etc/degraded" 2>/dev/null

printf '@@have\n'
for b in %[4]s; do
  command -v "$b" >/dev/null 2>&1 && printf '%%s\n' "$b"
done
pick_browser || true
[ -n "$browser" ] && printf '%%s\n' "$browser"

printf '@@snap\n%%s\n' "$snap_browsers"
printf '@@broken\n%%s\n' "$broken_browsers"

printf '@@unit\n'
if command -v systemctl >/dev/null 2>&1; then
  systemctl show --no-pager -p LoadState -p ActiveState -p SubState -- %[3]s 2>/dev/null
fi

home=$(getent passwd "$u" 2>/dev/null | cut -d: -f6)
[ -n "$home" ] || home="/home/$u"
dl="$r$home/Downloads"
printf '@@downloads\n%%s\n' "$dl"
printf '@@profile\n%%s\n' "$r$home/.config/deployer-remote"

printf '@@files\n'
now=$(date +%%s 2>/dev/null || echo 0)
if [ -d "$dl" ]; then
  ls -1t -- "$dl" 2>/dev/null | head -8 | while IFS= read -r n; do
    [ -f "$dl/$n" ] || continue
    meta=$(stat -c '%%Y %%s' -- "$dl/$n" 2>/dev/null) || meta="0 0"
    printf '%%s %%s\t%%s\n' "$meta" "$now" "$n"
  done
fi

# Reading the state of a session never fails: every part of it is optional, and
# a piece that is not there is an answer rather than an error. What comes back
# from a host with none of this is "absent", which is what the screen offers to
# fix.
exit 0
`

// RemoteStatus reports on a host's remote session: what is installed, what the
// settings are, and whether it is running.
func (s *Service) RemoteStatus(ctx context.Context, h *store.Host) (*RemoteSession, error) {
	script := fmt.Sprintf(remoteStatusScript,
		MaxRemoteLogBytes, pickBrowserScript(), RemoteUnit, strings.Join(remotePieces, " "))
	res, err := s.run(ctx, h, elevate(script, "", h.Username), "")
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, failure(res, "could not read the remote session on "+h.Name)
	}
	return parseRemoteStatus(res.Stdout, h.Username), nil
}

// pickBrowserScript renders the shared browser rule for a script to embed.
func pickBrowserScript() string {
	return fmt.Sprintf(remotePickBrowser, strings.Join(remoteBrowsers, " "))
}

// parseRemoteStatus turns the script's output into the one answer the screen
// works from. A host that has never been set up is not an error: it reads as a
// session that is absent, which is exactly what the screen offers to fix.
func parseRemoteStatus(out, user string) *RemoteSession {
	found := sections(out)
	session := &RemoteSession{
		Unit:      RemoteUnit,
		User:      user,
		Setup:     "absent",
		Port:      DefaultRemotePort,
		Geometry:  DefaultRemoteGeometry,
		Downloads: first(found["downloads"]),
		Profile:   first(found["profile"]),
		Files:     []RemoteFile{},
	}

	// "failed:4" carries the exit status the installer died on, which is the
	// difference between "apt could not be reached" and "there is no browser
	// to install".
	state := first(found["state"])
	if code, ok := strings.CutPrefix(state, "failed:"); ok {
		session.Setup = "failed"
		session.SetupExit, _ = strconv.Atoi(strings.TrimSpace(code))
	} else if state != "" {
		session.Setup = state
	}
	// The log arrives as a tail, so its first line is usually half of one. Told
	// how big the whole file was, decodeLog drops that half rather than showing
	// it as a line apt never wrote.
	if log, _ := decodeLog(found["log"], first(found["logsize"])); strings.TrimSpace(log) != "" {
		session.SetupLog = log
	}

	revision := ""
	for _, line := range found["config"] {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "PORT":
			if port, err := strconv.Atoi(value); err == nil && port > 0 {
				session.Port = port
			}
		case "GEOMETRY":
			if geometryPattern.MatchString(value) {
				session.Geometry = value
			}
		case "REVISION":
			revision = value
		}
	}
	// A host that has a session at all, running scripts this build did not
	// write, is a host where setting up again is the whole of the fix.
	session.Stale = revision != "" && revision != remoteRevision()

	session.Password = first(found["password"])
	session.Homepage = first(found["homepage"])
	session.NoSandbox = first(found["degraded"]) == "no-sandbox"
	session.SnapBrowser = first(found["snap"])
	session.BrokenBrowser = first(found["broken"])

	have := map[string]bool{}
	for _, line := range found["have"] {
		have[strings.TrimSpace(line)] = true
	}
	for _, piece := range remotePieces {
		if !have[piece] {
			session.Missing = append(session.Missing, piece)
		}
	}
	for _, browser := range remoteBrowsers {
		if have[browser] {
			session.Browser = browser
			break
		}
	}
	if session.Browser == "" {
		session.Missing = append(session.Missing, "a browser")
	}

	props := map[string]string{}
	for _, line := range found["unit"] {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			props[key] = value
		}
	}
	session.Active = props["ActiveState"]
	session.Sub = props["SubState"]
	session.Running = session.Active == "active" || session.Active == "activating"

	// Ready means a start would work: every piece present, a password stored,
	// and a unit systemd has actually loaded.
	session.Ready = len(session.Missing) == 0 && session.Password != "" && props["LoadState"] == "loaded"
	session.Files = parseRemoteFiles(found["files"])
	return session
}

// parseRemoteFiles reads the downloads listing: "<mtime> <size> <now>\t<name>",
// newest first. The host does the arithmetic on its own clock, because the
// phone's and the Pi's rarely agree to the second.
func parseRemoteFiles(lines []string) []RemoteFile {
	files := []RemoteFile{}
	for _, line := range lines {
		meta, name, ok := strings.Cut(line, "\t")
		if !ok || strings.TrimSpace(name) == "" {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 {
			continue
		}
		mtime, _ := strconv.ParseInt(fields[0], 10, 64)
		size, _ := strconv.ParseInt(fields[1], 10, 64)
		now, _ := strconv.ParseInt(fields[2], 10, 64)
		age := now - mtime
		if age < 0 || now == 0 || mtime == 0 {
			age = 0
		}
		files = append(files, RemoteFile{Name: name, Size: size, AgeS: age})
		if len(files) == 5 {
			break
		}
	}
	return files
}

// RemoteSetup is what a caller may choose about a session. Everything else —
// the display number, the VNC port, where things are written — is HostMan's
// business and is not worth a field on a phone.
type RemoteSetup struct {
	// Geometry is the virtual screen's size, "1280x800" by default.
	Geometry string `json:"geometry"`
	// Port is where noVNC answers, 6080 by default.
	Port int `json:"port"`
	// Homepage is the page the browser opens with.
	Homepage string `json:"homepage"`
	// Reset throws away the stored VNC password and makes a new one.
	Reset bool `json:"reset"`
}

// remoteSetupScript writes everything a session is made of and then hands the
// slow half to the host to get on with.
//
// The two generated scripts and the unit file are written here and now, because
// they are deterministic and take no time. Installing the packages is neither:
// on a Pi, apt fetching a browser is minutes of work over a link that may be a
// phone's. So it is detached — nohup setsid, output to a log, the exit status
// recorded by an EXIT trap — for the same reason a self-update is: an SSH
// session that ends takes its command with it, and a phone that locks its
// screen must not be able to cancel an install half way through a package.
//
// What comes back from this call is only "it started". Where it got to is what
// the status script is for.
const remoteSetupScript = `set -u
r=$1
u=$2
geom=$3
port=$4
url=$5
reset=$6
rev=$7

if [ "$(id -u 2>/dev/null || echo 1)" != 0 ]; then
  printf 'setting up a remote session needs root\n' >&2
  exit 3
fi

etc="$r%[1]s"
lib="$r%[2]s"
units="$r/etc/systemd/system"
mkdir -p "$etc" "$lib" "$units" || { printf 'could not write to %%s\n' "$etc" >&2; exit 4; }
# The session runs as the SSH user and has to read the password x11vnc
# authenticates against, so the directory is theirs and nobody else's.
chown "$u" "$etc" 2>/dev/null || true
chmod 750 "$etc" 2>/dev/null || true

home=$(getent passwd "$u" 2>/dev/null | cut -d: -f6)
[ -n "$home" ] || home="/home/$u"
profile="$r$home/.config/deployer-remote"
dl="$r$home/Downloads"

cat > "$lib/remote-session.sh" <<'SESSION'
%[3]s
SESSION
chmod 755 "$lib/remote-session.sh" || exit 5

cat > "$lib/remote-install.sh" <<'INSTALL'
%[4]s
INSTALL
chmod 750 "$lib/remote-install.sh" || exit 5

# Settings are written as lines to be read, never as a file to be sourced: the
# session script is given them as arguments by systemd, and HostMan parses
# them back. Nothing on the host is fed to a shell that would run what is in it.
{
  printf 'PORT=%%s\n' "$port"
  printf 'GEOMETRY=%%s\n' "$geom"
  printf 'DISPLAY_NUM=%%s\n' %[5]d
  printf 'VNC_PORT=%%s\n' %[6]d
  printf 'DOWNLOADS=%%s\n' "$dl"
  printf 'PROFILE=%%s\n' "$profile"
  printf 'REVISION=%%s\n' "$rev"
} > "$etc/config" || exit 6
printf '%%s\n' "$url" > "$etc/homepage" || exit 6

# The unit has no [Install] section on purpose: systemd calls that static, so
# the session cannot be enabled and does not come back after a reboot. A
# logged-in browser should run for as long as somebody is using it and no
# longer.
cat > "$units/%[7]s" <<UNIT
[Unit]
Description=HostMan remote browser session
After=network-online.target

[Service]
Type=simple
User=$u
ExecStart=%[2]s/remote-session.sh $geom %[5]d %[6]d $port $etc $profile $dl
Restart=no
TimeoutStopSec=20
UNIT
[ $? -eq 0 ] || exit 6

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
fi

: > "$etc/setup.log" || exit 7
printf 'running\n' > "$etc/setup.state" || exit 7
nohup setsid sh "$lib/remote-install.sh" "$r" "$u" "$reset" >> "$etc/setup.log" 2>&1 < /dev/null &
printf 'started\n'
`

// remoteInstallScript is the half that takes minutes. It runs detached, so its
// only way of reporting anything is the log it writes and the state file its
// EXIT trap leaves behind — which is what the status script reads back.
//
// A browser is installed by trying the names distributions actually use, in
// order, rather than by asking what this one is called: Raspberry Pi OS has
// called Chromium both things within living memory, and a failed apt tells us
// the answer more reliably than a version check would.
const remoteInstallScript = `set -u
r=$1
u=$2
reset=$3
etc="$r%[1]s"

trap 'code=$?; if [ "$code" -eq 0 ]; then printf "ok\n" > "$etc/setup.state"; else printf "failed:%%s\n" "$code" > "$etc/setup.state"; fi' EXIT

export DEBIAN_FRONTEND=noninteractive
command -v apt-get >/dev/null 2>&1 || {
  printf 'this host has no apt-get, so HostMan cannot install the pieces itself\n'
  exit 9
}
printf '== updating the package list\n'
apt-get update || exit 10

printf '== installing the virtual screen, the VNC server and noVNC\n'
apt-get install -y xvfb x11vnc novnc websockify curl xdg-utils || exit 11

printf '== installing a window manager\n'
apt-get install -y openbox || printf 'openbox is not available here; dialogs will be unmanaged\n'

printf '== looking for a browser\n'
%[3]s
pick_browser || true
if [ -z "$browser" ]; then
  printf '== installing a browser\n'
  apt-get install -y chromium || apt-get install -y chromium-browser || apt-get install -y firefox-esr || true
  pick_browser || true
fi

if [ -z "$browser" ]; then
  # Nothing apt offers here can run. Google publishes Chrome as an ordinary
  # package, and taking the .deb directly is the shortest way to a browser that
  # does — it adds Google's repository itself, so it keeps updating like
  # anything else installed here.
  arch=$(dpkg --print-architecture 2>/dev/null || echo unknown)
  if [ "$arch" != amd64 ]; then
    printf 'no browser here can run%%s%%s, and Chrome has no package build for %%s\n' \
      "${snap_browsers:+ (snaps: $snap_browsers)}" "${broken_browsers:+ (will not start: $broken_browsers)}" "$arch" >&2
    exit 15
  fi
  if [ -n "$snap_browsers" ] || [ -n "$broken_browsers" ]; then
    printf '== no browser here can run%%s%%s; fetching Chrome as a package instead\n' \
      "${snap_browsers:+ — snaps: $snap_browsers}" "${broken_browsers:+ — will not start: $broken_browsers}"
  else
    printf '== apt installed no browser; fetching Chrome as a package instead\n'
  fi
  # A directory rather than mktemp's own name, because apt refuses a package
  # whose filename does not end in .deb — "Unsupported file ... given on
  # commandline" — and mktemp has no portable way to put a suffix on one.
  tmp=$(mktemp -d /tmp/deployer-chrome.XXXXXX) || exit 16
  deb="$tmp/google-chrome-stable.deb"
  if ! curl -fsSL -o "$deb" https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb; then
    rm -rf "$tmp"
    printf 'could not download Chrome\n' >&2
    exit 16
  fi
  # apt installs a local package and its dependencies in one go. Where it will
  # not, dpkg puts the package in place and apt is asked to finish the job,
  # which is the older way of doing the same thing.
  if ! apt-get install -y "$deb"; then
    dpkg -i "$deb" || true
    apt-get -f install -y || { rm -rf "$tmp"; exit 17; }
  fi
  rm -rf "$tmp"
  pick_browser || { printf 'Chrome installed but will not run\n' >&2; exit 18; }
fi
printf '== the session will run %%s\n' "$browser"

if [ "$reset" = 1 ] || [ ! -s "$etc/vncpasswd" ]; then
  printf '== storing a VNC password\n'
  # Eight characters because that is all the VNC protocol carries: a longer one
  # would be silently cut, and half a password is worse than a short one. The
  # alphabet leaves out the characters that are read wrong off a screen.
  pw=$(tr -dc 'abcdefghijkmnpqrstuvwxyz23456789' < /dev/urandom 2>/dev/null | head -c 8)
  [ ${#pw} -eq 8 ] || exit 13
  x11vnc -storepasswd "$pw" "$etc/vncpasswd" >/dev/null 2>&1 || exit 14
  printf '%%s\n' "$pw" > "$etc/password" || exit 14
  chmod 600 "$etc/vncpasswd" "$etc/password" 2>/dev/null || true
  chown "$u" "$etc/vncpasswd" 2>/dev/null || true
fi

printf '== ready\n'
`

// remoteSessionScript is what the unit runs. systemd hands it its settings as
// arguments, so it reads no configuration of its own and evaluates nothing.
//
// Everything it starts stays in the unit's control group, which is what makes
// stopping the session one systemctl call: systemd kills the group, and the
// screen, the window manager, the VNC server, the web gateway and the browser
// go together.
const remoteSessionScript = `#!/bin/sh
# Written by HostMan. A virtual screen with a browser on it, offered over VNC.
set -u
geom=$1
dpy=$2
vnc=$3
web=$4
conf=$5
profile=$6
downloads=$7

export DISPLAY=":$dpy"
w=${geom%%x*}
h=${geom#*x}

mkdir -p "$profile" "$downloads" || exit 2

# Chromium asks where to save a file unless its profile says otherwise, and
# answering that dialog over VNC on a phone is the worst part of doing this by
# hand. Seeding the preference once, before the profile exists, is what makes a
# download land in the host's own Downloads directory without anybody clicking
# anything. An existing profile is left alone: by then it is the person's.
prefs="$profile/Default/Preferences"
if [ ! -e "$prefs" ]; then
  mkdir -p "$profile/Default" || exit 2
  printf '{"download":{"default_directory":"%%s","prompt_for_download":false},"savefile":{"default_directory":"%%s"}}\n' \
    "$downloads" "$downloads" > "$prefs" || exit 2
fi

url=$(cat "$conf/homepage" 2>/dev/null) || url=""
case "$url" in
  http://*|https://*) ;;
  *) url="about:blank" ;;
esac

Xvfb "$DISPLAY" -screen 0 "${w}x${h}x24" -nolisten tcp &
# The X server takes a moment to come up, and everything after this needs it.
# Waiting for its socket beats sleeping for a guess.
i=0
while [ ! -e "/tmp/.X11-unix/X$dpy" ] && [ "$i" -lt 100 ]; do
  i=$((i + 1))
  sleep 0.1
done

# Without a window manager the browser still runs; its dialogs are just stuck
# where they open. That is a degraded session, not a broken one, so a host
# without openbox carries on.
if command -v openbox >/dev/null 2>&1; then
  openbox &
fi

# -localhost is what keeps the VNC port off the network: the only way in is the
# gateway below, which is the port HostMan's link points at.
x11vnc -display "$DISPLAY" -rfbauth "$conf/vncpasswd" -rfbport "$vnc" \
  -localhost -forever -shared -noxdamage -quiet &

web_root=""
for d in /usr/share/novnc /usr/share/webapps/novnc /usr/share/novnc-common; do
  if [ -d "$d" ]; then web_root="$d"; break; fi
done
if [ -n "$web_root" ]; then
  websockify --web="$web_root" "$web" "localhost:$vnc" &
else
  websockify "$web" "localhost:$vnc" &
fi

%[1]s
pick_browser || true
if [ -z "$browser" ]; then
  if [ -n "$snap_browsers" ] || [ -n "$broken_browsers" ]; then
    printf 'deployer-remote: no browser here can run%%s%%s\n' \
      "${snap_browsers:+ — snaps: $snap_browsers}" "${broken_browsers:+ — will not start: $broken_browsers}"
    printf 'deployer-remote: set the session up again — HostMan will fetch one that is a package\n'
    exit 4
  fi
  printf 'deployer-remote: no browser installed\n'
  exit 3
fi

# Which browser, and what it really is. A distribution that ships its browser as
# a snap wrapper leaves a binary that is on the PATH and cannot run, and the
# resolved path is what says so.
printf 'deployer-remote: %%s is %%s, on %%s at %%sx%%s\n' \
  "$browser" "$(command -v "$browser")" "$DISPLAY" "$w" "$h"
# Asking it its version is the cheapest way to find out whether it can run at
# all: a browser that is really a wrapper around a snap that is not there fails
# this the same way it fails everything else, and says so in one line.
printf 'deployer-remote: %%s\n' "$("$browser" --version 2>&1 | head -1)"

# Chromium leaves these behind when it dies, and every launch after that refuses
# with "the profile appears to be in use" — which, on a screen with nothing else
# on it, looks exactly like a browser that never started at all. Only one
# session runs at a time, so a lock found here is always a stale one.
rm -f "$profile/SingletonLock" "$profile/SingletonSocket" "$profile/SingletonCookie" 2>/dev/null || true

# Whether the browser still has its sandbox. Firefox is left out of the fallback
# below entirely: its own sandbox is not a command-line matter.
sandboxed=1
rm -f "$conf/degraded" 2>/dev/null || true

case "$browser" in
  firefox*)
    set -- --profile "$profile" --width "$w" --height "$h" --new-window "$url"
    ;;
  *)
    # Three flags that are not about preference. --password-store=basic: without
    # it Chromium waits on a keyring no headless host will ever answer.
    # --disable-gpu: there is no GPU behind a virtual screen. And
    # --disable-dev-shm-usage: a VM with a small /dev/shm makes Chromium die
    # somewhere between starting and drawing, which reads as a black screen.
    set -- --user-data-dir="$profile" --no-first-run --no-default-browser-check \
      --password-store=basic --disable-features=Translate \
      --disable-gpu --disable-dev-shm-usage \
      --window-position=0,0 --window-size="$w,$h" --start-maximized "$url"
    # Chromium refuses to start as root with its sandbox on, and a host whose
    # SSH user is root is a host where the session would otherwise never come
    # up at all. Everywhere else the sandbox stays exactly where it is.
    if [ "$(id -u 2>/dev/null || echo 1000)" = 0 ]; then
      set -- --no-sandbox "$@"
      sandboxed=0
    fi
    ;;
esac

# A browser closed by a stray tap should not end the session — the screen, the
# VNC server and the gateway are all still there, and starting it again is
# cheaper than starting the session again.
#
# Its output goes to the journal rather than to /dev/null. A browser that will
# not start says why on stderr, and throwing that away leaves a black screen as
# the only symptom of every possible cause — which is no symptom at all. The
# Services screen is where those lines are read.
fails=0
while :; do
  started=$(date +%%s 2>/dev/null || echo 0)
  "$browser" "$@"
  ended=$(date +%%s 2>/dev/null || echo 0)
  # A browser that ran for a while was closed by whoever was using it. One that
  # died in seconds is failing, and saying so in words beats leaving somebody to
  # infer it from a screen that stays empty.
  if [ "$((ended - started))" -lt 5 ]; then
    fails=$((fails + 1))
    printf 'deployer-remote: %%s exited after %%ss (%%s in a row) — its own output is above\n' \
      "$browser" "$((ended - started))" "$fails"

    # Chromium will not start on a host whose kernel refuses it a sandbox, which
    # on a VPS is ordinary — unprivileged user namespaces turned off, or a
    # setuid helper the kernel will not honour. The choice then is a session
    # that never works or a browser with a weaker defence against the pages it
    # visits, and a session nobody can use protects nobody. So it falls back,
    # once, after the failure rather than in anticipation of it — and leaves a
    # mark, because a browser running without its sandbox is something the
    # person signing into their bank on it is owed in writing.
    if [ "$fails" -eq 2 ] && [ "$sandboxed" = 1 ]; then
      sandboxed=0
      set -- --no-sandbox "$@"
      printf 'deployer-remote: this host will not give %%s a sandbox — retrying without one\n' "$browser"
      printf 'no-sandbox\n' > "$conf/degraded" 2>/dev/null || true
    fi

    if [ "$fails" -ge 4 ]; then
      printf 'deployer-remote: giving it room — retrying every 20s\n'
      sleep 20
    fi
  else
    fails=0
  fi
  sleep 2
done &

wait
`

// renderRemoteSetup builds the script that writes a session onto a host: the
// session script and the installer are embedded in it, so this one string is
// everything HostMan would put there.
func renderRemoteSetup() string {
	return fmt.Sprintf(remoteSetupScript,
		remoteConfDir, remoteLibDir,
		fmt.Sprintf(remoteSessionScript, pickBrowserScript()),
		fmt.Sprintf(remoteInstallScript, remoteConfDir, "", pickBrowserScript()),
		remoteDisplay, remoteVNCPort, RemoteUnit)
}

// remoteRevision names the scripts this build writes, as a hash of them.
//
// A host keeps its copy: the session runs the script that was written when it
// was set up, and updating HostMan does not reach back and rewrite it. That is
// the right behaviour — a running session should not change under somebody —
// but silently is the wrong way to do it, because the fix HostMan shipped is
// then not the code the host is running and nothing on the screen says so.
//
// Hashing the scripts rather than keeping a number by hand means the answer is
// never out of date: change a line of the session script and every host that
// has the old one says so.
func remoteRevision() string {
	sum := sha256.Sum256([]byte(renderRemoteSetup()))
	return hex.EncodeToString(sum[:])[:12]
}

// SetupRemote installs and configures a host's remote session. It returns as
// soon as the install is under way; RemoteStatus is how its progress is read.
//
// It is idempotent. Running it again rewrites the scripts and the unit, keeps
// the password unless asked to replace it, and leaves the browser profile — and
// so every site it is signed into — alone.
func (s *Service) SetupRemote(ctx context.Context, h *store.Host, opts RemoteSetup) (*RemoteSession, error) {
	geometry, err := cleanGeometry(opts.Geometry)
	if err != nil {
		return nil, err
	}
	port, err := cleanRemotePort(opts.Port)
	if err != nil {
		return nil, err
	}
	homepage, err := CleanRemoteURL(opts.Homepage)
	if err != nil {
		return nil, err
	}
	if !userPattern.MatchString(h.Username) {
		return nil, invalid("%q is not a user a session can run as", h.Username)
	}

	res, err := s.run(ctx, h, elevate(renderRemoteSetup(),
		"", h.Username, geometry, strconv.Itoa(port), homepage, boolArg(opts.Reset), remoteRevision()), "")
	if err != nil {
		return nil, err
	}
	if res.ExitCode == 3 {
		return nil, errNeedsRoot(h.Username, h.Name)
	}
	if res.ExitCode != 0 {
		return nil, failure(res, "could not set up a remote session on "+h.Name)
	}
	return s.RemoteStatus(ctx, h)
}

// remoteStartScript points the session at a page and starts it, in one round
// trip. Writing the page first is what makes "open this site" a single tap
// rather than a URL typed into a browser over VNC with a phone keyboard.
const remoteStartScript = `set -u
r=$1
url=$2
etc="$r%[1]s"
[ -f "$etc/config" ] || { printf 'this host has no remote session set up\n' >&2; exit 3; }
if [ -n "$url" ]; then
  printf '%%s\n' "$url" > "$etc/homepage" || exit 4
fi
command -v systemctl >/dev/null 2>&1 || { printf 'systemd is not installed on this host\n' >&2; exit 5; }
systemctl --no-pager start -- %[2]s || exit 6
printf 'started\n'
`

// StartRemote starts the session, optionally opening a page in it.
func (s *Service) StartRemote(ctx context.Context, h *store.Host, page string) (*RemoteSession, error) {
	homepage, err := CleanRemoteURL(page)
	if err != nil {
		return nil, err
	}
	script := fmt.Sprintf(remoteStartScript, remoteConfDir, RemoteUnit)
	res, err := s.run(ctx, h, elevate(script, "", homepage), "")
	if err != nil {
		return nil, err
	}
	if res.ExitCode == 3 {
		return nil, invalid("this host has no remote session set up yet")
	}
	if res.ExitCode != 0 {
		return nil, failure(res, "could not start the remote session on "+h.Name)
	}
	return s.RemoteStatus(ctx, h)
}

// StopRemote stops the session. systemd kills the unit's whole control group,
// so the browser, the VNC server and the gateway go with it — which is what
// makes stopping it worth doing between fetches rather than leaving a
// logged-in browser running all week.
func (s *Service) StopRemote(ctx context.Context, h *store.Host) (*RemoteSession, error) {
	if err := s.Act(ctx, h, RemoteUnit, "stop"); err != nil {
		return nil, err
	}
	return s.RemoteStatus(ctx, h)
}

// remoteRemoveScript takes the session back off the host: the unit first, so
// nothing is left pointing at a file that has gone, then what HostMan wrote.
//
// Two things it does not touch. The packages stay, because apt installed them
// and something else may want them. The downloads stay, because they are the
// files somebody went to the trouble of fetching. The browser profile goes only
// when asked, because it is where the logins are: throwing it away silently
// would mean signing in to everything again with no warning.
const remoteRemoveScript = `set -u
r=$1
u=$2
purge=$3
etc="$r%[1]s"
lib="$r%[2]s"
unit="$r/etc/systemd/system/%[3]s"

if command -v systemctl >/dev/null 2>&1; then
  systemctl stop -- %[3]s >/dev/null 2>&1 || true
fi
rm -f -- "$unit" || exit 3
rm -rf -- "$etc" "$lib/remote-session.sh" "$lib/remote-install.sh" || exit 3
if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload >/dev/null 2>&1 || true
  systemctl reset-failed -- %[3]s >/dev/null 2>&1 || true
fi

if [ "$purge" = 1 ]; then
  home=$(getent passwd "$u" 2>/dev/null | cut -d: -f6)
  [ -n "$home" ] || home="/home/$u"
  rm -rf -- "$r$home/.config/deployer-remote" || exit 4
fi
printf 'removed\n'
`

// RemoveRemote deletes the session from a host. purge takes the browser profile
// — and every site it is signed into — with it.
func (s *Service) RemoveRemote(ctx context.Context, h *store.Host, purge bool) error {
	if !userPattern.MatchString(h.Username) {
		return invalid("%q is not a user a session could have run as", h.Username)
	}
	script := fmt.Sprintf(remoteRemoveScript, remoteConfDir, remoteLibDir, RemoteUnit)
	res, err := s.run(ctx, h, elevate(script, "", h.Username, boolArg(purge)), "")
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return failure(res, "could not remove the remote session from "+h.Name)
	}
	return nil
}

// RemoteURL is where the session is reached: noVNC, on the host, told to
// connect straight away at the size of whatever is looking at it.
//
// The password rides in the query string. It is a real trade-off and worth
// naming: it puts eight characters into a browser history on the LAN, and what
// it buys is not having to type them into a phone every time. The session is
// only reachable from the network the host is on, and anyone already on that
// network with HostMan open can read the password from this screen anyway.
func RemoteURL(address string, session *RemoteSession) string {
	if session == nil || session.Port == 0 || address == "" {
		return ""
	}
	query := url.Values{
		"autoconnect": {"1"},
		"resize":      {"scale"},
		"path":        {"websockify"},
	}
	if session.Password != "" {
		query.Set("password", session.Password)
	}
	return (&url.URL{
		Scheme:   "http",
		Host:     hostPort(address, session.Port),
		Path:     "/vnc.html",
		RawQuery: query.Encode(),
	}).String()
}

// hostPort joins an address and a port, bracketing an IPv6 literal.
func hostPort(address string, port int) string {
	if strings.Contains(address, ":") && !strings.HasPrefix(address, "[") {
		address = "[" + address + "]"
	}
	return address + ":" + strconv.Itoa(port)
}

// cleanGeometry checks a screen size. The bounds are not fussiness: a screen
// smaller than this lays sites out as if on a phone, which is what the session
// exists to avoid, and one larger costs the Pi memory it does not have.
func cleanGeometry(geometry string) (string, error) {
	geometry = strings.TrimSpace(strings.ToLower(geometry))
	if geometry == "" {
		return DefaultRemoteGeometry, nil
	}
	match := geometryPattern.FindStringSubmatch(geometry)
	if match == nil {
		return "", invalid("a screen size reads like 1280x800")
	}
	width, _ := strconv.Atoi(match[1])
	height, _ := strconv.Atoi(match[2])
	if width < 640 || width > 3840 || height < 480 || height > 2160 {
		return "", invalid("a screen between 640x480 and 3840x2160 is what this can offer")
	}
	return geometry, nil
}

// cleanRemotePort checks the port noVNC will answer on.
func cleanRemotePort(port int) (int, error) {
	if port == 0 {
		return DefaultRemotePort, nil
	}
	if port < 1024 || port > 65535 {
		return 0, invalid("a port between 1024 and 65535, please")
	}
	if port == remoteVNCPort {
		return 0, invalid("%d is the port the session's VNC server uses", remoteVNCPort)
	}
	return port, nil
}

// CleanRemoteURL checks the page a session opens with. It must be an ordinary
// web address: it reaches the browser as an argument, and a value that could be
// read as an option, a local file or a javascript: URL is not a page somebody
// asked for.
func CleanRemoteURL(page string) (string, error) {
	page = strings.TrimSpace(page)
	if page == "" {
		return "", nil
	}
	if len(page) > 2048 {
		return "", invalid("that address is too long")
	}
	if strings.ContainsAny(page, " \t\r\n\"'`\\") {
		return "", invalid("that does not look like a web address")
	}
	parsed, err := url.Parse(page)
	if err != nil || parsed.Host == "" {
		return "", invalid("that does not look like a web address")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", invalid("only http:// and https:// addresses can be opened")
	}
	return page, nil
}

// boolArg renders a flag as the "1" or "" the scripts test for.
func boolArg(on bool) string {
	if on {
		return "1"
	}
	return ""
}
