package hostops

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chinmay28/hostman/server/internal/store"
)

// Limits. A phone is reading these over a home network, and the whole file
// crosses the wire as one JSON string, so both ends stay modest.
const (
	// MaxViewBytes is how much of a file is fetched for viewing. Anything
	// longer is truncated, and the UI says so rather than pretending.
	MaxViewBytes = 512 << 10
	// MaxWriteBytes bounds a save.
	MaxWriteBytes = 1 << 20
	// maxEntries bounds one directory listing.
	maxEntries = 2000
)

// Entry is one item in a directory.
type Entry struct {
	Name string `json:"name"`
	// Type is "dir", "file", "link" or "other".
	Type string `json:"type"`
	// LinkType is what a symlink resolves to — "dir", "file", "other", or
	// "broken" where it resolves to nothing. Empty for anything else.
	LinkType string `json:"linkType,omitempty"`
	// Target is the symlink's text, shown as written rather than resolved.
	Target     string    `json:"target,omitempty"`
	Size       int64     `json:"size"`
	Mode       string    `json:"mode"`
	Owner      string    `json:"owner"`
	Group      string    `json:"group"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

// IsDir reports whether tapping this entry should descend into it: a directory,
// or a symlink to one.
func (e Entry) IsDir() bool { return e.Type == "dir" || (e.Type == "link" && e.LinkType == "dir") }

// Listing is one directory, as the host reported it.
type Listing struct {
	// Path is where the host ended up, with symlinks resolved, which is not
	// always the path that was asked for.
	Path    string  `json:"path"`
	Parent  string  `json:"parent"`
	Entries []Entry `json:"entries"`
	// Truncated means the directory holds more than maxEntries items.
	Truncated bool `json:"truncated"`
	// AsUser is who the commands ran as — root wherever sudo is available.
	AsUser string `json:"asUser"`
}

// listScript prints the resolved directory, the effective user, and one line
// per entry: type, resolved type, size, mtime, mode, owner, group, link target,
// name. GNU find does it in one process; busybox has no -printf, so a stat loop
// stands in. The name comes last so a name containing a tab still parses.
const listScript = `set -u
d=$1
cd -- "$d" 2>/dev/null || { printf 'cannot open %%s\n' "$d" >&2; exit 2; }
printf '@@path\n%%s\n' "$(pwd -P)"
printf '@@user\n%%s\n' "$(id -un 2>/dev/null || echo unknown)"
printf '@@entries\n'
if find . -maxdepth 0 -printf '' 2>/dev/null; then
  find . -maxdepth 1 -mindepth 1 -printf '%%y\t%%Y\t%%s\t%%T@\t%%m\t%%u\t%%g\t%%l\t%%f\n' 2>/dev/null | head -n %[1]d
else
  tab=$(printf '\t')
  for n in * .[!.]* ..?*; do
    [ -e "$n" ] || [ -L "$n" ] || continue
    if [ -L "$n" ]; then y=l; elif [ -d "$n" ]; then y=d; elif [ -f "$n" ]; then y=f; else y=o; fi
    if [ -d "$n" ]; then r=d; elif [ -f "$n" ]; then r=f; elif [ -e "$n" ]; then r=o; else r=N; fi
    s=$(stat -c "%%s${tab}%%Y${tab}%%a${tab}%%U${tab}%%G" -- "$n" 2>/dev/null) || s="0${tab}0${tab}0${tab}?${tab}?"
    l=''
    if [ -L "$n" ]; then l=$(readlink -- "$n" 2>/dev/null || printf ''); fi
    printf '%%s\t%%s\t%%s\t%%s\t%%s\n' "$y" "$r" "$s" "$l" "$n"
  done | head -n %[1]d
fi
`

// List reads one directory. An empty path means the SSH user's home.
func (s *Service) List(ctx context.Context, h *store.Host, dir string) (*Listing, error) {
	client, err := s.conn.Connect(ctx, h)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	if strings.TrimSpace(dir) == "" {
		// Resolved before elevating, so it is the SSH user's home and not
		// root's — sudo would otherwise answer with its own.
		home, err := client.Run(ctx, `printf '%s\n' "$HOME"`)
		if err != nil {
			return nil, err
		}
		dir = strings.TrimSpace(home.Stdout)
		if dir == "" {
			dir = "/"
		}
	}
	dir, err = CleanPath(dir)
	if err != nil {
		return nil, err
	}

	res, err := client.Run(ctx, elevate(fmt.Sprintf(listScript, maxEntries+1), dir))
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, failure(res, "could not read "+dir)
	}

	return parseListing(res.Stdout, dir), nil
}

// parseListing turns the script's output into a Listing, falling back to the
// path that was asked for if the host did not say where it ended up.
func parseListing(out, asked string) *Listing {
	found := sections(out)
	resolved := first(found["path"])
	if resolved == "" {
		resolved = asked
	}
	listing := &Listing{
		Path:    resolved,
		Parent:  path.Dir(resolved),
		AsUser:  first(found["user"]),
		Entries: []Entry{},
	}
	for _, line := range found["entries"] {
		if entry, ok := parseEntry(line); ok {
			listing.Entries = append(listing.Entries, entry)
		}
	}
	if len(listing.Entries) > maxEntries {
		listing.Entries = listing.Entries[:maxEntries]
		listing.Truncated = true
	}
	// Directories first, then by name: the order you want when the point of
	// the screen is to keep descending.
	sort.SliceStable(listing.Entries, func(i, j int) bool {
		a, b := listing.Entries[i], listing.Entries[j]
		if a.IsDir() != b.IsDir() {
			return a.IsDir()
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return listing
}

// entryTypes maps find's single-letter type onto the names the API uses.
var entryTypes = map[string]string{"d": "dir", "f": "file", "l": "link", "N": "broken"}

// parseEntry reads one line of the listing. A line that does not parse — a name
// containing a newline is the only real way to produce one — is dropped rather
// than shown as nonsense.
func parseEntry(line string) (Entry, bool) {
	f := strings.SplitN(line, "\t", 9)
	if len(f) < 9 {
		return Entry{}, false
	}
	e := Entry{
		Name:   f[8],
		Type:   entryType(f[0]),
		Target: f[7],
		Mode:   strings.TrimSpace(f[4]),
		Owner:  strings.TrimSpace(f[5]),
		Group:  strings.TrimSpace(f[6]),
	}
	if e.Name == "" {
		return Entry{}, false
	}
	if e.Type == "link" {
		e.LinkType = entryType(f[1])
	}
	e.Size, _ = strconv.ParseInt(strings.TrimSpace(f[2]), 10, 64)
	if secs, err := strconv.ParseFloat(strings.TrimSpace(f[3]), 64); err == nil && secs > 0 {
		e.ModifiedAt = time.Unix(int64(secs), 0).UTC()
	}
	return e, true
}

func entryType(code string) string {
	if name, ok := entryTypes[strings.TrimSpace(code)]; ok {
		return name
	}
	return "other"
}

// File is a file's contents plus what the host knows about it.
type File struct {
	// Path is the file after symlinks are resolved: editing /etc/resolv.conf
	// where it is a link means editing what it points at.
	Path       string    `json:"path"`
	Size       int64     `json:"size"`
	Mode       string    `json:"mode"`
	Owner      string    `json:"owner"`
	Group      string    `json:"group"`
	ModifiedAt time.Time `json:"modifiedAt"`
	// Content is the text, empty when Binary.
	Content string `json:"content"`
	// Truncated means only the first MaxViewBytes are here.
	Truncated bool `json:"truncated"`
	// Binary means the file is not text and was not decoded. Editing one by
	// hand would corrupt it, so the UI shows it rather than offering to.
	Binary bool   `json:"binary"`
	AsUser string `json:"asUser"`
}

// readScript resolves symlinks, refuses anything that is not a regular file,
// and sends the first MaxViewBytes base64-encoded so no encoding, control byte
// or missing final newline is lost on the way.
const readScript = `set -u
p=$1
if [ -L "$p" ]; then r=$(readlink -f -- "$p" 2>/dev/null || printf ''); [ -n "$r" ] && p=$r; fi
[ -e "$p" ] || { printf 'no such file: %%s\n' "$p" >&2; exit 2; }
if [ -d "$p" ]; then printf '%%s is a directory\n' "$p" >&2; exit 3; fi
[ -f "$p" ] || { printf '%%s is not a regular file\n' "$p" >&2; exit 4; }
[ -r "$p" ] || { printf 'permission denied: %%s\n' "$p" >&2; exit 5; }
tab=$(printf '\t')
printf '@@path\n%%s\n' "$p"
printf '@@user\n%%s\n' "$(id -un 2>/dev/null || echo unknown)"
printf '@@stat\n'
stat -c "%%s${tab}%%Y${tab}%%a${tab}%%U${tab}%%G" -- "$p" 2>/dev/null || printf '0\t0\t0\t?\t?\n'
printf '@@body\n'
head -c %[1]d -- "$p" | base64
`

// Read fetches a file for viewing or editing.
func (s *Service) Read(ctx context.Context, h *store.Host, filePath string) (*File, error) {
	clean, err := CleanPath(filePath)
	if err != nil {
		return nil, err
	}
	res, err := s.run(ctx, h, elevate(fmt.Sprintf(readScript, MaxViewBytes+1), clean), "")
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, failure(res, "could not read "+clean)
	}
	return parseFile(res.Stdout, clean)
}

// parseFile turns the script's output into a File, deciding on the way whether
// what came back is text at all.
func parseFile(out, asked string) (*File, error) {
	found := sections(out)
	f := &File{Path: first(found["path"]), AsUser: first(found["user"])}
	if f.Path == "" {
		f.Path = asked
	}
	if st := strings.SplitN(first(found["stat"]), "\t", 5); len(st) == 5 {
		f.Size, _ = strconv.ParseInt(strings.TrimSpace(st[0]), 10, 64)
		if secs, err := strconv.ParseInt(strings.TrimSpace(st[1]), 10, 64); err == nil && secs > 0 {
			f.ModifiedAt = time.Unix(secs, 0).UTC()
		}
		f.Mode, f.Owner, f.Group = strings.TrimSpace(st[2]), strings.TrimSpace(st[3]), strings.TrimSpace(st[4])
	}

	raw, err := base64.StdEncoding.DecodeString(strings.Join(found["body"], ""))
	if err != nil {
		return nil, fmt.Errorf("the host returned something that is not a file: %w", err)
	}
	if len(raw) > MaxViewBytes {
		raw = raw[:MaxViewBytes]
		f.Truncated = true
	}
	if isBinary(raw) {
		f.Binary = true
		return f, nil
	}
	f.Content = string(raw)
	return f, nil
}

// writeScript writes through a temporary file in the same directory so a save
// is atomic: a reader sees either the old file or the new one, never a partial
// write, and a failure part way through leaves the original alone. An existing
// file keeps its mode and owner — writing as root must not quietly hand a
// user's config to root.
const writeScript = `set -u
p=$1
if [ -L "$p" ]; then r=$(readlink -f -- "$p" 2>/dev/null || printf ''); [ -n "$r" ] && p=$r; fi
if [ -d "$p" ]; then printf '%s is a directory\n' "$p" >&2; exit 2; fi
d=$(dirname -- "$p")
[ -d "$d" ] || { printf 'no such directory: %s\n' "$d" >&2; exit 3; }
tmp=$(mktemp "$d/.hostman.XXXXXX") || { printf 'cannot write in %s\n' "$d" >&2; exit 4; }
trap 'rm -f "$tmp"' EXIT
base64 -d > "$tmp" || { printf 'could not decode the file contents\n' >&2; exit 5; }
if [ -e "$p" ]; then
  chmod --reference="$p" -- "$tmp" 2>/dev/null || chmod "$(stat -c %a -- "$p")" -- "$tmp" 2>/dev/null || true
  chown --reference="$p" -- "$tmp" 2>/dev/null || true
else
  chmod 644 -- "$tmp" 2>/dev/null || true
fi
mv -f -- "$tmp" "$p" || { printf 'could not replace %s\n' "$p" >&2; exit 6; }
trap - EXIT
printf 'written\n'
`

// Write replaces a file's contents, creating it if it does not exist.
func (s *Service) Write(ctx context.Context, h *store.Host, filePath, content string) error {
	clean, err := CleanPath(filePath)
	if err != nil {
		return err
	}
	if len(content) > MaxWriteBytes {
		return invalid("that file is too large to save from here (over %d KB)", MaxWriteBytes/1024)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	res, err := s.run(ctx, h, elevate(writeScript, clean), encoded)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return failure(res, "could not save "+clean)
	}
	return nil
}

const mkdirScript = `set -u
p=$1
[ -e "$p" ] && { printf '%s already exists\n' "$p" >&2; exit 2; }
mkdir -p -- "$p" || exit 3
printf 'created\n'
`

// Mkdir creates a directory, and any parent it needs.
func (s *Service) Mkdir(ctx context.Context, h *store.Host, dir string) error {
	clean, err := CleanPath(dir)
	if err != nil {
		return err
	}
	res, err := s.run(ctx, h, elevate(mkdirScript, clean), "")
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return failure(res, "could not create "+clean)
	}
	return nil
}

const renameScript = `set -u
from=$1
to=$2
[ -e "$from" ] || [ -L "$from" ] || { printf 'no such file: %s\n' "$from" >&2; exit 2; }
[ -e "$to" ] || [ -L "$to" ] && { printf '%s already exists\n' "$to" >&2; exit 3; }
mv -- "$from" "$to" || exit 4
printf 'moved\n'
`

// Rename moves a file or directory. It refuses to overwrite an existing name.
func (s *Service) Rename(ctx context.Context, h *store.Host, from, to string) error {
	cleanFrom, err := CleanPath(from)
	if err != nil {
		return err
	}
	cleanTo, err := CleanPath(to)
	if err != nil {
		return err
	}
	if cleanFrom == "/" {
		return invalid("refusing to move /")
	}
	if cleanFrom == cleanTo {
		return nil
	}
	res, err := s.run(ctx, h, elevate(renameScript, cleanFrom, cleanTo), "")
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return failure(res, "could not move "+cleanFrom)
	}
	return nil
}

// chmodScript changes a mode. A symlink is resolved first: a link's own bits
// mean nothing on Linux, so chmod would act on what it points at whatever this
// script did — resolving it makes that visible rather than surprising. The
// recursive form is `chmod -R`, exactly as it reads: everything inside gets the
// same mode, files included.
const chmodScript = `set -u
p=$1
m=$2
scope=$3
if [ -L "$p" ]; then r=$(readlink -f -- "$p" 2>/dev/null || printf ''); [ -n "$r" ] && p=$r; fi
case "$p" in /) printf 'refusing to change the mode of /\n' >&2; exit 2;; esac
[ -e "$p" ] || { printf 'no such file: %s\n' "$p" >&2; exit 3; }
if [ "$scope" = recursive ] && [ -d "$p" ]; then
  chmod -R "$m" -- "$p" || exit 4
