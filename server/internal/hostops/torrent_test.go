package hostops

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The downloader is a unit file, a settings file and three short scripts that
// run on somebody else's machine, so they are tested by running them: against a
// real filesystem, through a real shell, with deluge itself stubbed out. What
// is left is exactly the logic HostMan wrote, which is the part that can be
// wrong.

// delugeStubs are a host with deluge installed: a daemon that can say its
// version, and a console that records what it was asked and answers with
// whatever the test wants it to.
func delugeStubs(t *testing.T, dir, consoleBody string) map[string]string {
	t.Helper()
	return map[string]string{
		"deluged":        `[ "$1" = --version ] && { echo "deluged: 2.1.1"; echo "libtorrent: 2.0.7"; exit 0; }; exit 0`,
		"deluge-console": `printf '%s\n' "$*" >> ` + filepath.Join(dir, "console.log") + "\n" + consoleBody,
	}
}

func torrentSetupFor(root, user, downloads, reset string) string {
	return asUser(renderTorrentSetup(), root, user, downloads, reset, torrentRevision())
}

func torrentStatusFor(root, user string) string {
	return asUser(fmt.Sprintf(torrentStatusScript,
		torrentStateDir, consoleScript(), strings.Join(torrentPieces, " "),
		TorrentUnit, MaxTorrentListBytes, strings.Join(settingKeys, " ")), root, user)
}

func torrentAddFor(root, name, source, path string) string {
	return asUser(fmt.Sprintf(torrentAddScript, torrentStateDir, consoleScript(), TorrentUnit),
		root, name, source, path)
}

func torrentActionFor(root, id, action, data string) string {
	return asUser(fmt.Sprintf(torrentActionScript, torrentStateDir, consoleScript()),
		root, id, action, data)
}

// A host without deluge is a PATH without deluge, and the machine running these
// tests may well have it installed — so those two tests build a PATH of their
// own out of the ordinary tools the scripts use and nothing else. runScript
// cannot do it: it puts its stubs in front of the real PATH rather than instead
// of it, which is right for every other test here.
var bareTools = []string{
	// sh first: asUser builds `sh -c ...`, so the script cannot even start
	// without one.
	"sh", "awk", "base64", "cat", "chmod", "chown", "cut", "date", "df", "dirname",
	"grep", "head", "ls", "mkdir", "mv", "rm", "sed", "sleep", "stat", "tail",
	"timeout", "tr", "wc",
}

