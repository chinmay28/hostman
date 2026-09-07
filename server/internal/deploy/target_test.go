package deploy

import (
	"strings"
	"testing"
)

func TestRenderTargetPlaceholderForms(t *testing.T) {
	values := map[string]string{
		VarHost:     "pi5.local",
		VarHostName: "pi5",
		VarUser:     "chinmay",
		"port":      "8123",
	}
	cases := []struct {
		name     string
		template string
		want     string
	}{{
		name:     "the two-brace form install commands use",
		template: "http://{{host}}:{{port}}/",
		want:     "http://pi5.local:8123/",
	}, {
		name:     "one pair of braces, which is what a phone keyboard wants",
		template: "http://{HOST}:8123/",
		want:     "http://pi5.local:8123/",
	}, {
		// The point of the whole thing: one URL on the app, and every host it
		// is deployed to checks itself.
		name:     "a name the check completes itself",
		template: "http://{HOST}.local:8123/",
		want:     "http://pi5.local:8123/",
	}, {
		name:     "case is not something to have to remember",
		template: "http://{host}:{Port}/healthz",
		want:     "http://pi5.local:8123/healthz",
	}, {
		name:     "spaces inside the braces",
		template: "http://{ HOST }:8123/",
		want:     "http://pi5.local:8123/",
	}, {
		name:     "the host's name in HostMan, where that is what is wanted",
		template: "http://{HOSTNAME}.lan:8123/",
		want:     "http://pi5.lan:8123/",
	}, {
		name:     "a target with nothing to substitute is left alone",
		template: "http://pi5.local:8123/",
		want:     "http://pi5.local:8123/",
	}, {
		name:     "several placeholders in one target",
		template: "http://{HOST}:{PORT}/u/{USER}",
		want:     "http://pi5.local:8123/u/chinmay",
	}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := RenderTarget(c.template, values, false)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("RenderTarget(%q) = %q, want %q", c.template, got, c.want)
			}
		})
	}
}

// A check that writes ".local" itself has to work whether HostMan reaches the
// host as "pi5" or as "pi5.local" — the person writing the URL should not have
// to know which, and "pi5.local.local" answers nothing.
func TestRenderTargetHostSuffix(t *testing.T) {
	cases := []struct {
		address  string
		template string
		want     string
	}{
		{"pi5.local", "http://{HOST}.local:8123/", "http://pi5.local:8123/"},
		{"pi5", "http://{HOST}.local:8123/", "http://pi5.local:8123/"},
		{"pi5.local", "http://{HOST}:8123/", "http://pi5.local:8123/"},
		{"photos.example.com", "https://{HOST}/healthz", "https://photos.example.com/healthz"},
		{"photos.example.com", "https://{HOST}.example.com/", "https://photos.example.com/"},
		// A dot that is punctuation rather than a domain suffix takes nothing
		// away from the address.
		{"pi5.local", "http://{HOST}:8123/x.", "http://pi5.local:8123/x."},
		// An IP has no labels to take. The URL that comes out is wrong, which
		// is better than dialling 192.local and reporting it as the app's fault.
		{"192.168.2.123", "http://{HOST}.local:8123/", "http://192.168.2.123.local:8123/"},
		{"192.168.2.123", "http://{HOST}:8123/", "http://192.168.2.123:8123/"},
	}
	for _, c := range cases {
		values := map[string]string{VarHost: c.address}
		got, err := RenderTarget(c.template, values, false)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("RenderTarget(%q) with host %q = %q, want %q", c.template, c.address, got, c.want)
		}
	}
}

func TestRenderTargetRejectsUnknownPlaceholders(t *testing.T) {
	_, err := RenderTarget("http://{HOST}:{PORT}/{TOKEN}", map[string]string{VarHost: "pi5.local"}, false)
	if err == nil {
		t.Fatal("expected an error naming what HostMan cannot fill in")
	}
	for _, want := range []string{"PORT", "TOKEN"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}

// A systemd unit ends up in a command, so it is quoted like any other value.
func TestRenderTargetQuotesUnitNames(t *testing.T) {
	got, err := RenderTarget("{HOSTNAME}-sand.service", map[string]string{VarHostName: "pi 5"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if want := `'pi 5'-sand.service`; got != want {
		t.Errorf("RenderTarget = %s, want %s", got, want)
	}
}

// Shell syntax is not a placeholder. Install commands go through Render, which
// only ever sees two braces, so `awk '{print}'` survives being an app.
func TestRenderLeavesSingleBracesAlone(t *testing.T) {
	template := `install --port {{port}} | awk '{print}' --set ${HOME}`
	got, err := Render(template, map[string]string{"port": "8123"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := `install --port 8123 | awk '{print}' --set ${HOME}`; got != want {
		t.Errorf("Render = %s, want %s", got, want)
	}
}

func TestHostLabel(t *testing.T) {
	cases := map[string]string{
		"pi5.local":          "pi5",
		"pi5":                "pi5",
		"photos.example.com": "photos",
		"192.168.2.123":      "192.168.2.123",
		"fd00::1":            "fd00::1",
		"":                   "",
	}
	for address, want := range cases {
		if got := hostLabel(address); got != want {
			t.Errorf("hostLabel(%q) = %q, want %q", address, got, want)
		}
	}
}
