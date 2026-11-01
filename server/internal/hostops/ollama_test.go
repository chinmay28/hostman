package hostops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/hostman/server/internal/store"
)

// ollamaStub is an `ollama` that answers the way the real one does for the
// commands the scripts use, and writes every call to $OLLAMA_LOG so a test can
// see what it was asked. Its server is "up" unless $OLLAMA_DOWN is set.
const ollamaStub = `
[ -n "$OLLAMA_LOG" ] && printf '%s\n' "$*" >> "$OLLAMA_LOG"
if [ -n "$OLLAMA_DOWN" ] && [ "$1" != --version ]; then
  echo "Error: could not connect to ollama server, run 'ollama serve' to start it" >&2
  exit 1
fi
case "$1" in
  --version)
    if [ -n "$OLLAMA_DOWN" ]; then
      echo "Warning: could not connect to a running Ollama instance" >&2
      echo "Warning: client version is 0.6.2" >&2
    else
      echo "ollama version is 0.6.2"
    fi
    ;;
  list)
    # Aligned the way the real one's tabwriter aligns it: every column as
    # wide as its widest cell, header included.
    printf 'NAME                                ID              SIZE      MODIFIED        \n'
    printf 'qwen2.5-coder:7b                    2b0496514337    4.7 GB    3 days ago      \n'
    printf 'llama3.2:latest                     a80c4f17acd5    2.0 GB    2 weeks ago     \n'
    printf 'hf.co/org/Some-Model-GGUF:Q4_K_M    0123456789ab    12 GB     5 months ago    \n'
    ;;
  ps)
    printf 'NAME               ID              SIZE      PROCESSOR    CONTEXT    UNTIL              \n'
    printf 'llama3.2:latest    a80c4f17acd5    4.0 GB    100%% GPU     4096       4 minutes from now    \n'
    ;;
  pull)
    # What the real one writes when nothing is a terminal: the same layers
    # redrawn, carriage returns and cursor moves and all.
    printf 'pulling manifest \r\033[?25l\033[1A\033[K'
    printf 'pulling manifest \npulling 8934d96d3f08...   0%% ▕                ▏    0 B/4.1 GB  \r'
    printf 'pulling manifest \npulling 8934d96d3f08...  42%% ▕██████          ▏ 1.7 GB/4.1 GB   30 MB/s   1m20s\r'
    if [ "$2" = "nosuch:model" ]; then
      printf '\nError: pull model manifest: file does not exist\n' >&2
      exit 1
    fi
    printf 'pulling manifest \npulling 8934d96d3f08... 100%% ▕████████████████▏ 4.1 GB/4.1 GB  \n'
    printf 'verifying sha256 digest \nwriting manifest \nsuccess \033[?25h\n'
    ;;
  rm)
    case "$2" in
      llama3.2:latest) echo "deleted '$2'" ;;
      *) echo "Error: model '$2' not found" >&2; exit 1 ;;
    esac
    ;;
  stop) ;;
  serve) sleep 5 ;;
  *) echo "Error: unknown command \"$1\" for \"ollama\"" >&2; exit 1 ;;
esac
`

// ollamaBin is a PATH entry holding the stub, logging to a file the test
// reads, with a home of its own so a pull's bookkeeping lands somewhere a
// test can look.
func ollamaBin(t *testing.T) (bin, log string) {
	t.Helper()
	log = filepath.Join(t.TempDir(), "calls")
	t.Setenv("OLLAMA_LOG", log)
	t.Setenv("OLLAMA_DOWN", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	return stubBin(t, map[string]string{"ollama": ollamaStub}), log
}

// pullDir is where the scripts under test keep a pull's files.
func pullDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(os.Getenv("HOME"), ".local", "state", "hostman", "ollama")
}