// bareBin is a directory holding the stubs given to it and a symlink to each of
// the ordinary tools, so a script run with it as the whole PATH finds
// everything except what the test is proving is absent.
func bareBin(t *testing.T, stubs map[string]string) string {
	t.Helper()
	dir := stubBin(t, stubs)
	for _, tool := range bareTools {
		if _, taken := stubs[tool]; taken {
			continue
		}
		path, err := exec.LookPath(tool)
		if err != nil {
			continue
		}
		if err := os.Symlink(path, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// runBare runs a script with one directory as the entire PATH.
func runBare(t *testing.T, cmd, bin string) (string, int) {
	t.Helper()
	run := exec.Command("/bin/sh", "-c", cmd)
	run.Env = append(os.Environ(), "PATH="+bin)
	var out, errOut strings.Builder
	run.Stdout, run.Stderr = &out, &errOut
	code := 0
	if err := run.Run(); err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run %q: %v", cmd, err)
		}
		code = exit.ExitCode()
	}
	if code != 0 {
		t.Logf("script exited %d: %s", code, strings.TrimSpace(errOut.String()))
	}
	return out.String(), code
}

// stateDir is where a script under test wrote deluge's configuration.
func stateDir(root string) string { return filepath.Join(root, torrentStateDir) }

// A generated script has to be a script before it can be a correct one, and a
// syntax error would only show up as a downloader that will not answer.
func TestTorrentScriptsParse(t *testing.T) {
	scripts := map[string]string{
		"setup":   renderTorrentSetup(),
		"status":  fmt.Sprintf(torrentStatusScript, torrentStateDir, consoleScript(), "deluged", TorrentUnit, 1024, "a b"),
		"seeding": fmt.Sprintf(torrentSeedingScript, torrentStateDir, consoleScript(), TorrentUnit),
		"limit":   fmt.Sprintf(torrentLimitScript, torrentStateDir, consoleScript(), TorrentUnit),
		"add":     fmt.Sprintf(torrentAddScript, torrentStateDir, consoleScript(), TorrentUnit),
		"action":  fmt.Sprintf(torrentActionScript, torrentStateDir, consoleScript()),
		"remove":  fmt.Sprintf(torrentRemoveScript, torrentStateDir, TorrentUnit),
	}
	for name, script := range scripts {
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

// Setup writes the whole downloader: a state directory the daemon can use, a
// password for it, the settings HostMan reads back, and a unit systemd runs.
func TestTorrentSetupWritesTheDaemon(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	bin := stubBin(t, rootStubs(delugeStubs(t, work, "exit 0")))

	out, code := runScript(t, torrentSetupFor(root, "pi", "", ""), "", bin)
	if code != 0 {
		t.Fatalf("setup exited %d: %s", code, out)
	}

	config := read(t, filepath.Join(stateDir(root), "deployer.conf"))
	for _, want := range []string{
		"PORT=" + fmt.Sprint(torrentPort),
		"DOWNLOADS=/home/pi/Downloads/torrents",
		"USER=pi",
		"REVISION=" + torrentRevision(),
	} {
		if !strings.Contains(config, want) {
			t.Errorf("settings do not carry %q:\n%s", want, config)
		}
	}

	// The folder the files land in is made now rather than when the first
	// torrent arrives, so a host that cannot write there says so during setup.
	if info, err := os.Stat(filepath.Join(root, "home/pi/Downloads/torrents")); err != nil || !info.IsDir() {
		t.Errorf("the downloads folder was not created: %v", err)
	}

	unit := read(t, filepath.Join(root, "etc/systemd/system", TorrentUnit))
	for _, want := range []string{
		"User=pi",
		"--do-not-daemonize",
		"--config " + stateDir(root),
		fmt.Sprintf("--port %d", torrentPort),
		"[Install]",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("the unit does not carry %q:\n%s", want, unit)
		}
	}
	if !strings.Contains(unit, filepath.Join(bin, "deluged")) {
		t.Errorf("the unit does not run the deluged it found:\n%s", unit)
	}
}

// The password deluged authenticates against is generated on the host and
// stays there: nothing HostMan reads back carries it.
func TestTorrentSetupKeepsThePasswordOnTheHost(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	bin := stubBin(t, rootStubs(delugeStubs(t, work, "exit 0")))

	if out, code := runScript(t, torrentSetupFor(root, "pi", "", ""), "", bin); code != 0 {
		t.Fatalf("setup exited %d: %s", code, out)
	}
	auth := strings.TrimSpace(read(t, filepath.Join(stateDir(root), "auth")))
	fields := strings.Split(auth, ":")
	if len(fields) != 3 || fields[0] != torrentAccount || fields[2] != "10" {
		t.Fatalf("the auth file is not an account line: %q", auth)
	}
	if len(fields[1]) != 32 {
		t.Fatalf("the password is %d characters, not 32", len(fields[1]))
	}

	config := read(t, filepath.Join(stateDir(root), "deployer.conf"))
	unit := read(t, filepath.Join(root, "etc/systemd/system", TorrentUnit))
	if strings.Contains(config, fields[1]) || strings.Contains(unit, fields[1]) {
		t.Error("the password leaked into a file HostMan reads back")
	}

	// Setting up again is how a folder is changed and how a host takes a newer
	// HostMan's unit. Neither should log the daemon out of itself.
	if out, code := runScript(t, torrentSetupFor(root, "pi", "/srv/torrents", ""), "", bin); code != 0 {
		t.Fatalf("second setup exited %d: %s", code, out)
	}
	if again := strings.TrimSpace(read(t, filepath.Join(stateDir(root), "auth"))); again != auth {
		t.Error("setting up again replaced the password")
	}
	if config := read(t, filepath.Join(stateDir(root), "deployer.conf")); !strings.Contains(config, "DOWNLOADS=/srv/torrents") {
		t.Errorf("the folder was not changed:\n%s", config)
	}

	// Asked for a new password, it makes one — and only then.
	if out, code := runScript(t, torrentSetupFor(root, "pi", "", "1"), "", bin); code != 0 {
		t.Fatalf("reset exited %d: %s", code, out)
	}
	if again := strings.TrimSpace(read(t, filepath.Join(stateDir(root), "auth"))); again == auth {
		t.Error("a reset kept the old password")
	}
}

// deluged writes its own auth file the first time it runs, with a localclient
// account in it. A host where that has already happened must still end up with
// an account HostMan can use, and must keep the one it had.
func TestTorrentSetupAddsToAnExistingAuthFile(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	bin := stubBin(t, rootStubs(delugeStubs(t, work, "exit 0")))

	if err := os.MkdirAll(stateDir(root), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir(root), "auth"), []byte("localclient:abc123:10\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := runScript(t, torrentSetupFor(root, "pi", "", ""), "", bin); code != 0 {
		t.Fatalf("setup exited %d: %s", code, out)
	}

	auth := read(t, filepath.Join(stateDir(root), "auth"))
	if !strings.Contains(auth, "localclient:abc123:10") {
		t.Errorf("setup took away the account that was already there:\n%s", auth)
	}
	if !strings.Contains(auth, torrentAccount+":") {
		t.Errorf("setup left no account for HostMan:\n%s", auth)
	}
}

// Deluge is the one thing on this screen HostMan will not install, so a host
// without it is told what to install rather than left with a unit that cannot
// start.
func TestTorrentSetupRefusesAHostWithoutDeluge(t *testing.T) {
	root := t.TempDir()
	bin := bareBin(t, rootStubs(nil))

	out, code := runBare(t, torrentSetupFor(root, "pi", "", ""), bin)
	if code != 8 {
		t.Fatalf("setup exited %d, want 8: %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/systemd/system", TorrentUnit)); err == nil {
		t.Error("a host without deluge was given a unit anyway")
	}
}

// A unit file needs root. Saying so is better than half a downloader.
func TestTorrentSetupNeedsRoot(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	stubs := rootStubs(delugeStubs(t, work, "exit 0"))
	stubs["id"] = `[ "$1" = -u ] && { echo 1000; exit 0; }; echo pi`
	bin := stubBin(t, stubs)

	if out, code := runScript(t, torrentSetupFor(root, "pi", "", ""), "", bin); code != 3 {
		t.Fatalf("setup exited %d, want 3: %s", code, out)
	}
}

// The status probe answers everything about the downloader in one round trip.
// On a host with nothing installed that answer is "deluge is not here", which
// is a state to report rather than an error to fail with.
func TestTorrentStatusOnAnEmptyHost(t *testing.T) {
	root := t.TempDir()
	bin := bareBin(t, map[string]string{
		"getent": `printf 'pi:x:1000:1000::/home/pi:/bin/sh\n'`,
	})

	out, code := runBare(t, torrentStatusFor(root, "pi"), bin)
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, out)
	}
	daemon := parseTorrentStatus(out, "pi")
	if daemon.Installed {
		t.Error("a host with no deluge reads as installed")
	}
	if len(daemon.Missing) != 2 {
		t.Errorf("missing = %v, want both deluge commands", daemon.Missing)
	}
	if daemon.Configured || daemon.Ready || daemon.Running {
		t.Errorf("an empty host reads as set up: %+v", daemon)
	}
	// Before setup, the folder is the one setup would use — so the screen has
	// something to offer rather than an empty field.
	if daemon.Downloads != "/home/pi/Downloads/torrents" {
		t.Errorf("downloads = %q, want the default under the user's home", daemon.Downloads)
	}
	if daemon.Capacity == 0 {
		t.Error("the disk behind the folder was not measured")
	}
}

