// Package version carries HostMan's version number.
//
// The scheme is calendar-based: vYEAR.MONTH.PATCH, where the patch number is
// the repository's commit count — every commit is a patch release, so
// `v2026.8.42` is the 42nd commit on the 2026.8 line. The month is written as
// a plain number, not zero-padded: that keeps the string valid semver, which
// forbids a leading zero, and nothing here orders versions by sorting text.
//
// Year and Month are declared here in source and moved to the current month
// when a branch opens (`make bump-version`, which CLAUDE.md asks for as the
// first thing on a branch) — deliberately not read from the build clock, which
// would move the version without a commit and make a rebuild of an old tree
// disagree with what it originally shipped. The patch number can only come
// from git, which a compiled binary has no access to, so it is stamped at link
// time instead:
//
//	go build -ldflags "-X github.com/chinmay28/deployer/server/internal/version.Patch=$(git rev-list --count HEAD)"
//
// `make build` does this for you via scripts/version.mjs, which is also where
// the PWA's build reads Year/Month from — keep the two constants below in a
// form that file's regex can still find.
package version

import "strconv"

// Year and Month of the release line: the month the current branch opened in,
// UTC. `make bump-version` rewrites them; keep each on its own line in this
// shape, which is what that script and the PWA build look for. Month is a
// calendar month, 1–12.
//
// There is no semantic major/minor: the leading numbers say *when* a release
// line opened, not what it promises about compatibility. What breaks on an
// upgrade is called out in the changelog, which is the thing to read.
const (
	Year  = 2026
	Month = 9
)

// Patch is the repository's commit count, stamped at link time (see the
// package comment). A bare `go build` leaves it at "0": patch 0 means an
// unstamped development build, never a release.
var Patch = "0"

// Stamped reports whether the patch number came from git. It is false for a
// bare `go build`, and for a build made where git could not be asked — from a
// tarball, or from a shallow clone (see scripts/version.mjs).
func Stamped() bool { return Patch != "0" }

// String renders the full version, `v`-prefixed to match how the project tags
// releases. This is the one rendering: it's what /api/health reports, what
// /api/self hands Settings, and what the PWA shows under its name.
func String() string {
	return "v" + strconv.Itoa(Year) + "." + strconv.Itoa(Month) + "." + Patch
}