// A generated script has to be a script before it can be a correct one.
func TestOllamaScriptsParse(t *testing.T) {
	scripts := map[string]string{
		"status": ollamaStatusScript,
		"pull":   ollamaPullScript,
		"run":    ollamaPullRunScript,
		"cancel": ollamaCancelScript,
		"clear":  ollamaClearScript,
		"model":  ollamaModelScript,
		"start":  ollamaStartScript,
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

// The status script asks the command what is on the disk and in memory, and
// the parser reads the tables back — a value with a space in it, "4.7 GB" or
// "2 weeks ago", coming through whole.
func TestOllamaStatusScriptListsModels(t *testing.T) {
	bin, log := ollamaBin(t)
	out, code := runScript(t, asUser(ollamaStatusScript), "", bin)
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, out)
	}
	host := parseOllamaHost(out)

	if !host.Installed {
		t.Error("installed = false with ollama on the PATH")
	}
	if host.Version != "0.6.2" {
		t.Errorf("version = %q, want 0.6.2", host.Version)
	}
	if !host.Running || host.Error != "" {
		t.Errorf("running = %v, error = %q; the server answered", host.Running, host.Error)
	}
	if host.User == "" {
		t.Error("the status should say who asked")
	}
	if host.Pull != nil {
		t.Errorf("pull = %+v on a host where none was started", host.Pull)
	}

	// Sorted by name, case aside, rather than in ollama's order.
	if len(host.Models) != 3 {
		t.Fatalf("got %d models, want 3: %+v", len(host.Models), host.Models)
	}
	hf, llama, qwen := host.Models[0], host.Models[1], host.Models[2]
	if hf.Name != "hf.co/org/Some-Model-GGUF:Q4_K_M" || hf.Size != "12 GB" || hf.SizeBytes != 12_000_000_000 {
		t.Errorf("hugging face model = %+v", hf)
	}
	if llama.Name != "llama3.2:latest" || llama.ID != "a80c4f17acd5" || llama.Size != "2.0 GB" || llama.Modified != "2 weeks ago" {
		t.Errorf("llama = %+v", llama)
	}
	if llama.SizeBytes != 2_000_000_000 {
		t.Errorf("llama's bytes = %d", llama.SizeBytes)
	}
	if qwen.Name != "qwen2.5-coder:7b" || qwen.Modified != "3 days ago" || qwen.SizeBytes != 4_700_000_000 {
		t.Errorf("qwen = %+v", qwen)
	}

	if len(host.Loaded) != 1 {
		t.Fatalf("got %d loaded, want 1: %+v", len(host.Loaded), host.Loaded)
	}
	if l := host.Loaded[0]; l.Name != "llama3.2:latest" || l.Processor != "100% GPU" || l.Context != "4096" || l.Until != "4 minutes from now" {
		t.Errorf("loaded = %+v", l)
	}

	// Asked as the connecting user: nothing here goes through sudo.
	if strings.Contains(asUser(ollamaStatusScript), "sudo") {
		t.Error("the status must not elevate")
	}
	for _, call := range calls(t, log) {
		if strings.HasPrefix(call, "pull") || strings.HasPrefix(call, "rm") {
			t.Errorf("the status script changed something: %q", call)
		}
	}
}

// A server that is not running is a fact the screen says, not a failure: the
// command is still there, its version still reads, and what it said instead
// of a list comes back as the error.
func TestOllamaStatusScriptWhenTheServerIsDown(t *testing.T) {
	bin, _ := ollamaBin(t)
	t.Setenv("OLLAMA_DOWN", "1")
	out, code := runScript(t, asUser(ollamaStatusScript), "", bin)
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, out)
	}
	host := parseOllamaHost(out)
	if !host.Installed {
		t.Error("installed = false: the command is there, the server is not")
	}
	if host.Version != "0.6.2" {
		t.Errorf("version = %q, want it read from the warning", host.Version)
	}
	if host.Running {
		t.Error("running = true with a server that refused")
	}
	if want := "could not connect to ollama server, run 'ollama serve' to start it"; host.Error != want {
		t.Errorf("error = %q, want %q", host.Error, want)
	}
	if len(host.Models) != 0 || len(host.Loaded) != 0 {
		t.Errorf("models = %+v, loaded = %+v; want none", host.Models, host.Loaded)
	}
}

// A host without ollama is a PATH without it, and the status says so with a
// clean exit — not having a model server is a fact about the host.
func TestOllamaStatusScriptOnAHostWithoutOllama(t *testing.T) {
	bin := bareBin(t, map[string]string{"id": "echo pi"})
	t.Setenv("HOME", t.TempDir())
	out, code := runBare(t, asUser(ollamaStatusScript), bin)
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, out)
	}
	host := parseOllamaHost(out)
	if host.Installed {
		t.Error("installed = true on a PATH with no ollama")
	}
	if host.User != "pi" {
		t.Errorf("user = %q, want pi", host.User)
	}
	if host.Running || len(host.Models) != 0 {
		t.Errorf("a host without ollama reported %+v", host)
	}
}

