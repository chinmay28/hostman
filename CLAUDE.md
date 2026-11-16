# Working in this repository

## The version comes from the commit

The version is `vYEAR.MONTH.PATCH`. YEAR.MONTH is the month of HEAD's committer
date, in the committer's own timezone, and PATCH is the commit count
(`git rev-list --count HEAD`); `scripts/version.mjs` is the one place it is
assembled, and `server/internal/version` receives it at link time as `Patch`
and `Date`. There is nothing to bump: the line moves by itself with the first
commit of a new month, and a rebuild of an old commit still reports what it
shipped as. Never take the version from the build clock in a build that has a
commit to ask, and never write one by hand — not in source, not in a test (an
asserted literal would change with every commit).

Every build stamps both values through the script — `make`, the installer, the
release workflow and the Android app's Gradle build — so the binary, the PWA
and the APK always agree. A shallow clone reports patch 0 on purpose (the count
would be a plausible lie); anything that builds a release needs the whole
commit graph (`fetch-depth: 0`, `--filter=blob:none`).

`make version` prints the version this tree would build as.

A release is cut by a `## vYEAR.MONTH.PATCH` section in `CHANGELOG.md` naming
the commit's own version (see `.github/workflows/release.yml`). Describe work
under `## Unreleased` at the top; the maintainer renames it when releasing.

## The Android app

`apps/android` is HostMan's web UI in a web view, plus the real server for
full-stack mode; `apps/android/README.md` says how and why. Two rules keep it
working:

- **Decisions live in `:core`** (plain Kotlin, JVM-tested, no Android
  imports); `:app` is platform glue. `./gradlew --no-daemon --max-workers=2
  -PcoreOnly :core:test` needs no SDK.
- **The server stays the same binary everywhere.** What the phone needs —
  `-managed-by`, `-forward-bind`, `-discovery-port`, `-name`,
  `HOSTMAN_DNS_FILE`/`HOSTMAN_DNS`/`HOSTMAN_TZ` (`internal/hostenv`) — is
  additive, and inert at its default except the network-search answer
  (`internal/discovery`, on unless `-discovery-port 0`). `core/ServerLaunch.kt` must only pass flags
  `cmd/hostman` declares (a test checks), and the discovery constants and the
  session cookie name are checked against the Go source the same way. Never run
  the phone's server without a PIN.

The page's hooks into the app go through `apps/web/src/lib/nativeApp.ts`,
feature-detected on `window.hostmanApp`, and must do nothing in a browser.
Icons come from `art/hostman-logo.png` via `make icons`; `make icons-check`
fails on drift.
