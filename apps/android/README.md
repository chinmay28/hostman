# HostMan for Android — how it is built

The app (`apps/android`) is HostMan's own web interface in a web view, in front
of one of two servers: a HostMan on another machine (thin client), or the real
`hostman` binary running on the phone (full stack). Using it is in the
[guide](../../docs/android.md). It is modelled file for file on SAND Vault's
Android app in SAND Harbor.

```
 ┌──────────────────────── the phone ──────────────────────────────┐
 │  MainActivity                                                    │
 │   WebView ── HostMan's web UI, from Origin only                  │
 │     ▲  hostman-bridge.js (document start, Origin only)           │
 │     │  HostManAndroid ⇄ PageBridge: settings, watch, save, share │
 │     │                         │                                  │
 │     │                         ▼                                  │
 │     │                  DeploymentService ── GET /api/deployments/{id}
 │     │ thin client            full stack                          │
 │     │     │                http://127.0.0.1:8899  (PIN: the app's)
 │     │     │                     ▲                                │
 │     │     │   ServerService ── ServerSupervisor ── libhostman.so │
 │     │     │   (foreground)       restart, log    -managed-by …   │
 │     │     │                     ▲  HOSTMAN_DNS_FILE ← DnsWatcher │
 └─────┼─────┼─────────────────────┼────────────────────────────────┘
       │     ▼                     ▼
       │  http://nakedpi.local:8899   hosts, over SSH
```

## One interface, two servers

The web UI already does everything, and is built for a phone. A native client
would have to be written again for each screen and would fall behind the
moment the web gained one, so the app shows the server's page and adds only
what a web view lacks — and the one thing the web cannot do: report on a
deployment after the person has left.

Thin client and full stack differ in the origin the web view opens: the
configured server, or `http://127.0.0.1:<port>`. Everything else is the same
code keyed on that origin.

## The server on the phone

**The binary.** Full stack runs the real `hostman`, built by the app's Gradle
build (`buildHostmanWeb` then `buildHostmanServer`, before `preBuild`) with the
PWA embedded, and packaged as `lib/arm64-v8a/libhostman.so`: Android lets an app
execute files only from its native library directory, and only if they are
installed as files (`useLegacyPackaging`). `-PappServer=false` builds a thin
client without Go or Node.

It is `GOOS=linux GOARCH=arm64 CGO_ENABLED=0`, static, rather than
`GOOS=android`: HostMan's SQLite is `modernc.org/sqlite`, whose transpiled C
runtime wants a static Linux binary there (CountRoster's engine learned this).
Go's Linux port reads no zone database on Android, so the binary carries its
own (`-tags timetzdata`). 32-bit ARM and x86 are left out.

**What Android lacks**, and how the server is told (all additive; every flag
and variable is inert unset, so it is the same binary as everywhere else):

- `/etc/resolv.conf`. `DnsWatcher` writes the current network's DNS servers to
  a file on every network change and names it in `HOSTMAN_DNS_FILE`;
  [`server/internal/hostenv`](../../server/internal/hostenv) installs a resolver
  that re-reads it and asks those servers in turn (`HOSTMAN_DNS`, 1.1.1.1 and
  8.8.8.8, only while the phone names none). Names under `.local` are asked of
  multicast DNS as one-shot queries, which is what a Linux machine gets from
  avahi and a static binary never does.
- The local zone: Go on Android starts in UTC; `HOSTMAN_TZ` carries the phone's.
- A home host. HostMan normally registers the machine it runs on and updates
  itself through it. A phone has no sshd, no sudo and an APK instead of an
  installer, so the app passes `-managed-by "the HostMan Android app"`: no home
  host, no updater app, and `/api/self` says what updates HostMan instead, which
  Settings shows.
- `HOME`, `TMPDIR`, `XDG_*` and `USER` point at the app's own directories
  (`/sdcard` and `/data/local/tmp` are not writable; `os/user` has no
  `/etc/passwd`).

`core/ServerLaunch.kt` is the whole command line and environment, tested on its
own — including that every flag it passes is one `cmd/hostman` declares.

**Never open.** Every app on a phone can reach 127.0.0.1, and this server holds
a key that runs commands as root on every host. So it always runs behind
`HOSTMAN_PIN` (in the environment, not on the command line): the person's PIN
if they set one, else a random secret the app keeps in private storage. The app
signs its own web view in — `POST /api/session`, as the PIN screen does — and
hands the cookie to the web view's `CookieManager` before opening the page, and
again whenever the server restarts, since sessions live in its memory
(`core/SessionLogin`, `WebSession`). *Serve other devices* is refused until the
person has chosen a PIN of four characters or more.

**Binding.** Loopback by default, discovery off, port forwards on 127.0.0.1
(`-forward-bind`). *Serve other devices* binds `0.0.0.0`, answers the network
search on UDP 8900, and lets forwards listen on every interface.

**Keeping it running.** `ServerService` is a foreground service of type
`specialUse` owning one `ServerSupervisor`, exactly as SAND Vault's: health
wait on `/api/health`, restart with backoff, left down after five exits in two
minutes with the log's tail, SIGTERM (HostMan records an outcome for every
deployment in flight) then SIGKILL, and a stale server from an earlier run found
in `/proc` by its program path and ended first.

