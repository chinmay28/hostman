package hostops

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The scripts are the part of this package that runs on someone else's machine,
// so they are tested by running them — against a real filesystem, through a real
// shell, with the same parsers the SSH path uses. Only the transport is
// missing, and the transport is not what breaks.

// runScript executes what would be sent over SSH and returns stdout and the
// exit status. Commands come from asUser rather than elevate so a test proves
// what a script does, not what sudo does with it.
func runScript(t *testing.T, cmd, stdin string, extraPath ...string) (string, int) {
	t.Helper()
	run := exec.Command("/bin/sh", "-c", cmd)
	run.Stdin = strings.NewReader(stdin)
	if len(extraPath) > 0 {
		run.Env = append(os.Environ(), "PATH="+strings.Join(extraPath, ":")+":"+os.Getenv("PATH"))
	}
	var out, errOut strings.Builder
	run.Stdout = &out
	run.Stderr = &errOut
	err := run.Run()
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run %q: %v", cmd, err)
		}
		code = exit.ExitCode()
	}
	if code != 0 {
		t.Logf("script exited %d: %s", code, strings.TrimSpace(errOut.String()))
	}
	return out.String(), code
}

func list(t *testing.T, dir string, extraPath ...string) *Listing {
	t.Helper()
	out, code := runScript(t, asUser(fmt.Sprintf(listScript, maxEntries+1), dir), "", extraPath...)
	if code != 0 {
		t.Fatalf("list %s exited %d", dir, code)
	}
	return parseListing(out, dir)
}

func byName(l *Listing) map[string]Entry {
	found := map[string]Entry{}
	for _, e := range l.Entries {
		found[e.Name] = e
	}
	return found
}

