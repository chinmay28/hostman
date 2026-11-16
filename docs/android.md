# HostMan on Android

The HostMan app puts HostMan on an Android phone. It works one of two ways, and
you can switch between them at any time in the app's settings:

| Mode | What runs where | Use it when |
|---|---|---|
| **Connect to a server** (thin client) | HostMan runs on another machine — usually the Raspberry Pi the [installer](../README.md#install) put it on. The phone opens it, as a browser would. | You already run HostMan, and want it on your phone. This is the usual way. |
| **Run HostMan on this phone** (full stack) | The whole of HostMan runs on the phone: its SSH key, its database, the deployments, the shells, and the web interface. | You have no always-on machine to run it on, or want a HostMan in your pocket that does not depend on one. |

Either way the app shows HostMan's own web interface, so everything the browser
does, the app does: hosts, apps and deployments with their logs, shells,
services, files, cron, port forwards, Claude Code. This page covers what is
different in the app. How it is built is in
[apps/android/README.md](../apps/android/README.md).

## Install the app

Every [release](https://github.com/chinmay28/hostman/releases) attaches it as
`hostman.apk`, and the latest is always at:

```
https://github.com/chinmay28/hostman/releases/latest/download/hostman.apk
```

Open that address on the phone, download the APK, and open it. Android asks once
to let your browser install apps; allow it. A newer release installs over the
old one, keeping your settings and, in full-stack mode, your hosts and key.

It needs Android 10 or newer. Full-stack mode needs a 64-bit ARM phone (nearly
every phone sold since 2017); on any other, the app is a thin client only and
says so.

## The first screen

The app asks where HostMan runs.

- **Connect to a server.** Type its address — `nakedpi.local`,
  `192.168.2.123`, or `https://hostman.example.com` — or tap **Find on this
  network**, which lists every HostMan that answers on your Wi-Fi. An address
  with no `http://` or `https://` means `http://` on HostMan's own port, 8899.
  The search needs a HostMan from the same release as the app or later (it
  answers on UDP port 8900; `-discovery-port 0` turns that off); an older one is
  still reachable by typing its address.
- **Run HostMan on this phone.** HostMan starts on the phone and opens on its
  overview, with no hosts yet. Add them as you would anywhere: by address, with
  the SSH user's password once so HostMan can authorize its own key — or by
  running the two commands Settings shows on each host.

The app's version is under **Continue**; the developer badge is at the bottom.

## Following a deployment

Open a deployment that is running — start one, or tap it in an app's history —
and the app puts it in a notification: what is being deployed where, and the
last line it printed. Leave the app, lock the phone: the notification keeps
up, and when the deployment ends it says how — *Deployed CountRoster to
nakedpi*, or *… failed* with the reason. Tap it to open the deployment's log.

The deployment itself runs on HostMan, wherever that is; the notification only
watches it, and **Stop following** in it stops the watching, not the
deployment. If HostMan goes quiet — updating HostMan itself restarts it half way
through — the app keeps asking for ten minutes before it gives up. Turn the
notifications off with *Follow deployments in a notification* in the app's
settings.

## Settings

The app's settings are behind **⚙ App settings** in HostMan's own Settings tab
(under *This app*), and behind a long press on the app's icon, which works
whatever the server offers.

| Setting | Mode | What it does |
|---|---|---|
| Run HostMan on this phone | both | Full stack on, thin client off. If no server address is set yet, the app asks for one as you switch. Each mode keeps its own settings. |
| Server address, Find servers | thin client | Which HostMan the app opens. |
| PIN for other devices | full stack | The PIN a laptop or another phone signs in with. The app signs itself in either way. |
| Serve other devices on this network | full stack | Off: only this phone can reach its HostMan. On: other devices on the Wi-Fi can open `http://<phone's address>:8899`, with the PIN — which must be set first — and apps' port forwards listen for them too. The connection is not encrypted. |
| Keep running when the app is closed | full stack | Off: HostMan stops when you leave the app. On: health checks and port forwards carry on, and other devices can still reach it; a notification with **Stop** says it is running. |
| Name on the network | full stack | What a search from another phone lists this one as. |
| Port | full stack | 8899 unless something else on the phone has it. |
| Server log, Restart, Stop | full stack | What HostMan on the phone has said lately, and control over it. |
| Follow deployments in a notification | both | See above. |

Switching to a thin client stops HostMan on the phone but deletes nothing:
switching back finds its hosts as you left them.

## Full stack: what to know

- **It is never open.** A port on 127.0.0.1 is reachable by every app on a
  phone, and this server holds a key that runs commands as root on every host.
  So it always runs behind a PIN: yours if you set one, else a secret the app
  generated and keeps to itself. The app signs its own web view in; you never
  see a PIN screen unless you set a PIN and open HostMan from another device.
- **Where its data lives.** HostMan's database — with its SSH private key — is
  in the app's private storage, which no other app can read and Android's
  backups leave out. **Uninstalling the app deletes it.** A reinstall makes a
  new key, which every host then needs authorized again (Settings shows the
  commands, or *Set up access* on a host does it with its password).
- **Names on the home network.** Hosts called `nakedpi.local` are found by
  multicast DNS, as a Linux machine finds them through avahi; anything else is
  asked of the DNS servers of whichever network the phone is on.
- **When the phone sleeps**, Android slows down background work, even with
  *Keep running* on. A long deployment goes best with the screen on, or with
  the deployment's page open.
- **The phone is not a host.** HostMan on a Pi registers the Pi as its *home*
  host and updates itself from Settings. On a phone there is no sshd to reach
  and nothing to install into, so there is no home host, and Settings says
  that the app is what updates HostMan here: install a newer `hostman.apk`.

### What full stack cannot do

- **Manage the phone itself.** It has no sshd and no sudo; HostMan manages
  other machines from it.
- **Update itself.** *Update HostMan* is replaced by a note; a newer APK is the
  update.
- **Run while the phone is off the network.** Deployments, health checks and
  port forwards need the hosts to be reachable from wherever the phone is.
- **Run on 32-bit ARM or x86 phones.** Only `arm64-v8a` carries a server.

## Thin client: what to know

- **The server is the real one.** Deployments, health checks and forwards run
  on the Pi whether or not the app is open; the app is a window on it.
- **A PIN** set on the server (`HOSTMAN_PIN`) is asked for by the page, as in a
  browser, once per sign-in.
- **Updating HostMan** from Settings works as in a browser: the page says it is
  reconnecting while HostMan restarts, and the deployment's notification waits
  it out.

## Links, files and shells

- **Other sites** — an app's own page, a forwarded port, Claude Code's sign-in,
  the model library — open in the phone's browser. HostMan's own pages stay in
  the app.
- **Uploading** in the file browser opens Android's file picker; pick several
  files at once. Android's picker has no folder mode, so upload a folder's
  files instead.
- **Shells** are HostMan's own web terminal, as in a browser; there is no
  separate SSH client.
