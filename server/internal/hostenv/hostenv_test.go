package hostenv

import (
	"net"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

// restore puts back the globals Apply replaces.
func restore(t *testing.T) {
	r, loc := net.DefaultResolver, time.Local
	t.Cleanup(func() { net.DefaultResolver, time.Local = r, loc })
}

func TestApplyWithNothingSetChangesNothing(t *testing.T) {
	restore(t)
	before, loc := net.DefaultResolver, time.Local
	applied, err := Apply(env(nil))
	if err != nil || len(applied) != 0 {
		t.Fatalf("Apply = %q, %v; want nothing", applied, err)
	}
	if net.DefaultResolver != before || time.Local != loc {
		t.Fatal("Apply changed a global with no variable set")
	}
}

func TestApplyInstallsResolverAndZone(t *testing.T) {
	restore(t)
	before := net.DefaultResolver
	applied, err := Apply(env(map[string]string{
		EnvDNS: "1.1.1.1",
		EnvTZ:  "Asia/Kolkata",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(applied, []string{"dns", "tz"}) {
		t.Fatalf("applied %q", applied)
	}
	if net.DefaultResolver == before || !net.DefaultResolver.PreferGo {
		t.Fatal("no pure-Go resolver was installed")
	}
	if time.Local.String() != "Asia/Kolkata" {
		t.Fatalf("time.Local = %s", time.Local)
	}
}

func TestApplyKeepsWhatWorksBesideABadZone(t *testing.T) {
	restore(t)
	loc := time.Local
	applied, err := Apply(env(map[string]string{
		EnvDNSFile: filepath.Join(t.TempDir(), "dns"),
		EnvTZ:      "Nowhere/Atlantis",
	}))
	if err == nil {
		t.Fatal("a zone that does not exist was accepted")
	}
	if !reflect.DeepEqual(applied, []string{"dns"}) {
		t.Fatalf("applied %q, want dns alone", applied)
	}
	if time.Local != loc {
		t.Fatal("time.Local changed for a bad zone")
	}
}

func TestDNSSourcePrefersTheFile(t *testing.T) {
	if dnsSource(env(nil)) != nil {
		t.Fatal("a source with nothing set")
	}
	src := dnsSource(env(map[string]string{
		EnvDNS:     "9.9.9.9",
		EnvDNSFile: filepath.Join(t.TempDir(), "absent"),
	}))
	if got := src(); !reflect.DeepEqual(got, []string{"9.9.9.9:53"}) {
		t.Fatalf("with the file missing: got %q, want HOSTMAN_DNS", got)
	}
}
