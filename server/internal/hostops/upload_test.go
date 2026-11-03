package hostops

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// upload runs the upload script the way the SSH path would, with body on stdin
// and the promised size taken from it unless a test says otherwise.
func upload(t *testing.T, dir, name, body string, replace bool, size ...int) (*Uploaded, int) {
	t.Helper()
	n := len(body)
	if len(size) > 0 {
		n = size[0]
	}
	mode := "keep"
	if replace {
		mode = "replace"
	}
	out, code := runScript(t, asUser(uploadScript, dir, name, strconv.Itoa(n), mode), body)
	if code != 0 {
		return nil, code
	}
	return parseUploaded(out, filepath.Join(dir, name), int64(n)), 0
}

// The body goes over the wire as raw bytes, so what matters is that nothing on
// the way treats it as text: NUL, every byte value, no newline at the end.
func TestUploadScriptKeepsEveryByte(t *testing.T) {
	var all strings.Builder
	for b := 0; b < 256; b++ {
		all.WriteByte(byte(b))
	}
	cases := map[string]string{
		"empty":           "",
		"text":            "hello from a phone\n",
		"every byte":      all.String(),
		"no newline":      "ends abruptly",
		"nul in the lead": "\x00\x00start",
		"large":           strings.Repeat("0123456789abcdef", 64<<10), // 1 MiB
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			up, code := upload(t, dir, "file.bin", body, false)
			if code != 0 {
				t.Fatalf("upload exited %d", code)
			}
			if up.Path != filepath.Join(dir, "file.bin") || up.Size != int64(len(body)) {
				t.Errorf("uploaded = %+v, want %s with %d bytes", up, filepath.Join(dir, "file.bin"), len(body))
			}
			onDisk, err := os.ReadFile(up.Path)
			if err != nil {
				t.Fatal(err)
			}
			if string(onDisk) != body {
				t.Errorf("on disk differs from what was sent (%d bytes vs %d)", len(onDisk), len(body))
			}
			info, err := os.Stat(up.Path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o644 {
				t.Errorf("mode = %v, want 644 for a new file", info.Mode().Perm())
			}
		})
	}
}

// A folder arrives one file at a time, each naming its place inside the
// folder. The host makes the folders as it goes.
func TestUploadScriptMakesTheFoldersInTheName(t *testing.T) {
	dir := t.TempDir()
	up, code := upload(t, dir, "trip/day 1/photo.jpg", "jpeg", false)
	if code != 0 {
		t.Fatalf("upload exited %d", code)
	}
	want := filepath.Join(dir, "trip", "day 1", "photo.jpg")
	if up.Path != want {
		t.Errorf("path = %q, want %q", up.Path, want)
	}
	if got, _ := os.ReadFile(want); string(got) != "jpeg" {
		t.Errorf("on disk = %q, want jpeg", got)
	}
	// A second file in the same folder finds it already there.
	if _, code := upload(t, dir, "trip/day 1/other.jpg", "jpeg2", false); code != 0 {
		t.Fatalf("second upload exited %d", code)
	}
	// A folder in the name that is really a file cannot be made.
	if _, code := upload(t, dir, "trip/day 1/photo.jpg/inside", "x", false); code != 3 {
		t.Errorf("exit = %d, want 3 — a file cannot have folders made under it", code)
	}
}