else
  chmod "$m" -- "$p" || exit 4
fi
printf '@@path\n%s\n' "$p"
printf '@@mode\n%s\n' "$(stat -c %a -- "$p" 2>/dev/null || printf '')"
`

// Chmod sets the permission bits on a file or directory, and on everything
// inside it when recursive is set. It answers with the mode the host reports
// afterwards, which is the one worth showing: what was asked for and what took
// effect are not always the same digits — a directory's mode read back through
// stat drops a leading zero, and a filesystem mounted noexec has opinions.
func (s *Service) Chmod(ctx context.Context, h *store.Host, target, mode string, recursive bool) (string, error) {
	clean, err := CleanPath(target)
	if err != nil {
		return "", err
	}
	if clean == "/" {
		return "", invalid("refusing to change the mode of /")
	}
	octal, err := CleanMode(mode)
	if err != nil {
		return "", err
	}
	scope := "single"
	if recursive {
		scope = "recursive"
	}
	res, err := s.run(ctx, h, elevate(chmodScript, clean, octal, scope), "")
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", failure(res, "could not change the mode of "+clean)
	}
	if got := first(sections(res.Stdout)["mode"]); got != "" {
		return got, nil
	}
	return octal, nil
}

// removeScript deletes one entry. A non-empty directory needs the caller to ask
// for it explicitly — the difference between rmdir and rm -rf is the difference
// between a typo and a bad afternoon.
const removeScript = `set -u
p=$1
case "$p" in /) printf 'refusing to delete /\n' >&2; exit 2;; esac
[ -e "$p" ] || [ -L "$p" ] || { printf 'no such file: %s\n' "$p" >&2; exit 3; }
if [ -d "$p" ] && [ ! -L "$p" ]; then
  if [ "$2" = recursive ]; then rm -rf -- "$p" || exit 4; else rmdir -- "$p" || exit 5; fi
