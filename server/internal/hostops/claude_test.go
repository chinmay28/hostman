package hostops

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The Claude scripts run as the SSH user in that user's home, so they are
// tested in a home of their own with a stand-in `claude` on the PATH: a shell
// script that answers --version, auth status and auth login the way the real
// one does, and records what it was told.

const fakeClaude = `#!/bin/sh
case "$1" in
  --version) echo "2.1.211 (Claude Code)" ;;
  auth)
    case "$2" in
      status)
        if [ -f "$HOME/.claude/.credentials.json" ]; then
          echo '{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty"}'
        elif [ -f "$HOME/.claude/settings.json" ] && grep -q ANTHROPIC_API_KEY "$HOME/.claude/settings.json"; then
          echo '{"loggedIn":true,"authMethod":"api_key"}'
        else
          echo '{"loggedIn":false}'
        fi ;;
      login)
        echo "Opening browser to sign in…"
        echo "If the browser didn't open, visit: https://claude.com/cai/oauth/authorize?code=true&client_id=abc&state=xyz"
        printf 'Paste code here if prompted > '
        read code || exit 9
        mkdir -p "$HOME/.claude"
        printf '%s %s\n' "$code" "$*" > "$HOME/.claude/.credentials.json"
        echo "Logged in" ;;
    esac ;;
esac
`

// claudeHome makes a home directory with the stand-in installed in
// ~/.local/bin, or not, and returns it with the environment to run under.
func claudeHome(t *testing.T, installed bool) (string, []string) {
	t.Helper()
	home := t.TempDir()
	if installed {
		bin := filepath.Join(home, ".local", "bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(fakeClaude), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A PATH of its own, so the claude this machine may have is not the one
	// the scripts find.
	env := []string{"HOME=" + home, "PATH=/usr/local/bin:/usr/bin:/bin", "TMPDIR=" + t.TempDir()}
	return home, env
}

// runIn is runScript with a home of its own.
func runIn(t *testing.T, env []string, cmd, stdin string) (string, string, int) {
	t.Helper()
	run := exec.Command("/bin/sh", "-c", cmd)
	run.Stdin = strings.NewReader(stdin)
	run.Env = env
	var out, errOut strings.Builder
	run.Stdout = &out
	run.Stderr = &errOut
	err := run.Run()
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run: %v", err)
		}
		code = exit.ExitCode()
	}
	return out.String(), errOut.String(), code
}

func status(t *testing.T, env []string) *ClaudeHost {
	t.Helper()
	out, errOut, code := runIn(t, env, asUser(fmt.Sprintf(claudeStatusScript, MaxClaudeLogBytes, claudeStateDir)), "")
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, errOut)
	}
	return parseClaudeStatus(out, "pi")
}

