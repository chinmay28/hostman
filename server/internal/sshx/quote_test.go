package sshx

import (
	"os/exec"
	"strings"
	"testing"
)

// Quote guards the setup scripts: a username or key that reaches a host's shell
// unquoted is command injection. The only honest check is a real shell, so this
// asks /bin/sh what it makes of the quoted form.
func TestQuoteSurvivesARealShell(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no shell available")
	}
	values := []string{
		"pi",
		"ssh-ed25519 AAAAC3Nza hostman",
		`it's mine`,
		"a b\tc",
		"$(touch /tmp/hostman-injection-test)",
		"`id`",
		"$HOME",
		`"; rm -rf / ;"`,
		"end'; echo pwned; '",
		"back\\slash",
		"*",
	}
	for _, want := range values {
		out, err := exec.Command("sh", "-c", "printf %s "+Quote(want)).Output()
		if err != nil {
			t.Fatalf("sh on %q: %v", want, err)
		}
		if string(out) != want {
			t.Errorf("Quote(%q) round-tripped as %q", want, out)
		}
	}
}

func TestQuoteWrapsTheWholeValue(t *testing.T) {
	got := Quote("pi")
	if !strings.HasPrefix(got, "'") || !strings.HasSuffix(got, "'") {
		t.Errorf("Quote(%q) = %q, want it wrapped in single quotes", "pi", got)
	}
}