// A host with the downloader set up and running: the settings come back, the
// unit's state comes back, and so does everything deluge is working on.
func TestTorrentStatusReadsTheDaemon(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	stubs := rootStubs(delugeStubs(t, work, `case "$*" in
  *info*) cat <<'INFO'
Name: ubuntu-24.04.1-desktop-amd64.iso
ID: 8cbd1d8a2cbd0f6c9b1b3b0e5b3b3b3b3b3b3b3b
State: Downloading Down Speed: 1.2 M/s Up Speed: 24.0 K/s
Seeds: 12 (145) Peers: 3 (58) Availability: 2.31 Seed Rank: -
Size: 512.0 M/5.7 G Downloaded: 512.0 M Uploaded: 0 B Share Ratio: -1.00
ETA: 12m 30s Seeding: - Active: 4m 2s
Last Transfer: 2s Complete Seen: Never
Tracker: releases.ubuntu.com
Tracker status: releases.ubuntu.com: Announce OK
Progress: 8.77% [##------------------]
Download Folder: /home/pi/Downloads/torrents
INFO
  ;;
esac
exit 0`))
	bin := stubBin(t, stubs)

	if out, code := runScript(t, torrentSetupFor(root, "pi", "", ""), "", bin); code != 0 {
		t.Fatalf("setup exited %d: %s", code, out)
	}
	out, code := runScript(t, torrentStatusFor(root, "pi"), "", bin)
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, out)
	}

	daemon := parseTorrentStatus(out, "pi")
	if !daemon.Installed || !daemon.Configured {
		t.Fatalf("a set-up host does not read as one: %+v", daemon)
	}
	if daemon.Version != "2.1.1" {
		t.Errorf("version = %q, want deluged's own", daemon.Version)
	}
	if daemon.Stale {
		t.Error("a host set up by this build reads as stale")
	}
	if len(daemon.Torrents) != 1 {
		t.Fatalf("torrents = %d, want 1: %+v", len(daemon.Torrents), daemon.Torrents)
	}
	got := daemon.Torrents[0]
	if got.Name != "ubuntu-24.04.1-desktop-amd64.iso" || got.State != "Downloading" {
		t.Errorf("torrent = %+v", got)
	}
	if got.Progress != 8.77 || got.ETA != 750 {
		t.Errorf("progress = %v, eta = %v", got.Progress, got.ETA)
	}
	if daemon.Trouble != "" {
		t.Errorf("trouble = %q on a healthy daemon", daemon.Trouble)
	}

	// The console was asked as the user, against HostMan's own config
	// directory and its own port — not deluge's default, which is where a
	// daemon somebody else runs would be answering.
	log := read(t, filepath.Join(work, "console.log"))
	if !strings.Contains(log, "-c "+stateDir(root)) {
		t.Errorf("the console was pointed somewhere else:\n%s", log)
	}
	if !strings.Contains(log, fmt.Sprintf("-d 127.0.0.1 -p %d -U %s -P ", torrentPort, torrentAccount)) {
		t.Errorf("the console did not connect as HostMan's own account:\n%s", log)
	}
	// The listing has to be the verbose one: the short form deluge prints by
	// default is one line a torrent with none of the numbers on this screen.
	if !strings.Contains(log, "info --verbose") {
		t.Errorf("the console asked for the short listing:\n%s", log)
	}
}

// A host whose unit says the daemon is dead is not asked what it is
// downloading: it would be a slow way of being told what systemd already said.
func TestTorrentStatusSkipsAStoppedDaemon(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	stubs := rootStubs(delugeStubs(t, work, "exit 0"))
	setup := stubBin(t, stubs)
	if out, code := runScript(t, torrentSetupFor(root, "pi", "", ""), "", setup); code != 0 {
		t.Fatalf("setup exited %d: %s", code, out)
	}

	stubs["systemctl"] = `case "$1" in is-active) exit 3;; esac
printf 'LoadState=loaded\nActiveState=inactive\nSubState=dead\nUnitFileState=enabled\n'`
	bin := stubBin(t, stubs)

	out, code := runScript(t, torrentStatusFor(root, "pi"), "", bin)
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, out)
	}
	daemon := parseTorrentStatus(out, "pi")
	if daemon.Running {
		t.Error("a dead daemon reads as running")
	}
	if !daemon.Enabled {
		t.Error("a unit systemd calls enabled does not read as enabled")
	}
	if !daemon.Ready {
		t.Error("a stopped daemon that is set up is not ready to be started")
	}
	if daemon.Trouble != "" {
		t.Errorf("trouble = %q — a stopped daemon is not trouble", daemon.Trouble)
	}
	if _, err := os.Stat(filepath.Join(work, "console.log")); err == nil {
		t.Error("a stopped daemon was asked what it was downloading")
	}
}

// A .torrent file picked on a phone is written on the host, handed to deluge
// with the folder it should download into, and taken away again.
func TestAddScriptWritesTheFileAndHandsItOver(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	stubs := rootStubs(delugeStubs(t, work, `printf 'Torrent added!\n'; exit 0`))
	bin := stubBin(t, stubs)
	if out, code := runScript(t, torrentSetupFor(root, "pi", "", ""), "", bin); code != 0 {
		t.Fatalf("setup exited %d: %s", code, out)
	}

	body := base64.StdEncoding.EncodeToString([]byte("d8:announce4:x:x4:infod4:name4:testee"))
	out, code := runScript(t, torrentAddFor(root, "picked.torrent", "", ""), body, bin)
	if code != 0 {
		t.Fatalf("add exited %d: %s", code, out)
	}
	if err := torrentConsoleError(out); err != nil {
		t.Fatalf("a torrent deluge took reads as a failure: %v", err)
	}

	log := read(t, filepath.Join(work, "console.log"))
	want := fmt.Sprintf(`add -p "/home/pi/Downloads/torrents" "%s"`, filepath.Join(stateDir(root), "incoming/picked.torrent"))
	if !strings.Contains(log, want) {
		t.Errorf("deluge was asked %q, want it to carry %q", log, want)
	}
	// Deluge keeps its own copy, so HostMan's is rubbish the moment it has
	// been taken.
	if _, err := os.Stat(filepath.Join(stateDir(root), "incoming/picked.torrent")); err == nil {
		t.Error("the torrent file was left behind on the host")
	}
}

