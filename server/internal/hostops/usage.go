package hostops

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/chinmay28/hostman/server/internal/store"
)

// Usage is what a directory holds, added up all the way down.
type Usage struct {
	// Path is the directory measured, with a symlink resolved to where it led.
	Path string `json:"path"`
	// Files counts everything under Path that is not a directory: regular
	// files, symlinks (as links, not what they point at), sockets, devices.
	Files int64 `json:"files"`
	// Dirs counts the directories under Path. Path itself is not one of them.
	Dirs int64 `json:"dirs"`
	// Bytes is the space the tree takes on disk, as du reports it — blocks
	// allocated rather than lengths added up, so a sparse file counts for what
	// it uses and a small one for the block it sits in.
	Bytes int64 `json:"bytes"`
	// Unreadable is how many places the walk could not enter or measure. When
	// it is not zero the numbers above are a floor rather than a total.
	Unreadable int `json:"unreadable"`
	// AsUser is who did the counting — root wherever sudo is available.
	AsUser string `json:"asUser"`
}

// usageScript walks a directory three times: once for what is not a directory,
// once for what is, and once with du for the space. The dentry cache makes the
// second and third walks cheap, and three portable commands beat one clever
// pipeline that busybox's find cannot run. All three trip over the same
// unenterable directories, so only the first walk's complaints are kept: they
// go to a temporary file, and the number of lines in it comes back as how many
// places went unmeasured. A host with nowhere to put that file still answers,
// just without the caveat. Nothing is left on stderr, so a permission problem
// deep in the tree does not read as the whole request failing.
const usageScript = `set -u
p=$1
if [ -L "$p" ]; then r=$(readlink -f -- "$p" 2>/dev/null || printf ''); [ -n "$r" ] && p=$r; fi
[ -e "$p" ] || { printf 'no such file: %s\n' "$p" >&2; exit 2; }
[ -d "$p" ] || { printf '%s is not a directory\n' "$p" >&2; exit 3; }
[ -r "$p" ] && [ -x "$p" ] || { printf 'permission denied: %s\n' "$p" >&2; exit 4; }
tmp=$(mktemp 2>/dev/null) || tmp=/dev/null
[ "$tmp" = /dev/null ] || trap 'rm -f "$tmp"' EXIT
files=$(find "$p" -mindepth 1 ! -type d -print 2>"$tmp" | wc -l)
dirs=$(find "$p" -mindepth 1 -type d -print 2>/dev/null | wc -l)
kb=$(du -sk -- "$p" 2>/dev/null | cut -f1)
unreadable=$(grep -c . "$tmp" 2>/dev/null || printf 0)
printf '@@path\n%s\n' "$p"
printf '@@user\n%s\n' "$(id -un 2>/dev/null || echo unknown)"
printf '@@files\n%s\n' "$files"
printf '@@dirs\n%s\n' "$dirs"
printf '@@kb\n%s\n' "$kb"
printf '@@unreadable\n%s\n' "$unreadable"
`

// Usage counts what is under a directory and how much disk it takes. A
// symlink to a directory is measured as the directory. Anything that is not a
// directory is refused: a file's size is already in the listing.
func (s *Service) Usage(ctx context.Context, h *store.Host, dir string) (*Usage, error) {
	clean, err := CleanPath(dir)
	if err != nil {
		return nil, err
	}
	res, err := s.run(ctx, h, elevate(usageScript, clean), "")
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, failure(res, "could not measure "+clean)
	}
	return parseUsage(res.Stdout, clean)
}

// parseUsage reads the script's sections. Missing or non-numeric counts mean
// the host answered with something other than the script's output, which is
// an error rather than a directory with nothing in it.
func parseUsage(out, asked string) (*Usage, error) {
	found := sections(out)
	u := &Usage{Path: first(found["path"]), AsUser: first(found["user"])}
	if u.Path == "" {
		u.Path = asked
	}
	var err error
	if u.Files, err = count(found, "files"); err != nil {
		return nil, err
	}
	if u.Dirs, err = count(found, "dirs"); err != nil {
		return nil, err
	}
	kb, err := count(found, "kb")
	if err != nil {
		return nil, err
	}
	u.Bytes = kb * 1024
	unreadable, err := count(found, "unreadable")
	if err != nil {
		return nil, err
	}
	u.Unreadable = int(unreadable)
	return u, nil
}

func count(found map[string][]string, name string) (int64, error) {
	raw := first(found[name])
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("the host did not report a %s count (got %q)", name, raw)
	}
	return n, nil
}
