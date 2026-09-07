package deploy

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
)

// A health check is written once against an app and then used on every host
// that app lands on, so the one thing it has to be able to say is "whichever
// machine this is". An install command says that with {{host}}, but a health
// check is a URL, usually typed on a phone, and four braces is a lot to reach
// for there. Targets take {HOST} as well — one pair of braces, any case — and
// the same values go in.
//
// Single braces are deliberately not accepted in install commands, where
// `awk '{print}'` and `${VAR}` are ordinary things to write and a substitution
// would be a bug rather than a convenience.

// targetPlaceholder matches {{name}} or {NAME} in a health check target. The
// two-brace form is listed first so it wins on a target that uses it.
var targetPlaceholder = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}|\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}`)

// writtenSuffix matches the start of a domain suffix the check writes itself,
// as in {HOST}.local. A bare dot is not one: it is punctuation.
var writtenSuffix = regexp.MustCompile(`^\.[A-Za-z0-9]`)

// RenderTarget substitutes placeholders in a health check target. Names are
// matched exactly first and then without regard to case, so {HOST} finds the
// host and {PORT} finds a parameter called port. When quote is true the values
// are wrapped as shell words — a systemd unit name ends up in a command, a URL
// does not.
func RenderTarget(template string, values map[string]string, quote bool) (string, error) {
	folded := foldNames(values)
	var out strings.Builder
	var unknown []string
	last := 0
	for _, m := range targetPlaceholder.FindAllStringSubmatchIndex(template, -1) {
		name := ""
		if m[2] >= 0 {
			name = template[m[2]:m[3]]
		} else {
			name = template[m[4]:m[5]]
		}
		key, value, ok := resolve(values, folded, name)
		if !ok {
			unknown = append(unknown, name)
			continue
		}
		if key == VarHost && writtenSuffix.MatchString(template[m[1]:]) {
			value = hostLabel(value)
		}
		if quote {
			value = ShellQuote(value)
		}
		out.WriteString(template[last:m[0]])
		out.WriteString(value)
		last = m[1]
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return "", fmt.Errorf("unknown placeholder(s): %s", strings.Join(dedupe(unknown), ", "))
	}
	out.WriteString(template[last:])
	return out.String(), nil
}

// foldNames indexes the available names by their lowercase form, so a target
// written in capitals still finds them. A name that is already lowercase wins
// its own slot, which keeps the answer the same on every render.
func foldNames(values map[string]string) map[string]string {
	folded := make(map[string]string, len(values))
	for name := range values {
		lower := strings.ToLower(name)
		if existing, ok := folded[lower]; !ok || (existing != lower && name == lower) {
			folded[lower] = name
		}
	}
	return folded
}

// resolve looks a placeholder up, exactly and then case-insensitively, and
// reports which name answered — the caller cares whether it was the host.
func resolve(values, folded map[string]string, name string) (key, value string, ok bool) {
	if v, found := values[name]; found {
		return name, v, true
	}
	if k, found := folded[strings.ToLower(name)]; found {
		return k, values[k], true
	}
	return "", "", false
}

// hostLabel is the first part of an address — "pi5" out of "pi5.local" — for
// checks that write the rest of the name themselves, as in {HOST}.local. Doing
// this means one URL works whether HostMan reaches a host as `pi5` or as
// `pi5.local`, instead of quietly asking for `pi5.local.local`. An address
// that is an IP has no labels to take and is left whole, so the check fails
// visibly on a URL nobody meant to write rather than dialling 192.local.
func hostLabel(address string) string {
	if net.ParseIP(strings.Trim(address, "[]")) != nil {
		return address
	}
	if i := strings.Index(address, "."); i > 0 {
		return address[:i]
	}
	return address
}
