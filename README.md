# HostMan

A general-purpose host manager for machines on a network. Add a host by
address (`nakedpi.local` or `192.168.2.123`), point HostMan at an app's
one-line install command, and deploy, watch, and redeploy it later from your
phone in one tap.

It runs on the same kind of box it manages: a single static Go binary that
serves both the REST API and a mobile-first PWA, SQLite for state, no runtime
dependencies.

## Install

On the machine you want to run HostMan on — a Raspberry Pi is the intended
home:

```sh
curl -fsSL https://raw.githubusercontent.com/chinmay28/hostman/main/scripts/quickstart.sh | sudo bash
```

Then open `http://<that machine>:8899` on your phone and add it to the home
screen.

Re-run the same command to upgrade. Upgrades build first and only then touch the
running service: the database is snapshotted, the new binary is health-checked
after it starts, and if it doesn't come up the previous binary and database are
restored automatically.

```sh
# a PIN for the web UI, a different port, a specific version
curl -fsSL .../quickstart.sh | sudo DEPLOYER_PIN=1234 DEPLOYER_PORT=9000 DEPLOYER_REF=v1.0 bash

# remove the service (your data and SSH key are kept)
curl -fsSL .../quickstart.sh | sudo bash -s -- --uninstall
```

## The home host

HostMan recognises the machine it runs on and registers it as a host, tagged
**Home**. Recognition is by `/etc/machine-id`, so it holds however you reach the
machine — `127.0.0.1`, a LAN address, or `nakedpi.local` all resolve to the same
identity.

The home host is an ordinary host in every other respect: HostMan still
connects to it over SSH, and still needs its key authorized and passwordless
sudo. That is deliberate. Running install commands directly would mean running
them inside HostMan's systemd sandbox, where `NoNewPrivileges=yes` blocks
`sudo` outright — going through SSH keeps the sandbox intact.

Delete the home host if you don't want it; a restart won't bring it back.

## Updating HostMan

Settings shows the running version with an **Update HostMan** button. It builds
from a git ref you can change at the confirmation step (branch, tag or commit),
and is recorded as a normal deployment with a full log.

The awkward part is that the update restarts HostMan half way through, killing
the process watching it — and, with it, the SSH session carrying the install
script, because `sshd` hangs up a remote command when its client disappears. So
a self-update on the home host runs **detached**: `nohup setsid`, output to a
file on the host, with the exit status recorded by an `EXIT` trap so an install
that ends in `exit 1` still reports one.

HostMan follows that file. When it is restarted by the update it hands the
deployment over rather than recording a verdict, and the process that comes back
picks the same file up, replays everything written while it was gone, and
finishes the record.

**The outage is expected, and treated as such.** While HostMan is down the UI
says "Can't reach HostMan — reconnecting…" and keeps showing the last state it
reported, rather than an error; the log page reconnects on its own; and health
checks are suppressed for any app with a deployment in flight, so HostMan does
not report itself as "Not responding" while it is the thing restarting. After a
deployment, a health check retries for a minute before calling anything
unhealthy, which is what a service that restarts needs.

## Adding a host

HostMan generates its own ed25519 keypair on first run and keeps it in its
database. There is no other key material to manage. A host needs two things
before HostMan can use it: that public key in the SSH user's
`authorized_keys`, and passwordless sudo for the user, since install commands
end in `| sudo bash`.

**The add-host form will do both for you.** Give it the SSH user's password and
HostMan signs in with it once, appends its own key, writes
`/etc/sudoers.d/deployer` through `sudo -S`, and then reconnects with the key
alone to prove it worked — reporting each step as it goes. Every step is
idempotent, so a partial run is simply repeated: hosts added earlier, or a
first attempt that failed, get the same thing from **Set up access** on the
host's page.

The password is used for that one connection and nothing else. It is never
written to the database, never logged, and never sent back to the browser; it
reaches the host over the SSH handshake and `sudo`'s stdin, never a command
line where the host's process list would show it. The sudoers file is checked
with `visudo` before it is moved into place, because a malformed drop-in breaks
`sudo` for everyone on the machine.

Leave the password empty and nothing changes from before: the host is saved and
you run the two commands from Settings on the machine yourself, signed in as the
user HostMan will connect as.

```sh
# 1. trust HostMan's key
mkdir -p ~/.ssh && chmod 700 ~/.ssh && echo 'ssh-ed25519 AAAA... deployer' >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys

# 2. allow unattended installs, since install scripts end in `| sudo bash`
echo "$(whoami) ALL=(ALL) NOPASSWD:ALL" | sudo tee /etc/sudoers.d/deployer >/dev/null && sudo chmod 440 /etc/sudoers.d/deployer
```

That is also the route for a host with password logins turned off, which is what
HostMan reports if the handshake is refused.

**Test connection** checks all of it at once — reachability, key auth,
passwordless sudo — and tells you which part is missing.

A host's SSH key is pinned the first time HostMan connects. If it ever changes,
connections fail loudly rather than quietly trusting the new one.

## Adding an app

An app is a name plus the one-line command that installs it:

```
curl -fsSL https://raw.githubusercontent.com/chinmay28/countroster/main/scripts/quickstart.sh | sudo bash
```

Anything you want to vary per deployment becomes a **parameter**, written as
`{{name}}` in the command. `{{host}}`, `{{hostname}}` and `{{user}}` are always
available and refer to the target host.

Parameter values are substituted as complete shell-quoted words, so a value can
never add commands of its own. That also means you must not put quotes around a
placeholder yourself — `--port {{port}}`, not `--port "{{port}}"` — and HostMan
rejects the app rather than silently producing literal quotes.

Install commands run with `pipefail`, so a failing `curl` in
`curl ... | sudo bash` fails the deployment instead of being masked by bash's
exit status, and with `DEBIAN_FRONTEND=noninteractive` so package managers don't
stop to ask questions.

How an app comes back off a host is a second command, declared beside the
first, because an install script rarely knows how to undo itself:

```
curl -fsSL https://raw.githubusercontent.com/chinmay28/countroster/main/scripts/quickstart.sh | sudo bash -s -- --uninstall
```