// until polls the status until want is satisfied, or gives up.
func until(t *testing.T, env []string, what string, want func(*ClaudeHost) bool) *ClaudeHost {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		c := status(t, env)
		if want(c) {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("gave up waiting for %s: %+v", what, c)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestClaudeStatusOnABareHost(t *testing.T) {
	home, env := claudeHome(t, false)
	c := status(t, env)
	if c.Installed || c.SignedIn || c.Ready || c.Version != "" {
		t.Fatalf("bare host = %+v", c)
	}
	if c.Install != "absent" || c.Login != "absent" || c.Home != home || c.Arch == "" {
		t.Fatalf("bare host = %+v", c)
	}
}

func TestClaudeStatusReadsTheCLIAndTheAccount(t *testing.T) {
	home, env := claudeHome(t, true)
	c := status(t, env)
	if !c.Installed || c.Version != "2.1.211" || !strings.HasSuffix(c.Path, "/.local/bin/claude") {
		t.Fatalf("installed = %+v", c)
	}
	if c.SignedIn || c.Ready {
		t.Fatalf("not signed in yet, but %+v", c)
	}

	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte("{}"), 0o600)
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{
  "numStartups": 3,
  "oauthAccount": {
    "accountUuid": "u",
    "emailAddress": "cm@example.net",
    "organizationName": "Personal"
  },
  "subscriptionType": "max",
  "projects": {}
}`), 0o600)
	c = status(t, env)
	if !c.SignedIn || c.Auth != "oauth" || c.Account != "cm@example.net" || c.Plan != "max" || !c.Ready {
		t.Fatalf("signed in = %+v", c)
	}
}

func TestClaudeInstallRunsDetachedAndReportsProgress(t *testing.T) {
	home, env := claudeHome(t, false)
	// An "installer" that puts the stand-in where the real one goes.
	installer := filepath.Join(t.TempDir(), "install.sh")
	os.WriteFile(installer, []byte("#!/bin/sh\necho 'Installing Claude Code...'\nmkdir -p \"$HOME/.local/bin\"\ncat > \"$HOME/.local/bin/claude\" <<'EOF'\n"+fakeClaude+"EOF\nchmod 755 \"$HOME/.local/bin/claude\"\necho done\n"), 0o755)

	script := fmt.Sprintf(claudeInstallScript, claudeStateDir, "file://"+installer)
	if _, errOut, code := runIn(t, env, asUser(script), ""); code != 0 {
		t.Fatalf("start exited %d: %s", code, errOut)
	}
	c := until(t, env, "the install to finish", func(c *ClaudeHost) bool { return c.Install == "done" || c.Install == "failed" })
	if c.Install != "done" || !c.Installed || c.Version != "2.1.211" {
		t.Fatalf("after install = %+v\nlog:\n%s", c, c.InstallLog)
	}
	for _, want := range []string{"fetching the installer", "Installing Claude Code...", "installed 2.1.211"} {
		if !strings.Contains(c.InstallLog, want) {
			t.Errorf("log lacks %q:\n%s", want, c.InstallLog)
		}
	}
	if _, err := os.Stat(filepath.Join(home, claudeStateDir, "install.pid")); !os.IsNotExist(err) {
		t.Error("the pid file was left behind")
	}
}

func TestClaudeInstallFailureIsReported(t *testing.T) {
	_, env := claudeHome(t, false)
	script := fmt.Sprintf(claudeInstallScript, claudeStateDir, "file:///nonexistent/install.sh")
	if _, errOut, code := runIn(t, env, asUser(script), ""); code != 0 {
		t.Fatalf("start exited %d: %s", code, errOut)
	}
	c := until(t, env, "the install to fail", func(c *ClaudeHost) bool { return c.Install == "failed" })
	if c.InstallExit != 5 || !strings.Contains(c.InstallLog, "could not download") {
		t.Fatalf("failed install = %+v", c)
	}
}

func TestClaudeLoginWaitsForACodeThenSignsIn(t *testing.T) {
	home, env := claudeHome(t, true)
	if _, errOut, code := runIn(t, env, asUser(fmt.Sprintf(claudeLoginScript, claudeStateDir), "--console"), ""); code != 0 {
		t.Fatalf("start exited %d: %s", code, errOut)
	}
	c := until(t, env, "the link", func(c *ClaudeHost) bool { return c.Login == "waiting" })
	if !strings.HasPrefix(c.LoginURL, "https://claude.com/cai/oauth/authorize?code=true") || strings.ContainsAny(c.LoginURL, " '\"") {
		t.Fatalf("url = %q", c.LoginURL)
	}

	// A code that is not a code never reaches the host.
	if _, err := (&Service{}).LoginCode(nil, nil, "rm -rf /"); err == nil {
		t.Error("a shell command was accepted as a code")
	}

	if _, errOut, code := runIn(t, env, asUser(fmt.Sprintf(claudeLoginCodeScript, claudeStateDir), "abc123#state456"), ""); code != 0 {
		t.Fatalf("code exited %d: %s", code, errOut)
	}
	c = until(t, env, "the sign-in to finish", func(c *ClaudeHost) bool { return c.SignedIn })
	if c.Login != "absent" && c.Login != "done" {
		t.Errorf("login state after finishing = %q", c.Login)
	}
	got, _ := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	if !strings.HasPrefix(string(got), "abc123#state456 auth login --console") {
		t.Fatalf("the CLI got %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, claudeStateDir, "login.code")); !os.IsNotExist(err) {
		t.Error("the code file was left behind")
	}

	// Handing a code to nobody is a plain refusal.
	if _, _, code := runIn(t, env, asUser(fmt.Sprintf(claudeLoginCodeScript, claudeStateDir), "abc"), ""); code != 3 {
		t.Errorf("code with no sign-in waiting exited %d, want 3", code)
	}
}

func TestClaudeLoginCanBeCancelled(t *testing.T) {
	home, env := claudeHome(t, true)
	runIn(t, env, asUser(fmt.Sprintf(claudeLoginScript, claudeStateDir)), "")
	until(t, env, "the link", func(c *ClaudeHost) bool { return c.Login == "waiting" })
	if _, errOut, code := runIn(t, env, asUser(fmt.Sprintf(claudeCancelLoginScript, claudeStateDir)), ""); code != 0 {
		t.Fatalf("cancel exited %d: %s", code, errOut)
	}
	c := status(t, env)
	if c.Login != "absent" || c.LoginURL != "" {
		t.Fatalf("after cancel = %+v", c)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", ".credentials.json")); !os.IsNotExist(err) {
		t.Error("a cancelled sign-in signed in anyway")
	}
}

func TestClaudeAPIKeyIsWrittenIntoSettings(t *testing.T) {
	home, env := claudeHome(t, true)
	settings := filepath.Join(home, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(settings), 0o700)
	os.WriteFile(settings, []byte(`{"model": "sonnet", "env": {"FOO": "bar"}}`), 0o600)

	if _, errOut, code := runIn(t, env, asUser(claudeAPIKeyScript), "sk-ant-api03-abcdefghijklmnopqrstuvwxyz\n"); code != 0 {
		t.Fatalf("exited %d: %s", code, errOut)
	}
	raw, _ := os.ReadFile(settings)
	var got struct {
		Model string            `json:"model"`
		Env   map[string]string `json:"env"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("settings are not JSON: %s", raw)
	}
	if got.Model != "sonnet" || got.Env["FOO"] != "bar" || got.Env["ANTHROPIC_API_KEY"] != "sk-ant-api03-abcdefghijklmnopqrstuvwxyz" {
		t.Fatalf("settings = %s", raw)
	}
	if info, _ := os.Stat(settings); info.Mode().Perm() != 0o600 {
		t.Errorf("settings mode = %o", info.Mode().Perm())
	}
	c := status(t, env)
	if !c.SignedIn || c.Auth != "api_key" {
		t.Fatalf("with a key = %+v", c)
	}

	for _, bad := range []string{"", "hunter2", "sk-ant-x", "sk-ant-" + strings.Repeat("a", 30) + "'; rm -rf /"} {
		if _, err := (&Service{}).SetAPIKey(nil, nil, bad); err == nil {
			t.Errorf("%q was accepted as a key", bad)
		}
	}
}