// noFindPath returns a PATH entry holding a `find` that always fails, so the
// listing falls back to the stat loop busybox hosts need.
func noFindPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "find"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// populate builds a directory with one of everything a listing has to survive.
func populate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string, mode os.FileMode) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil { // umask does not apply to Chmod
			t.Fatal(err)
		}
	}
	write("hosts", "127.0.0.1 localhost\n", 0o644)
	write("secret.key", "shh\n", 0o600)
	write("a name with spaces.conf", "x=1\n", 0o644)
	write(".hidden", "hidden\n", 0o644)
	if err := os.Mkdir(filepath.Join(dir, "conf.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "conf.d"), filepath.Join(dir, "to-dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "hosts"), filepath.Join(dir, "to-file")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nowhere-at-all", filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Both listing implementations — GNU find's one-shot -printf and the stat loop
// that stands in for it on busybox — must describe a directory the same way.
func TestListScript(t *testing.T) {
	dir := populate(t)
	for _, tc := range []struct {
		name string
		path []string
	}{
		{name: "with find"},
		{name: "without find", path: []string{noFindPath(t)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listing := list(t, dir, tc.path...)
			if listing.Path != dir {
				t.Errorf("path = %q, want %q", listing.Path, dir)
			}
			if listing.Parent != filepath.Dir(dir) {
				t.Errorf("parent = %q, want %q", listing.Parent, filepath.Dir(dir))
			}
			if listing.AsUser == "" {
				t.Error("asUser is empty, want the account the script ran as")
			}
			if listing.Truncated {
				t.Error("a directory of eight things should not be truncated")
			}

			found := byName(listing)
			if len(found) != 8 {
				t.Fatalf("saw %d entries (%v), want 8", len(found), found)
			}
			// Hidden files are the ones worth having on a config browser.
			if _, ok := found[".hidden"]; !ok {
				t.Error("dotfiles should be listed")
			}
			if e := found["hosts"]; e.Type != "file" || e.Mode != "644" || e.Size != 20 {
				t.Errorf("hosts = %+v, want a 20-byte 644 file", e)
			}
			if e := found["secret.key"]; e.Mode != "600" {
				t.Errorf("secret.key mode = %q, want 600", e.Mode)
			}
			if e := found["a name with spaces.conf"]; e.Type != "file" {
				t.Errorf("a name with spaces = %+v, want a file", e)
			}
			if e := found["conf.d"]; e.Type != "dir" || !e.IsDir() {
				t.Errorf("conf.d = %+v, want a directory", e)
			}
			if e := found["to-dir"]; e.Type != "link" || e.LinkType != "dir" || !e.IsDir() {
				t.Errorf("to-dir = %+v, want a symlink that opens as a directory", e)
			}
			if e := found["to-file"]; e.Type != "link" || e.LinkType != "file" || e.IsDir() {
				t.Errorf("to-file = %+v, want a symlink to a file", e)
			}
			if e := found["dangling"]; e.Type != "link" || e.LinkType != "broken" {
				t.Errorf("dangling = %+v, want a broken symlink", e)
			}
			if e := found["dangling"]; e.Target != "/nowhere-at-all" {
				t.Errorf("dangling target = %q, want the link written as it is", e.Target)
			}
			if e := found["hosts"]; e.Owner == "" || e.Group == "" || e.ModifiedAt.IsZero() {
				t.Errorf("hosts = %+v, want owner, group and mtime", e)
			}
		})
	}
}

func TestListScriptSortsDirectoriesFirst(t *testing.T) {
	listing := list(t, populate(t))
	var order []string
	for _, e := range listing.Entries {
		order = append(order, e.Name)
	}
	// conf.d and to-dir are the two that open as directories.
	if len(order) < 3 || order[0] != "conf.d" || order[1] != "to-dir" {
		t.Errorf("order = %v, want the two directories first", order)
	}
	if !sortedAfter(order[2:]) {
		t.Errorf("files = %v, want them in name order", order[2:])
	}
}

func sortedAfter(names []string) bool {
	for i := 1; i < len(names); i++ {
		if strings.ToLower(names[i-1]) > strings.ToLower(names[i]) {
			return false
		}
	}
	return true
}

func TestListScriptOnAnEmptyDirectory(t *testing.T) {
	listing := list(t, t.TempDir())
	if len(listing.Entries) != 0 {
		t.Errorf("entries = %v, want none", listing.Entries)
	}
	listing = list(t, t.TempDir(), noFindPath(t))
	if len(listing.Entries) != 0 {
		t.Errorf("entries without find = %v, want none — an unmatched glob is not a file", listing.Entries)
	}
}

func TestListScriptRefusesWhatItCannotOpen(t *testing.T) {
	_, code := runScript(t, asUser(fmt.Sprintf(listScript, maxEntries+1), "/no/such/directory"), "")
	if code != 2 {
		t.Errorf("exit = %d, want 2 for a directory that isn't there", code)
	}
}

func readFile(t *testing.T, path string) (*File, int) {
	t.Helper()
	out, code := runScript(t, asUser(fmt.Sprintf(readScript, MaxViewBytes+1), path), "")
	if code != 0 {
		return nil, code
	}
	f, err := parseFile(out, path)
	if err != nil {
		t.Fatalf("parseFile: %v", err)
	}
	return f, 0
}

func writeFile(t *testing.T, path, content string) int {
	t.Helper()
	_, code := runScript(t, asUser(writeScript, path), base64.StdEncoding.EncodeToString([]byte(content)))
	return code
}

func TestReadScript(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hostman.conf")
	const content = "# a config\nkey = value\n"
	if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}

	f, code := readFile(t, path)
	if code != 0 {
		t.Fatalf("read exited %d", code)
	}
	if f.Content != content {
		t.Errorf("content = %q, want %q", f.Content, content)
	}
	if f.Size != int64(len(content)) || f.Mode != "640" {
		t.Errorf("file = %+v, want %d bytes and mode 640", f, len(content))
	}
	if f.Binary || f.Truncated {
		t.Errorf("file = %+v, want plain untruncated text", f)
	}
	if f.Owner == "" || f.ModifiedAt.IsZero() || f.AsUser == "" {
		t.Errorf("file = %+v, want owner, mtime and the acting user", f)
	}
}

