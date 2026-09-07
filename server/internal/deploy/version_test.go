package deploy

import (
	"testing"

	"github.com/chinmay28/deployer/server/internal/store"
)

func TestInstallationVersion(t *testing.T) {
	cases := []struct {
		name string
		in   store.Installation
		want string
	}{{
		name: "a version parameter says it outright",
		in:   store.Installation{Params: map[string]string{"version": "1.4.0"}},
		want: "1.4.0",
	}, {
		name: "a git ref is the version a build came from",
		in:   store.Installation{Params: map[string]string{"ref": "v1.2.3"}},
		want: "v1.2.3",
	}, {
		name: "prefixed and camelCase names count too",
		in:   store.Installation{Params: map[string]string{"port": "8080", "imageTag": "2026.08.1"}},
		want: "2026.08.1",
	}, {
		name: "the more explicit parameter wins",
		in: store.Installation{Params: map[string]string{
			"ref": "main", "app_version": "3.1", "commit": "b0f4c1a",
		}},
		want: "3.1",
	}, {
		name: "the plainer name wins among equals",
		in:   store.Installation{Params: map[string]string{"version": "2.0", "chart_version": "0.9"}},
		want: "2.0",
	}, {
		name: "a branch name is a version when it is what was deployed",
		in:   store.Installation{Params: map[string]string{"ref": "main"}},
		want: "main",
	}, {
		name: "a full commit hash is shown the way git shows one",
		in:   store.Installation{Params: map[string]string{"ref": "9f2b1c4d5e6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c"}},
		want: "9f2b1c4",
	}, {
		name: "a short hash is left as it was written",
		in:   store.Installation{Params: map[string]string{"commit": "b0f4c1a"}},
		want: "b0f4c1a",
	}, {
		name: "an empty parameter says nothing",
		in:   store.Installation{Params: map[string]string{"version": "  "}},
		want: "",
	}, {
		name: "a value that is not a version is not shown as one",
		in: store.Installation{Params: map[string]string{
			"release_notes": "the one where deploys got faster",
		}},
		want: "",
	}, {
		name: "names that only look like versions are left alone",
		in: store.Installation{Params: map[string]string{
			"conversion": "yes", "reftype": "git", "port": "8899",
		}},
		want: "",
	}, {
		name: "an app that installs whatever is current names no version",
		in: store.Installation{
			HealthType:   store.HealthHTTP,
			HealthTarget: "http://{{host}}:8787/",
			Params:       map[string]string{"port": "8787"},
		},
		want: "",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := InstallationVersion(&c.in); got != c.want {
				t.Errorf("InstallationVersion = %q, want %q", got, c.want)
			}
		})
	}
}

// The app HostMan creates for itself takes the ref it builds from, so its own
// card is the first place a version shows up.
func TestInstallationVersionForSelfUpdateApp(t *testing.T) {
	in := &store.Installation{
		HealthType:   store.HealthHTTP,
		HealthTarget: "http://{{host}}:8899/api/health",
		HostAddress:  "127.0.0.1",
		Params:       map[string]string{"ref": "v1.0.42"},
	}
	if got, want := InstallationVersion(in), "v1.0.42"; got != want {
		t.Errorf("InstallationVersion = %q, want %q", got, want)
	}
}

func TestInstallationVersionNilSafe(t *testing.T) {
	if got := InstallationVersion(nil); got != "" {
		t.Errorf("InstallationVersion(nil) = %q, want empty", got)
	}
}
