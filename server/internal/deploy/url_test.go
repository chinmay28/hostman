package deploy

import (
	"testing"

	"github.com/chinmay28/deployer/server/internal/store"
)

func TestInstallationURL(t *testing.T) {
	cases := []struct {
		name string
		in   store.Installation
		want string
	}{{
		// The health path is where the app reports on itself; a person tapping
		// "Open" wants the front door.
		name: "health check URL, without its health path",
		in: store.Installation{
			HealthType:   store.HealthHTTP,
			HealthTarget: "http://{{host}}:8899/api/health",
			HostAddress:  "nakedpi.local",
		},
		want: "http://nakedpi.local:8899/",
	}, {
		name: "port supplied as a parameter and used by the check",
		in: store.Installation{
			HealthType:   store.HealthHTTP,
			HealthTarget: "http://{{host}}:{{port}}/",
			HostAddress:  "192.168.2.123",
			Params:       map[string]string{"port": "8787"},
		},
		want: "http://192.168.2.123:8787/",
	}, {
		name: "https keeps its scheme",
		in: store.Installation{
			HealthType:   store.HealthHTTP,
			HealthTarget: "https://{{host}}/healthz",
			HostAddress:  "photos.example.com",
		},
		want: "https://photos.example.com/",
	}, {
		// A browser would use 443 anyway, and nobody types it.
		name: "the scheme's own port is left out",
		in: store.Installation{
			HealthType:   store.HealthHTTP,
			HealthTarget: "https://{{host}}:443/",
			HostAddress:  "photos.example.com",
		},
		want: "https://photos.example.com/",
	}, {
		// One health check, written once, and each host's card links to that
		// host.
		name: "a host-based check URL, one pair of braces",
		in: store.Installation{
			HealthType:   store.HealthHTTP,
			HealthTarget: "http://{HOST}.local:8123/",
			HostAddress:  "nakedpi.local",
		},
		want: "http://nakedpi.local:8123/",
	}, {
		// No URL was ever written down, but the port parameter still says where
		// the app listens.
		name: "systemd check with a port parameter",
		in: store.Installation{
			HealthType:   store.HealthSystemd,
			HealthTarget: "countroster.service",
			HostAddress:  "nakedpi.local",
			Params:       map[string]string{"port": "3000"},
		},
		want: "http://nakedpi.local:3000/",
	}, {
		name: "the lowest port is the one offered",
		in: store.Installation{
			HealthType:  store.HealthNone,
			HostAddress: "pi",
			Params:      map[string]string{"http_port": "8080", "httpsPort": "8443"},
		},
		want: "http://pi:8080/",
	}, {
		name: "port 443 without a URL still means https",
		in: store.Installation{
			HealthType:  store.HealthNone,
			HostAddress: "pi",
			Params:      map[string]string{"port": "443"},
		},
		want: "https://pi/",
	}, {
		name: "an IPv6 address is bracketed",
		in: store.Installation{
			HealthType:  store.HealthNone,
			HostAddress: "fd00::1",
			Params:      map[string]string{"port": "8080"},
		},
		want: "http://[fd00::1]:8080/",
	}, {
		name: "a placeholder HostMan cannot fill offers no link",
		in: store.Installation{
			HealthType:   store.HealthHTTP,
			HealthTarget: "http://{{host}}:{{port}}/",
			HostAddress:  "pi.local",
		},
		want: "",
	}, {
		// The check names a unit, not an address, and no parameter says a port.
		name: "nothing declared, nothing offered",
		in: store.Installation{
			HealthType:   store.HealthSystemd,
			HealthTarget: "backup.service",
			HostAddress:  "pi.local",
			Params:       map[string]string{"ref": "main"},
		},
		want: "",
	}, {
		name: "a health check that is not HTTP at all",
		in: store.Installation{
			HealthType:   store.HealthHTTP,
			HealthTarget: "ftp://{{host}}:21/",
			HostAddress:  "pi.local",
		},
		want: "",
	}, {
		// The ports are known, but not the machine to reach them on.
		name: "no host address",
		in: store.Installation{
			HealthType: store.HealthNone,
			Params:     map[string]string{"port": "8080"},
		},
		want: "",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := InstallationURL(&c.in); got != c.want {
				t.Errorf("InstallationURL = %q, want %q", got, c.want)
			}
		})
	}
}

// HostMan's own installation is the one every user has, and its link is the
// one that has to work.
func TestInstallationURLForSelfUpdateApp(t *testing.T) {
	in := &store.Installation{
		HealthType:   store.HealthHTTP,
		HealthTarget: "http://{{host}}:8899/api/health",
		HostAddress:  "127.0.0.1",
		Params:       map[string]string{"ref": "main"},
	}
	if got, want := InstallationURL(in), "http://127.0.0.1:8899/"; got != want {
		t.Errorf("InstallationURL = %q, want %q", got, want)
	}
}

func TestInstallationURLNilSafe(t *testing.T) {
	if got := InstallationURL(nil); got != "" {
		t.Errorf("InstallationURL(nil) = %q, want empty", got)
	}
}
