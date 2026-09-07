package hostops

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Everything else about the downloader is tested with deluge stubbed out, which
// proves HostMan's own logic and proves nothing at all about deluge. This one
// runs the real thing: a real deluged, started from the unit's own ExecStart
// line, driven by the real deluge-console with the commands HostMan sends it.
//
// It is what checks the assumptions that no amount of stubbing can — that the
// auth file is the format deluged reads, that `connect` takes a host, a user
// and a password in that order, that `add -p` is how a folder is named, that
// `rm --confirm` is understood, and that what `info` prints is what
// parseTorrentList expects. Those are the parts of this feature that live in
// somebody else's software and can change under it.
//
// It starts a daemon that opens ports and speaks to the network, so it is
// opt-in: DEPLOYER_DELUGE=1, and deluge installed.
//
//	DEPLOYER_DELUGE=1 go test ./internal/hostops/ -run RealDeluge -v
func requireDeluge(t *testing.T) {
	t.Helper()
	if os.Getenv("DEPLOYER_DELUGE") != "1" {
		t.Skip("set DEPLOYER_DELUGE=1 to run against a real deluge")
	}
	for _, piece := range torrentPieces {
		if _, err := exec.LookPath(piece); err != nil {
			t.Skipf("this machine has no %s", piece)
		}
	}
}

// aTorrentFile is the smallest thing deluge will accept as one: a bencoded
// dictionary with an info dictionary in it, for a single four-byte file.
func aTorrentFile(name string) []byte {
	pieces := strings.Repeat("\x01", 20)
	return []byte(fmt.Sprintf("d8:announce26:udp://tracker.invalid:69694:infod6:lengthi4e4:name%d:%s12:piece lengthi16384e6:pieces20:%see",
		len(name), name, pieces))
}