else
  rm -f -- "$p" || exit 4
fi
printf 'removed\n'
`

// Remove deletes a file, or a directory. A directory with anything in it is
// only removed when recursive is set.
func (s *Service) Remove(ctx context.Context, h *store.Host, target string, recursive bool) error {
	clean, err := CleanPath(target)
	if err != nil {
		return err
	}
	if clean == "/" {
		return invalid("refusing to delete /")
	}
	mode := "single"
	if recursive {
		mode = "recursive"
	}
	res, err := s.run(ctx, h, elevate(removeScript, clean, mode), "")
	if err != nil {
		return err
	}
	// rmdir refusing a directory with something in it is the one failure the
	// caller can do something about, so it is told apart from the rest.
	if res.ExitCode == 5 && strings.Contains(strings.ToLower(res.Stderr), "not empty") {
		return fmt.Errorf("%w: %s", ErrNotEmpty, clean)
	}
	if res.ExitCode != 0 {
		return failure(res, "could not delete "+clean)
	}
	return nil
}

// ErrNotEmpty means a directory was not removed because it has contents. The
// caller can offer to delete them too.
var ErrNotEmpty = errors.New("the directory is not empty")

// CleanPath normalizes a path from the browser and rejects anything that is not
// a plain absolute path.
func CleanPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	switch {
	case p == "":
		return "", invalid("a path is required")
	case !strings.HasPrefix(p, "/"):
		return "", invalid("the path must be absolute, starting with /")
	case strings.ContainsAny(p, "\x00\n"):
		return "", invalid("the path contains a character that cannot be used")
	}
	return path.Clean(p), nil
}

// CleanMode checks a permission mode from the browser. Only octal is accepted —
// three digits, or four where the first carries setuid, setgid or the sticky
// bit. Symbolic modes like u+x are not offered by the UI, and refusing them
// here means the digits handed to chmod can never be anything else.
func CleanMode(mode string) (string, error) {
	m := strings.TrimSpace(mode)
	if m == "" {
		return "", invalid("a mode is required, written in octal like 755")
	}
	if len(m) < 3 || len(m) > 4 {
		return "", invalid("%q is not a mode: it is three octal digits, or four with a leading special bit", mode)
	}
	for _, d := range m {
		if d < '0' || d > '7' {
			return "", invalid("%q is not a mode: octal digits only, like 755 or 0644", mode)
		}
	}
	return m, nil
}

// isBinary decides whether bytes are text a person can edit. A NUL byte settles
// it; otherwise anything that is not valid UTF-8 is treated as binary, since
// showing it in a textarea would only mangle it on the way back.
func isBinary(b []byte) bool {
	head := b
	if len(head) > 8000 {
		head = head[:8000]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return true
	}
	// Truncating — at MaxViewBytes, or at the 8 KB sniffed here — can cut a
	// multi-byte rune in half. A dangling sequence at the very end is not what
	// makes a file binary, so it is dropped before judging.
	return !utf8.Valid(trimPartialRune(head))
}

// trimPartialRune drops a trailing UTF-8 sequence that has been cut short.
func trimPartialRune(b []byte) []byte {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if b[i]&0xC0 != 0x80 { // not a continuation byte: a rune starts here
			if utf8.RuneStart(b[i]) && !utf8.Valid(b[i:]) {
				return b[:i]
			}
			return b
		}
	}
	return b
}
