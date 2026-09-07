package hostops

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The remote session is three generated scripts and a unit file, all of which
// run on somebody else's machine. So they are tested by running them: against a
// real filesystem, through a real shell, with the parts that would install
// packages or talk to systemd stubbed out. What is left is exactly the logic
// HostMan wrote, which is the part that can be wrong.

// stubBin builds a directory of fake commands. Each is a shell script that does
// whatever the test needs it to; putting it first on PATH is what keeps apt-get
// off the machine running the tests.
func stubBin(t *testing.T, stubs map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range stubs {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// rootStubs are what every one of these scripts needs to believe it is running
// on a host: a root uid, an account with a home, and a systemd to tell.
func rootStubs(extra map[string]string) map[string]string {
	stubs := map[string]string{
		"id":        `[ "$1" = -u ] && { echo 0; exit 0; }; echo root`,
		"getent":    `printf 'pi:x:1000:1000::/home/pi:/bin/sh\n'`,
		"systemctl": `exit 0`,
	}
	for name, body := range extra {
		stubs[name] = body
	}
	return stubs
}

// sessionScript and installScript are the two generated scripts as a host gets
// them, which is what the tests run.
func sessionScript() string {
	return fmt.Sprintf(remoteSessionScript, pickBrowserScript())
}

func installScript() string {
	return fmt.Sprintf(remoteInstallScript, remoteConfDir, "", pickBrowserScript())
}

func setupScriptFor(root, user, geometry, port, page, reset string) string {
	return asUser(renderRemoteSetup(), root, user, geometry, port, page, reset, remoteRevision())
}

// waitForFile reads a file a backgrounded command is expected to write. Setup
// returns before the install it launched has run, which is the whole point of
// detaching it.
func waitForFile(t *testing.T, path string) string {
	t.Helper()
	for i := 0; i < 100; i++ {
		if body, err := os.ReadFile(path); err == nil {
			return string(body)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s was never written", path)
	return ""
}

func read(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

// Every generated script has to be a script before it can be a correct one, and
// a syntax error would only show up as a session that will not start.
func TestGeneratedScriptsParse(t *testing.T) {
	scripts := map[string]string{
		"session": sessionScript(),
		"install": installScript(),
		"setup":   renderRemoteSetup(),
		"status":  statusScript(),
		"start":   fmt.Sprintf(remoteStartScript, remoteConfDir, RemoteUnit),
		"remove":  fmt.Sprintf(remoteRemoveScript, remoteConfDir, remoteLibDir, RemoteUnit),
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

// Setup writes what a session is made of and hands the slow half — apt — to the
// host to get on with. The launch is stubbed here so nothing is installed; what
// is checked is that the files are right and that the install was started.
func TestSetupScriptWritesTheSessionAndStartsTheInstall(t *testing.T) {
	root := t.TempDir()
	launched := filepath.Join(root, "launched")
	bin := stubBin(t, rootStubs(map[string]string{
		"nohup": `printf '%s\n' "$*" >> ` + launched,
	}))

	out, code := runScript(t, setupScriptFor(root, "pi", "1280x800", "6080", "https://example.com/login", ""), "", bin)
	if code != 0 {
		t.Fatalf("setup exited %d: %s", code, out)
	}
	if got := strings.TrimSpace(out); got != "started" {
		t.Errorf("setup said %q, want it to report that the install started", got)
	}

	unit := read(t, filepath.Join(root, "etc/systemd/system", RemoteUnit))
	// No [Install] section is the whole reason a session cannot be enabled and
	// does not come back after a reboot.
	if strings.Contains(unit, "[Install]") {
		t.Error("the unit must stay static, so it cannot be enabled to run at boot")
	}
	if !strings.Contains(unit, "User=pi") {
		t.Errorf("the session must run as the SSH user, got:\n%s", unit)
	}
	// The session script reads no configuration of its own: systemd hands it
	// everything, which is what keeps a file on the host from being run.
	wantExec := fmt.Sprintf("ExecStart=%s/remote-session.sh 1280x800 %d %d 6080 %s %s/home/pi/.config/deployer-remote %s/home/pi/Downloads",
		remoteLibDir, remoteDisplay, remoteVNCPort, root+remoteConfDir, root, root)
	if !strings.Contains(unit, wantExec) {
		t.Errorf("unit runs the wrong command:\n%s\nwant it to contain:\n%s", unit, wantExec)
	}

	config := read(t, filepath.Join(root, remoteConfDir, "config"))
	for _, want := range []string{"PORT=6080", "GEOMETRY=1280x800", "DOWNLOADS=" + root + "/home/pi/Downloads"} {
		if !strings.Contains(config, want) {
			t.Errorf("config is missing %q:\n%s", want, config)
		}
	}
	if got := strings.TrimSpace(read(t, filepath.Join(root, remoteConfDir, "homepage"))); got != "https://example.com/login" {
		t.Errorf("homepage is %q", got)
	}
	if got := strings.TrimSpace(read(t, filepath.Join(root, remoteConfDir, "setup.state"))); got != "running" {
		t.Errorf("setup state is %q, want the install to be reported as under way", got)
	}

	// Detached, for the same reason a self-update is: a phone that locks its
	// screen must not be able to kill apt half way through a package. Detached
	// also means the launch outlives the call, so the test waits for it.
	if got := waitForFile(t, launched); !strings.Contains(got, "setsid sh "+root+remoteLibDir+"/remote-install.sh") {
		t.Errorf("the install was launched as %q, want it detached", got)
	}
	for _, script := range []string{"remote-session.sh", "remote-install.sh"} {
		info, err := os.Stat(filepath.Join(root, remoteLibDir, script))
		if err != nil {
			t.Fatalf("%s: %v", script, err)
		}
		if info.Mode().Perm()&0o100 == 0 && script == "remote-session.sh" {
			t.Errorf("%s is not executable (%s)", script, info.Mode())
		}
	}
}

// Setting up again must not sign anybody out. The scripts and the unit are
// rewritten; the password and the browser profile are what stay.
func TestSetupScriptIsIdempotent(t *testing.T) {
	root := t.TempDir()
	bin := stubBin(t, rootStubs(map[string]string{"nohup": `exit 0`}))

	if _, code := runScript(t, setupScriptFor(root, "pi", "", "6080", "", ""), "", bin); code != 0 {
		t.Fatalf("first setup exited %d", code)
	}
	kept := filepath.Join(root, remoteConfDir, "password")
	if err := os.WriteFile(kept, []byte("hunter22\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, code := runScript(t, setupScriptFor(root, "pi", "1024x768", "6081", "https://example.com/", ""), "", bin); code != 0 {
		t.Fatalf("second setup exited %d", code)
	}
	if got := strings.TrimSpace(read(t, kept)); got != "hunter22" {
		t.Errorf("the password changed to %q on a second setup", got)
	}
	if got := read(t, filepath.Join(root, remoteConfDir, "config")); !strings.Contains(got, "PORT=6081") {
		t.Errorf("the second setup did not take: %s", got)
	}
}

// Without root there is no apt, no /etc and no unit, so setup says so rather
// than failing half way through with something less useful.
func TestSetupScriptNeedsRoot(t *testing.T) {
	root := t.TempDir()
	bin := stubBin(t, map[string]string{"id": `echo 1000`})
	_, code := runScript(t, setupScriptFor(root, "pi", "", "6080", "", ""), "", bin)
	if code != 3 {
		t.Errorf("setup exited %d without root, want 3 so the API can say what is missing", code)
	}
}

func installScriptFor(root, user, reset string) string {
	return asUser(installScript(), root, user, reset)
}

// The installer is the half that takes minutes. It reports where it got to
// through a state file, because by then nothing is listening.
func TestInstallScriptStoresAPasswordAndReportsSuccess(t *testing.T) {
	root := t.TempDir()
	etc := filepath.Join(root, remoteConfDir)
	if err := os.MkdirAll(etc, 0o750); err != nil {
		t.Fatal(err)
	}
	bin := stubBin(t, map[string]string{
		"apt-get": `exit 0`,
		// -storepasswd writes the obfuscated file x11vnc authenticates against.
		"x11vnc": `[ "$1" = -storepasswd ] && printf 'stored\n' > "$3"; exit 0`,
		"chown":  `exit 0`,
		// A browser that is already a package: the installer has nothing to
		// fetch, which is what keeps this test off the network.
		"chromium": `exit 0`,
	})

	out, code := runScript(t, installScriptFor(root, "pi", ""), "", bin)
	if code != 0 {
		t.Fatalf("install exited %d: %s", code, out)
	}
	if got := strings.TrimSpace(read(t, filepath.Join(etc, "setup.state"))); got != "ok" {
		t.Errorf("state is %q, want ok", got)
	}

	// Eight characters because that is all the VNC protocol carries; a longer
	// one would be cut in half by the server and read as a wrong password.
	password := strings.TrimSpace(read(t, filepath.Join(etc, "password")))
	if len(password) != 8 {
		t.Errorf("password is %q (%d characters), want 8", password, len(password))
	}
	if strings.ContainsAny(password, "lo01") {
		t.Errorf("password %q uses characters that are misread off a screen", password)
	}
	if _, err := os.Stat(filepath.Join(etc, "vncpasswd")); err != nil {
		t.Errorf("x11vnc's password file was not written: %v", err)
	}

	// A second run leaves the password alone: the point of running setup again
	// is usually a setting, and a new password would log the phone out of a
	// session somebody is in the middle of.
	if _, code := runScript(t, installScriptFor(root, "pi", ""), "", bin); code != 0 {
		t.Fatalf("second install exited %d", code)
	}
	if got := strings.TrimSpace(read(t, filepath.Join(etc, "password"))); got != password {
		t.Errorf("password changed to %q on a second run", got)
	}
	// Unless it is asked for, which is the way back from one that leaked.
	if _, code := runScript(t, installScriptFor(root, "pi", "1"), "", bin); code != 0 {
		t.Fatalf("reset install exited %d", code)
	}
	if got := strings.TrimSpace(read(t, filepath.Join(etc, "password"))); got == password {
		t.Error("a reset should have made a new password")
	}
}

// A failed install has to leave the reason behind it: the state file carries the
// exit status and the log carries what apt said.
func TestInstallScriptRecordsAFailure(t *testing.T) {
	root := t.TempDir()
	etc := filepath.Join(root, remoteConfDir)
	if err := os.MkdirAll(etc, 0o750); err != nil {
		t.Fatal(err)
	}
	bin := stubBin(t, map[string]string{
		"apt-get": `printf 'E: Unable to fetch some archives\n' >&2; exit 100`,
	})
	if _, code := runScript(t, installScriptFor(root, "pi", ""), "", bin); code != 10 {
		t.Errorf("install exited %d, want 10 for a package list that would not update", code)
	}
	if got := strings.TrimSpace(read(t, filepath.Join(etc, "setup.state"))); got != "failed:10" {
		t.Errorf("state is %q, want the exit status recorded", got)
	}
}

func statusScript() string {
	return fmt.Sprintf(remoteStatusScript, MaxRemoteLogBytes, pickBrowserScript(),
		RemoteUnit, strings.Join(remotePieces, " "))
}

// Status is one round trip that has to answer for a host in any state, starting
// with a host where none of this has ever been run.
func TestStatusScriptOnAHostWithNothingSetUp(t *testing.T) {
	root := t.TempDir()
	bin := stubBin(t, map[string]string{
		"getent":    `printf 'pi:x:1000:1000::/home/pi:/bin/sh\n'`,
		"systemctl": `printf 'LoadState=not-found\nActiveState=inactive\nSubState=dead\n'`,
	})
	out, code := runScript(t, asUser(statusScript(), root, "pi"), "", bin)
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, out)
	}
	session := parseRemoteStatus(out, "pi")
	if session.Setup != "absent" {
		t.Errorf("setup is %q, want absent", session.Setup)
	}
	if session.Ready || session.Running {
		t.Errorf("nothing is installed, so nothing is ready or running: %+v", session)
	}
	if len(session.Missing) == 0 {
		t.Error("status should name what is missing")
	}
	if session.Downloads != root+"/home/pi/Downloads" {
		t.Errorf("downloads is %q", session.Downloads)
	}
}

// And a host where it is all there and running, which is the state the screen
// spends its time in.
func TestStatusScriptOnAReadyHost(t *testing.T) {
	root := t.TempDir()
	etc := filepath.Join(root, remoteConfDir)
	downloads := filepath.Join(root, "home/pi/Downloads")
	for _, dir := range []string{etc, downloads} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(etc, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("setup.state", "ok\n")
	write("setup.log", "== updating the package list\n== ready\n")
	write("config", "PORT=6081\nGEOMETRY=1024x768\nDISPLAY_NUM=99\n")
	write("password", "qm4rt7xz\n")
	write("homepage", "https://example.com/login\n")
	write("vncpasswd", "stored\n")
	if err := os.WriteFile(filepath.Join(downloads, "statement.pdf"), []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := stubBin(t, map[string]string{
		"getent":      `printf 'pi:x:1000:1000::/home/pi:/bin/sh\n'`,
		"systemctl":   `printf 'LoadState=loaded\nActiveState=active\nSubState=running\n'`,
		"Xvfb":        `exit 0`,
		"x11vnc":      `exit 0`,
		"websockify":  `exit 0`,
		"chromium":    `exit 0`,
		"firefox-esr": `exit 0`,
	})
	out, code := runScript(t, asUser(statusScript(), root, "pi"), "", bin)
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, out)
	}

	session := parseRemoteStatus(out, "pi")
	if session.Setup != "ok" || !session.Ready || !session.Running {
		t.Fatalf("want a ready, running session, got %+v", session)
	}
	if len(session.Missing) != 0 {
		t.Errorf("nothing should be missing, got %v", session.Missing)
	}
	// Chromium first: it is what a Pi already has, and the one whose download
	// directory can be set without clicking through a settings screen over VNC.
	if session.Browser != "chromium" {
		t.Errorf("browser is %q, want the first one on the list that is installed", session.Browser)
	}
	if session.Port != 6081 || session.Geometry != "1024x768" {
		t.Errorf("settings did not come back: port %d, geometry %q", session.Port, session.Geometry)
	}
	if session.Password != "qm4rt7xz" || session.Homepage != "https://example.com/login" {
		t.Errorf("password or homepage did not come back: %+v", session)
	}
	if !strings.Contains(session.SetupLog, "== ready") {
		t.Errorf("setup log is %q", session.SetupLog)
	}
	if len(session.Files) != 1 || session.Files[0].Name != "statement.pdf" || session.Files[0].Size != 10 {
		t.Errorf("downloads did not come back: %+v", session.Files)
	}
	if session.Files[0].AgeS < 0 || session.Files[0].AgeS > 60 {
		t.Errorf("a file just written is %ds old", session.Files[0].AgeS)
	}
}

// A failed install is the state worth getting right: the screen has to be able
// to say what happened and show the end of the log.
func TestParseStatusReadsAFailedSetup(t *testing.T) {
	out := "@@state\nfailed:11\n@@log\n" +
		"PT0gaW5zdGFsbGluZwpFOiBVbmFibGUgdG8gbG9jYXRlIHBhY2thZ2Ugbm92bmMK\n" +
		"@@have\nXvfb\n@@unit\nLoadState=loaded\nActiveState=inactive\nSubState=dead\n"
	session := parseRemoteStatus(out, "pi")
	if session.Setup != "failed" || session.SetupExit != 11 {
		t.Errorf("want a failure with its exit status, got %q/%d", session.Setup, session.SetupExit)
	}
	if !strings.Contains(session.SetupLog, "Unable to locate package novnc") {
		t.Errorf("the log did not survive: %q", session.SetupLog)
	}
	if session.Ready {
		t.Error("a host missing half the session is not ready")
	}
	if !contains(session.Missing, "x11vnc") || !contains(session.Missing, "a browser") {
		t.Errorf("missing is %v, want what is not installed named", session.Missing)
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// Starting points the session at a page and starts it in one round trip, which
// is what makes "open this site" a tap rather than a URL typed over VNC.
func TestStartScriptWritesThePageThenStarts(t *testing.T) {
	root := t.TempDir()
	etc := filepath.Join(root, remoteConfDir)
	if err := os.MkdirAll(etc, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(etc, "config"), []byte("PORT=6080\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(etc, "homepage"), []byte("https://old.example/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	started := filepath.Join(root, "started")
	bin := stubBin(t, map[string]string{"systemctl": `printf '%s\n' "$*" >> ` + started})

	script := fmt.Sprintf(remoteStartScript, remoteConfDir, RemoteUnit)
	if _, code := runScript(t, asUser(script, root, "https://bank.example/login"), "", bin); code != 0 {
		t.Fatalf("start exited %d", code)
	}
	if got := strings.TrimSpace(read(t, filepath.Join(etc, "homepage"))); got != "https://bank.example/login" {
		t.Errorf("homepage is %q", got)
	}
	if got := read(t, started); !strings.Contains(got, "start -- "+RemoteUnit) {
		t.Errorf("systemctl was asked for %q", got)
	}

	// Starting without naming a page keeps the last one rather than blanking it.
	if _, code := runScript(t, asUser(script, root, ""), "", bin); code != 0 {
		t.Fatalf("start exited %d", code)
	}
	if got := strings.TrimSpace(read(t, filepath.Join(etc, "homepage"))); got != "https://bank.example/login" {
		t.Errorf("homepage became %q when no page was named", got)
	}
}

// Starting a session that was never set up is a mistake worth naming, not a
// systemctl error about a unit nobody has heard of.
func TestStartScriptRefusesAHostWithNoSession(t *testing.T) {
	root := t.TempDir()
	bin := stubBin(t, map[string]string{"systemctl": `exit 0`})
	script := fmt.Sprintf(remoteStartScript, remoteConfDir, RemoteUnit)
	if _, code := runScript(t, asUser(script, root, ""), "", bin); code != 3 {
		t.Errorf("start exited %d on a host with no session, want 3", code)
	}
}

// Removing takes what HostMan wrote and nothing else — the packages stay, the
// downloads stay, and the profile with its logins goes only when asked.
func TestRemoveScriptLeavesTheDownloadsAndTakesTheProfileOnlyWhenAsked(t *testing.T) {
	build := func(t *testing.T) (root string, bin string) {
		root = t.TempDir()
		dirs := []string{
			filepath.Join(root, remoteConfDir),
			filepath.Join(root, remoteLibDir),
			filepath.Join(root, "etc/systemd/system"),
			filepath.Join(root, "home/pi/Downloads"),
			filepath.Join(root, "home/pi/.config/deployer-remote/Default"),
		}
		for _, dir := range dirs {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for _, file := range []string{
			filepath.Join(root, "etc/systemd/system", RemoteUnit),
			filepath.Join(root, remoteLibDir, "remote-session.sh"),
			filepath.Join(root, remoteConfDir, "password"),
			filepath.Join(root, "home/pi/Downloads/statement.pdf"),
			filepath.Join(root, "home/pi/.config/deployer-remote/Default/Cookies"),
		} {
			if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return root, stubBin(t, map[string]string{
			"systemctl": `exit 0`,
			"getent":    `printf 'pi:x:1000:1000::/home/pi:/bin/sh\n'`,
		})
	}
	script := fmt.Sprintf(remoteRemoveScript, remoteConfDir, remoteLibDir, RemoteUnit)

	t.Run("keeps the profile", func(t *testing.T) {
		root, bin := build(t)
		if _, code := runScript(t, asUser(script, root, "pi", ""), "", bin); code != 0 {
			t.Fatalf("remove exited %d", code)
		}
		for _, gone := range []string{
			filepath.Join(root, "etc/systemd/system", RemoteUnit),
			filepath.Join(root, remoteConfDir),
			filepath.Join(root, remoteLibDir, "remote-session.sh"),
		} {
			if _, err := os.Stat(gone); !os.IsNotExist(err) {
				t.Errorf("%s is still there", gone)
			}
		}
		for _, kept := range []string{
			filepath.Join(root, "home/pi/Downloads/statement.pdf"),
			filepath.Join(root, "home/pi/.config/deployer-remote/Default/Cookies"),
		} {
			if _, err := os.Stat(kept); err != nil {
				t.Errorf("%s should have been left alone: %v", kept, err)
			}
		}
	})

	t.Run("purges the profile when asked", func(t *testing.T) {
		root, bin := build(t)
		if _, code := runScript(t, asUser(script, root, "pi", "1"), "", bin); code != 0 {
			t.Fatalf("remove exited %d", code)
		}
		if _, err := os.Stat(filepath.Join(root, "home/pi/.config/deployer-remote")); !os.IsNotExist(err) {
			t.Error("the profile should have gone with a purge")
		}
		// The files somebody went to the trouble of downloading are not part of
		// the session, and are never what a removal means.
		if _, err := os.Stat(filepath.Join(root, "home/pi/Downloads/statement.pdf")); err != nil {
			t.Errorf("a purge took the downloads with it: %v", err)
		}
	})
}

// The session script is what actually runs on the virtual screen. It is checked
// here rather than run — starting an X server in a unit test would be testing
// Xvfb — but the parts HostMan got to choose are worth asserting.
func TestSessionScriptChoosesSafeDefaults(t *testing.T) {
	script := sessionScript()
	for _, want := range []string{
		// The VNC server never listens on the network: the gateway is the only
		// way in, and it is the port HostMan's link points at.
		"-localhost",
		// Without this Chromium waits for a keyring no headless host will answer.
		"--password-store=basic",
		// Downloads land in the host's own directory without a dialog, which is
		// the entire point of doing this on the host at all.
		"prompt_for_download",
		"-rfbauth",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the session script should contain %q", want)
		}
	}
	if strings.Contains(script, "-nopw") {
		t.Error("the session must never offer VNC without a password")
	}
	// It is handed its settings by systemd and reads only the page to open, so
	// nothing on the host is fed to a shell that would run what is in it.
	if strings.Contains(script, ". \"$conf") || strings.Contains(script, "source ") {
		t.Error("the session script must not source anything")
	}
}

func TestCleanGeometry(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", DefaultRemoteGeometry, false},
		{"1280x800", "1280x800", false},
		{" 1024X768 ", "1024x768", false},
		{"640x480", "640x480", false},
		{"320x240", "", true},
		{"4000x2000", "", true},
		{"1280 x 800", "", true},
		{"1280x800; rm -rf /", "", true},
	}
	for _, tc := range cases {
		got, err := cleanGeometry(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("cleanGeometry(%q) = %q, want a refusal", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("cleanGeometry(%q): %v", tc.in, err)
		} else if got != tc.want {
			t.Errorf("cleanGeometry(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The page reaches the browser as an argument, so anything that could be read
// as an option, a local file or a script is not a page somebody asked for.
func TestCleanRemoteURL(t *testing.T) {
	for _, ok := range []string{"", "https://example.com/login", "http://192.168.2.5:8080/a?b=c"} {
		if _, err := CleanRemoteURL(ok); err != nil {
			t.Errorf("CleanRemoteURL(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{
		"javascript:alert(1)",
		"file:///etc/shadow",
		"--headless",
		"about:blank",
		"https://example.com/ --incognito",
		"https://example.com/`id`",
		"example.com",
	} {
		if got, err := CleanRemoteURL(bad); err == nil {
			t.Errorf("CleanRemoteURL(%q) = %q, want a refusal", bad, got)
		}
	}
}

func TestCleanRemotePort(t *testing.T) {
	if port, err := cleanRemotePort(0); err != nil || port != DefaultRemotePort {
		t.Errorf("an unset port should mean the default, got %d/%v", port, err)
	}
	if _, err := cleanRemotePort(80); err == nil {
		t.Error("a privileged port should be refused")
	}
	if _, err := cleanRemotePort(remoteVNCPort); err == nil {
		t.Error("the session's own VNC port should be refused")
	}
}

// The link is what the Open button hands to the phone's browser, so it has to
// connect on its own and fit the screen it lands on.
func TestRemoteURL(t *testing.T) {
	session := &RemoteSession{Port: 6080, Password: "qm4rt7xz"}
	got := RemoteURL("nakedpi.local", session)
	for _, want := range []string{
		"http://nakedpi.local:6080/vnc.html?",
		"autoconnect=1",
		"password=qm4rt7xz",
		"resize=scale",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RemoteURL = %q, want it to contain %q", got, want)
		}
	}
	if got := RemoteURL("fd00::1", session); !strings.HasPrefix(got, "http://[fd00::1]:6080/") {
		t.Errorf("an IPv6 host needs brackets, got %q", got)
	}
	if got := RemoteURL("", session); got != "" {
		t.Errorf("no address means no link, got %q", got)
	}
}

// The session script's own behaviour, run for real: a browser that will not
// start has to say so somewhere a person can read it. This is the test the
// first version of this file did not have, and its absence cost an evening —
// the browser's output went to /dev/null, so a session that never came up
// looked exactly like a session with nothing wrong except a black screen.
func TestSessionScriptReportsABrowserThatWillNotStart(t *testing.T) {
	// The script waits for the X server's socket at the path X itself uses, so
	// the stub has to put one there. A machine that will not allow that is one
	// where this test cannot run.
	const display = "91"
	socket := "/tmp/.X11-unix/X" + display
	if err := os.MkdirAll("/tmp/.X11-unix", 0o1777); err != nil {
		t.Skipf("no /tmp/.X11-unix to fake an X server in: %v", err)
	}
	if f, err := os.Create(socket); err != nil {
		t.Skipf("cannot write %s: %v", socket, err)
	} else {
		f.Close()
	}
	t.Cleanup(func() { os.Remove(socket) })

	root := t.TempDir()
	conf := filepath.Join(root, "conf")
	if err := os.MkdirAll(conf, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conf, "homepage"), []byte("https://example.com/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := stubBin(t, map[string]string{
		// Not root: root already runs the browser without a sandbox, so there
		// would be nothing for the fallback below to give up. These tests run
		// as whoever runs them, and that is often root.
		"id":         `[ "$1" = -u ] && { echo 1000; exit 0; }; echo pi`,
		"Xvfb":       `sleep 30`,
		"x11vnc":     `sleep 30`,
		"websockify": `sleep 30`,
		// A browser that answers --version and then dies when it tries to open
		// a window, which is how a real one fails. The message is the one
		// Chromium gives when it cannot use its profile, on stderr — the
		// stream that used to be thrown away.
		"chromium": `[ "$1" = --version ] && { echo 'Chromium 120.0.0.0'; exit 0; }
printf 'The profile appears to be in use by another Chromium process\n' >&2
exit 1`,
	})

	script := filepath.Join(root, "session.sh")
	if err := os.WriteFile(script, []byte(sessionScript()), 0o755); err != nil {
		t.Fatal(err)
	}
	// It runs until it is stopped, the way systemd runs it, so the test stops it.
	run := exec.Command("timeout", "9", "sh", script, "1280x800", display, "5999", "6080",
		conf, filepath.Join(root, "profile"), filepath.Join(root, "Downloads"))
	run.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, _ := run.CombinedOutput()
	log := string(out)

	// The browser's own words, which are the only thing that ever says why.
	if !strings.Contains(log, "The profile appears to be in use") {
		t.Errorf("the browser's output never reached the journal:\n%s", log)
	}
	// HostMan's reading of them, for the times when the browser says nothing.
	if !strings.Contains(log, "exited after") {
		t.Errorf("a browser dying on the spot should be reported as such:\n%s", log)
	}
	// And which browser it was, since a distribution that ships a snap wrapper
	// leaves a binary on the PATH that cannot run.
	if !strings.Contains(log, "chromium is "+filepath.Join(bin, "chromium")) {
		t.Errorf("the session should name the browser it resolved:\n%s", log)
	}

	// A host that will not give the browser a sandbox gets a session without one
	// rather than no session at all — after the failure, never in anticipation
	// of it, and never quietly.
	if !strings.Contains(log, "will not give chromium a sandbox") {
		t.Errorf("a browser that keeps dying should be retried without its sandbox:\n%s", log)
	}
	if got := strings.TrimSpace(read(t, filepath.Join(conf, "degraded"))); got != "no-sandbox" {
		t.Errorf("the fallback left %q behind, want it marked so the screen can say so", got)
	}
	// Once, not on every failure: the second attempt is what turns it off, and
	// the ones after that are already without it.
	if n := strings.Count(log, "retrying without one"); n != 1 {
		t.Errorf("the sandbox was given up %d times, want once", n)
	}

	// The lock a crashed browser leaves behind is cleared on the way in;
	// leaving it is what turns one crash into a session that never works again.
	if _, err := os.Stat(filepath.Join(root, "profile", "SingletonLock")); err == nil {
		t.Error("a stale singleton lock survived the start")
	}
	// The profile was seeded before the browser ran, which is what stops
	// Chromium asking where to save every file over VNC.
	prefs, err := os.ReadFile(filepath.Join(root, "profile", "Default", "Preferences"))
	if err != nil {
		t.Fatalf("the profile was not seeded: %v", err)
	}
	if !strings.Contains(string(prefs), filepath.Join(root, "Downloads")) {
		t.Errorf("downloads are not pointed at the host's own directory: %s", prefs)
	}
}

// Updating HostMan does not reach back and rewrite the scripts already on a
// host — a running session should not change under somebody — so a host still
// running the old ones has to say so. Otherwise a fix that shipped is not the
// code the host is running, and nothing on the screen admits it.
func TestStatusReportsASessionWrittenByAnOlderHostMan(t *testing.T) {
	current := "PORT=6080\nGEOMETRY=1280x800\nREVISION=" + remoteRevision() + "\n"
	older := "PORT=6080\nGEOMETRY=1280x800\nREVISION=0000deadbeef\n"

	if session := parseRemoteStatus("@@state\nok\n@@config\n"+current, "pi"); session.Stale {
		t.Error("a session written by this build is not stale")
	}
	if session := parseRemoteStatus("@@state\nok\n@@config\n"+older, "pi"); !session.Stale {
		t.Error("a session written by an older build should say so")
	}
	// A host with nothing set up has nothing to be stale, and saying it does
	// would put an update prompt in front of somebody who has never run this.
	if session := parseRemoteStatus("@@state\nabsent\n", "pi"); session.Stale {
		t.Error("a host with no session at all is not stale")
	}
}

// The revision has to follow the scripts by itself: one kept by hand is one
// that is eventually wrong, which is the failure it exists to prevent.
func TestRevisionFollowsTheScripts(t *testing.T) {
	before := remoteRevision()
	if len(before) != 12 {
		t.Errorf("revision is %q, want something short enough for a config line", before)
	}
	if again := remoteRevision(); again != before {
		t.Errorf("the same build gave two revisions: %q then %q", before, again)
	}
	if !strings.Contains(renderRemoteSetup(), "--disable-dev-shm-usage") {
		t.Error("the rendered setup should carry the session script it writes")
	}
}

// A browser running without its sandbox is something the screen has to say, not
// something buried in a journal. The host marks it; this is the reading of it.
func TestStatusReportsABrowserWithoutItsSandbox(t *testing.T) {
	if session := parseRemoteStatus("@@state\nok\n@@degraded\nno-sandbox\n", "pi"); !session.NoSandbox {
		t.Error("a session that gave up its sandbox should say so")
	}
	if session := parseRemoteStatus("@@state\nok\n@@degraded\n", "pi"); session.NoSandbox {
		t.Error("an ordinary session should not claim to be degraded")
	}
}

// Ubuntu ships chromium and firefox as snaps, and a snap cannot run in a
// session like this: confinement walls it out of the profile directory, and a
// system service has no runtime directory for snapd to work in. It fails on the
// spot with nothing on stdout, so the session has to recognise it rather than
// run it and wonder.
func TestSessionScriptWillNotRunASnapBrowser(t *testing.T) {
	const display = "92"
	socket := "/tmp/.X11-unix/X" + display
	if err := os.MkdirAll("/tmp/.X11-unix", 0o1777); err != nil {
		t.Skipf("no /tmp/.X11-unix to fake an X server in: %v", err)
	}
	if f, err := os.Create(socket); err != nil {
		t.Skipf("cannot write %s: %v", socket, err)
	} else {
		f.Close()
	}
	t.Cleanup(func() { os.Remove(socket) })

	root := t.TempDir()
	conf := filepath.Join(root, "conf")
	if err := os.MkdirAll(conf, 0o750); err != nil {
		t.Fatal(err)
	}
	bin := stubBin(t, map[string]string{
		"id":         `[ "$1" = -u ] && { echo 1000; exit 0; }; echo pi`,
		"Xvfb":       `sleep 10`,
		"x11vnc":     `sleep 10`,
		"websockify": `sleep 10`,
	})
	// What Ubuntu leaves on the PATH: a name that resolves into /snap.
	snapDir := filepath.Join(root, "snap", "bin")
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapDir, "chromium"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(snapDir, "chromium"), filepath.Join(bin, "chromium")); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join(root, "session.sh")
	if err := os.WriteFile(script, []byte(sessionScript()), 0o755); err != nil {
		t.Fatal(err)
	}
	run := exec.Command("timeout", "8", "sh", script, "1280x800", display, "5999", "6080",
		conf, filepath.Join(root, "profile"), filepath.Join(root, "Downloads"))
	run.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	out, err := run.CombinedOutput()
	log := string(out)

	if !strings.Contains(log, "no browser here can run — snaps: chromium") {
		t.Errorf("a snap browser should be named as the problem:\n%s", log)
	}
	// Refusing is only half of it: the next step has to be on the screen too.
	if !strings.Contains(log, "set the session up again") {
		t.Errorf("the session should say what fixes it:\n%s", log)
	}
	// It must not try to run it and sit there in a retry loop.
	if strings.Contains(log, "exited after") {
		t.Errorf("the snap was launched anyway:\n%s", log)
	}
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 4 {
		t.Errorf("session exited %v, want 4 so a snap is told apart from no browser at all", err)
	}
}

// The screen has to tell "no browser" apart from "a browser that looks
// installed and cannot run", because they are fixed by different things.
func TestStatusNamesASnapOnlyBrowser(t *testing.T) {
	session := parseRemoteStatus("@@state\nok\n@@have\nXvfb\nx11vnc\nwebsockify\n@@snap\nchromium firefox\n", "pi")
	if session.SnapBrowser != "chromium firefox" {
		t.Errorf("snap browsers came back as %q", session.SnapBrowser)
	}
	if session.Browser != "" {
		t.Errorf("a snap is not a browser this can use, got %q", session.Browser)
	}
	if !contains(session.Missing, "a browser") {
		t.Errorf("missing is %v, want a browser still wanted", session.Missing)
	}
}

// A host whose only browser is a snap gets one that is a package. This is the
// installer half of that: what apt offers is unusable, so Chrome's own .deb is
// fetched, and the log says which of the two reasons it was.
func TestInstallScriptFetchesAPackageBrowserWhereOnlySnapsExist(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, remoteConfDir), 0o750); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	// What Ubuntu leaves behind: a chromium that resolves into /snap.
	snapDir := filepath.Join(root, "snap", "bin")
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		filepath.Join(snapDir, "chromium"): "#!/bin/sh\nexit 1\n",
		filepath.Join(bin, "dpkg"):         "#!/bin/sh\necho amd64\n",
		filepath.Join(bin, "chown"):        "#!/bin/sh\nexit 0\n",
		filepath.Join(bin, "x11vnc"):       "#!/bin/sh\n[ \"$1\" = -storepasswd ] && printf 'stored\\n' > \"$3\"; exit 0\n",
		// curl writes the file it was asked for, and records the name: apt
		// refuses a package that is not called .deb, and the name is exactly
		// what was wrong the first time this ran on a real host.
		filepath.Join(bin, "curl"): "#!/bin/sh\nprintf '%s\\n' \"$3\" >> " +
			filepath.Join(bin, "..", "downloaded") + "\n: > \"$3\"\n",
		filepath.Join(bin, "apt-get"): "#!/bin/sh\ncase \"$*\" in *.deb*|*/tmp/deployer-chrome*) " +
			"printf '#!/bin/sh\\nexit 0\\n' > " + filepath.Join(bin, "google-chrome") +
			"; chmod +x " + filepath.Join(bin, "google-chrome") + ";; esac\nexit 0\n",
	} {
		if err := os.WriteFile(name, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(snapDir, "chromium"), filepath.Join(bin, "chromium")); err != nil {
		t.Fatal(err)
	}

	out, code := runScript(t, installScriptFor(root, "pi", ""), "", bin)
	if code != 0 {
		t.Fatalf("install exited %d: %s", code, out)
	}
	if !strings.Contains(out, "no browser here can run — snaps: chromium") {
		t.Errorf("the log should say why it went and got one:\n%s", out)
	}
	if !strings.Contains(out, "the session will run google-chrome") {
		t.Errorf("the installer should end naming a browser that is a package:\n%s", out)
	}
	// apt will not touch a local package whose name does not end in .deb: it
	// answers "Unsupported file ... given on commandline" and stops. A
	// temporary name from mktemp does not, which cost a round trip to find.
	downloaded := strings.TrimSpace(read(t, filepath.Join(bin, "..", "downloaded")))
	if !strings.HasSuffix(downloaded, ".deb") {
		t.Errorf("Chrome was downloaded to %q, which apt will refuse to install", downloaded)
	}
	if got := strings.TrimSpace(read(t, filepath.Join(root, remoteConfDir, "setup.state"))); got != "ok" {
		t.Errorf("state is %q, want ok", got)
	}
}

// Ubuntu's chromium-browser is not a symlink into /snap: it is a shell script
// that calls out to one, and it announces itself by failing on a line about
// xdg-settings. The path alone does not give it away, so the file is read — and
// whatever the file says, a browser that cannot report its own version is not
// one this can use.
func TestBrowserRuleRejectsASnapWrapperAndAnythingThatWillNotRun(t *testing.T) {
	bin := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// What Ubuntu leaves at /usr/bin/chromium-browser.
	write("chromium-browser", "#!/bin/sh\n# redirect to the snap\nexec snap run chromium \"$@\"\n")
	// A browser that is on the PATH and cannot answer for itself. It comes
	// before the working one in the list, or the rule would stop before
	// reaching it — which is itself the right behaviour.
	write("chromium", "#!/bin/sh\nexit 127\n")
	// And one that works.
	write("google-chrome", "#!/bin/sh\n[ \"$1\" = --version ] && echo 'Google Chrome 128.0'\nexit 0\n")

	script := pickBrowserScript() + `
pick_browser || true
printf 'browser=%s\n' "$browser"
printf 'snaps=%s\n' "$snap_browsers"
printf 'broken=%s\n' "$broken_browsers"
`
	out, code := runScript(t, "sh -c "+shellQuoteForTest(script), "", bin)
	if code != 0 {
		t.Fatalf("the rule exited %d: %s", code, out)
	}
	if !strings.Contains(out, "browser=google-chrome") {
		t.Errorf("want the one that runs to be chosen:\n%s", out)
	}
	if !strings.Contains(out, "snaps=chromium-browser") {
		t.Errorf("the wrapper script should be read and recognised:\n%s", out)
	}
	if !strings.Contains(out, "broken=chromium") {
		t.Errorf("a browser that cannot say its version should be named:\n%s", out)
	}
}

// shellQuoteForTest wraps a script for `sh -c`.
func shellQuoteForTest(script string) string {
	return "'" + strings.ReplaceAll(script, "'", `'\''`) + "'"
}
