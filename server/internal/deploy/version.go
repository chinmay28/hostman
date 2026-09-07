package deploy

import (
	"regexp"
	"sort"
	"strings"

	"github.com/chinmay28/deployer/server/internal/store"
)

// Nothing on a host reports back which version of an app is running there, but
// the deploy that installed it said so: an install command that can install
// more than one version takes it as a parameter — `ref`, `tag`, `version` —
// and HostMan keeps the parameters of the last successful deploy. So the
// version is read back out of those, the same way ports are, and shown beside
// the app: "On nakedpi · v1.4.0 · port 8899". An app whose command installs
// whatever is current names no version, and none is shown for it.

// versionWords are the parameter names that name a version, most explicit
// first. Where a command takes several, `version` is a better answer than
// `ref`, and a tag is a better answer than the commit it points at. Anything
// vaguer than these — `branch`, `channel` — would be guessing and is left out.
var versionWords = []string{"version", "release", "tag", "ref", "revision", "commit"}

var versionParams = compileVersionParams(versionWords)

// compileVersionParams builds the matcher for each word: the name itself, a
// name that ends in it (`app_version`, `git-ref`), or the camelCase spelling
// of the same (`imageTag`). "conversion" is not a version, so a word only
// counts where something separates it from what comes before.
func compileVersionParams(words []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(words))
	for _, w := range words {
		camel := strings.ToUpper(w[:1]) + w[1:]
		out = append(out, regexp.MustCompile(`(?i:(?:^|[_.-])`+w+`$)|[a-z0-9]`+camel+`$`))
	}
	return out
}

// maxVersionLen is how long a version can be before it stops being one. Tags,
// branches and commits are all far shorter; anything longer is some other
// value that happened to match the name, and it would only crowd out the rest
// of the line.
const maxVersionLen = 32

// InstallationVersion returns the version of an app that a host is running, or
// "" where the app's parameters do not say. Like ports, it is read off what was
// deployed rather than asked of the machine: it is the version HostMan put
// there, which is the version running unless somebody changed it by hand.
func InstallationVersion(in *store.Installation) string {
	if in == nil {
		return ""
	}
	for _, param := range versionParams {
		// Several names can match the same word — `version` and `app_version`
		// both do — so the shorter, plainer one wins, and equal lengths are
		// settled alphabetically rather than by map order.
		var names []string
		for name := range in.Params {
			if param.MatchString(name) {
				names = append(names, name)
			}
		}
		sort.Slice(names, func(i, j int) bool {
			if len(names[i]) != len(names[j]) {
				return len(names[i]) < len(names[j])
			}
			return names[i] < names[j]
		})
		for _, name := range names {
			if v := cleanVersion(in.Params[name]); v != "" {
				return v
			}
		}
	}
	return ""
}

// cleanVersion is the value as it should be read, or "" where it is not a
// version anybody wrote down: a version has no spaces in it and does not run
// on for a paragraph.
func cleanVersion(value string) string {
	v := strings.TrimSpace(value)
	if v == "" {
		return ""
	}
	if strings.ContainsFunc(v, func(r rune) bool { return r <= ' ' }) {
		return ""
	}
	// A full commit hash says the same thing in a seventh of the width, which
	// is how git itself shows one — and it is the one version long enough to
	// be worth shortening rather than dropping.
	if isHex(v) && len(v) >= 20 {
		return v[:7]
	}
	if len(v) > maxVersionLen {
		return ""
	}
	return v
}

// isHex reports whether a value is a hexadecimal string, which is what a
// commit hash is and what a tag almost never is.
func isHex(v string) bool {
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}
