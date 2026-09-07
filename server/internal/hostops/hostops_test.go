package hostops

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The scripts in this package take everything the user typed as positional
// arguments, so the only way a path could become a command is if the quoting
// were wrong. That is worth proving against a real shell rather than by
// inspecting the string, so these run the generated command through /bin/sh.
func TestArgumentsSurviveTheShell(t *testing.T) {
	const echoArgs = `printf '%s\n' "$1" "$2"`
	nasty := []string{
		"/etc/hosts",
		"/tmp/a b c",
		"/tmp/it's here",
		"/tmp/$(touch pwned)",
		"/tmp/`touch pwned`",
		"/tmp/x; rm -rf /",
		"/tmp/*",
		`/tmp/back\slash`,
		"/tmp/quote\"double",
		"/tmp/tab\there",
	}
	for _, arg := range nasty {
		t.Run(arg, func(t *testing.T) {
			out, err := exec.Command("/bin/sh", "-c", asUser(echoArgs, arg, "second")).Output()
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			want := arg + "\nsecond\n"
			if string(out) != want {
				t.Errorf("shell saw %q, want %q", out, want)
			}
		})
	}
}

// $0 is set so a shell error message names HostMan rather than "sh".
func TestScriptArgumentsStartAtOne(t *testing.T) {
	out, err := exec.Command("/bin/sh", "-c", asUser(`printf '%s\n' "$0" "$#"`, "/tmp", "x")).Output()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := string(out); got != "deployer\n2\n" {
		t.Errorf("got %q, want the script to see $0=deployer and two arguments", got)
	}
}

// elevate must run the same command either way: as root where sudo is there for
// the taking, and as the connecting user where it is not.
func TestElevateFallsBackWithoutSudo(t *testing.T) {
	cmd := elevate("printf hello\n", "/etc")
	plain := asUser("printf hello\n", "/etc")
	if strings.Count(cmd, plain) != 2 {
		t.Fatalf("both branches should run the same command, got %q", cmd)
	}
	if !strings.HasPrefix(cmd, "if sudo -n true") {
		t.Errorf("elevate should ask the host whether sudo works, got %q", cmd)
	}

	// With a PATH holding nothing but sh, the `if` fails and the else branch
	// still runs — which is the whole point of having one.
	bin := t.TempDir()
	if err := os.Symlink("/bin/sh", filepath.Join(bin, "sh")); err != nil {
		t.Fatal(err)
	}
	run := exec.Command("/bin/sh", "-c", elevate(`printf '%s\n' "$1"`, "/etc"))
	run.Env = []string{"PATH=" + bin}
	out, err := run.Output()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "/etc" {
		t.Errorf("without sudo the command ran as %q, want it to still run", got)
	}
}

// A refusal by HostMan and a refusal by the host are different problems with
// different fixes, and the API turns them into different status codes. That
// only works while the sentinel survives being returned as an error.
func TestInvalidIsRecognisable(t *testing.T) {
	err := invalid("that is not a valid %s", "user name")
	if !errors.Is(err, ErrInvalid) {
		t.Error("errors.Is should recognise an invalid-request error")
	}
	if err.Error() != "that is not a valid user name" {
		t.Errorf("message = %q, want it left to speak for itself", err)
	}
	if errors.Is(errors.New("the host said no"), ErrInvalid) {
		t.Error("an ordinary error should not pass for an invalid request")
	}
	if _, err := CleanPath("etc/hosts"); !errors.Is(err, ErrInvalid) {
		t.Errorf("CleanPath err = %v, want it marked as an invalid request", err)
	}
}

func TestSectionsKeepsLinesIntact(t *testing.T) {
	out := "@@path\n/etc\n@@entries\nf\tf\t1\t2\t644\troot\troot\t\t  spaced  \n@@user\nroot\n"
	got := sections(out)
	if len(got["entries"]) != 1 || !strings.HasSuffix(got["entries"][0], "  spaced  ") {
		t.Errorf("entries = %q, want the trailing spaces of a name left alone", got["entries"])
	}
	if first(got["path"]) != "/etc" || first(got["user"]) != "root" {
		t.Errorf("sections = %v, want path and user", got)
	}
	// Anything before the first marker belongs to no section.
	if len(sections("noise\n@@a\nb\n")["a"]) != 1 {
		t.Error("output before the first marker should be ignored")
	}
}