// A pull is started detached and comes straight back; the status then reads
// where it got to from the files the runner leaves behind, with the bar's
// redrawing boiled down to one line per step and the last figure it showed.
func TestOllamaPullRunsDetachedAndIsFollowed(t *testing.T) {
	bin, log := ollamaBin(t)
	out, code := runScript(t, asUser(ollamaPullScript, "llama3.2:3b"), "", bin)
	if code != 0 {
		t.Fatalf("pull exited %d: %s", code, out)
	}
	if strings.TrimSpace(out) != "started" {
		t.Errorf("pull said %q, want started", out)
	}

	dir := pullDir(t)
	state := waitForPull(t, dir, "ok")
	if state != "ok" {
		t.Fatalf("pull ended as %q, want ok (log: %s)", state, readAll(t, filepath.Join(dir, "pull.log")))
	}
	var pulled bool
	for _, call := range calls(t, log) {
		if call == "pull llama3.2:3b" {
			pulled = true
		}
	}
	if !pulled {
		t.Errorf("ollama was asked %q, want a pull of llama3.2:3b", calls(t, log))
	}

	out, code = runScript(t, asUser(ollamaStatusScript), "", bin)
	if code != 0 {
		t.Fatalf("status exited %d: %s", code, out)
	}
	host := parseOllamaHost(out)
	if host.Pull == nil {
		t.Fatal("the status should report the pull")
	}
	if host.Pull.Model != "llama3.2:3b" || host.Pull.State != "ok" || host.Pull.Percent != 100 {
		t.Errorf("pull = %+v", host.Pull)
	}
	if host.Pull.Status != "success" {
		t.Errorf("status line = %q, want the last thing ollama said", host.Pull.Status)
	}
	// One line per step, redraws collapsed, no escapes and no bar.
	want := "pulling manifest\npulling 8934d96d3f08... 100% 4.1 GB/4.1 GB\nverifying sha256 digest\nwriting manifest\nsuccess"
	if host.Pull.Log != want {
		t.Errorf("log =\n%s\nwant\n%s", host.Pull.Log, want)
	}

	// Clearing forgets it.
	if out, code := runScript(t, asUser(ollamaClearScript), "", bin); code != 0 {
		t.Fatalf("clear exited %d: %s", code, out)
	}
	out, _ = runScript(t, asUser(ollamaStatusScript), "", bin)
	if parseOllamaHost(out).Pull != nil {
		t.Error("the pull is still reported after being cleared")
	}
}

// A pull that fails records how, and the error ollama printed is the status
// line the screen shows.
func TestOllamaPullThatFails(t *testing.T) {
	bin, _ := ollamaBin(t)
	if out, code := runScript(t, asUser(ollamaPullScript, "nosuch:model"), "", bin); code != 0 {
		t.Fatalf("pull exited %d: %s", code, out)
	}
	dir := pullDir(t)
	if state := waitForPull(t, dir, "failed:1"); state != "failed:1" {
		t.Fatalf("pull ended as %q, want failed:1", state)
	}
	out, _ := runScript(t, asUser(ollamaStatusScript), "", bin)
	pull := parseOllamaHost(out).Pull
	if pull == nil || pull.State != "failed" || pull.Exit != 1 {
		t.Fatalf("pull = %+v, want a failure with exit 1", pull)
	}
	if pull.Status != "Error: pull model manifest: file does not exist" {
		t.Errorf("status = %q, want ollama's error", pull.Status)
	}
	if pull.Percent != 42 {
		t.Errorf("percent = %d, want the last figure the bar showed", pull.Percent)
	}
}