func TestJobState(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		exit int
	}{
		{"", "absent", 0}, {"running", "running", 0}, {"0", "done", 0},
		{"5", "failed", 5}, {"lost", "failed", -1}, {"garbage", "failed", -1},
	} {
		if got, exit := jobState(tc.in); got != tc.want || exit != tc.exit {
			t.Errorf("jobState(%q) = %s/%d, want %s/%d", tc.in, got, exit, tc.want, tc.exit)
		}
	}
}

// A conversation file is the CLI's own, one JSON object per line. These are
// the lines a listing reads, in the shapes the CLI writes them.
func conversationFile(t *testing.T, home, dir, id string, lines ...string) string {
	t.Helper()
	project := filepath.Join(home, ".claude", "projects", strings.ReplaceAll(dir, "/", "-"))
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(project, id+".jsonl")
	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func userLine(dir, when, text string) string {
	return fmt.Sprintf(`{"type":"user","message":{"role":"user","content":%q},"uuid":"u","timestamp":%q,"cwd":%q,"sessionId":"s","gitBranch":"main","isSidechain":false}`, text, when, dir)
}

func history(t *testing.T, env []string) []ClaudeConversation {
	t.Helper()
	out, errOut, code := runIn(t, env, asUser(fmt.Sprintf(claudeHistoryScript, MaxClaudeHistory, claudeHistoryHead, claudeHistoryTail)), "")
	if code != 0 {
		t.Fatalf("history exited %d: %s", code, errOut)
	}
	return parseClaudeHistory(out)
}

func TestClaudeHistoryOnAHostWithNoneIsEmpty(t *testing.T) {
	_, env := claudeHome(t, true)
	if got := history(t, env); len(got) != 0 {
		t.Fatalf("history = %+v", got)
	}
}

func TestClaudeHistoryListsTheCLIsConversationsNewestFirst(t *testing.T) {
	home, env := claudeHome(t, true)
	old := conversationFile(t, home, "/home/pi/apps", "11111111-1111-4111-8111-111111111111",
		userLine("/home/pi/apps", "2026-09-01T10:00:00.000Z", "Fix the deploy script\nIt fails on reboot"),
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"On it."}]},"timestamp":"2026-09-01T10:00:05.000Z","cwd":"/home/pi/apps"}`,
	)
	titled := conversationFile(t, home, "/home/pi", "22222222-2222-4222-8222-222222222222",
		`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-09-02T09:00:00.000Z","sessionId":"s"}`,
		`{"type":"user","isMeta":true,"message":{"role":"user","content":"<local-command-caveat>ignored</local-command-caveat>"},"timestamp":"2026-09-02T09:00:00.000Z","cwd":"/home/pi"}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"<system-reminder>not me</system-reminder>"},{"type":"text","text":"Why is the Pi hot?"}]},"timestamp":"2026-09-02T09:00:01.000Z","cwd":"/home/pi","gitBranch":""}`,
		`{"type":"ai-title","aiTitle":"Investigating Pi temperature","sessionId":"s"}`,
		`{"type":"custom-title","customTitle":"Hot Pi","sessionId":"s"}`,
	)
	// A file the CLI opened and nobody spoke into: nothing to show.
	empty := conversationFile(t, home, "/home/pi", "33333333-3333-4333-8333-333333333333",
		`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-09-03T09:00:00.000Z","sessionId":"s"}`,
	)
	// A subagent's transcript lives a level down and is not resumable.
	sub := filepath.Join(filepath.Dir(titled), "22222222-2222-4222-8222-222222222222", "subagents")
	os.MkdirAll(sub, 0o700)
	os.WriteFile(filepath.Join(sub, "agent-1.jsonl"), []byte(userLine("/home/pi", "2026-09-04T09:00:00.000Z", "subagent work")+"\n"), 0o600)

	t1 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	os.Chtimes(old, t1, t1)
	os.Chtimes(titled, t2, t2)
	os.Chtimes(empty, t2.Add(time.Hour), t2.Add(time.Hour))

	got := history(t, env)
	if len(got) != 2 {
		t.Fatalf("history = %+v, want two", got)
	}
	a, b := got[0], got[1]
	if a.ID != "22222222-2222-4222-8222-222222222222" || a.Name != "Hot Pi" || a.Dir != "/home/pi" || a.Branch != "" {
		t.Errorf("newest = %+v", a)
	}
	if !a.ModifiedAt.Equal(t2) || a.StartedAt == nil || !a.StartedAt.Equal(time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("newest times = %v / %v", a.ModifiedAt, a.StartedAt)
	}
	if b.ID != "11111111-1111-4111-8111-111111111111" || b.Name != "Fix the deploy script" || b.Dir != "/home/pi/apps" || b.Branch != "main" || b.Size == 0 {
		t.Errorf("older = %+v", b)
	}
}