// A magnet link needs no file at all, and a torrent can be sent somewhere other
// than the folder the downloader was set up with.
func TestAddScriptTakesAMagnetAndAFolder(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	bin := stubBin(t, rootStubs(delugeStubs(t, work, `printf 'Torrent added!\n'; exit 0`)))
	if out, code := runScript(t, torrentSetupFor(root, "pi", "", ""), "", bin); code != 0 {
		t.Fatalf("setup exited %d: %s", code, out)
	}

	magnet := "magnet:?xt=urn:btih:8cbd1d8a2cbd0f6c9b1b3b0e5b3b3b3b3b3b3b3b&dn=thing"
	out, code := runScript(t, torrentAddFor(root, "", magnet, "/srv/media"), "", bin)
	if code != 0 {
		t.Fatalf("add exited %d: %s", code, out)
	}
	log := read(t, filepath.Join(work, "console.log"))
	if !strings.Contains(log, fmt.Sprintf(`add -p "/srv/media" "%s"`, magnet)) {
		t.Errorf("the magnet did not reach deluge whole:\n%s", log)
	}
	if info, err := os.Stat(filepath.Join(root, "srv/media")); err != nil || !info.IsDir() {
		t.Errorf("the folder asked for was not created: %v", err)
	}
}

// The add script runs as the SSH user and cannot start a service. A stopped
// daemon is reported as such so HostMan can start it and ask again, rather
// than the screen refusing something somebody clearly asked for.
func TestAddScriptSaysWhenTheDaemonIsStopped(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	stubs := rootStubs(delugeStubs(t, work, "exit 0"))
	if out, code := runScript(t, torrentSetupFor(root, "pi", "", ""), "", stubBin(t, stubs)); code != 0 {
		t.Fatalf("setup exited %d: %s", code, out)
	}
	stubs["systemctl"] = `case "$1" in is-active) exit 3;; esac; exit 0`
	bin := stubBin(t, stubs)

	if out, code := runScript(t, torrentAddFor(root, "", "magnet:?xt=urn:btih:"+strings.Repeat("a", 40), ""), "", bin); code != 4 {
		t.Fatalf("add exited %d, want 4: %s", code, out)
	}
	if _, err := os.Stat(filepath.Join(work, "console.log")); err == nil {
		t.Error("a stopped daemon was talked to anyway")
	}
}

// Adding a torrent to a host that has no downloader is a request to fix, not a
// failure on the host.
func TestAddScriptRefusesAHostWithNoDownloader(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	bin := stubBin(t, rootStubs(delugeStubs(t, work, "exit 0")))

	if out, code := runScript(t, torrentAddFor(root, "", "magnet:?xt=urn:btih:"+strings.Repeat("a", 40), ""), "", bin); code != 3 {
		t.Fatalf("add exited %d, want 3: %s", code, out)
	}
}

// Pause and resume are one command each. Removing is two spellings of one,
// because deluge 2.1 wants a --confirm that nothing before it has heard of.
func TestActionScriptPausesAndRemoves(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	id := strings.Repeat("a1", 20)
	bin := stubBin(t, rootStubs(delugeStubs(t, work, "exit 0")))
	if out, code := runScript(t, torrentSetupFor(root, "pi", "", ""), "", bin); code != 0 {
		t.Fatalf("setup exited %d: %s", code, out)
	}

	if out, code := runScript(t, torrentActionFor(root, id, "pause", ""), "", bin); code != 0 {
		t.Fatalf("pause exited %d: %s", code, out)
	}
	if log := read(t, filepath.Join(work, "console.log")); !strings.Contains(log, "pause "+id) {
		t.Errorf("pause did not reach deluge:\n%s", log)
	}

	if out, code := runScript(t, torrentActionFor(root, id, "remove", "1"), "", bin); code != 0 {
		t.Fatalf("remove exited %d: %s", code, out)
	}
	if log := read(t, filepath.Join(work, "console.log")); !strings.Contains(log, "rm --confirm --remove_data "+id) {
		t.Errorf("remove did not ask for the data too:\n%s", log)
	}
}

// An older deluge complains about the flag it has never heard of, and the same
// removal is asked for again in the words that version knows.
func TestActionScriptFallsBackForAnOlderDeluge(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	id := strings.Repeat("b2", 20)
	console := `case "$*" in
  *--confirm*) printf 'usage: rm [-h] torrent\nrm: error: unrecognized arguments: --confirm\n'; exit 1;;
esac
exit 0`
	bin := stubBin(t, rootStubs(delugeStubs(t, work, console)))
	if out, code := runScript(t, torrentSetupFor(root, "pi", "", ""), "", bin); code != 0 {
		t.Fatalf("setup exited %d: %s", code, out)
	}

	out, code := runScript(t, torrentActionFor(root, id, "remove", ""), "", bin)
	if code != 0 {
		t.Fatalf("remove exited %d: %s", code, out)
	}
	if err := torrentConsoleError(out); err != nil {
		t.Fatalf("the retry was still read as a failure: %v", err)
	}
	log := read(t, filepath.Join(work, "console.log"))
	if !strings.Contains(log, "rm "+id) {
		t.Errorf("the older spelling was never tried:\n%s", log)
	}
}

