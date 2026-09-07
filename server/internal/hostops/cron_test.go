package hostops

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chinmay28/deployer/server/internal/store"
)

// fakeCron is a stand-in for the crontab command, doing the parts these scripts
// use: listing a user's crontab, refusing when there isn't one, and installing a
// file after refusing to parse it. Real cron is not on every build machine, and
// the half worth testing here is what HostMan does with its answers.
const fakeCron = `#!/bin/sh
dir=$FAKE_CRON_DIR
user=self
mode=install
file=
while [ $# -gt 0 ]; do
  case "$1" in
    -l) mode=list;;
    -u) user=$2; shift;;
    --) ;;
    *) file=$1;;
  esac
  shift
done
target="$dir/$user"
if [ "$mode" = list ]; then
  [ -f "$target" ] || { echo "no crontab for $user" >&2; exit 1; }
  echo "warning: this cron is chatty on stderr" >&2
  cat "$target"
  exit 0
fi
if grep -q BADLINE "$file"; then
  echo '"$file":1: bad minute' >&2
  echo "errors in crontab file, can't install." >&2
  exit 1
fi
cp "$file" "$target"
`

// withFakeCron puts the stand-in on PATH and returns the directory it keeps
// crontabs in.
func withFakeCron(t *testing.T) (store string, path []string) {
	t.Helper()
	bin, store := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "crontab"), []byte(fakeCron), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_CRON_DIR", store)
	return store, []string{bin}
}

func readCron(t *testing.T, user string, path []string) (*Crontab, int) {
	t.Helper()
	out, code := runScript(t, asUser(readCronScript, user), "", path...)
	if code != 0 {
		return nil, code
	}
	name := user
	if name == "" {
		name = "self"
	}
	cron, err := parseCrontab(out, name)
	if err != nil {
		t.Fatalf("parseCrontab: %v", err)
	}
	return cron, 0
}

func writeCron(t *testing.T, user, content string, path []string) int {
	t.Helper()
	body := base64.StdEncoding.EncodeToString([]byte(ensureFinalNewline(content)))
	_, code := runScript(t, asUser(writeCronScript, user), body, path...)
	return code
}

// A user who has never had a crontab gets an empty one to start from, not an
// error — that is the normal way to add a first job.
func TestReadCrontabWhenThereIsNone(t *testing.T) {
	_, path := withFakeCron(t)
	cron, code := readCron(t, "", path)
	if code != 0 {
		t.Fatalf("read exited %d, want the missing crontab treated as empty", code)
	}
	if cron.Exists || cron.Content != "" {
		t.Errorf("cron = %+v, want an empty crontab that does not exist yet", cron)
	}
}

func TestCrontabRoundTrip(t *testing.T) {
	dir, path := withFakeCron(t)
	const content = "# back up the photos\n0 3 * * * /usr/local/bin/backup.sh\n"

	if code := writeCron(t, "", content, path); code != 0 {
		t.Fatalf("write exited %d", code)
	}
	if onDisk, err := os.ReadFile(filepath.Join(dir, "self")); err != nil || string(onDisk) != content {
		t.Fatalf("installed %q (%v), want %q", onDisk, err, content)
	}

	cron, code := readCron(t, "", path)
	if code != 0 {
		t.Fatalf("read exited %d", code)
	}
	if !cron.Exists {
		t.Error("exists = false, want true once there is one")
	}
	if cron.Content != content {
		t.Errorf("content = %q, want %q — and nothing cron said on stderr", cron.Content, content)
	}
}

// cron is the parser that matters: content it refuses must come back as its own
// complaint, with the existing crontab left alone.
func TestWriteCrontabRejectedByCron(t *testing.T) {
	dir, path := withFakeCron(t)
	if code := writeCron(t, "", "0 3 * * * ok.sh\n", path); code != 0 {
		t.Fatalf("write exited %d", code)
	}
	out, code := runScript(t, asUser(writeCronScript, ""), base64.StdEncoding.EncodeToString([]byte("BADLINE\n")), path...)
	if code == 0 {
		t.Fatalf("installing a broken crontab succeeded (%q)", out)
	}
	if body, _ := os.ReadFile(filepath.Join(dir, "self")); string(body) != "0 3 * * * ok.sh\n" {
		t.Errorf("crontab is now %q, want the working one still in place", body)
	}
}

func TestCrontabForAnotherUser(t *testing.T) {
	dir, path := withFakeCron(t)
	if code := writeCron(t, "root", "@reboot /usr/local/bin/warm-cache\n", path); code != 0 {
		t.Fatalf("write exited %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "root")); err != nil {
		t.Fatalf("root's crontab was not written: %v", err)
	}
	cron, code := readCron(t, "root", path)
	if code != 0 {
		t.Fatalf("read exited %d", code)
	}
	if cron.User != "root" || !strings.Contains(cron.Content, "warm-cache") {
		t.Errorf("cron = %+v, want root's", cron)
	}
	// The connecting user's own crontab is a different one.
	if own, _ := readCron(t, "", path); own.Exists {
		t.Error("writing root's crontab should not have created one for the SSH user")
	}
}

func TestCrontabContentIsNotMangled(t *testing.T) {
	_, path := withFakeCron(t)
	// Percent signs are cron's own escape, quotes and backslashes are common in
	// commands, and a line that looks like a section marker must stay a line.
	const content = "MAILTO=\"me@example.com\"\n" +
		"0 2 * * * /bin/sh -c 'date +\\%F >> /var/log/x.log' # 100%% sure\n" +
		"@@not-a-marker\n"
	if code := writeCron(t, "", content, path); code != 0 {
		t.Fatalf("write exited %d", code)
	}
	cron, code := readCron(t, "", path)
	if code != 0 {
		t.Fatalf("read exited %d", code)
	}
	if cron.Content != content {
		t.Errorf("content = %q, want %q", cron.Content, content)
	}
}

func TestCronTarget(t *testing.T) {
	h := &store.Host{Username: "pi"}
	cases := []struct {
		user       string
		wantTarget string
		wantOwn    bool
		wantErr    bool
	}{
		{user: "", wantTarget: "", wantOwn: true},
		{user: "pi", wantTarget: "", wantOwn: true}, // their own, no sudo needed
		{user: "  pi  ", wantTarget: "", wantOwn: true},
		{user: "root", wantTarget: "root"},
		{user: "www-data", wantTarget: "www-data"},
		{user: "no spaces", wantErr: true},
		{user: "-dash-first", wantErr: true},
		{user: "root; rm -rf /", wantErr: true},
		{user: strings.Repeat("u", 40), wantErr: true},
	}
	for _, tc := range cases {
		target, own, err := cronTarget(h, tc.user)
		if tc.wantErr {
			if err == nil {
				t.Errorf("cronTarget(%q) = %q, want an error", tc.user, target)
			}
			continue
		}
		if err != nil {
			t.Errorf("cronTarget(%q): %v", tc.user, err)
			continue
		}
		if target != tc.wantTarget || own != tc.wantOwn {
			t.Errorf("cronTarget(%q) = (%q, %v), want (%q, %v)", tc.user, target, own, tc.wantTarget, tc.wantOwn)
		}
	}
}

func TestEnsureFinalNewline(t *testing.T) {
	cases := map[string]string{
		"":                "",
		"a":               "a\n",
		"a\n":             "a\n",
		"a\n\n":           "a\n\n",
		"line one\nline2": "line one\nline2\n",
	}
	for in, want := range cases {
		if got := ensureFinalNewline(in); got != want {
			t.Errorf("ensureFinalNewline(%q) = %q, want %q", in, got, want)
		}
	}
}