// A second pull is refused while the first runs, by name, so the screen can
// say what is in the way. One that is only recorded as running — the runner
// gone — is not in the way at all.
func TestOllamaPullRefusesToJoinOneRunning(t *testing.T) {
	bin, _ := ollamaBin(t)
	dir := pullDir(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A pretend pull: this very test process stands in for the runner.
	writeState(t, dir, "running", "mistral:7b", os.Getpid())
	out, code := runScript(t, asUser(ollamaPullScript, "llama3.2:3b"), "", bin)
	if code != 6 {
		t.Fatalf("pull exited %d, want 6 (busy): %s", code, out)
	}
	if strings.TrimSpace(out) != "mistral:7b" {
		t.Errorf("the refusal named %q, want the model in the way", out)
	}

	// The runner died without saying so: the status reads it as lost, and a
	// new pull may start.
	writeState(t, dir, "running", "mistral:7b", 2_000_000_000)
	out, _ = runScript(t, asUser(ollamaStatusScript), "", bin)
	if pull := parseOllamaHost(out).Pull; pull == nil || pull.State != "lost" {
		t.Errorf("pull = %+v, want lost", pull)
	}
	if out, code := runScript(t, asUser(ollamaPullScript, "llama3.2:3b"), "", bin); code != 0 {
		t.Errorf("a pull after a lost one exited %d: %s", code, out)
	}
	waitForPull(t, dir, "ok")
}

// Cancelling marks the pull first and kills the runner's whole process group
// second, so the download under the shell stops too and the state reads as
// cancelled whether or not the runner's trap had its say.
func TestOllamaCancelStopsTheRunner(t *testing.T) {
	// A runner that would take far longer than the test: the real one is a
	// shell around `ollama pull`, and this is a shell around sleep.
	slow := map[string]string{"ollama": `[ "$1" = pull ] && exec sleep 30; ` + ollamaStub}
	bin := stubBin(t, slow)
	t.Setenv("OLLAMA_LOG", "")
	t.Setenv("OLLAMA_DOWN", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", "")
	dir := pullDir(t)

	if out, code := runScript(t, asUser(ollamaPullScript, "llama3.2:3b"), "", bin); code != 0 {
		t.Fatalf("pull exited %d: %s", code, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "pull.pid")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the runner never wrote its pid")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Clearing while it runs is refused; it has to be cancelled first.
	if _, code := runScript(t, asUser(ollamaClearScript), "", bin); code != 6 {
		t.Errorf("clear during a pull exited %d, want 6", code)
	}

	if out, code := runScript(t, asUser(ollamaCancelScript), "", bin); code != 0 || strings.TrimSpace(out) != "cancelled" {
		t.Fatalf("cancel exited %d, said %q", code, out)
	}
	pid := strings.TrimSpace(readAll(t, filepath.Join(dir, "pull.pid")))
	deadline = time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat("/proc/" + pid); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the runner (pid %s) is still there after a cancel", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
	out, _ := runScript(t, asUser(ollamaStatusScript), "", bin)
	pull := parseOllamaHost(out).Pull
	if pull == nil || pull.State != "cancelled" {
		t.Errorf("pull = %+v, want cancelled", pull)
	}
	// Cancelling nothing is nothing, not an error.
	if out, code := runScript(t, asUser(ollamaCancelScript), "", bin); code != 0 || strings.TrimSpace(out) != "nothing" {
		t.Errorf("a second cancel exited %d, said %q", code, out)
	}
}

// The model verbs go to ollama as spelled, and what it says when it refuses
// comes back as the one line a person can act on.
func TestOllamaModelScript(t *testing.T) {
	bin, log := ollamaBin(t)
	if out, code := runScript(t, asUser(ollamaModelScript, "rm", "llama3.2:latest"), "", bin); code != 0 {
		t.Fatalf("rm exited %d: %s", code, out)
	}
	if out, code := runScript(t, asUser(ollamaModelScript, "stop", "llama3.2:latest"), "", bin); code != 0 {
		t.Fatalf("stop exited %d: %s", code, out)
	}
	got := calls(t, log)
	want := []string{"rm llama3.2:latest", "stop llama3.2:latest"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("ollama was asked %q, want %q", got, want)
	}

	path := filepath.Join(t.TempDir(), "err")
	out, code := runScript(t, asUser(ollamaModelScript, "rm", "ghost:latest")+" 2>"+quoteForTest(path), "", bin)
	if code == 0 {
		t.Fatal("removing a model that is not there should fail")
	}
	stderr, _ := os.ReadFile(path)
	if err := ollamaFailure(string(stderr), out, "remove", "ghost:latest"); err.Error() != "model 'ghost:latest' not found" {
		t.Errorf("error = %q", err)
	}
}

func TestOllamaScriptsRefuseAHostWithoutOllama(t *testing.T) {
	bin := bareBin(t, nil)
	t.Setenv("HOME", t.TempDir())
	for name, cmd := range map[string]string{
		"pull":  asUser(ollamaPullScript, "llama3.2"),
		"model": asUser(ollamaModelScript, "rm", "llama3.2"),
		"start": asUser(ollamaStartScript),
	} {
		if out, code := runBare(t, cmd, bin); code != 7 {
			t.Errorf("%s exited %d, want 7: %s", name, code, out)
		}
	}
}

// Starting goes through the unit where the installer left one, as root
// where that is free and through sudo where it is not — and without sudo it
// says so rather than trying something else.
func TestOllamaStartScript(t *testing.T) {
	calls := filepath.Join(t.TempDir(), "systemctl-calls")
	stubs := map[string]string{
		"ollama":    ollamaStub,
		"systemctl": `printf '%s\n' "$*" >> ` + quoteForTest(calls) + `; [ "$1" = cat ] && exit 0; exit 0`,
		"sudo":      `shift; exec "$@"`,
		"id":        `echo 1000`,
	}
	bin := stubBin(t, stubs)
	t.Setenv("OLLAMA_LOG", "")
	t.Setenv("OLLAMA_DOWN", "")
	t.Setenv("HOME", t.TempDir())
	if out, code := runScript(t, asUser(ollamaStartScript), "", bin); code != 0 {
		t.Fatalf("start exited %d: %s", code, out)
	}
	got, _ := os.ReadFile(calls)
	if !strings.Contains(string(got), "start ollama.service") {
		t.Errorf("systemctl was asked %q, want the unit started", got)
	}

	// No sudo, not root, a unit that exists: refused with the code the
	// service turns into "needs passwordless sudo".
	stubs["sudo"] = `exit 1`
	bin = stubBin(t, stubs)
	if out, code := runScript(t, asUser(ollamaStartScript), "", bin); code != 3 {
		t.Errorf("start without sudo exited %d, want 3: %s", code, out)
	}
}

// refuseConnector lives in vbox_test.go: anything reaching a host for a
// request that should have been refused first fails the test.
func TestOllamaRequestsAreRefusedBeforeConnecting(t *testing.T) {
	svc := NewService(refuseConnector{t})
	h := &store.Host{Name: "pi"}
	cases := []struct {
		name string
		call func() error
		want string
	}{
		{"a pull of nothing", func() error { _, err := svc.PullOllamaModel(context.Background(), h, ""); return err }, "not a model name"},
		{"a pull of an option", func() error { _, err := svc.PullOllamaModel(context.Background(), h, "--insecure"); return err }, "not a model name"},
		{"a pull carrying a command", func() error { _, err := svc.PullOllamaModel(context.Background(), h, "llama3; reboot"); return err }, "not a model name"},
		{"a pull with a space", func() error { _, err := svc.PullOllamaModel(context.Background(), h, "llama 3"); return err }, "not a model name"},
		{"a verb nothing offers", func() error { _, err := svc.OllamaAction(context.Background(), h, "serve", "llama3"); return err }, "remove, stop, start, cancel or clear"},
		{"the ollama verb rather than ours", func() error { _, err := svc.OllamaAction(context.Background(), h, "rm", "llama3"); return err }, "an ollama action is"},
		{"removing nothing", func() error { _, err := svc.OllamaAction(context.Background(), h, "remove", ""); return err }, "not a model name"},
		{"stopping a quoted name", func() error { _, err := svc.OllamaAction(context.Background(), h, "stop", `"llama3"`); return err }, "not a model name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want an invalid request", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want it to say %q", err, tc.want)
			}
		})
	}
	if len(OllamaActions) != len(ollamaActionNames) {
		t.Errorf("OllamaActions has %d verbs but the message lists %d", len(OllamaActions), len(ollamaActionNames))
	}
	for _, name := range ollamaActionNames {
		if !OllamaActions[name] {
			t.Errorf("the message offers %q, which is not an action", name)
		}
	}
}

// The names ollama itself accepts all pass, and the shapes that could be an
// option or a second argument do not.
func TestOllamaModelPattern(t *testing.T) {
	good := []string{"llama3.2", "llama3.2:3b", "qwen2.5-coder:7b-instruct-q4_K_M", "library/llama3.2:latest",
		"hf.co/bartowski/Llama-3.2-3B-Instruct-GGUF:Q4_K_M", "registry.example.com:5000/team/model:v1", "a"}
	for _, name := range good {
		if !ollamaModelPattern.MatchString(name) {
			t.Errorf("%q should be a model name", name)
		}
	}
	bad := []string{"", "-rm", "--help", "llama 3", "llama3\n", "llama3'", `llama3"`, "llama3;reboot", "$(reboot)",
		".hidden", strings.Repeat("a", 300)}
	for _, name := range bad {
		if ollamaModelPattern.MatchString(name) {
			t.Errorf("%q should not be a model name", name)
		}
	}
}

func TestParseTable(t *testing.T) {
	lines := []string{
		"NAME               ID              SIZE      PROCESSOR    UNTIL              ",
		"llama3.2:latest    a80c4f17acd5    4.0 GB    100% GPU     4 minutes from now    ",
		"short              0123456789ab    40 MB     100% CPU     Forever",
		"",
	}
	rows := parseTable(lines)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want 2", rows)
	}
	if rows[0]["NAME"] != "llama3.2:latest" || rows[0]["SIZE"] != "4.0 GB" || rows[0]["UNTIL"] != "4 minutes from now" {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[1]["PROCESSOR"] != "100% CPU" || rows[1]["UNTIL"] != "Forever" {
		t.Errorf("row 1 = %+v", rows[1])
	}
	// A column the header does not have is simply not there.
	if _, ok := rows[0]["CONTEXT"]; ok {
		t.Error("a column not in the header appeared in a row")
	}
	// A header alone is an empty table; no lines is no table.
	if rows := parseTable(lines[:1]); len(rows) != 0 {
		t.Errorf("a header alone gave %+v", rows)
	}
	if rows := parseTable(nil); rows != nil {
		t.Errorf("no lines gave %+v", rows)
	}
	// A row shorter than the header is what it is, not a crash.
	if rows := parseTable([]string{"NAME    ID    SIZE", "x"}); rows[0]["NAME"] != "x" || rows[0]["SIZE"] != "" {
		t.Errorf("a short row gave %+v", rows)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[string]int64{
		"4.7 GB": 4_700_000_000,
		"2.0 GB": 2_000_000_000,
		"12 GB":  12_000_000_000,
		"815 MB": 815_000_000,
		"1.2 TB": 1_200_000_000_000,
		"300 B":  300,
		"":       0,
		"big":    0,
		"4.7":    0,
		"x GB":   0,
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%q) = %d, want %d", in, got, want)
		}
	}
}

