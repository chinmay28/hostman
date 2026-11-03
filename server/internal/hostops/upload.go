package hostops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/chinmay28/hostman/server/internal/store"
)

// An upload is a file on the phone, or the laptop, going to the host. It is
// the one operation here whose input does not fit in a request body that is
// read whole: a photo is megabytes, a backup is gigabytes, and both have to
// cross HostMan on their way. So the body is streamed — browser to HostMan to
// the host's stdin — and never held. A folder arrives as one upload per file,
// each carrying its path inside the folder, and the host makes the folders as
// the files call for them; that keeps every request small enough to retry on
// its own and lets the screen show progress file by file.
//
// Nothing is overwritten unless the caller says so. An upload that stops short
// — the phone locked, wifi went — never lands: the host counts what arrived
// against what was promised and throws the partial file away.

// ErrExists means a file was not written because one is already there. The
// caller can ask again with replace set.
var ErrExists = errors.New("already exists")

// Uploaded is where a file ended up and how big it is there.
type Uploaded struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// uploadScript receives a file on stdin and places it under a directory. It
// writes through a temporary file next to the destination and moves it into
// place only once the byte count matches, so a reader sees either the old
// file or the whole new one, and a transfer cut off part way leaves nothing
// behind. A file that is replaced keeps its mode and owner, as a save does;
// a new one gets 644. Folders named in the relative path are created on the
// way, as `mkdir -p` makes them.
const uploadScript = `set -u
d=$1
n=$2
size=$3
replace=$4
[ -d "$d" ] || { printf 'no such directory: %s\n' "$d" >&2; exit 2; }
p="$d/$n"
case "$n" in */*) mkdir -p -- "$(dirname -- "$p")" || { printf 'cannot create folders under %s\n' "$d" >&2; exit 3; };; esac
if [ -L "$p" ]; then r=$(readlink -f -- "$p" 2>/dev/null || printf ''); [ -n "$r" ] && p=$r; fi
if [ -d "$p" ]; then printf '%s is a folder\n' "$p" >&2; exit 4; fi
if [ -e "$p" ] && [ "$replace" != replace ]; then printf '%s already exists\n' "$p" >&2; exit 5; fi
pd=$(dirname -- "$p")
tmp=$(mktemp "$pd/.hostman.XXXXXX") || { printf 'cannot write in %s\n' "$pd" >&2; exit 6; }
trap 'rm -f "$tmp"' EXIT
cat > "$tmp" || { printf 'could not write %s\n' "$p" >&2; exit 7; }
got=$(wc -c < "$tmp" | tr -d ' ')
[ "$got" -eq "$size" ] || { printf 'the upload stopped at %s of %s bytes\n' "$got" "$size" >&2; exit 8; }
if [ -e "$p" ]; then
  chmod --reference="$p" -- "$tmp" 2>/dev/null || chmod "$(stat -c %a -- "$p")" -- "$tmp" 2>/dev/null || true
  chown --reference="$p" -- "$tmp" 2>/dev/null || true
else
  chmod 644 -- "$tmp" 2>/dev/null || true
fi
mv -f -- "$tmp" "$p" || { printf 'could not place %s\n' "$p" >&2; exit 9; }
trap - EXIT
printf '@@path\n%s\n' "$p"
printf '@@size\n%s\n' "$got"
`

// uploadExitExists is the script's exit code for a file that is already
// there. It is the one refusal the caller can do something about.
const uploadExitExists = 5

// Upload streams body into dir under name, which is a path relative to dir —
// "photo.jpg", or "trip/day1/photo.jpg" for a file inside an uploaded folder.
// size is how many bytes the caller promised; an upload that delivers any other
// number is thrown away and reported. An existing file is refused with
// ErrExists unless replace is set.
func (s *Service) Upload(ctx context.Context, h *store.Host, dir, name string, size int64, body io.Reader, replace bool) (*Uploaded, error) {
	cleanDir, err := CleanPath(dir)
	if err != nil {
		return nil, err
	}
	rel, err := CleanRelPath(name)
	if err != nil {
		return nil, err
	}
	if size < 0 {
		return nil, invalid("the upload's size must be known before it starts")
	}
	if body == nil {
		body = strings.NewReader("")
	}
	mode := "keep"
	if replace {
		mode = "replace"
	}

	client, err := s.conn.Connect(ctx, h)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	cmd := elevate(uploadScript, cleanDir, rel, strconv.FormatInt(size, 10), mode)
	res, err := client.RunReader(ctx, cmd, body)
	if err != nil {
		return nil, err
	}
	target := path.Join(cleanDir, rel)
	if res.ExitCode == uploadExitExists {
		return nil, fmt.Errorf("%s %w", target, ErrExists)
	}
	if res.ExitCode != 0 {
		return nil, failure(res, "could not upload "+target)
	}
	return parseUploaded(res.Stdout, target, size), nil
}

// parseUploaded reads back where the host put the file, falling back to where
// it was asked to.
func parseUploaded(out, asked string, size int64) *Uploaded {
	found := sections(out)
	up := &Uploaded{Path: first(found["path"]), Size: size}
	if up.Path == "" {
		up.Path = asked
	}
	if n, err := strconv.ParseInt(first(found["size"]), 10, 64); err == nil {
		up.Size = n
	}
	return up
}

// CleanRelPath checks a path relative to a directory, as an upload names its
// place inside the folder it came from. It is slash-separated, has no empty,
// "." or ".." segments, and so can only ever name something under the
// directory it is joined to.
func CleanRelPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	switch {
	case p == "":
		return "", invalid("a file name is required")
	case strings.HasPrefix(p, "/"):
		return "", invalid("the name must be relative to the folder, not start with /")
	case strings.ContainsAny(p, "\x00\n"):
		return "", invalid("the name contains a character that cannot be used")
	}
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "":
			return "", invalid("%q is not a file name: it has an empty part", p)
		case ".", "..":
			return "", invalid("%q is not a file name: it has a %q in it", p, seg)
		}
	}
	return p, nil
}