// Bytes have to survive the round trip exactly: no trailing newline invented,
// none dropped, and nothing reinterpreted on the way.
func TestReadWriteRoundTrip(t *testing.T) {
	cases := map[string]string{
		"empty":                "",
		"no trailing newline":  "just one line",
		"trailing newlines":    "line\n\n\n",
		"crlf":                 "dos\r\nline\r\n",
		"utf-8":                "café ☕ — naïve\n",
		"quotes and dollars":   "a='$HOME'; b=\"`whoami`\"\n",
		"backslashes":          `C:\not\a\path` + "\n",
		"a very long line":     strings.Repeat("x", 200_000) + "\n",
		"leading whitespace":   "  indented\n\ttabbed\n",
		"looks like a marker":  "@@entries\n@@body\nreal content\n",
		"percent formatting":   "%s %d %%\n",
		"shell metacharacters": "* ? [a-z] | & ; ( ) < >\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "file.txt")
			if code := writeFile(t, path, content); code != 0 {
				t.Fatalf("write exited %d", code)
			}
			onDisk, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(onDisk) != content {
				t.Fatalf("on disk = %q, want %q", truncate(string(onDisk)), truncate(content))
			}
			f, code := readFile(t, path)
			if code != 0 {
				t.Fatalf("read exited %d", code)
			}
			if f.Content != content {
				t.Errorf("read back = %q, want %q", truncate(f.Content), truncate(content))
			}
		})
	}
}

func truncate(s string) string {
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}

// A file is edited in place: it keeps its permissions, and its inode changes
// because the write goes through a temporary file that replaces it whole.
func TestWriteScriptPreservesMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.conf")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := writeFile(t, path, "new\n"); code != 0 {
		t.Fatalf("write exited %d", code)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 600 — a save must not widen a file's permissions", info.Mode().Perm())
	}
}

func TestWriteScriptCreatesWithASaneMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.conf")
	if code := writeFile(t, path, "fresh\n"); code != 0 {
		t.Fatalf("write exited %d", code)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, want 644 for a new file", info.Mode().Perm())
	}
}

// Writing through a symlink must edit what it points at. Replacing the link
// itself is how /etc/resolv.conf gets quietly disconnected from systemd.
func TestWriteScriptFollowsSymlinks(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.conf")
	link := filepath.Join(dir, "link.conf")
	if err := os.WriteFile(real, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if code := writeFile(t, link, "new\n"); code != 0 {
		t.Fatalf("write exited %d", code)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link = %v (%v), want it left as a symlink", info, err)
	}
	if body, err := os.ReadFile(real); err != nil || string(body) != "new\n" {
		t.Errorf("target = %q (%v), want the write to have landed on it", body, err)
	}

	// Reading follows the same link, and says so.
	f, code := readFile(t, link)
	if code != 0 {
		t.Fatalf("read exited %d", code)
	}
	if f.Path != real {
		t.Errorf("path = %q, want the resolved %q", f.Path, real)
	}
}

func TestWriteScriptLeavesTheOriginalOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "nope.conf")
	if code := writeFile(t, path, "x\n"); code == 0 {
		t.Fatal("writing into a directory that does not exist should fail")
	}
	// And nothing is left behind in the parent it could reach.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("left %v behind, want no temporary files", entries)
	}
}

func TestReadScriptRefusesWhatCannotBeEdited(t *testing.T) {
	dir := t.TempDir()
	if _, code := readFile(t, dir); code != 3 {
		t.Errorf("reading a directory exited %d, want 3", code)
	}
	if _, code := readFile(t, filepath.Join(dir, "missing")); code != 2 {
		t.Errorf("reading a missing file exited %d, want 2", code)
	}
	// A named pipe would block forever on `head`; it is not a regular file.
	fifo := filepath.Join(dir, "pipe")
	if out, err := exec.Command("mkfifo", fifo).CombinedOutput(); err != nil {
		t.Skipf("mkfifo unavailable: %v: %s", err, out)
	}
	if _, code := readFile(t, fifo); code != 4 {
		t.Errorf("reading a fifo exited %d, want 4", code)
	}
}