## Native where it pays

- **Deployments in a notification** (`DeploymentService`,
  `core/DeploymentWatch`). The deployment page calls
  `window.hostmanApp.watchDeployment(id, title)` for a running deployment
  (`apps/web/src/lib/nativeApp.ts`, nothing in a browser). A foreground service
  polls `GET /api/deployments/{id}` every 3 s with the web view's cookies, shows
  the last line of output (colour codes and progress-bar carriage returns
  resolved), and posts how it ended on its own channel. An unreachable server is
  retried every 10 s for ten minutes — a self-update restarts HostMan half way —
  before it gives up. It watches; it never runs or stops anything.
- **Find on this network** (`LanDiscovery`, `core/DiscoveryProtocol`, the Go
  responder in [`server/internal/discovery`](../../server/internal/discovery)):
  a UDP broadcast, answered with port, name and version, only from private
  addresses. `DiscoveryProtocolTest` checks the constants against the Go source.
- **Downloads and the share sheet** (the bridge, `Downloads`, `Sharing`,
  `SaveToDownloadsActivity`): ported whole from SAND Vault. HostMan's page does
  not download files today; the bridge makes `<a download>` and
  `navigator.share({files})` work the day it does, into *Downloads/HostMan*.
- **The developer badge** on the setup screen, flashed full screen natively
  (`DevFlash`, `core/DevFlashCurve` — the web's keyframes, checked against
  `styles.css` by a test).

Left to the web on purpose: the shell (HostMan's web terminal already works in
a web view; an `ssh://` hand-off would need a key the phone app has no business
holding), and every management screen.

## Navigation and the bridge

`core/NavigationPolicy` keeps only the origin's pages (and `blob:`/`data:`) in
the app; other `http(s)` — an app's page, a forwarded port, Claude Code's
sign-in — goes to the phone's browser; `intent:` is sanitised as Chrome does;
`javascript:`, `file:`, `content:` and `ws:` navigations are refused.

`assets/hostman-bridge.js` and the `HostManAndroid` object are given to the
origin alone (`addDocumentStartJavaScript` and `addWebMessageListener` with
that origin as the only rule). Messages are `core/Bridge.kt`: `hello`,
`settings`, `watch`, and the chunked file transfer (`begin`/`chunk`/`end`,
`fetch`, `share`) whose limits `core/Transfers.kt` enforces. The page side is
tested against the real script in `apps/web/src/lib/bridge.test.ts`.

## Code layout

```
apps/android/
├── core/   plain Kotlin, JVM-tested, no Android:
│           Origin, NavigationPolicy, AppConfig, ModeSwitch, ServerLaunch
│           (+ DnsList, MachineName), ServerSupervisor (+ RestartPolicy,
│           StaleServers), Health, SessionLogin, DeploymentWatch, Bridge,
│           Transfers, FileNames, AcceptTypes, DiscoveryProtocol, DevFlashCurve
└── app/    the Android shell around it:
            MainActivity, PageBridge + assets/hostman-bridge.js, WebSession,
            ServerService + LocalServer + DnsWatcher, DeploymentService,
            Downloads, Sharing, SaveToDownloadsActivity, SetupActivity,
            SettingsActivity, DevFlash, LanDiscovery, ConfigStore
```

```sh
./gradlew --no-daemon -PcoreOnly :core:test        # no Android SDK needed
./gradlew --no-daemon :app:lintDebug :app:assembleRelease
make android                                       # from the repo root, version-stamped
```

The icons are cut from `art/hostman-logo.png` by `make icons`
([tools/icongen](../../tools/icongen/README.md)). The version is the
repository's: `-PappVersionName`/`-PappVersionCode` (the commit count) from
`scripts/version.mjs`, `dev`/`1` unstamped. Releases are signed with one fixed key, read from the `ANDROID_KEYSTORE_BASE64`
and `ANDROID_KEYSTORE_PASSWORD` repository secrets (a PKCS12 keystore, alias
`harbor`, shared by every Harbor app), so each release installs over the last.
A build without them, any local build included, falls back to the debug key.