It is written the same way, takes the same parameters, and is checked when the
app is saved rather than when somebody is trying to remove something. It is
optional, and an app without one simply has no **Uninstall** button — see
[Removing an app from a host](#removing-an-app-from-a-host).

An app can also declare a **health check** — an HTTP URL (`http://{HOST}:8787/`)
or a systemd unit (`countroster.service`) — which is what turns "the script ran"
into "the app is actually running". It runs after each deploy and every minute
after that.

One health check covers every host the app is deployed to, so it is written
against the host rather than a machine: `{HOST}` becomes whichever machine is
being checked. A health check takes a single pair of braces in any case —
`{HOST}`, `{host}`, `{PORT}` — which is a great deal easier to type on a phone
than `{{host}}`, though that still works. Install commands keep to `{{name}}`
only, because `awk '{print}'` is an ordinary thing to write in a shell command
and substituting into it would be a bug.

`{HOST}` fills in the host's address, and it also fills in sensibly when the URL
writes part of the name itself: `http://{HOST}.local:8123/` reaches `pi5.local`
whether HostMan knows that host as `pi5` or as `pi5.local`, rather than asking
for `pi5.local.local`.

Between the health check URL and a parameter called `port`, an app has usually
already said which **ports** it answers on, so HostMan reads them back out and
shows them wherever the app is listed: `on nakedpi · port 8787 · updated 3 min
ago`. Knowing an app is healthy is only half of knowing where to open it.
Nothing is scanned and nothing is guessed — an app that declares neither shows
no port at all.

The **version** running there comes from the same place. An install command that
can install more than one version takes it as a parameter — `ref`, `tag`,
`version` — and HostMan keeps the parameters of the last deploy, so the card
reads `On nakedpi · v1.4.0 · port 8787 · updated 3 min ago`. A full commit hash
is shortened the way git shows one; an app deployed to several hosts shows the
version they agree on, or `2 versions` when they don't. An app whose command
installs whatever is current pins no version, and none is claimed for it.

An address is also a link, so every app HostMan can place gets an **Open**
button — on the dashboard, in the Apps tab and on the app's own page — which
hands it to the phone's browser, outside the PWA, where it can be bookmarked and
shared like any other page. It opens the app's own root rather than the health
path: `/healthz` is where an app reports on itself, not where it greets a
person. An app deployed to several hosts asks which one instead of picking. An
app on the machine HostMan runs on is registered under `127.0.0.1`, which on a
phone means the phone, so the link uses the address you reached HostMan at
instead. An app that has declared neither a URL nor a port gets no button, on
the same grounds as the missing port: a dead link is worse than none.

## Deploying

Deploy picks a host, shows the parameters, and shows the exact command that will
run. The log streams live to your phone while it runs, and can be canceled.

A successful deployment records an installation, which is what makes the next
one a single tap: **Redeploy** reopens the same confirmation prefilled with the
parameters you used last time. Deployments of the same app to the same host are
serialized — a second one is refused while the first is running.

## Removing an app from a host

There is more than one thing "remove" can mean here, and HostMan keeps them
apart because they are not interchangeable.

**Uninstall** runs the app's uninstall command on the machine. It is a
deployment in every respect — same confirmation showing exactly what will run,
same live log, same record in the history — and it goes through the app's
parameters, using the ones that install was given, because undoing an install
generally has to know what it was told: the port it took, the directory it
unpacked into. Once the command succeeds HostMan forgets the installation, and
with it the health check that would otherwise keep asking after something
deliberately removed. The log stays: it is the record of what was run.

A failed uninstall keeps the installation. The app may well still be there, and
a record that quietly disappeared is the worse of the two wrong answers — so the
app stays listed, with the failure to read.

**Forget** is the other one: it drops HostMan's record and touches the host not
at all. That is what you want for an app you removed by hand, or one whose host
is never coming back.

Two refusals. An app that declared no uninstall command cannot be uninstalled —
the button isn't there, and the API says so rather than running nothing and
calling it a success. And **HostMan will not uninstall itself from the machine
it runs on**: an update survives the restart it causes by running detached and
being picked back up afterwards, but an uninstall has nothing left to pick it
back up, and would take the log, the record and the UI down with it half way
through.

Deleting the app itself is a third thing again, and is still HostMan-only:
it removes the app and its history from HostMan, and anything already installed
on a host keeps running there. The confirmation says which hosts those are,
since after the delete there is nothing left to remove them with.

## Managing a host

A host's page has a **Manage** section for the jobs that otherwise mean finding
a laptop and an SSH client.

**Files** browses the machine. Tap a directory to descend, a file to read it,
and **Edit** to change it — a config, a unit file, a script. There is no agent
and no file transfer: a listing is one `find` (or a `stat` loop where busybox
has no `find -printf`), and a file's contents cross the wire base64-encoded so
nothing is lost to an encoding, a control byte, or a missing final newline.

Saving writes through a temporary file in the same directory and moves it into
place, so a reader sees either the old file or the new one, never half of each,
and a failure part way through leaves the original alone. An existing file keeps
its mode and its owner. A symlink is followed: editing `/etc/resolv.conf` edits
what it points at rather than replacing the link.

**Permissions** are set from the same sheet the rename and delete live on, laid
out as the three-by-three grid the octal digits already are: a row per audience,
a tap per bit, with the digits themselves still editable for anyone who arrived
knowing they want 640. On a folder, *everything inside it too* is `chmod -R`,
and it means what it says — the same mode reaches the files under it, not only
the directories. That is what turns 755 into 777 in one go, and it is also how a
folder of configs becomes a folder of executable configs, so it is off unless it
is asked for and the sheet says so before it runs. A symlink has no permissions
worth setting, so the mode goes to what it points at, the way editing one does.
Only octal is accepted — `u+x` never reaches a shell — and `/` is refused.

Two things are deliberately not editable. A file over 512 KB comes back
truncated, and saving the part you can see would throw the rest away; a binary
file is shown as binary rather than run through a textarea that would mangle it.

**Services** is every systemd service and timer someone installed on the
machine by hand — the unit files under `/etc/systemd/system` and
`/usr/local/lib/systemd/system`. The distribution's own hundreds, in
`/usr/lib/systemd/system`, are not what you pick up a phone to fix, so they are
left out; where a unit file lives is the same rule systemd itself uses to decide
who wins. Filter chips cut the list four ways and put failures first.

Timers are listed beside the services because a scheduled job is written as a
pair — a `.service` with no `[Install]` section and a `.timer` that starts it —
and the timer is the half that carries the schedule and the half that gets
enabled. Listing only services showed such a job as a unit nothing appeared to
start, and a timer-only install, where the thing being scheduled is the
distribution's, showed nothing at all. A timer's row says when it next fires
rather than how long it has been waiting, and its screen says what it starts
instead of a memory figure and a PID it does not have. Sockets, paths and
targets are still left alone: naming them is one thing, and HostMan names the
ones that matter, but managing them from a phone is another.

Tapping one opens what it is doing — running, failed and why, for how long, its
memory and its PID — with **Start**, **Stop**, **Restart** and **Reload**, a
toggle for whether it comes back after a reboot, and the tail of its journal at
50, 200 or 1000 lines. HostMan waits for `systemctl` to finish rather than
returning as soon as the command is sent, so a service that takes half a minute
to come up takes half a minute to answer.

The memory figure and the PID are the two systemd most often declines to give,
and a dash where a number belongs reads as HostMan not asking. `MemoryCurrent`
is `[not set]` unless the unit's cgroup has memory accounting turned on, which
has been the default only since systemd 238 and still needs the controller
present on a cgroup v1 host; `MainPID` stays 0 for the life of a `Type=forking`
service whose `PIDFile=` systemd could not follow. Neither fact is missing — the
kernel is still counting that cgroup and still has the processes in it — so
where systemd has no answer HostMan reads the cgroup itself, in either
hierarchy, and takes the PID from the processes in it. A host that accounts for
no memory at all leaves adding up what the unit's processes have resident, which
counts shared pages more than once and is shown as the approximation it is.
Where there is genuinely nothing to show — a `Type=oneshot` unit that is active
with nothing running has no process to have a PID — the screen says which of
those it is rather than leaving a dash to be read as a failure.

A unit with no `[Install]` section has nothing to enable — systemd calls it
`static`, and it runs only when something else pulls it in. Rather than leave
that as "started by another unit", the screen asks systemd which one and names
it: the socket, timer or path unit that activates it, or whatever wants,
requires, binds to or upholds it, most specific first. Services among them link
to their own screen. `PartOf` and `Requisite` are left out, since the first only
propagates stop and restart and the second refuses to start rather than
starting. systemd only names units it has loaded, so nothing named means nothing
is pulling it in right now — which the screen says in those words rather than
claiming nothing ever will.

**Add a service** writes a service from six fields — what to run, as whom, from
where, and what to do when it stops — rather than from a blank unit file, with
the file it is about to write on screen the whole time for anyone who would
rather type it. systemd is what validates it: the file is written, systemd is
told to read it, and anything it refuses to load is taken straight back off the
disk, because a unit file in /etc/systemd/system that systemd cannot read is
worse than no file at all. It is created stopped, and starting and enabling it
are separate steps, so a service that will not start says exactly that instead
of looking like an install that half happened. A name systemd already knows is
refused: a unit of the same name in /etc/systemd/system does not replace the
distribution's, it shadows it, and doing that to sshd by accident from a phone
is not a mistake worth leaving available.

**Deleting** takes the unit file, the symlinks that start it at boot, and the
drop-in overrides that are meaningless without it, then clears the failed state
so a service that died on its way out does not haunt `systemctl --failed`.
Whatever the service actually ran is left where it is. Two things it will not
do: delete a service that is still running — stopping it is a decision the
person deleting it makes, because deleting the unit of a running process leaves
it up with nothing left to describe or stop it — and delete anything outside an
administrator's own unit directories, since /usr/lib belongs to the package
manager.

The unit file is editable on the same screen, and saving runs
`systemctl daemon-reload` straight afterwards — a unit file edited and not
reloaded is a change that has not happened, which is the most common way an edit
from a phone appears to do nothing. The service keeps running the old settings
until it is restarted, and the screen says so.

The state comes from one `systemctl show` per screen: key=value lines that parse
the same on every version worth supporting, and unlike `status` it never wraps,
colours or truncates what it says. None of these screens poll — each is an SSH
session, so they refresh when you ask.

**Remote session** runs a browser on the host and hands it to your phone. It is
the answer to the one job none of the rest of this can do: a file behind a login.
Some things can only be done by a person in front of a browser — signing in,
clicking through a consent page, agreeing to something — and the machine that
should end up holding the file is the host, not the phone. So the browser runs
there, and a download lands in the host's own Downloads directory, with no
transfer, no second copy and no phone in the middle.

Setting it up installs Xvfb, x11vnc, noVNC and a browser with apt, writes a
service, and stores a VNC password the screen shows you. That is minutes of
package installing on a Pi, so it runs **detached** — the same trick a
self-update uses, and for the same reason: an SSH session that ends takes its
command with it, and a phone that locks its screen must not be able to kill apt
half way through a package. The screen follows the log while it happens and says
what stopped it if something does.

**The session gets a screen of its own rather than the host's.** Attaching to the
real display is the familiar recipe and the wrong one here: it needs a desktop to
already be running, it cannot attach to a Wayland session at all — which is what
Raspberry Pi OS has run by default since Bookworm — and what it shows is whatever
the machine is showing to anyone walking past it. A private Xvfb screen works the
same on a headless Pi as on one with a monitor, and nothing on the real display
changes while you use it.

**It is off unless you turn it on.** The unit has no `[Install]` section, so
systemd calls it static: it cannot be enabled and does not come back after a
reboot. A logged-in browser is a bearer of every credential you typed into it,
and one that runs all week is a worse thing to own than one that runs for the two
minutes it takes to fetch a file. Start it, open it, take the file, stop it —
which is four taps, and the screen is laid out for exactly that.

**The profile survives, which is what makes the second visit a tap.** The browser
keeps its profile in the SSH user's home, so a site signed into last week is
still signed in this week, and the sign-in with the authenticator app happens
once rather than every time. It is also why removing the session asks whether to
take the profile with it: throwing away the logins silently would be the wrong
kind of quiet. Downloads are never removed either way.

Two details make it usable from a phone rather than merely possible. The address
bar is on HostMan's screen, not in the session — typing a URL into a browser
over VNC on a phone keyboard is the worst part of doing this by hand — so
starting the session and opening the site are one tap and one round trip. And
Chromium's "ask where to save each file" is turned off in the profile before it
first runs, so a download needs no dialog: it goes straight to
`~/Downloads`, which the same screen then lists, newest first, with a way
through to the file browser.

**A browser that cannot say its own version is not a browser this can use.**
Ubuntu ships both chromium and firefox as snaps, and `apt install chromium` there
quietly installs one — which fails on the spot in a session like this, printing
nothing: snap confinement walls the browser out of the hidden profile directory
in the home it is otherwise allowed into, and a system service has no user
runtime directory for snapd to work in. A path that resolves into `/snap` is the
obvious form of that and is skipped. The less obvious one is
`/usr/bin/chromium-browser`, which is not a symlink into `/snap` at all but a
shell script that calls out to it, so the file is read rather than trusted.

Neither check would catch the next thing, so there is a third that catches all
of them: the browser is asked its version, and one that cannot answer — a
wrapper for something that is not installed, a missing library, a half-finished
package — is not going to render a page either. Where nothing on the host can
answer, setup fetches Chrome as an ordinary package, which adds Google's
repository itself and so keeps updating like everything else. The screen names
what it found and why it was no good, because "no browser" and "a browser that
looks installed and will not start" are fixed by different things. On an
architecture with no package build of a browser, setup says that instead of
installing something that cannot run.

**A host that will not give the browser a sandbox gets a session without one
rather than no session at all.** Chromium refuses to start where the kernel
denies it a sandbox, which on a VPS is ordinary — unprivileged user namespaces
turned off, or a setuid helper the kernel will not honour — and the choice is
then between a browser with a weaker defence against the pages it visits and a
session nobody can use. A session nobody can use protects nobody, so it falls
back: once, after the second failure rather than in anticipation of one, saying
so in the journal and leaving a mark that puts a warning on the screen. Signing
into a bank on a browser without its sandbox is a decision, and decisions
belong to the person making them, in writing.

Updating HostMan does not reach back and rewrite the scripts already on a host
— a session somebody is using should not change under them — so a host set up by
an older version keeps running what it was given. That is only right if it is
said out loud, so HostMan stamps the scripts it writes with a hash of them and
the screen reports a host running an older set, with the button to write the
current one. Setting up again keeps the password and the profile; a running
session keeps the old script until it is stopped and started.

**A session that comes up black — an X screen with a mouse pointer and nothing
else — is the browser failing to start**, and that has any number of causes with
only one symptom. So the browser's own output goes to the session's journal
rather than to `/dev/null`, and the last of that journal is on this screen,
under the way in, rather than only on the Services screen. HostMan adds its own
line when the browser dies within seconds of starting, and names the binary it
resolved: a distribution that ships its browser as a snap wrapper leaves
something on the `PATH` that cannot run, and the resolved path is what says so.
Chromium's own two traps are handled before they happen — the singleton lock a
crashed browser leaves behind, which makes every later launch refuse, is cleared
on the way in, and it is started with the GPU and `/dev/shm` assumptions a
virtual screen on a small VM cannot meet turned off.

The link HostMan offers carries the VNC password in its query string. That is a
real trade-off and worth naming: it puts eight characters into a browser history
on your LAN, and what it buys is not typing them on a phone every time. The VNC
server itself only ever listens on the host's loopback interface — the one door
in is the noVNC port, which is `6080` unless you change it.

**Torrents** hands a torrent to the host and lets it do the downloading. Paste a
magnet link, or pick a `.torrent` file on the phone, and the files land on the
host's own disk. That is the whole point of it: a torrent opened on a phone
downloads over the phone's connection, onto the phone's storage, and then has to
be moved to the machine that was always supposed to have it. The phone is the
remote control; the host has the disk, the wired connection and the uptime.

**Deluge is the host's to install, and HostMan says so rather than installing
it.** Everything else on this screen is a package HostMan will fetch for you;
this one is not, and deliberately. A BitTorrent client is a decision about what a
machine does on a network, and on plenty of machines somebody has already made
it — a seedbox with its own daemon, a distribution that packages it differently,
a host where it has no business being at all. So a host without it gets the line
to run, copyable, and nothing else until it has been run: `sudo apt install -y
deluged deluge-console`.

**The daemon HostMan sets up is HostMan's own.** Its state lives in
`/var/lib/deployer-torrent`, it runs as the SSH user, and it answers on port
58946 rather than deluge's own 58846 — so a `deluged` the host already runs
carries on untouched, with its own torrents and its own client attached, instead
of one of the two losing a race to bind a port. There is still no agent: adding
a torrent is `deluge-console` over one SSH session, the same way everything else
here works. deluged authenticates its clients out of an auth file, and the
password in it is generated on the host and read there by the scripts that need
it — never stored by HostMan, never in an API response, never on a screen.

**It comes back after a reboot**, which is the opposite of the remote browser
session's rule and for the opposite reason. A browser holding your logins should
run for the two minutes you are using it; a download that takes six hours should
survive the machine restarting in the middle of it. So this unit has an
`[Install]` section and is enabled, and deluge picks each torrent up from its own
state. Adding a torrent to a stopped daemon starts it rather than refusing —
somebody who has just handed HostMan a torrent has said what they want clearly
enough.

The rest of the screen is a bar per torrent with its speed, its swarm and what is
left, pause and resume, and the disk behind the download folder — a torrent
filling a Pi's card is the ordinary way this goes wrong, and the figure belongs
on the screen before the download rather than after it. It is an ordinary
service, so its journal is on the **Services** screen with everything else.

**It keeps following after the download has finished.** A torrent that finishes
is not done — it seeds, and the ratio and the upload figure are what somebody is
watching then — so the screen carries on refreshing while the daemon is up,
slowly once nothing is downloading, and not at all while the phone is locked or
on another app. Coming back to it asks again straight away.

**An empty answer is not the same as an empty list**, and the screen no longer
confuses them. A probe that could not reach deluge — the daemon busy, the
console slow to start, the command cut short — used to come back with no
torrents and take a running download off the screen until you left and returned.
The probe now says which of the four things happened: it asked, the daemon is
stopped, there is no console, or there is no downloader. An answer that never
reached deluge leaves the last one on the screen and says that is what it is.

**What happens after seeding is deluge's own rule.** Left alone it seeds for
ever, which is the polite thing to do and is also how a list nobody is watching
fills up with things that finished last month. The screen sets a ratio — what a
torrent uploads against what it downloaded, which gives a slow torrent longer
than a fast one — and optionally has deluge drop the entry from the list when it
stops. That is `stop_seed_at_ratio`, `stop_seed_ratio` and
`remove_seed_at_ratio` on the daemon itself rather than anything HostMan
enforces, so it holds at three in the morning, which is when torrents finish.
Dropping the entry never touches the files: deluge removes the torrent, not the
download.

**How many run at once is a setting, not a surprise.** Deluge queues everything
past a limit, and its defaults are small — a handful active, the rest sitting at
0% as Queued, which reads as a stuck download the first time it happens. The
screen sets the limit, or takes it off entirely. It is one number over deluge's
three: `max_active_limit`, `max_active_downloading` and `max_active_seeding` are
all set to it, so the knob means what it says — this many at once, whatever they
are doing — and like the seeding rule it lives on the daemon itself.

Removing a torrent asks whether the part already downloaded goes with it, because
that is the one thing here that cannot be undone. Removing the downloader never
touches the files at all: the service and deluge's state go, deluge stays
installed, and everything already downloaded stays exactly where it is.

**Terminal** is a login shell on the host, with a pty behind it — the same
thing `ssh you@host` gives you, so `htop` draws, `vim` works, tab completion
completes and colours are colours. There is still no agent: it is one SSH
session with a terminal requested on it.

**The shell belongs to HostMan, not to the screen looking at it**, and
everything else about this follows from that. A phone drops its connection
constantly — the screen locks, another app comes to the front, wifi hands over
to cellular — and a terminal that lived in the page would lose the directory it
was in, the command half typed, and whatever was running, every single time. So
the shell is held open on HostMan's side and everything it prints is kept.
Going back is not closing: it stays open, the host list is still one tap away,
and coming back rejoins the same shell with its scrollback. Two phones can watch
one shell, and each says how many are. It closes itself after fifteen minutes
with nobody watching, or when you tell it to, or when you type `exit`.

That is also why the screen and the keyboard travel differently. Output arrives
as server-sent events, which HostMan can be asked to resume from a byte offset,
so a phone that was away for ten minutes reconnects and gets exactly what it
missed — and is told, in the terminal, when it was away long enough that
something scrolled past unrecoverably, because a terminal that jumps silently is
a terminal lying about what ran. Keystrokes go the other way as ordinary POSTs,
which need no connection to have stayed up at all. Both directions are base64:
a pty deals in bytes, half a character can legitimately end a chunk, and Ctrl-C
is one byte rather than a word.

**The keys a phone keyboard does not have are along the bottom** — `Esc`, `Tab`,
`^C`, the four arrows, and `Ctrl`, with a second row of the punctuation a shell
is made of and a phone buries two layers down. `Ctrl` is sticky rather than a
chord, because there is no holding a key down on a touchscreen: tap it, then tap
the letter. The bar refuses focus rather than taking it, which is the whole
trick — a key that focused itself would dismiss the keyboard, and a Tab key that
closes the keyboard is worse than no Tab key.

**Copy and paste are keys on that bar too**, because neither is a gesture a
phone offers over a terminal: xterm selects with a mouse and a phone has none,
and a drag over the screen means scroll. So Copy with nothing picked arms a
mode in which a drag takes lines instead — a line at a time, which is the
precision a finger has and the shape a command and its output have anyway, and
held against the top or bottom edge it keeps going, because what is worth
copying is usually taller than a phone. Copy again takes what is picked, and
Cancel leaves without it. Paste puts the clipboard in as one paste rather than
as typing — bracketed, when the shell asked for that — so a pasted script does
not start running itself a line at a time before all of it has arrived.

Both have a way round, and they need it: a browser hands a script the clipboard
only on a page it trusts, which means https or localhost, and HostMan is
usually plain http on a LAN. So copying falls back to the older way — a hidden
box, a selection, and a command a tap is allowed to run — and, if even that is
refused, to a box of the text with the phone's own Copy on it. Pasting falls
back to an empty box to paste into and send. Copy and paste work over plain
http, which is the whole point of having them here rather than leaving them to
the browser.

**How many columns fit is the other half of the problem**, so the text size is a
control rather than a setting, with the column count between the two buttons.
A phone held upright is about 55 columns at a comfortable size and about 70 at a
small one; turned sideways it is 150, which is more than most output assumes.
HostMan follows all of it: the terminal is sized to the *visual* viewport
rather than the window — so the soft keyboard covers the bottom of the screen
instead of covering the prompt — and the host is told the new window each time,
which is what makes a full-screen program redraw into the space you can actually
see. `tput cols` and HostMan agree, always.

**A shell runs as the user HostMan signs in as, and does not become root.**
That is the opposite of the rule everywhere else on this screen, and the
exception is deliberate: a file browser that could not open `/etc` would be
hiding the reality, but a prompt that says one thing and runs as another is how
a machine gets broken by somebody who was being careful. `sudo` is right there,
and typing it is a decision.

A shell is the general answer and usually not the quickest one, so it is the
last of the three ways in rather than the first: a config is fewer taps in
**Files**, and restarting something is one tap in **Services**.

**Claude** is Claude Code on the host, driven from a chat. Say what you want
done on the machine and watch it be done: the commands it runs, the files it
changes, and the questions it asks along the way appear as cards, and the
answer to a question is a button. It is the Terminal's other half — the same
machine, the same user, the same `sudo` — for the jobs where typing the
commands is the slow part.

**Getting it there happens once, on this screen.** HostMan installs the CLI
for the SSH user with Anthropic's own installer — into `~/.local/bin`, with no
sudo and nothing system-wide, and it keeps itself up to date from there. The
sign-in belongs to the host: `claude auth login` prints a link and waits for a
code, the phone is what opens the link, and the code the phone is shown goes
back through HostMan to the process waiting for it. The token the CLI ends up
with is written by the CLI into the user's home directory; HostMan never
reads it, never stores it, and never sends it anywhere. An API key is the
alternative, written into the CLI's own settings file on the host and forgotten
here. Both take longer than a request — a download onto a Pi, a person with a
phone — so both run detached on the host, and the screen follows the log.

**A session belongs to HostMan, not to the screen looking at it**, exactly as
a shell does. It is one `claude` process on the host, started over SSH in
print mode with its standard streams held open, speaking the CLI's own
line-per-message protocol; everything it says is kept as a numbered log of
events, and a phone that comes back asks for the events it missed. Leaving
the screen keeps the session open; coming back rejoins it with the whole
conversation; a second phone sees the same one. Left alone with nothing
running it closes after an hour, and a session Claude is still working in is
left alone until it is finished.

**Permission prompts are cards in the chat, not a modal.** In its default mode
the CLI asks before running a command or changing a file, and the question
arrives here with the exact command on it and three answers: allow, allow for
the rest of the session, deny. Two phones watching the same session both see
the question, and the one that answers settles it for both. **Skip all** is
the CLI's `--dangerously-skip-permissions`, chosen when a session starts and
never the default; a session running that way wears a red stripe the whole way
through, so there is no moment at which it reads like an ordinary one. The
model and the permission mode can both be changed mid-conversation, and the
CLI's acknowledgement is what the screen waits for before saying so.

**The conversation is the CLI's, so it survives HostMan.** Each session has a
CLI session id, and `claude --resume` on the host picks the same conversation
up in a terminal — which is also the way back in after HostMan has been
restarted, since the process itself dies with it.

**Scheduled jobs** edits the crontab, the whole file at once, the way
`crontab -e` does — for the user HostMan signs in as, and for root. Cron is
what validates it: a crontab it refuses to parse is not installed, the old one
stays, and its complaint is what you see. A user with no crontab yet is an empty
editor, not an error.

**Power** reboots or shuts the machine down. The command is scheduled a few
seconds ahead and detached, so HostMan gets a clean answer instead of the
connection dying mid-command and having to guess what that meant — the same
reason a self-update runs detached. The host is marked offline as soon as it
accepts, because it is about to be.

All of this runs as root wherever the SSH user has passwordless sudo, and as
that user where it doesn't; which one it was is shown on the screen. That is not
an escalation — HostMan already holds a key that can run anything on the
machine — but a file browser that could not open `/etc` would be hiding the
reality rather than limiting it.

## Why it restarted

A Raspberry Pi that reboots on its own is the one question a phone is worst at
answering, because the answer is never in one place. **Why it restarted** reads
all of them in a single SSH session and says what it thinks, with the evidence
under it.

There are four places to read. `wtmp` remembers when the machine came up and —
the valuable part — whether anything recorded a shutdown before it, which is the
one signal that survives on a host that keeps no logs at all. The end of the
previous boot's journal, at any priority, is where an orderly shutdown shows
itself. That same boot's warnings and worse are where a kernel panic, an
out-of-memory kill, a critical temperature, a soft lockup, an SD card giving up
and the Pi's under-voltage warning all are; filtering by priority is what makes
reading hours of log affordable on a Pi. And `vcgencmd get_throttled` is the
only place a Raspberry Pi records its supply having sagged.

The verdict is a guess and is labelled as one. It tells a restart something asked
for from a panic, a lock-up, an out-of-memory kill, a thermal shutdown, an
under-voltage, a storage failure and a plain power cut — and, often, from "it
went down without saying why", which is an honest answer and a common one.
Confidence reads **the machine said so** only where the machine said so in as
many words; everything inferred is at best the best explanation available. Each
sign keeps the log line it was found in, because a verdict nobody can check is
not worth much on a screen this size.

**When a line was logged matters as much as what it says.** A machine that ran
out of memory at nine and restarted at three did not restart because it ran out
of memory. Every sign carries how many seconds before the restart it was
written, and only the ones inside two minutes of it are allowed to be the cause.
The rest are still shown — as the weather rather than the event.

The most useful finding is often an absence. A machine that was asked to restart
says so at length; a machine that lost power says nothing at all and its log
stops mid-sentence, which cannot be told apart from a lock-up so complete the
kernel never got to write about it. HostMan says that instead of picking the
more dramatic of the two. It also counts: one unexplained restart is bad luck,
and four in a week is the reason someone opened the screen.

**A journal kept in memory is the commonest reason there is no answer at all.**
Debian, and so Raspberry Pi OS, leaves systemd's log in memory unless
`/var/log/journal` exists — so the log of the boot that crashed dies with the
boot that crashed. Where that is the case the screen says so in those words and
offers to fix it, since creating that directory is the whole of the fix. Failing
that, the lines immediately before the last kernel banner in `/var/log/syslog`
are exactly the end of the previous boot, and are read instead.

## Monitoring

No agent is installed on the host. HostMan opens an SSH session and reads
`/proc/stat`, `/proc/meminfo`, `/proc/loadavg`, every process's `/proc/*/stat`,
`df` and the thermal zone in a single round trip, sampling `/proc/stat` twice a
second apart so CPU usage is a real interval average rather than an average
since boot.

Hosts are polled every 30s in the background and every 5s while you have a host
open. Samples are kept for 24 hours.

**A number on its own does not say whether it is normal.** 40% CPU means one
thing on a machine that idles at 5% and another on one that sits at 35% all day,
so a host shows the range its CPU and memory moved through over the last 24
hours with the average marked inside it. The band is the day; the tick is the
mean. That is the whole of the retention window, so it is everything HostMan
knows about the machine. The arithmetic is done in SQL and only the six numbers
are sent — a phone polling every few seconds never carries a day of samples to
work them out.

**"Busy" is only half an answer, so a host also names what is using it**: its
five biggest consumers of CPU, and its five biggest consumers of memory. The CPU
figures are earned rather than read. `ps` reports a process's average since it
started, which on a machine up for a month says nothing about now — a backup
that pegged a core at 3am still looks like the busiest thing on an idle
afternoon. So /proc is walked at both ends of the same second the probe already
sleeps for, and what is reported is the jiffies a process gained in between over
the jiffies the whole machine gained. That makes each figure a share of the
machine on the same scale as the CPU meter above it: two processes at 25% on a
four-core Pi are half of it, not two cores of trouble. A process has to exist at
both ends of that second to be given a figure, so one that started inside the
window shows only its memory until the next poll.

The snapshot rides along with the sample — no second SSH session, no `top`
running anywhere — but it is the only one kept, in memory rather than in the
database. What is using a machine is a question about now, and a day of process
lists would cost more rows than the telemetry they sit beside. A restart of
HostMan forgets them until the next poll, which is seconds away.

## Security

HostMan holds a key that can run commands as root on every host you add. Treat
it accordingly:

- **Keep it on your LAN or Tailscale network.** Don't expose it to the internet.
- Anyone who can reach the UI can **browse, edit and re-permission any file on
  every host**, as root, and restart the machines. That follows from the key HostMan already
  holds rather than adding to it, but it does put a root shell's reach behind a
  web page — which is the whole argument for the PIN and for the LAN.
- The **Terminal is a shell**, and it is a shell for anyone who reaches the UI.
  It is the plainest statement of the point above rather than a new one: the key
  could always have opened one. It runs as the SSH user rather than as root, and
  a shell's handle is an unguessable id that is useless without a session — but
  neither of those is a defence worth leaning on, and the PIN and the LAN are.
  A shell left open holds an SSH connection until it is closed or is left alone
  for fifteen minutes.
- It runs **unauthenticated by default**. Set a PIN with `-pin` /
  `DEPLOYER_PIN` if anything less trusted can reach it.
- The **database contains the SSH private key**. The installer keeps it at mode
  700 under `/var/lib/deployer`; back it up like a secret.
- A **host password given during setup is never stored** — not in the database,
  not in the log. It exists for one request. It does cross the network to
  HostMan in the clear if you are on plain `http`, so set a host up from a
  network you trust, the same one you would type the PIN over.
- A **remote session is a signed-in browser** sitting on the host: whoever can
  reach its noVNC port and read its password — which this UI shows — is signed
  in to whatever you were. It is off by default, cannot be enabled to start at
  boot, and is worth stopping when you are done rather than leaving up.
- A **torrent downloader is a machine downloading whatever it is told to**, from
  strangers, as the SSH user, into a folder of your choosing. deluged listens on
  loopback only and HostMan never turns that off, but anyone who can reach the
  UI can put anything on that disk and fill it. It is not set up until you set it
  up, and removing it takes the daemon and its state away again.
- The systemd unit runs as a dedicated non-root user with `NoNewPrivileges`,
  `ProtectSystem=strict`, an empty capability set and a system-call filter.
- Rotating the key in Settings invalidates every host until you install the new
  public key on each one.

## Development

```sh
make build          # PWA into the Go embed directory, then the binary
make run            # build and start on :8899
make test           # Go tests, including SSH integration tests where sshd exists
make test-web       # the PWA's unit tests
make test-installer # install, upgrade, rollback and uninstall, in a sandbox
make test-provision # set a host up over SSH for real (root; changes the machine)
make test-torrent   # drive a real deluge: add, pause and remove a torrent

cd apps/web && npm run dev   # Vite dev server, proxying /api to :8899

make version        # print the version this tree would build as
make bump-version   # move the version line to this month, UTC
```

### Versioning

The version is `vYEAR.MONTH.PATCH` — a calendar version, where the patch number
is the repository's commit count, so `v2026.8.42` is the 42nd commit on the
2026.8 line. There is no semantic major/minor: the leading numbers say *when* a
release line opened, not what it promises about compatibility.

`Year` and `Month` are constants in `server/internal/version/version.go`. A
line opens with a branch: the first commit on a branch runs `make bump-version`,
which sets them to the current month in UTC (CLAUDE.md asks Claude to do this
without being told). They are deliberately not read from the build clock, so
rebuilding an old tree still reports what it originally shipped. The month is
not zero-padded (`v2026.8.42`, not `v2026.08.42`), because semver forbids a
leading zero and every version has to stay something a semver parser accepts.

The patch number only exists at build time, so `scripts/version.mjs` works it
out once and both halves of the build take it from there: the linker stamps it
into the binary, Vite inlines it into the bundle. The app header shows what the
PWA was built as, Settings and `/api/health` show what the binary was, and they
agree because a build makes both together.

A version ending in `.0` is an unstamped build rather than a release — a bare
`go build`, or a build where git couldn't be asked. That includes a **shallow**
clone, which is why the installer clones with `--filter=blob:none` instead of
`--depth 1`: cheap, but with the whole commit graph.

Server flags:

| Flag    | Env             | Default            | Meaning                    |
| ------- | --------------- | ------------------ | -------------------------- |
| `-addr` | `DEPLOYER_ADDR` | `:8899`            | listen address             |
| `-db`   | `DEPLOYER_DB`   | `data/deployer.db` | SQLite path                |
| `-pin`  | `DEPLOYER_PIN`  | _(empty)_          | optional PIN; empty = open |
| `-v`    |                 | `false`            | verbose logging            |
| `-self-user` | `DEPLOYER_SELF_USER` | _(the account HostMan runs as)_ | SSH user for the home host |
| `-self-repo` | `DEPLOYER_REPO` | `chinmay28/hostman` | repository a self-update builds from |
| `-self-ref`  | `DEPLOYER_REF`  | `main`             | default ref for a self-update |

The installer sets `DEPLOYER_SELF_USER` to whoever ran it, since the service
account is a `nologin` user and cannot be SSHed to.

Tests cover the probe parser against realistic and degraded `/proc` output, the
store, the API handlers, and — where a local `sshd` can be started — real SSH
connections: key auth, host-key pinning and rejection, live deployments, exit
codes, cancellation, log streaming and health checks.

Uninstalling is tested over the same real connection, both ways round: a command
that succeeds has to actually run on the host, be recorded as an uninstall
rather than a deploy, and leave the installation forgotten and the log behind;
one that exits non-zero has to leave the installation exactly where it was. Its
refusals get their own tests too — an app that declared no uninstall command,
and HostMan asked to uninstall itself from the machine it runs on.

The shell scripts behind host management are tested by running them: against a
real filesystem, through a real shell, with the parsers the SSH path uses, both
with GNU `find` and with it forced to fail so the busybox fallback is covered.
`systemctl` and `journalctl` are stood in for by scripts that answer the way the
real ones do, so the listing, the parsing, the fallback for a `journalctl` too
old to know `--no-hostname`, and what creating and deleting a service leave on
disk are all exercised without a running systemd.

The restart diagnosis gets the same treatment, with `PATH` replaced rather than
extended: half of what it has to get right is what it does when a command is
*not* there, and a stub cannot express absence. So the journal that answers, the
journal that has nothing from before the restart, the `last` too old to print
ISO timestamps, the rsyslog file read in its place and the host with none of
them are each a real run of the real script. The verdict itself is tested
against fixtures of every cause, including the two that matter most: a sign far
enough from the restart that it cannot be the cause, and a log that simply stops. The refusals get their own
tests, because they are the point: writing over an existing unit file, taking a
name systemd already knows, deleting a service that is still running, and
deleting anything under /usr/lib all have to fail.

The installer gets the same treatment in a sandbox: install, upgrade, the
rollback a failed upgrade is supposed to trigger, uninstall, and the Go
toolchain it fetches to build with — a download that is refused, one that
returns nothing at all, and one that works. The toolchain already on the
machine has to survive the first two, because an installer that removes it
before it has something to put in its place leaves the host unable to build
anything and every later run failing on a different line than the one that
broke.
Quoting is tested the same way — every path a person could type is handed to
`/bin/sh` and has to come back out the other side unchanged.

## API

| Method   | Path                                | Purpose                              |
| -------- | ----------------------------------- | ------------------------------------ |
| `GET`    | `/api/health`                       | liveness, and the running version    |
| `GET`    | `/api/overview`                     | everything the dashboard needs       |
| `GET`    | `/api/session`                      | whether a PIN is required            |
| `POST`   | `/api/session`                      | exchange a PIN for a session cookie  |
| `GET`    | `/api/settings/ssh`                 | public key and host setup commands   |
| `POST`   | `/api/settings/ssh/rotate`          | new keypair (re-authorize all hosts) |
| `GET`    | `/api/hosts`                        | hosts, each with its latest sample   |
| `POST`   | `/api/hosts`                        | add a host                           |
| `GET`    | `/api/hosts/{id}`                   | one host                             |
| `PATCH`  | `/api/hosts/{id}`                   | edit name, address, port or user     |
| `DELETE` | `/api/hosts/{id}`                   | remove a host and its history        |
| `POST`   | `/api/hosts/{id}/test`              | check reachability, key auth, sudo   |
| `POST`   | `/api/hosts/{id}/provision`         | one-time password setup, not stored  |
| `GET`    | `/api/hosts/{id}/metrics`           | samples, `?minutes=` up to 1440, the day's ranges, and the top five consumers of CPU and memory |
| `POST`   | `/api/hosts/{id}/reboot`            | restart the machine (there is no shutdown) |
| `GET`    | `/api/hosts/{id}/boot`              | why it last restarted, and the evidence |
| `POST`   | `/api/hosts/{id}/boot/journal`      | keep the journal across restarts, so the next one is explainable |
| `GET`    | `/api/hosts/{id}/cron`              | a crontab, `?user=` (default: the SSH user) |
| `PUT`    | `/api/hosts/{id}/cron`              | install a crontab; cron validates it |
| `GET`    | `/api/hosts/{id}/services`          | hand-installed services and timers, and their state |
| `POST`   | `/api/hosts/{id}/services`          | write a new unit; systemd validates it |
| `DELETE` | `/api/hosts/{id}/services`          | delete a stopped service, `?name=`   |
| `GET`    | `/api/hosts/{id}/services/unit`     | one service or timer, `?name=`       |
| `GET`    | `/api/hosts/{id}/services/logs`     | its journal, `?name=&lines=` (20–2000) |
| `POST`   | `/api/hosts/{id}/services/action`   | start, stop, restart, reload, enable or disable |
| `POST`   | `/api/hosts/{id}/services/reload`   | `daemon-reload` after a unit file changes |
| `GET`    | `/api/hosts/{id}/shell`             | the login shells open on this host   |
| `POST`   | `/api/hosts/{id}/shell`             | open one, `{"cols","rows"}`; the size is clamped, never refused |
| `GET`    | `/api/shell/{sid}`                  | one shell: its size, its byte offset, whether it is still running |
| `DELETE` | `/api/shell/{sid}`                  | end it                               |
| `GET`    | `/api/shell/{sid}/stream`           | the screen as server-sent events, `?from=` a byte offset to resume |
| `POST`   | `/api/shell/{sid}/input`            | keystrokes, base64 — a shell's most important keys are not text |
| `POST`   | `/api/shell/{sid}/resize`           | tell the pty its window changed      |
| `GET`    | `/api/hosts/{id}/claude`            | Claude Code for the SSH user: installed, signed in, and how an install or sign-in is going |
| `POST`   | `/api/hosts/{id}/claude/install`    | install the CLI for that user, detached; the status follows the log |
| `POST`   | `/api/hosts/{id}/claude/login`      | start a sign-in, `{"console"}`; the link appears in the status once printed |
| `POST`   | `/api/hosts/{id}/claude/login/code` | hand the waiting sign-in its code, `{"code"}` |
| `DELETE` | `/api/hosts/{id}/claude/login`      | cancel a waiting sign-in             |
| `POST`   | `/api/hosts/{id}/claude/key`        | store an API key in the CLI's settings, `{"key"}` |
| `GET`    | `/api/hosts/{id}/claude/sessions`   | the conversations open on this host  |
| `POST`   | `/api/hosts/{id}/claude/sessions`   | start one, `{"dir","model","mode","name"}` |
| `GET`    | `/api/claude/{sid}`                 | one session: model, mode, busy, pending questions, cost |
| `DELETE` | `/api/claude/{sid}`                 | end it                               |
| `GET`    | `/api/claude/{sid}/stream`          | the conversation as server-sent events, `?from=` an event number to resume |
| `POST`   | `/api/claude/{sid}/message`         | say something, `{"text"}`            |
| `POST`   | `/api/claude/{sid}/answer`          | answer a permission request, `{"requestId","allow","always","reason"}` |
| `POST`   | `/api/claude/{sid}/model`           | change the model, `{"model"}`; waits for the CLI to accept |
| `POST`   | `/api/claude/{sid}/mode`            | change the permission mode, `{"mode"}` |
| `POST`   | `/api/claude/{sid}/interrupt`       | stop what Claude is doing            |
| `GET`    | `/api/hosts/{id}/remote`            | the host's browser session: what is installed, how far a setup got, whether it is running, and what has been downloaded |
| `POST`   | `/api/hosts/{id}/remote`            | install or reconfigure it; the packages install detached |
| `DELETE` | `/api/hosts/{id}/remote`            | remove it, `?purge=true` to delete the browser profile too |
| `POST`   | `/api/hosts/{id}/remote/action`     | start it (with the page to open) or stop it |
| `GET`    | `/api/hosts/{id}/torrents`          | the downloader: whether deluge is installed, what is set up, and what it is downloading |
| `POST`   | `/api/hosts/{id}/torrents`          | add one: a magnet link, the address of a `.torrent`, or the file itself, base64 |
| `POST`   | `/api/hosts/{id}/torrents/action`   | start or stop the daemon; pause, resume or remove a torrent (`"data":true` deletes what it downloaded); or `"seeding"` with a `ratio` and `remove` to set what happens after a torrent finishes |
| `POST`   | `/api/hosts/{id}/torrents/setup`    | write the daemon onto the host, or change where it downloads |
| `DELETE` | `/api/hosts/{id}/torrents/setup`    | take it off; deluge and the downloaded files stay |
| `GET`    | `/api/hosts/{id}/files`             | list a directory, `?path=` (default: home) |
| `DELETE` | `/api/hosts/{id}/files`             | delete `?path=`, `&recursive=true` for a full directory |
| `GET`    | `/api/hosts/{id}/files/content`     | read a file, `?path=`                |
| `PUT`    | `/api/hosts/{id}/files/content`     | write a file, keeping mode and owner |
| `POST`   | `/api/hosts/{id}/files/mkdir`       | create a directory                   |
| `POST`   | `/api/hosts/{id}/files/rename`      | move a file, never over an existing one |
| `POST`   | `/api/hosts/{id}/files/chmod`       | set the mode, `"recursive":true` for everything inside |
| `GET`    | `/api/apps`                         | apps                                 |
| `POST`   | `/api/apps`                         | add an app                           |
| `GET`    | `/api/apps/{id}`                    | one app                              |
| `PATCH`  | `/api/apps/{id}`                    | edit an app                          |
| `DELETE` | `/api/apps/{id}`                    | delete an app and its history        |
| `POST`   | `/api/apps/{id}/deploy`             | deploy to a host                     |
| `GET`    | `/api/installations`                | what is deployed where, with health  |
| `POST`   | `/api/installations/{id}/redeploy`  | run again with the saved parameters  |
| `POST`   | `/api/installations/{id}/check`     | run the health check now             |
| `POST`   | `/api/installations/{id}/uninstall` | run the app's uninstall command on the host, then forget it |
| `DELETE` | `/api/installations/{id}`           | forget it (nothing is uninstalled)   |
| `GET`    | `/api/deployments`                  | history, `?appId=`/`?hostId=`        |
| `GET`    | `/api/deployments/{id}`             | one deployment, with its log         |
| `GET`    | `/api/deployments/{id}/stream`      | live log as server-sent events       |
| `POST`   | `/api/deployments/{id}/cancel`      | stop a running deployment            |
| `GET`    | `/api/self`                         | version, home host, update readiness |
| `POST`   | `/api/self/update`                  | update HostMan on the home host     |

## Layout

```
server/
  cmd/deployer/      entrypoint: flags, wiring, graceful shutdown
  cmd/icongen/       draws the app icons
  internal/store/    SQLite schema, append-only migrations, queries
  internal/sshx/     HostMan's keypair and SSH connections
  internal/metrics/  the agentless /proc probe and its parser
  internal/hosts/    connect, test, and poll hosts
  internal/hostops/  managing a host: files, services, crontab, power state,
                     the remote browser session, the torrent downloader, and
                     guessing why it last restarted
  internal/shell/    login shells held open between visits, and their scrollback
  internal/claudecli/ the Claude Code CLI's streaming protocol: the command line,
                     what goes in, and what comes out as events
  internal/claude/   conversations with Claude Code held open between visits:
                     the event log, permission questions, model and mode changes
  internal/selfhost/ recognising this machine, and the app that updates it
  internal/deploy/   command rendering, the deployment runner, health checks
  internal/api/      REST handlers, SSE log stream, optional PIN gate
  internal/version/  the version number, and where YEAR.MONTH is declared
  internal/web/      serves the embedded PWA
apps/web/            the PWA: React, Vite, no UI framework
scripts/             the installer, its test harness, and version.mjs
```