func TestReadScriptTruncatesAndFlagsBinary(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.log")
	if err := os.WriteFile(big, []byte(strings.Repeat("a", MaxViewBytes+5000)), 0o644); err != nil {
		t.Fatal(err)
	}
	f, code := readFile(t, big)
	if code != 0 {
		t.Fatalf("read exited %d", code)
	}
	if !f.Truncated || len(f.Content) != MaxViewBytes {
		t.Errorf("content = %d bytes, truncated = %v, want %d and true", len(f.Content), f.Truncated, MaxViewBytes)
	}
	if f.Size != int64(MaxViewBytes+5000) {
		t.Errorf("size = %d, want the whole file's size %d", f.Size, MaxViewBytes+5000)
	}

	binary := filepath.Join(dir, "binary")
	if err := os.WriteFile(binary, []byte{0x7f, 'E', 'L', 'F', 0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatal(err)
	}
	f, code = readFile(t, binary)
	if code != 0 {
		t.Fatalf("read exited %d", code)
	}
	if !f.Binary || f.Content != "" {
		t.Errorf("file = %+v, want it flagged binary with no content", f)
	}
}

func TestMkdirScript(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b")
	if _, code := runScript(t, asUser(mkdirScript, nested), ""); code != 0 {
		t.Fatalf("mkdir exited %d", code)
	}
	if info, err := os.Stat(nested); err != nil || !info.IsDir() {
		t.Fatalf("stat = %v (%v), want a directory and its parent created", info, err)
	}
	if _, code := runScript(t, asUser(mkdirScript, nested), ""); code != 2 {
		t.Errorf("creating it twice exited %d, want 2", code)
	}
}

func TestRenameScript(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "old.conf"), filepath.Join(dir, "new.conf")
	if err := os.WriteFile(from, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, code := runScript(t, asUser(renameScript, from, to), ""); code != 0 {
		t.Fatalf("rename exited %d", code)
	}
	if _, err := os.Stat(to); err != nil {
		t.Fatalf("stat new name: %v", err)
	}

	// Renaming onto something that exists would destroy it silently.
	other := filepath.Join(dir, "other.conf")
	if err := os.WriteFile(other, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, code := runScript(t, asUser(renameScript, to, other), ""); code != 3 {
		t.Errorf("rename onto an existing file exited %d, want 3", code)
	}
	if body, _ := os.ReadFile(other); string(body) != "keep\n" {
		t.Errorf("the existing file became %q, want it untouched", body)
	}
	if _, code := runScript(t, asUser(renameScript, filepath.Join(dir, "gone"), to), ""); code != 2 {
		t.Errorf("renaming something absent exited %d, want 2", code)
	}
}

func TestRemoveScript(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "x.conf")
	if err := os.WriteFile(file, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, code := runScript(t, asUser(removeScript, file, "single"), ""); code != 0 {
		t.Fatalf("remove exited %d", code)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Error("the file is still there")
	}

	empty := filepath.Join(dir, "empty")
	if err := os.Mkdir(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, code := runScript(t, asUser(removeScript, empty, "single"), ""); code != 0 {
		t.Errorf("removing an empty directory exited %d, want 0", code)
	}

	full := filepath.Join(dir, "full")
	if err := os.MkdirAll(filepath.Join(full, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, code := runScript(t, asUser(removeScript, full, "single"), ""); code != 5 {
		t.Errorf("removing a directory with contents exited %d, want 5", code)
	}
	if _, err := os.Stat(full); err != nil {
		t.Error("it should still be there, having been refused")
	}
	if _, code := runScript(t, asUser(removeScript, full, "recursive"), ""); code != 0 {
		t.Errorf("recursive remove exited %d, want 0", code)
	}
	if _, err := os.Stat(full); !os.IsNotExist(err) {
		t.Error("recursive remove left it behind")
	}
}

func TestRemoveScriptRefusesRoot(t *testing.T) {
	if _, code := runScript(t, asUser(removeScript, "/", "recursive"), ""); code != 2 {
		t.Fatalf("deleting / exited %d, want a refusal (2)", code)
	}
	if _, code := runScript(t, asUser(removeScript, "/no/such/thing", "single"), ""); code != 3 {
		t.Errorf("deleting something absent exited %d, want 3", code)
	}
}

// A symlink is deleted as itself, never followed into what it points at.
func TestRemoveScriptDoesNotFollowSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(filepath.Join(target, "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, code := runScript(t, asUser(removeScript, link, "single"), ""); code != 0 {
		t.Fatalf("removing a symlink exited %d", code)
	}
	if _, err := os.Stat(filepath.Join(target, "keep")); err != nil {
		t.Errorf("what the link pointed at was removed too: %v", err)
	}
}

// chmodOut runs the chmod script and returns the mode the host reported.
func chmodOut(t *testing.T, target, mode, scope string) (string, int) {
	t.Helper()
	out, code := runScript(t, asUser(chmodScript, target, mode, scope), "")
	return first(sections(out)["mode"]), code
}

func modeOf(t *testing.T, p string) string {
	t.Helper()
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%o", info.Mode().Perm())
}

func TestChmodScript(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "app.conf")
	if err := os.WriteFile(file, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, code := chmodOut(t, file, "644", "single")
	if code != 0 {
		t.Fatalf("chmod exited %d", code)
	}
	if got != "644" {
		t.Errorf("reported mode %q, want the one the host read back (644)", got)
	}
	if on := modeOf(t, file); on != "644" {
		t.Errorf("on disk = %s, want 644", on)
	}

	// The four-digit form carries the sticky and setgid bits through.
	if _, code := chmodOut(t, dir, "1755", "single"); code != 0 {
		t.Fatalf("chmod with a special bit exited %d", code)
	}
	if info, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	} else if info.Mode()&os.ModeSticky == 0 {
		t.Errorf("mode = %v, want the sticky bit set", info.Mode())
	}
}

// The difference between one directory and everything under it is the whole
// point of the recursive flag, so both are proven rather than assumed.
func TestChmodScriptRecursive(t *testing.T) {
	dir := t.TempDir()
	inner := filepath.Join(dir, "inner")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(inner, "deep.conf")
	if err := os.WriteFile(deep, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(deep, 0o600); err != nil { // umask does not apply to Chmod
		t.Fatal(err)
	}

	if _, code := chmodOut(t, dir, "750", "single"); code != 0 {
		t.Fatalf("chmod exited %d", code)
	}
	if on := modeOf(t, dir); on != "750" {
		t.Errorf("the directory is %s, want 750", on)
	}
	if on := modeOf(t, deep); on != "600" {
		t.Errorf("a file inside became %s, want 600 — one directory means one directory", on)
	}

	if _, code := chmodOut(t, dir, "777", "recursive"); code != 0 {
		t.Fatalf("recursive chmod exited %d", code)
	}
	for _, p := range []string{dir, inner, deep} {
		if on := modeOf(t, p); on != "777" {
			t.Errorf("%s is %s, want 777 all the way down", p, on)
		}
	}
}

// A symlink's own bits mean nothing, so the mode has to reach what it points at
// — and the script says which path that was.
func TestChmodScriptFollowsSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.conf")
	if err := os.WriteFile(target, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.conf")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	out, code := runScript(t, asUser(chmodScript, link, "640", "single"), "")
	if code != 0 {
		t.Fatalf("chmod through a symlink exited %d", code)
	}
	if got := first(sections(out)["path"]); got != target {
		t.Errorf("path = %q, want the file it resolved to (%q)", got, target)
	}
	if on := modeOf(t, target); on != "640" {
		t.Errorf("the target is %s, want 640", on)
	}
}

// A recursive chmod walks the tree it was given and no further: a symlink
// inside it is not a way out into the rest of the filesystem.
func TestChmodScriptRecursiveDoesNotEscapeThroughSymlinks(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.conf")
	if err := os.WriteFile(outside, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outside, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, code := chmodOut(t, dir, "777", "recursive"); code != 0 {
		t.Fatalf("recursive chmod exited %d", code)
	}
	if on := modeOf(t, outside); on != "600" {
		t.Errorf("a file outside the tree became %s, want it left at 600", on)
	}
}

func TestChmodScriptRefusesRootAndTheAbsent(t *testing.T) {
	if _, code := chmodOut(t, "/", "777", "recursive"); code != 2 {
		t.Fatalf("chmod on / exited %d, want a refusal (2)", code)
	}
	if _, code := chmodOut(t, "/no/such/thing", "644", "single"); code != 3 {
		t.Errorf("chmod on something absent exited %d, want 3", code)
	}
}