// waitForDeluge gives the daemon the second it needs to bind its port.
func waitForDeluge(t *testing.T, conf string) {
	t.Helper()
	password := ""
	for _, line := range strings.Split(read(t, filepath.Join(conf, "auth")), "\n") {
		if fields := strings.Split(line, ":"); len(fields) == 3 && fields[0] == torrentAccount {
			password = fields[1]
		}
	}
	if password == "" {
		t.Fatal("setup left no account for HostMan in the auth file")
	}
	connect := fmt.Sprintf("connect 127.0.0.1:%d %s %s; info", torrentPort, torrentAccount, password)
	for i := 0; i < 100; i++ {
		out, err := exec.Command("deluge-console", "-c", conf, connect).CombinedOutput()
		if err == nil && !strings.Contains(string(out), "Failed to connect") {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("deluged never answered on its port")
}

func TestRealDelugeTakesWhatHostManSends(t *testing.T) {
	requireDeluge(t)
	root := t.TempDir()
	work := t.TempDir()
	conf := stateDir(root)

	// systemd is the one thing still stubbed: this test is about deluge, and a
	// unit file under a temporary root is not something to hand the machine's
	// own init. is-active answers yes, because the daemon below really is.
	bin := stubBin(t, map[string]string{
		"systemctl": `case "$1" in is-active) exit 0;; esac; exit 0`,
	})

	if out, code := runScript(t, torrentSetupFor(root, currentUser(t), "", ""), "", bin); code != 0 {
		t.Fatalf("setup exited %d: %s", code, out)
	}

	// The unit's own ExecStart, run as systemd would run it. If deluged does
	// not understand these flags, this is where it says so.
	unit := read(t, filepath.Join(root, "etc/systemd/system", TorrentUnit))
	command := ""
	for _, line := range strings.Split(unit, "\n") {
		if rest, ok := strings.CutPrefix(line, "ExecStart="); ok {
			command = rest
		}
	}
	if command == "" {
		t.Fatal("the unit has no ExecStart")
	}
	fields := strings.Fields(command)
	daemon := exec.Command(fields[0], fields[1:]...)
	log, err := os.Create(filepath.Join(work, "deluged.log"))
	if err != nil {
		t.Fatal(err)
	}
	daemon.Stdout, daemon.Stderr = log, log
	if err := daemon.Start(); err != nil {
		t.Fatalf("could not start %s: %v", command, err)
	}
	t.Cleanup(func() {
		_ = daemon.Process.Kill()
		_, _ = daemon.Process.Wait()
		if t.Failed() {
			t.Logf("deluged said:\n%s", read(t, filepath.Join(work, "deluged.log")))
		}
	})
	waitForDeluge(t, conf)

	// A daemon with nothing in it is an empty list, not trouble.
	out, code := runScript(t, torrentStatusFor(root, currentUser(t)), "", bin)
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, out)
	}
	empty := parseTorrentStatus(out, currentUser(t))
	if !empty.Installed || !empty.Configured {
		t.Fatalf("a real deluge does not read as installed and set up: %+v", empty)
	}
	if len(empty.Torrents) != 0 || empty.Trouble != "" {
		t.Fatalf("an empty daemon reads as %+v / %q", empty.Torrents, empty.Trouble)
	}

	// The file a phone would have picked, sent the way the API sends it.
	body := base64.StdEncoding.EncodeToString(aTorrentFile("deployer-test-file"))
	folder := filepath.Join(work, "downloads")
	out, code = runScript(t, torrentAddFor(root, "picked.torrent", "", folder), body, bin)
	if code != 0 {
		t.Fatalf("add exited %d: %s", code, out)
	}
	if err := torrentConsoleError(out); err != nil {
		t.Fatalf("deluge would not take the torrent: %v\n%s", err, out)
	}

	// The same torrent again. Deluge answers with an RPC error, a traceback and
	// a base64 dump of the file — and none of it is a failure worth showing
	// anybody, because the torrent they asked for is downloading.
	out, code = runScript(t, torrentAddFor(root, "picked.torrent", "", folder), body, bin)
	if code != 0 {
		t.Fatalf("the second add exited %d: %s", code, out)
	}
	if err := torrentConsoleError(out); err != nil {
		t.Errorf("adding a torrent that was already there read as a failure: %v", err)
	}

	// What `info` prints is what the screen is built from, so it is read back
	// through the parser rather than looked at.
	added := waitForTorrent(t, root, bin, 1)
	got := added.Torrents[0]
	if got.Name != "deployer-test-file" {
		t.Errorf("name = %q, want the name inside the torrent file", got.Name)
	}
	if !torrentIDPattern.MatchString(got.ID) {
		t.Errorf("id = %q, want an info hash", got.ID)
	}
	if got.State == "" {
		t.Error("deluge said nothing about the state")
	}
	if got.Size == 0 {
		t.Errorf("size = %d, want the four bytes the torrent describes", got.Size)
	}

	// Pausing is what the screen's own button does.
	out, code = runScript(t, torrentActionFor(root, got.ID, "pause", ""), "", bin)
	if code != 0 {
		t.Fatalf("pause exited %d: %s", code, out)
	}
	if err := torrentConsoleError(out); err != nil {
		t.Fatalf("deluge would not pause it: %v\n%s", err, out)
	}
	paused := waitForState(t, root, bin, got.ID, "Paused")
	if paused == "" {
		t.Error("the torrent never reported itself paused")
	}

	// The seeding rule is deluge's own setting, so the test that it is spelt
	// right is deluge accepting it and saying it back.
	rule := asUser(fmt.Sprintf(torrentSeedingScript, torrentStateDir, consoleScript(), TorrentUnit),
		root, "True", "1.50", "True")
	out, code = runScript(t, rule, "", bin)
	if code != 0 {
		t.Fatalf("the seeding rule exited %d: %s", code, out)
	}
	if err := torrentConsoleError(out); err != nil {
		t.Fatalf("deluge would not take the seeding rule: %v\n%s", err, out)
	}
	out, code = runScript(t, torrentStatusFor(root, currentUser(t)), "", bin)
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, out)
	}
	after := parseTorrentStatus(out, currentUser(t))
	if after.Seeding.Ratio != 1.5 || !after.Seeding.Remove {
		t.Errorf("deluge came back with %+v, want the rule it was given", after.Seeding)
	}
	if !after.Asked {
		t.Error("a daemon that answered does not read as having answered")
	}
	if len(after.Torrents) != 1 {
		t.Errorf("the listing was lost alongside the settings: %+v", after.Torrents)
	}
	if after.ActiveLimit == 0 {
		t.Error("deluge said nothing about how many torrents it works on at once")
	}

	// The torrent limit, the same way: the test that it is spelt right is
	// deluge accepting it and saying it back.
	limit := asUser(fmt.Sprintf(torrentLimitScript, torrentStateDir, consoleScript(), TorrentUnit),
		root, "12")
	out, code = runScript(t, limit, "", bin)
	if code != 0 {
		t.Fatalf("the torrent limit exited %d: %s", code, out)
	}
	if err := torrentConsoleError(out); err != nil {
		t.Fatalf("deluge would not take the torrent limit: %v\n%s", err, out)
	}
	out, code = runScript(t, torrentStatusFor(root, currentUser(t)), "", bin)
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, out)
	}
	if got := parseTorrentStatus(out, currentUser(t)).ActiveLimit; got != 12 {
		t.Errorf("deluge came back with a limit of %d, want the 12 it was given", got)
	}

	// And removing it, with the flags deluge 2.1 wants.
	//
	// What deluge-console says about a removal is worth nothing: it does the
	// work and then never exits, so the script cuts it short and what comes
	// back is a timeout and a traceback about a removal that happened. This is
	// the reason TorrentAction asks for the list again rather than believing
	// it, and the list is what this checks too.
	out, code = runScript(t, torrentActionFor(root, got.ID, "remove", "1"), "", bin)
	if code != 0 {
		t.Fatalf("remove exited %d: %s", code, out)
	}
	if left := waitForTorrent(t, root, bin, 0); len(left.Torrents) != 0 {
		t.Errorf("the torrent is still there: %+v", left.Torrents)
	}
}

// currentUser is the account this test runs as, which is the account the
// scripts are told the daemon belongs to.
func currentUser(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("id", "-un").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// waitForTorrent asks until deluge reports the number of torrents expected.
// Adding one is not instantaneous: libtorrent checks it first.
func waitForTorrent(t *testing.T, root, bin string, want int) *TorrentDaemon {
	t.Helper()
	var daemon *TorrentDaemon
	for i := 0; i < 50; i++ {
		out, code := runScript(t, torrentStatusFor(root, currentUser(t)), "", bin)
		if code != 0 {
			t.Fatalf("status exited %d: %s", code, out)
		}
		daemon = parseTorrentStatus(out, currentUser(t))
		if len(daemon.Torrents) == want {
			return daemon
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("deluge never reported %d torrents: %+v", want, daemon)
	return nil
}

// waitForState asks until one torrent reaches a state, and returns "" if it
// never does.
func waitForState(t *testing.T, root, bin, id, want string) string {
	t.Helper()
	for i := 0; i < 50; i++ {
		out, code := runScript(t, torrentStatusFor(root, currentUser(t)), "", bin)
		if code != 0 {
			t.Fatalf("status exited %d: %s", code, out)
		}
		for _, torrent := range parseTorrentStatus(out, currentUser(t)).Torrents {
			if torrent.ID == id && torrent.State == want {
				return torrent.State
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return ""
}