func TestParseEntry(t *testing.T) {
	cases := []struct {
		name string
		line string
		want Entry
	}{
		{
			name: "file",
			line: "f\tf\t1024\t1712345678.1234567890\t644\tpi\tusers\t\thosts",
			want: Entry{Name: "hosts", Type: "file", Size: 1024, Mode: "644", Owner: "pi", Group: "users"},
		},
		{
			name: "directory",
			line: "d\td\t4096\t1712345678\t755\troot\troot\t\tsystemd",
			want: Entry{Name: "systemd", Type: "dir", Size: 4096, Mode: "755", Owner: "root", Group: "root"},
		},
		{
			name: "symlink to a directory",
			line: "l\td\t7\t1712345678\t777\troot\troot\t/usr/share\tshare",
			want: Entry{Name: "share", Type: "link", LinkType: "dir", Target: "/usr/share", Size: 7, Mode: "777", Owner: "root", Group: "root"},
		},
		{
			name: "broken symlink",
			line: "l\tN\t9\t1712345678\t777\troot\troot\t/gone\tdangling",
			want: Entry{Name: "dangling", Type: "link", LinkType: "broken", Target: "/gone", Size: 9, Mode: "777", Owner: "root", Group: "root"},
		},
		{
			name: "a name with a tab in it keeps the tab",
			line: "f\tf\t0\t1712345678\t644\troot\troot\t\tone\ttwo",
			want: Entry{Name: "one\ttwo", Type: "file", Mode: "644", Owner: "root", Group: "root"},
		},
		{
			name: "a socket is neither file nor directory",
			line: "s\ts\t0\t1712345678\t755\troot\troot\t\tdocker.sock",
			want: Entry{Name: "docker.sock", Type: "other", Mode: "755", Owner: "root", Group: "root"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseEntry(tc.line)
			if !ok {
				t.Fatalf("parseEntry(%q) refused the line", tc.line)
			}
			tc.want.ModifiedAt = got.ModifiedAt // checked separately
			if got != tc.want {
				t.Errorf("entry = %+v, want %+v", got, tc.want)
			}
			if want := time.Unix(1712345678, 0).UTC(); got.ModifiedAt != want {
				t.Errorf("modifiedAt = %v, want %v", got.ModifiedAt, want)
			}
		})
	}
}

func TestParseEntryRejectsNonsense(t *testing.T) {
	for _, line := range []string{"", "f\tf\t1", "\t\t\t\t\t\t\t\t", "not a line at all"} {
		if _, ok := parseEntry(line); ok {
			t.Errorf("parseEntry(%q) = ok, want it dropped", line)
		}
	}
}

func TestEntryIsDir(t *testing.T) {
	cases := []struct {
		entry Entry
		want  bool
	}{
		{Entry{Type: "dir"}, true},
		{Entry{Type: "link", LinkType: "dir"}, true},
		{Entry{Type: "link", LinkType: "file"}, false},
		{Entry{Type: "link", LinkType: "broken"}, false},
		{Entry{Type: "file"}, false},
	}
	for _, tc := range cases {
		if got := tc.entry.IsDir(); got != tc.want {
			t.Errorf("%+v.IsDir() = %v, want %v", tc.entry, got, tc.want)
		}
	}
}

func TestCleanPath(t *testing.T) {
	ok := map[string]string{
		"/etc/hosts":      "/etc/hosts",
		"  /etc/hosts  ":  "/etc/hosts",
		"/etc/../etc/./x": "/etc/x",
		"/":               "/",
		"/var/log/":       "/var/log",
		// Shell metacharacters are a path's business; quoting happens at the
		// shell, so nothing here needs stripping.
		"/tmp/x; rm -rf": "/tmp/x; rm -rf",
	}
	for in, want := range ok {
		got, err := CleanPath(in)
		if err != nil {
			t.Errorf("CleanPath(%q) failed: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("CleanPath(%q) = %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{"", "   ", "etc/hosts", "../etc", "~/x", "/etc/\x00", "/etc/a\nb"} {
		if got, err := CleanPath(in); err == nil {
			t.Errorf("CleanPath(%q) = %q, want an error", in, got)
		}
	}
}

// Only octal reaches chmod. Symbolic modes are not offered by the UI, and a
// mode that is not digits is refused here rather than handed to a shell.
func TestCleanMode(t *testing.T) {
	ok := map[string]string{
		"755":   "755",
		" 644 ": "644",
		"0644":  "0644",
		"1777":  "1777",
		"000":   "000",
		"4755":  "4755",
	}
	for in, want := range ok {
		got, err := CleanMode(in)
		if err != nil {
			t.Errorf("CleanMode(%q) failed: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("CleanMode(%q) = %q, want %q", in, got, want)
		}
	}
	bad := []string{"", "  ", "75", "75555", "u+x", "888", "7o5", "-rwxr-xr-x", "755;reboot", "0x1ff"}
	for _, in := range bad {
		got, err := CleanMode(in)
		if err == nil {
			t.Errorf("CleanMode(%q) = %q, want an error", in, got)
			continue
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("CleanMode(%q) gave %v, want it recognisable as a bad request", in, err)
		}
	}
}

func TestIsBinary(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want bool
	}{
		{"empty", nil, false},
		{"ascii", []byte("# /etc/hosts\n127.0.0.1 localhost\n"), false},
		{"utf-8", []byte("café — naïve\n"), false},
		{"a null byte anywhere", []byte("text\x00more"), true},
		{"latin-1", []byte{0x68, 0x69, 0xe9, 0x0a}, true},
		{"elf header", []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0}, true},
		{"truncated mid-rune", []byte("héllo wörl\xc3"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isBinary(tc.in); got != tc.want {
				t.Errorf("isBinary(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