func TestClaudeHistoryReadsTheTitleFromTheTailOfALongFile(t *testing.T) {
	home, env := claudeHome(t, true)
	lines := []string{userLine("/srv/app", "2026-09-05T10:00:00.000Z", "Refactor everything")}
	// Far more than the head carries, so the title at the end is only in
	// the tail — and the cut through the middle must not confuse anything.
	filler := strings.Repeat("x", 3000)
	for i := 0; i < 20; i++ {
		lines = append(lines, fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":%q}]},"cwd":"/srv/app"}`, filler))
	}
	lines = append(lines, `{"type":"custom-title","customTitle":"The big refactor","sessionId":"s"}`)
	conversationFile(t, home, "/srv/app", "44444444-4444-4444-8444-444444444444", lines...)

	got := history(t, env)
	if len(got) != 1 || got[0].Name != "The big refactor" || got[0].Dir != "/srv/app" {
		t.Fatalf("history = %+v", got)
	}
	if got[0].Size < int64(20*3000) {
		t.Errorf("size = %d, want the whole file's", got[0].Size)
	}
}

func TestConversationNamesFallBackInOrder(t *testing.T) {
	base := ClaudeConversation{ID: "x"}
	cases := []struct {
		name  string
		lines []string
		want  string
		ok    bool
	}{
		{"nothing", nil, "", false},
		{"only machinery", []string{`{"type":"user","message":{"role":"user","content":"<command-name>/clear</command-name>"},"cwd":"/h"}`}, "", false},
		{"first words", []string{userLine("/h", "2026-09-05T10:00:00.000Z", "  Hello there  ")}, "Hello there", true},
		{"summary over words", []string{userLine("/h", "2026-09-05T10:00:00.000Z", "Hello"), `{"type":"summary","summary":"A greeting"}`}, "A greeting", true},
		{"ai title over summary", []string{`{"type":"summary","summary":"A greeting"}`, `{"type":"ai-title","aiTitle":"Greeting"}`}, "Greeting", true},
		{"custom over all", []string{`{"type":"ai-title","aiTitle":"Greeting"}`, `{"type":"custom-title","customTitle":"Mine"}`}, "Mine", true},
		{"last prompt as a last resort", []string{`{"type":"last-prompt","lastPrompt":"what now"}`}, "what now", true},
		{"a long one is cut", []string{userLine("/h", "t", strings.Repeat("é", 60))}, strings.Repeat("é", 40) + "…", true},
		{"a cut line parses as nothing", []string{`{"type":"user","message":{"role":"user","con`, userLine("/h", "t", "After the cut")}, "After the cut", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, ok := readConversation(base, tc.lines)
			if ok != tc.ok || c.Name != tc.want {
				t.Fatalf("got %q, %v; want %q, %v", c.Name, ok, tc.want, tc.ok)
			}
		})
	}
}