// Nothing is overwritten by accident. Asked to replace, the file keeps the
// permissions it had, as a save does.
func TestUploadScriptRefusesToOverwriteUnlessAsked(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, code := upload(t, dir, "notes.txt", "new", false); code != uploadExitExists {
		t.Fatalf("exit = %d, want %d for a file that is already there", code, uploadExitExists)
	}
	if got, _ := os.ReadFile(path); string(got) != "old" {
		t.Errorf("on disk = %q, want the old file untouched", got)
	}

	if _, code := upload(t, dir, "notes.txt", "new", true); code != 0 {
		t.Fatalf("replace exited %d", code)
	}
	if got, _ := os.ReadFile(path); string(got) != "new" {
		t.Errorf("on disk = %q, want it replaced", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want the 600 the file had kept", info.Mode().Perm())
	}
}

// A transfer that stops short — the phone locked, the wifi went — leaves
// nothing behind: not a partial file, and not the old one damaged.
func TestUploadScriptDiscardsAShortUpload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backup.tar")
	if err := os.WriteFile(path, []byte("the old backup"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, code := upload(t, dir, "backup.tar", "only half", true, 100); code != 8 {
		t.Fatalf("exit = %d, want 8 for fewer bytes than promised", code)
	}
	if got, _ := os.ReadFile(path); string(got) != "the old backup" {
		t.Errorf("on disk = %q, want the old file kept", got)
	}
	if _, code := upload(t, dir, "fresh.tar", "too many bytes", false, 3); code != 8 {
		t.Errorf("exit = %d, want 8 for more bytes than promised", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "fresh.tar")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a short upload to a new name should leave no file")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".hostman.") {
			t.Errorf("temporary file %s left behind", e.Name())
		}
	}
}

func TestUploadScriptRefusesWhatItCannotPlace(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, code := upload(t, dir, "folder", "x", true); code != 4 {
		t.Errorf("exit = %d, want 4 — a folder cannot be replaced by a file", code)
	}
	if _, code := upload(t, filepath.Join(dir, "missing"), "x", "x", false); code != 2 {
		t.Errorf("exit = %d, want 2 for a directory that is not there", code)
	}
}

// Uploading onto a symlink writes through to what it points at, the way saving
// does: the link stays a link.
func TestUploadScriptFollowsSymlinks(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.conf")
	if err := os.WriteFile(real, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(dir, "link.conf")); err != nil {
		t.Fatal(err)
	}
	up, code := upload(t, dir, "link.conf", "new", true)
	if code != 0 {
		t.Fatalf("upload exited %d", code)
	}
	if up.Path != real {
		t.Errorf("path = %q, want the link resolved to %q", up.Path, real)
	}
	if got, _ := os.ReadFile(real); string(got) != "new" {
		t.Errorf("target = %q, want it written through the link", got)
	}
	if info, err := os.Lstat(filepath.Join(dir, "link.conf")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Error("the link itself should still be a link")
	}
}

// Every name an upload carries is joined to the folder it goes in, so a name
// that could climb out of it is refused before anything is sent.
func TestCleanRelPath(t *testing.T) {
	ok := []string{"photo.jpg", "trip/day1/photo.jpg", "a b/c d.txt", " spaced.txt ", "it's.txt", "back\\slash", "..hidden", "...", "a..b/c"}
	for _, name := range ok {
		if got, err := CleanRelPath(name); err != nil || got != strings.TrimSpace(name) {
			t.Errorf("CleanRelPath(%q) = %q, %v; want it accepted as is", name, got, err)
		}
	}
	bad := []string{"", "   ", "/etc/passwd", "../up.txt", "a/../b", "a/./b", ".", "..", "a//b", "a/", "nul\x00byte", "new\nline"}
	for _, name := range bad {
		if _, err := CleanRelPath(name); !errors.Is(err, ErrInvalid) {
			t.Errorf("CleanRelPath(%q) err = %v, want it refused as invalid", name, err)
		}
	}
}

func TestParseUploaded(t *testing.T) {
	up := parseUploaded("@@path\n/srv/real.bin\n@@size\n12\n", "/srv/link.bin", 12)
	if up.Path != "/srv/real.bin" || up.Size != 12 {
		t.Errorf("parsed = %+v, want the host's path and size", up)
	}
	up = parseUploaded("", "/srv/asked.bin", 7)
	if up.Path != "/srv/asked.bin" || up.Size != 7 {
		t.Errorf("parsed = %+v, want the asked path and promised size when the host said nothing", up)
	}
}

// The elevated form must carry stdin through to the script, whichever branch
// of the sudo check runs.
func TestUploadScriptThroughElevate(t *testing.T) {
	dir := t.TempDir()
	cmd := elevate(uploadScript, dir, "x.bin", "5", "keep")
	out, code := runScript(t, cmd, "bytes")
	if code != 0 {
		t.Fatalf("exited %d: %s", code, out)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "x.bin")); string(got) != "bytes" {
		t.Errorf("on disk = %q, want bytes", got)
	}
	if !strings.Contains(out, fmt.Sprintf("@@path\n%s/x.bin", dir)) {
		t.Errorf("output %q should name the file", out)
	}
}
