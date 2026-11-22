# Changelog

Releases are `vYEAR.MONTH.PATCH` — a calendar version, where YEAR.MONTH is the
month of the release's commit and the patch number is the repository's commit
count, so `v2026.11.42` is the 42nd commit, made in November 2026. See
[`server/internal/version/version.go`](./server/internal/version/version.go).

Each section below is the body of the corresponding GitHub release, and is what
cuts it: `.github/workflows/release.yml` publishes a commit only when this file
has a `## <version>` heading naming that commit's own version exactly. A push to
`main` without one is an ordinary commit; a tag without one is refused. Work not
yet released sits under `## Unreleased`, which never matches a version — rename
it to the version when cutting the release.

## Unreleased

### Torrents

- **Pick several `.torrent` files at once.** The picker on the Torrents screen
  now takes as many files as you choose and hands each to deluge as a torrent
  of its own, saying which one is going (`Adding 2 of 5…`). One that is not a
  torrent, or that deluge will not take, no longer costs the rest: the others
  are added, and the screen names the ones that were not and why.

## v2026.11.79

**Downloads:** `hostman-<version>-linux-arm64` and `-linux-amd64` (the server,
static, with the web UI built in) and **`hostman.apk`** (new: HostMan for
Android — on the phone, open
`https://github.com/chinmay28/hostman/releases/latest/download/hostman.apk` and
install it; see [`docs/android.md`](./docs/android.md)). All are in
`SHA256SUMS`. `scripts/quickstart.sh` still builds from source on the machine,
as before.

### HostMan for Android

A phone app for HostMan, in two modes you can switch between in its settings:

- **Connect to a server.** HostMan runs on your Pi and the phone opens it. Type
  the address, or tap **Find on this network** to list every HostMan that
  answers on your Wi-Fi.
- **Run HostMan on this phone.** The whole of HostMan runs on the phone — its
  SSH key, its database, deployments, shells — with nothing else to keep
  running. It is always behind a PIN (yours, or a secret only the app knows),
  finds `.local` hosts by multicast DNS, and can serve other devices on the
  Wi-Fi when you give it a PIN for them. It cannot manage the phone itself or
  update itself: a newer `hostman.apk` is the update.

Either way the app shows HostMan's own web interface, so everything the browser
does, the app does. What it adds:

- **Deployments keep reporting after you leave.** Open a running deployment and
  it follows you into the notification shade with its latest output, then says
  how it ended. Tap it for the log.
- The file picker for uploads, other sites in the phone's browser, the app's
  settings from HostMan's Settings tab or a long press on its icon, and the
  developer badge on the first screen.

### On the server

- **Answers the app's network search** on UDP port 8900, from private
  addresses only. `-discovery-port 0` turns it off; `-name` sets what it is
  listed as.
- New flags for a HostMan that something else runs, all inert by default:
  `-managed-by` (no home host and no self-update), `-forward-bind` (where port
  forwards listen), and `HOSTMAN_DNS_FILE` / `HOSTMAN_DNS` / `HOSTMAN_TZ` for a
  host without `/etc/resolv.conf` or a zone of its own.

### The version comes from the commit

The version is still `vYEAR.MONTH.PATCH`, but YEAR.MONTH is now the month of
the commit being built rather than constants bumped by hand, so
`make bump-version` is gone. `make version` still prints it.