// What the bar wrote, boiled down: escapes and block characters gone, each
// redraw of a layer replacing the last, the end of the story last.
func TestCleanProgress(t *testing.T) {
	raw := "pulling manifest \r\x1b[?25l\x1b[1A\x1b[K" +
		"pulling manifest \npulling abc123...   0% ▕          ▏    0 B/4.1 GB\r" +
		"pulling manifest \npulling abc123...  42% ▕████      ▏ 1.7 GB/4.1 GB   30 MB/s   1m20s\r" +
		"pulling manifest \npulling abc123... 100% ▕██████████▏ 4.1 GB/4.1 GB\n" +
		"verifying sha256 digest \nwriting manifest \nsuccess \x1b[?25h\n"
	got := cleanProgress(raw)
	want := []string{
		"pulling manifest",
		"pulling abc123... 100% 4.1 GB/4.1 GB",
		"verifying sha256 digest",
		"writing manifest",
		"success",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("cleaned =\n%q\nwant\n%q", got, want)
	}
	if got := lastPercent(got); got != 100 {
		t.Errorf("last percent = %d, want 100", got)
	}
	if got := lastPercent([]string{"pulling manifest"}); got != -1 {
		t.Errorf("percent with no figure = %d, want -1", got)
	}
	// A pull that failed mid-way keeps the figure it got to, and the error
	// after it.
	got = cleanProgress("pulling abc123...  42% ▕████      ▏ 1.7 GB/4.1 GB\rpulling abc123...  43% ▕████      ▏ 1.8 GB/4.1 GB\nError: max retries exceeded\n")
	if len(got) != 2 || got[0] != "pulling abc123... 43% 1.8 GB/4.1 GB" || got[1] != "Error: max retries exceeded" {
		t.Errorf("cleaned = %q", got)
	}
	if lastPercent(got) != 43 {
		t.Errorf("percent = %d, want 43", lastPercent(got))
	}
	if cleanProgress("") != nil {
		t.Error("nothing written should clean to nothing")
	}
}

