package deploy

import (
	"fmt"
	"testing"

	"github.com/chinmay28/deployer/server/internal/store"
)

func TestInstallationPorts(t *testing.T) {
	cases := []struct {
		name string
		in   store.Installation
		want []int
	}{{
		name: "port written into the health check URL",
		in: store.Installation{
			HealthType:   store.HealthHTTP,
			HealthTarget: "http://{{host}}:8899/api/health",
			HostAddress:  "nakedpi.local",
		},
		want: []int{8899},
	}, {
		name: "port written into a host-based check URL",
		in: store.Installation{
			HealthType:   store.HealthHTTP,
			HealthTarget: "http://{HOST}.local:8123/",
			HostAddress:  "pi5.local",
		},
		want: []int{8123},
	}, {
		name: "port supplied as a parameter and used by the check",
		in: store.Installation{
			HealthType:   store.HealthHTTP,
			HealthTarget: "http://{{host}}:{{port}}/",
			HostAddress:  "192.168.2.123",
			Params:       map[string]string{"port": "8787"},
		},
		want: []int{8787},
	}, {
		// A systemd check says nothing about ports, but the parameters still do.
		name: "systemd check with a port parameter",
		in: store.Installation{
			HealthType:   store.HealthSystemd,
			HealthTarget: "countroster.service",
			Params:       map[string]string{"port": "3000"},
		},
		want: []int{3000},
	}, {
		name: "several ports, deduplicated and ordered",
		in: store.Installation{
			HealthType:   store.HealthHTTP,
			HealthTarget: "http://{{host}}:{{http_port}}/healthz",
			HostAddress:  "pi",
			Params:       map[string]string{"http_port": "8080", "httpsPort": "8443", "PORT": "8080"},
		},
		want: []int{8080, 8443},
	}, {
		name: "no port in the URL means the scheme's default",
		in: store.Installation{
			HealthType:   store.HealthHTTP,
			HealthTarget: "https://{{host}}/",
			HostAddress:  "pi.local",
		},
		want: []int{443},
	}, {
		name: "a placeholder HostMan cannot fill claims no port",
		in: store.Installation{
			HealthType:   store.HealthHTTP,
			HealthTarget: "http://{{host}}:{{port}}/",
			HostAddress:  "pi.local",
		},
		want: nil,
	}, {
		name: "numbers that are not ports are left alone",
		in: store.Installation{
			Params: map[string]string{
				"port":     "not-a-number",
				"support":  "yes",
				"backport": "99999", // matches the name, outside the range
				"timeout":  "30",
			},
		},
		want: nil,
	}, {
		name: "nothing declared, nothing shown",
		in:   store.Installation{HealthType: store.HealthNone, Params: map[string]string{"ref": "main"}},
		want: nil,
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := InstallationPorts(&c.in)
			if fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Errorf("InstallationPorts = %v, want %v", got, c.want)
			}
		})
	}
}

// The app HostMan creates for itself is the one every install has, so its
// port is the one users will see first.
func TestInstallationPortsForSelfUpdateApp(t *testing.T) {
	in := &store.Installation{
		HealthType:   store.HealthHTTP,
		HealthTarget: "http://{{host}}:8899/api/health",
		HostAddress:  "127.0.0.1",
		Params:       map[string]string{"ref": "main"},
	}
	got := InstallationPorts(in)
	if len(got) != 1 || got[0] != 8899 {
		t.Errorf("InstallationPorts = %v, want [8899]", got)
	}
}

func TestInstallationPortsNilSafe(t *testing.T) {
	if got := InstallationPorts(nil); got != nil {
		t.Errorf("InstallationPorts(nil) = %v, want nil", got)
	}
}