// Removing the downloader takes the service and deluge's state. It does not
// take deluge, and it never takes what was downloaded.
func TestTorrentRemoveScriptLeavesTheFiles(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	bin := stubBin(t, rootStubs(delugeStubs(t, work, "exit 0")))
	if out, code := runScript(t, torrentSetupFor(root, "pi", "", ""), "", bin); code != 0 {
		t.Fatalf("setup exited %d: %s", code, out)
	}
	downloaded := filepath.Join(root, "home/pi/Downloads/torrents/film.mkv")
	if err := os.WriteFile(downloaded, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	script := asUser(fmt.Sprintf(torrentRemoveScript, torrentStateDir, TorrentUnit), root)
	if out, code := runScript(t, script, "", bin); code != 0 {
		t.Fatalf("remove exited %d: %s", code, out)
	}
	if _, err := os.Stat(stateDir(root)); err == nil {
		t.Error("deluge's state was left behind")
	}
	if _, err := os.Stat(filepath.Join(root, "etc/systemd/system", TorrentUnit)); err == nil {
		t.Error("the unit was left behind")
	}
	if _, err := os.Stat(downloaded); err != nil {
		t.Error("removing the downloader deleted what had been downloaded")
	}
}

// --- parsing ---

// Deluge puts several facts on one line, leaves out the ones it has nothing to
// say about, and prints the ones it has in more than one shape. This is what
// `info --verbose` really writes, taken from a running deluge 2.1.
func TestParseTorrentList(t *testing.T) {
	out := `Name: A Film (2019)
ID: 8cbd1d8a2cbd0f6c9b1b3b0e5b3b3b3b3b3b3b3b
State: Downloading Down Speed: 1.2 M/s Up Speed: 24.0 K/s
Seeds: 12 (145) Peers: 3 (58) Availability: 2.31 Seed Rank: -
Size: 512.0 M/5.7 G Downloaded: 512.0 M Uploaded: 0 B Share Ratio: 0.02
ETA: 1h 2m Seeding: - Active: 12m 4s
Last Transfer: 2s Complete Seen: Never
Tracker: tracker.example
Tracker status: tracker.example: Announce OK
Progress: 8.77% [##------------------]
Download Folder: /home/pi/Downloads/torrents

Name: debian-12.5.0-arm64-netinst.iso
ID: 3f0e1d8a2cbd0f6c9b1b3b0e5b3b3b3b3b3b3b3b
State: Paused
Size: 628.0 M Downloaded: 628.0 M Uploaded: 1.2 G Share Ratio: 1.96
ETA: - Seeding: 4h 2m Active: 6h 1m
Last Transfer: 1m 2s Complete Seen: Never
Tracker: tracker.example
Tracker status: tracker.example: Announce OK
Download Folder: /srv/media
`
	torrents, trouble := parseTorrentList(out)
	if trouble != "" {
		t.Fatalf("trouble = %q on a good listing", trouble)
	}
	if len(torrents) != 2 {
		t.Fatalf("torrents = %d, want 2", len(torrents))
	}

	first := torrents[0]
	if first.Name != "A Film (2019)" {
		t.Errorf("name = %q", first.Name)
	}
	if first.ID != "8cbd1d8a2cbd0f6c9b1b3b0e5b3b3b3b3b3b3b3b" {
		t.Errorf("id = %q", first.ID)
	}
	if first.State != "Downloading" {
		t.Errorf("state = %q", first.State)
	}
	if first.Down != 1258291 || first.Up != 24576 {
		t.Errorf("down = %d, up = %d", first.Down, first.Up)
	}
	if first.Done != 536870912 || first.Size != 6120328396 {
		t.Errorf("done = %d, size = %d", first.Done, first.Size)
	}
	// The ETA shares its line with the seeding and active times, and stops
	// where the next of them starts.
	if first.ETA != 3720 || first.ETAText != "1h 2m" {
		t.Errorf("eta = %d (%q)", first.ETA, first.ETAText)
	}
	if first.Seeds != 12 || first.SeedsTotal != 145 || first.Peers != 3 || first.PeersTotal != 58 {
		t.Errorf("swarm = %+v", first)
	}
	if first.Ratio != 0.02 || first.Progress != 8.77 {
		t.Errorf("ratio = %v, progress = %v", first.Ratio, first.Progress)
	}
	if first.Folder != "/home/pi/Downloads/torrents" {
		t.Errorf("folder = %q", first.Folder)
	}

	// A paused torrent has no speeds and no swarm, a finished one is given one
	// size rather than the same figure twice and no progress bar at all, and
	// "ETA: -" is deluge declining to guess. None of that is a parse failure.
	second := torrents[1]
	if second.State != "Paused" || second.Down != 0 || second.SeedsTotal != 0 {
		t.Errorf("paused torrent = %+v", second)
	}
	if second.Done != 658505728 || second.Size != 658505728 {
		t.Errorf("done = %d, size = %d — a finished torrent is given one size", second.Done, second.Size)
	}
	if second.Progress != 100 {
		t.Errorf("progress = %v, want a finished torrent at 100", second.Progress)
	}
	if second.ETA != 0 || second.ETAText != "" {
		t.Errorf("eta = %d (%q), want nothing said", second.ETA, second.ETAText)
	}
	if second.Ratio != 1.96 || second.Folder != "/srv/media" {
		t.Errorf("finished torrent = %+v", second)
	}
}

// A magnet link is a torrent deluge knows almost nothing about until its
// metadata arrives: no size, no swarm, and a ratio of -1 meaning "not yet".
func TestParseTorrentListReadsAMagnetWithNoMetadata(t *testing.T) {
	out := `Name: archlinux
ID: c12fe1c06bba254a9dc9f519b335aa7c1367a88a
State: Downloading Down Speed: 0.0 K/s Up Speed: 0.0 K/s
Seeds: 0 (-1) Peers: 0 (-1) Availability: 0.00 Seed Rank: -
Size: 0 B Downloaded: 0 B Uploaded: 0 B Share Ratio: -1.00
ETA: - Seeding: - Active: 5s
Progress: 0.00% [--------------------]
Download Folder: /srv/media
`
	torrents, trouble := parseTorrentList(out)
	if len(torrents) != 1 || trouble != "" {
		t.Fatalf("torrents = %+v, trouble = %q", torrents, trouble)
	}
	got := torrents[0]
	if got.Size != 0 || got.Progress != 0 {
		t.Errorf("a torrent with no metadata reads as %+v", got)
	}
	// -1 is deluge for "the tracker has not said yet", and a negative count on
	// a screen is worse than none.
	if got.SeedsTotal != 0 || got.PeersTotal != 0 || got.Ratio != 0 {
		t.Errorf("negative figures reached the screen: %+v", got)
	}
}

// "Nothing is downloading" and "nobody could be asked" are different answers,
// and a screen that showed the second as the first would be lying.
func TestParseTorrentListReportsTrouble(t *testing.T) {
	torrents, trouble := parseTorrentList("Failed to connect to 127.0.0.1:58946: Connection refused\n")
	if len(torrents) != 0 {
		t.Fatalf("torrents = %+v", torrents)
	}
	if !strings.Contains(trouble, "Connection refused") {
		t.Errorf("trouble = %q", trouble)
	}

	if _, trouble := parseTorrentList("\n \n"); trouble != "" {
		t.Errorf("an empty listing is not trouble, got %q", trouble)
	}
}

// Deluge colours its output when it thinks something is watching, and those
// bytes must not end up in a torrent's name.
func TestParseTorrentListStripsColour(t *testing.T) {
	out := "\x1b[36mName:\x1b[0m \x1b[1mA Film\x1b[0m\nID: " + strings.Repeat("c3", 20) + "\nProgress: 50.00% [##]\n"
	torrents, _ := parseTorrentList(out)
	if len(torrents) != 1 || torrents[0].Name != "A Film" {
		t.Fatalf("torrents = %+v", torrents)
	}
}

func TestParseByteSize(t *testing.T) {
	cases := map[string]int64{
		"512.0 MiB": 536870912,
		"1.2 MiB":   1258291,
		"0.0 KiB":   0,
		"628 B":     628,
		"5.7 GiB":   6120328396,
		"1.0 TiB":   1099511627776,
		"":          0,
		"nonsense":  0,
		"12 parsec": 0,
	}
	for input, want := range cases {
		if got := parseByteSize(input); got != want {
			t.Errorf("parseByteSize(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestParseTorrentETA(t *testing.T) {
	cases := map[string]int64{
		"12m 30s": 750,
		"1h 2m":   3720,
		"3d 4h":   273600,
		"45s":     45,
		"∞":       0,
		"":        0,
	}
	for input, want := range cases {
		if got := parseTorrentETA(input); got != want {
			t.Errorf("parseTorrentETA(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestParseDiskFree(t *testing.T) {
	free, capacity := parseDiskFree("/dev/root       30801080 5203632  24259036  18% /")
	if free != 24259036*1024 || capacity != 30801080*1024 {
		t.Errorf("free = %d, capacity = %d", free, capacity)
	}
	if free, capacity := parseDiskFree("df: nope"); free != 0 || capacity != 0 {
		t.Errorf("a df that failed reads as %d/%d", free, capacity)
	}
}

// --- what HostMan will and will not send ---

func TestCleanTorrentSource(t *testing.T) {
	good := []string{
		"magnet:?xt=urn:btih:8cbd1d8a2cbd0f6c9b1b3b0e5b3b3b3b3b3b3b3b&dn=thing&tr=udp://t.example:80",
		"magnet:?dn=thing&xt=urn:btih:c12fe1c06bba254a9dc9f519b335aa7c1367a88a",
		"https://example.com/files/thing.torrent",
		"http://example.com/thing.torrent?token=abc",
	}
	for _, source := range good {
		if _, err := CleanTorrentSource(source); err != nil {
			t.Errorf("CleanTorrentSource(%q) refused it: %v", source, err)
		}
	}
	bad := []string{
		"",
		"thing.torrent",
		"/home/pi/thing.torrent",
		"ftp://example.com/thing.torrent",
		"file:///etc/shadow",
		"magnet:?dn=no-hash-here",
		`https://example.com/"; rm -rf /`,
		"--config=/etc/passwd",
		"magnet:?xt=urn:btih:8cbd1d8a2cbd0f6c9b1b3b0e5b3b3b3b3b3b3b3b и пробел",
		"https://example.com/" + strings.Repeat("a", 5000),
	}
	for _, source := range bad {
		if _, err := CleanTorrentSource(source); err == nil {
			t.Errorf("CleanTorrentSource(%q) allowed it", source)
		}
	}
}

func TestCleanTorrentAdd(t *testing.T) {
	torrent := base64.StdEncoding.EncodeToString([]byte("d8:announce20:http://t.example/a4:infod4:name5:thingee"))

	source, name, body, err := cleanTorrentAdd(TorrentAdd{File: torrent, Name: "Some Film (2019).torrent"})
	if err != nil {
		t.Fatalf("a torrent file was refused: %v", err)
	}
	if source != "" || body == "" {
		t.Errorf("source = %q, body = %q", source, body)
	}
	// The name only ever names a file on the host — deluge takes the torrent's
	// real name from inside it — so it is reduced to something that can only be
	// a filename.
	if name != "Some-Film-2019.torrent" {
		t.Errorf("name = %q", name)
	}

	refusals := map[string]TorrentAdd{
		"nothing at all":      {},
		"both at once":        {Source: "magnet:?xt=urn:btih:" + strings.Repeat("a", 40), File: torrent},
		"not base64":          {File: "!!!!"},
		"empty file":          {File: base64.StdEncoding.EncodeToString(nil)},
		"a photo":             {File: base64.StdEncoding.EncodeToString([]byte("\xff\xd8\xff\xe0 JFIF"))},
		"bencode but no info": {File: base64.StdEncoding.EncodeToString([]byte("d3:foo3:bare"))},
		"far too large":       {File: base64.StdEncoding.EncodeToString(append([]byte("d4:info"), make([]byte, MaxTorrentFileBytes)...))},
	}
	for what, in := range refusals {
		if _, _, _, err := cleanTorrentAdd(in); err == nil {
			t.Errorf("%s was accepted", what)
		}
	}
}

func TestTorrentFileName(t *testing.T) {
	cases := map[string]string{
		"ubuntu.torrent":         "ubuntu.torrent",
		"../../etc/passwd":       "etc-passwd.torrent",
		"  spaced name .torrent": "spaced-name.torrent",
		"":                       "picked.torrent",
		".hidden":                "hidden.torrent",
		strings.Repeat("x", 200): strings.Repeat("x", 80) + ".torrent",
	}
	for input, want := range cases {
		if got := torrentFileName(input); got != want {
			t.Errorf("torrentFileName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCleanDownloads(t *testing.T) {
	if got, err := cleanDownloads(""); err != nil || got != "" {
		t.Errorf("an empty folder is the host's to fill in, got %q, %v", got, err)
	}
	if got, err := cleanDownloads(" /srv/media/ "); err != nil || got != "/srv/media" {
		t.Errorf("cleanDownloads = %q, %v", got, err)
	}
	for _, folder := range []string{"srv/media", "/", `/srv/"media"`, "/srv/media\n/etc"} {
		if _, err := cleanDownloads(folder); err == nil {
			t.Errorf("cleanDownloads(%q) allowed it", folder)
		}
	}
}

// deluge-console reports a torrent it would not take by printing a line and
// exiting 0, so the exit status alone would have a screen saying a torrent was
// added and then never showing it.
func TestTorrentConsoleError(t *testing.T) {
	fine := []string{
		"@@code\n0\n@@out\nTorrent added!\n",
		"@@code\n0\n@@out\n\n",
		// Asking for something already there is not a failure: what the caller
		// wanted is true.
		"@@code\n0\n@@out\nError: Torrent already in session (8cbd1d8a).\n",
	}
	for _, out := range fine {
		if err := torrentConsoleError(out); err != nil {
			t.Errorf("torrentConsoleError(%q) = %v", out, err)
		}
	}

	bad := map[string]string{
		"a refusal with a zero status": "@@code\n0\n@@out\nError: Unable to add torrent, decoding filedump failed.\n",
		"a non-zero status":            "@@code\n1\n@@out\nFailed to connect to 127.0.0.1:58946\n",
		"a status and nothing said":    "@@code\n2\n@@out\n\n",
	}
	for what, out := range bad {
		if err := torrentConsoleError(out); err == nil {
			t.Errorf("%s was read as success", what)
		}
	}
}

// The revision is a hash of what setup writes, so a change to the unit is a
// change to the revision and every host running the old one says so.
func TestTorrentRevisionTracksTheScript(t *testing.T) {
	daemon := parseTorrentStatus("@@config\nREVISION=older\n", "pi")
	if !daemon.Stale {
		t.Error("a host running an older unit does not read as stale")
	}
	current := parseTorrentStatus("@@config\nREVISION="+torrentRevision()+"\n", "pi")
	if current.Stale {
		t.Error("a host running this build's unit reads as stale")
	}
}

// systemd calls a Type=simple service started as soon as it has forked, which
// is a moment before deluged is listening. HostMan starts a stopped daemon and
// says this in the same breath, so the first attempt can arrive before anybody
// is there to hear it.
func TestAddScriptWaitsForADaemonThatIsStillStarting(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	console := `n=$(cat ` + filepath.Join(work, "tries") + ` 2>/dev/null || echo 0)
n=$((n + 1))
printf '%s' "$n" > ` + filepath.Join(work, "tries") + `
if [ "$n" -lt 3 ]; then printf 'Failed to connect to 127.0.0.1:58946\n'; exit 1; fi
printf 'Torrent added!\n'`
	bin := stubBin(t, rootStubs(delugeStubs(t, work, console)))
	if out, code := runScript(t, torrentSetupFor(root, "pi", "", ""), "", bin); code != 0 {
		t.Fatalf("setup exited %d: %s", code, out)
	}

	out, code := runScript(t, torrentAddFor(root, "", "magnet:?xt=urn:btih:"+strings.Repeat("a", 40), ""), "", bin)
	if code != 0 {
		t.Fatalf("add exited %d: %s", code, out)
	}
	if err := torrentConsoleError(out); err != nil {
		t.Fatalf("the add gave up on a daemon that was coming up: %v", err)
	}
	if tries := strings.TrimSpace(read(t, filepath.Join(work, "tries"))); tries != "3" {
		t.Errorf("the daemon was asked %s times, want 3", tries)
	}
}

// A daemon that is refusing connections for good is reported rather than
// waited on forever.
func TestAddScriptGivesUpOnADaemonThatNeverAnswers(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	console := `printf 'Failed to connect to 127.0.0.1:58946\n'; exit 1`
	bin := stubBin(t, rootStubs(delugeStubs(t, work, console)))
	if out, code := runScript(t, torrentSetupFor(root, "pi", "", ""), "", bin); code != 0 {
		t.Fatalf("setup exited %d: %s", code, out)
	}

	out, code := runScript(t, torrentAddFor(root, "", "magnet:?xt=urn:btih:"+strings.Repeat("a", 40), ""), "", bin)
	if code != 0 {
		t.Fatalf("add exited %d: %s", code, out)
	}
	err := torrentConsoleError(out)
	if err == nil {
		t.Fatal("a daemon that never answered was reported as a torrent added")
	}
	if !strings.Contains(err.Error(), "Failed to connect") {
		t.Errorf("the error does not say what happened: %v", err)
	}
}

// Only a line that reads like a failure is trouble. A deluge that prints
// something harmless has still answered, and an empty list under a red banner
// would be a screen inventing a problem.
func TestParseTorrentListIgnoresHarmlessNoise(t *testing.T) {
	_, trouble := parseTorrentList("DeprecationWarning: the twisted reactor is old\n")
	if trouble != "" {
		t.Errorf("trouble = %q, want a notice to be left alone", trouble)
	}
}

// An empty list means different things depending on what the probe managed to
// do, and a screen that could not tell them apart would throw away a running
// download the first time deluge was slow to answer.
func TestTorrentStatusSaysWhetherDelugeAnswered(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	stubs := rootStubs(delugeStubs(t, work, `printf 'Failed to connect to 127.0.0.1:58946\n'; exit 1`))
	bin := stubBin(t, stubs)
	if out, code := runScript(t, torrentSetupFor(root, "pi", "", ""), "", bin); code != 0 {
		t.Fatalf("setup exited %d: %s", code, out)
	}

	out, code := runScript(t, torrentStatusFor(root, "pi"), "", bin)
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, out)
	}
	daemon := parseTorrentStatus(out, "pi")
	if daemon.Asked {
		t.Error("a console that failed reads as an answer")
	}
	if daemon.Trouble == "" {
		t.Error("nothing was said about why there is no list")
	}

	// A daemon that is stopped is not trouble: the screen says so already, in a
	// place with a button under it.
	stubs["systemctl"] = `case "$1" in is-active) exit 3;; esac
printf 'LoadState=loaded\nActiveState=inactive\nSubState=dead\nUnitFileState=enabled\n'`
	out, code = runScript(t, torrentStatusFor(root, "pi"), "", stubBin(t, stubs))
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, out)
	}
	stopped := parseTorrentStatus(out, "pi")
	if stopped.Asked {
		t.Error("a stopped daemon reads as having answered")
	}
	if stopped.Trouble != "" {
		t.Errorf("trouble = %q — a stopped daemon is not trouble", stopped.Trouble)
	}
}

// The daemon's settings ride back in the same console as the listing, so they
// are taken out of it before the torrents are read.
func TestSplitSettings(t *testing.T) {
	listing, seeding, limit := splitSettings(`Name: A Film
ID: ` + strings.Repeat("a", 40) + `
remove_seed_at_ratio: True
stop_seed_at_ratio: True
stop_seed_ratio: 1.5
max_active_limit: 8
`)
	if strings.Contains(listing, "stop_seed") || strings.Contains(listing, "max_active") {
		t.Errorf("the settings were left in the listing:\n%s", listing)
	}
	if seeding.Ratio != 1.5 || !seeding.Remove {
		t.Errorf("seeding = %+v", seeding)
	}
	if limit != 8 {
		t.Errorf("limit = %d, want deluge's own 8", limit)
	}
	torrents, trouble := parseTorrentList(listing)
	if len(torrents) != 1 || trouble != "" {
		t.Errorf("torrents = %+v, trouble = %q", torrents, trouble)
	}

	// Deluge keeps a ratio whether or not it is using one, so the switch is
	// what decides — otherwise a screen would show a rule that never fires.
	_, off, _ := splitSettings("stop_seed_at_ratio: False\nstop_seed_ratio: 2.0\nremove_seed_at_ratio: False\n")
	if off.Ratio != 0 || off.Remove {
		t.Errorf("a rule that is switched off reads as %+v", off)
	}

	// -1 is deluge for no limit at all, and worth passing along as itself; a
	// console that was never asked reads as not knowing rather than as a limit.
	if _, _, none := splitSettings("max_active_limit: -1\n"); none != -1 {
		t.Errorf("no limit at all reads as %d", none)
	}
	if _, _, unknown := splitSettings("Name: A Film\n"); unknown != 0 {
		t.Errorf("a console that never said reads as a limit of %d", unknown)
	}
}

// The seeding rule is deluge's own setting, so it holds when nobody is looking
// at the screen — which is when torrents finish.
func TestSeedingScriptTellsDeluge(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	bin := stubBin(t, rootStubs(delugeStubs(t, work, `printf 'Configuration value successfully updated.\n'`)))
	if out, code := runScript(t, torrentSetupFor(root, "pi", "", ""), "", bin); code != 0 {
		t.Fatalf("setup exited %d: %s", code, out)
	}

	script := asUser(fmt.Sprintf(torrentSeedingScript, torrentStateDir, consoleScript(), TorrentUnit),
		root, "True", "1.50", "True")
	out, code := runScript(t, script, "", bin)
	if code != 0 {
		t.Fatalf("seeding exited %d: %s", code, out)
	}
	if err := torrentConsoleError(out); err != nil {
		t.Fatalf("deluge would not take the rule: %v", err)
	}
	log := read(t, filepath.Join(work, "console.log"))
	for _, want := range []string{
		"config --set stop_seed_at_ratio True",
		"config --set stop_seed_ratio 1.50",
		"config --set remove_seed_at_ratio True",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("deluge was never told %q:\n%s", want, log)
		}
	}
	// One console for three settings: starting one is a second of python.
	if lines := strings.Count(strings.TrimSpace(log), "\n") + 1; lines != 1 {
		t.Errorf("the rule took %d consoles, want 1:\n%s", lines, log)
	}
}

// The torrent limit is deluge's own queue setting, and it is one knob over
// deluge's three: setting only the total would leave deluge's own smaller
// per-kind defaults holding the queue under the number that was chosen.
func TestLimitScriptTellsDeluge(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	bin := stubBin(t, rootStubs(delugeStubs(t, work, `printf 'Configuration value successfully updated.\n'`)))
	if out, code := runScript(t, torrentSetupFor(root, "pi", "", ""), "", bin); code != 0 {
		t.Fatalf("setup exited %d: %s", code, out)
	}

	script := asUser(fmt.Sprintf(torrentLimitScript, torrentStateDir, consoleScript(), TorrentUnit),
		root, "12")
	out, code := runScript(t, script, "", bin)
	if code != 0 {
		t.Fatalf("limit exited %d: %s", code, out)
	}
	if err := torrentConsoleError(out); err != nil {
		t.Fatalf("deluge would not take the limit: %v", err)
	}
	log := read(t, filepath.Join(work, "console.log"))
	for _, want := range []string{
		"config --set max_active_limit 12",
		"config --set max_active_downloading 12",
		"config --set max_active_seeding 12",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("deluge was never told %q:\n%s", want, log)
		}
	}
	// One console for three settings: starting one is a second of python.
	if lines := strings.Count(strings.TrimSpace(log), "\n") + 1; lines != 1 {
		t.Errorf("the limit took %d consoles, want 1:\n%s", lines, log)
	}
}

func TestCleanActiveLimit(t *testing.T) {
	if got, err := cleanActiveLimit(10); err != nil || got != 10 {
		t.Errorf("cleanActiveLimit(10) = %v, %v", got, err)
	}
	// -1 is deluge's own word for no limit at all.
	if got, err := cleanActiveLimit(-1); err != nil || got != -1 {
		t.Errorf("cleanActiveLimit(-1) = %v, %v", got, err)
	}
	for _, limit := range []int{0, -2, MaxActiveTorrents + 1, 1 << 30} {
		if _, err := cleanActiveLimit(limit); err == nil {
			t.Errorf("cleanActiveLimit(%d) allowed it", limit)
		}
	}
}

func TestCleanSeedRatio(t *testing.T) {
	// Nothing at all is a perfectly good answer: it is deluge's own default.
	if got, err := cleanSeedRatio(0); err != nil || got != 0 {
		t.Errorf("cleanSeedRatio(0) = %v, %v", got, err)
	}
	if got, err := cleanSeedRatio(1.5); err != nil || got != 1.5 {
		t.Errorf("cleanSeedRatio(1.5) = %v, %v", got, err)
	}
	for _, ratio := range []float64{-1, 0.01, MaxSeedRatio + 1, 1e9} {
		if _, err := cleanSeedRatio(ratio); err == nil {
			t.Errorf("cleanSeedRatio(%v) allowed it", ratio)
		}
	}
}