func TestParseOllamaPullStates(t *testing.T) {
	base := map[string][]string{"pullmodel": {"llama3.2:3b"}}
	for state, want := range map[string]struct {
		state string
		exit  int
	}{
		"running":    {"running", 0},
		"ok":         {"ok", 0},
		"failed:1":   {"failed", 1},
		"failed:143": {"cancelled", 143},
		"failed:130": {"cancelled", 130},
		"cancelled":  {"cancelled", 0},
		"lost":       {"lost", 0},
	} {
		got := map[string][]string{"pull": {state}}
		for k, v := range base {
			got[k] = v
		}
		pull := parseOllamaPull(got)
		if pull == nil || pull.State != want.state || pull.Exit != want.exit || pull.Model != "llama3.2:3b" {
			t.Errorf("%q parsed as %+v, want %+v", state, pull, want)
		}
	}
	if parseOllamaPull(map[string][]string{"pull": {""}}) != nil {
		t.Error("no state should be no pull")
	}
	if pull := parseOllamaPull(map[string][]string{"pull": {"running"}}); pull.Percent != -1 {
		t.Errorf("a pull with no log has percent %d, want -1", pull.Percent)
	}
}

// The unit's state is read from what systemctl shows, and a unit that is not
// there at all reads as none — a server started by hand has no unit.
func TestParseOllamaHostUnit(t *testing.T) {
	out := "@@have\n/usr/local/bin/ollama\n@@user\npi\n@@version\nollama version is 0.6.2\n@@unit\nLoadState=loaded\nActiveState=active\n@@list\nNAME  ID  SIZE  MODIFIED\n"
	if host := parseOllamaHost(out); host.Unit != "active" || !host.Running || len(host.Models) != 0 {
		t.Errorf("host = %+v", host)
	}
	out = "@@have\n/usr/local/bin/ollama\n@@user\npi\n@@version\n\n@@unit\nLoadState=not-found\nActiveState=inactive\n@@listerr\nError: something\n"
	if host := parseOllamaHost(out); host.Unit != "" || host.Running || host.Error != "something" {
		t.Errorf("host = %+v", host)
	}
}

