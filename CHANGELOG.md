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

## v2026.11.85

### Torrents

- **A list of picked `.torrent` files is added several at a time.** Files went
  to the host one after another, and each add was followed by a full read of
  the downloader — two SSH sessions and two runs of `deluge-console` per file,
  which on a Pi made a season take minutes. Now four go at once, each add
  answers only whether deluge took the torrent, and the downloader is read
  once when the list has gone. The first file still goes alone, so a stopped
  daemon is started once.

## v2026.11.84

### Torrents

- **Add several `.torrent` files in one go.** Picking files on the Torrents
  screen now puts them on a list, and each further pick adds to it; **Add N
  files** hands every one to deluge as a torrent of its own, saying which is
  going (`Adding 2 of 5…`). The list is there because a phone's picker cannot
  be relied on for several files at once — some hand over one, some hand over
  nothing when several were ticked — so picking one at a time builds the same
  list. One that is not a torrent, or that deluge will not take, does not cost
  the rest: the others are added, and the screen names the ones that were not
  and why.
- **Pick a folder of `.torrent` files.** For a phone whose picker gives up one
  file at a time, **Pick a folder of .torrent files** takes every `.torrent`
  under the folder, subfolders included, onto the list in one go. Whatever else
  is in the folder is left out rather than reported.

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