func TestOllamaFailureFallsBack(t *testing.T) {
	if got := ollamaFailure("", "odd\nlast line\n", "remove", "x").Error(); got != "last line" {
		t.Errorf("fallback = %q, want the last line", got)
	}
	if got := ollamaFailure("", "", "remove", "x").Error(); got != "could not remove x" {
		t.Errorf("empty output = %q", got)
	}
	if got := ollamaFailure("", "", "start", "").Error(); got != "could not start" {
		t.Errorf("empty output without a model = %q", got)
	}
}

// waitForPull waits for the detached runner to leave a state other than
// running, and returns it.
func waitForPull(t *testing.T, dir, want string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		b, err := os.ReadFile(filepath.Join(dir, "pull.state"))
		state := strings.TrimSpace(string(b))
		if err == nil && state != "running" {
			return state
		}
		if time.Now().After(deadline) {
			t.Fatalf("the pull never left running (want %s)", want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func writeState(t *testing.T, dir, state, model string, pid int) {
	t.Helper()
	for name, body := range map[string]string{
		"pull.state": state + "\n",
		"pull.model": model + "\n",
		"pull.pid":   strconv.Itoa(pid) + "\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readAll(t *testing.T, path string) string {
	t.Helper()
	b, _ := os.ReadFile(path)
	return string(b)
}

// quoteForTest is sshx.Quote by another name, so a test reads as a test.
func quoteForTest(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
