#!/usr/bin/env bash
#
# Installs or upgrades HostMan as a systemd service.
#
#   curl -fsSL https://raw.githubusercontent.com/chinmay28/hostman/main/scripts/quickstart.sh | sudo bash
#
# Re-running upgrades in place: the database is snapshotted, the new build is
# health-checked after it starts, and a failed upgrade rolls back to the
# previous binary and database.
#
# Environment overrides:
#   DEPLOYER_PORT   port to listen on              (default 8899)
#   DEPLOYER_PIN    optional PIN for the web UI    (default none)
#   DEPLOYER_REF    git branch, tag or commit      (default main)
#   DEPLOYER_REPO   repository to build from
#   DEPLOYER_SELF_USER  account HostMan SSHes to this machine as (default: the
#                       user running the installer), for updating itself
#
# Paths can be moved with DEPLOYER_INSTALL_DIR, DEPLOYER_DATA_DIR,
# DEPLOYER_SERVICE_USER, DEPLOYER_UNIT and DEPLOYER_GO_DIR.
#
set -euo pipefail

PORT="${DEPLOYER_PORT:-8899}"
PIN="${DEPLOYER_PIN:-}"
REF="${DEPLOYER_REF:-main}"
REPO="${DEPLOYER_REPO:-https://github.com/chinmay28/hostman.git}"

SERVICE_USER="${DEPLOYER_SERVICE_USER:-deployer}"
# The account HostMan connects to *this* machine as when updating itself. The
# service user is a nologin account, so the person running the installer is the
# sensible default.
SELF_USER="${DEPLOYER_SELF_USER:-${SUDO_USER:-}}"
INSTALL_DIR="${DEPLOYER_INSTALL_DIR:-/opt/deployer}"
BUILD_DIR="$INSTALL_DIR/src"
DATA_DIR="${DEPLOYER_DATA_DIR:-/var/lib/deployer}"
BACKUP_DIR="$DATA_DIR/backups"
DB_PATH="$DATA_DIR/deployer.db"
UNIT="${DEPLOYER_UNIT:-/etc/systemd/system/deployer.service}"
# Where the build-time Go toolchain is installed. Overridable so the installer's
# own tests can exercise the upgrade without touching the machine's Go.
GO_DIR="${DEPLOYER_GO_DIR:-/usr/local/go}"
GO_VERSION=1.24.7
NODE_MAJOR=22
KEEP_BACKUPS=5

log()  { printf '\033[1;35m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m!!\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "Run this with sudo."

case "${1:-}" in
  --uninstall) UNINSTALL=1 ;;
  "")          UNINSTALL=0 ;;
  *)           die "Unknown option: $1 (only --uninstall is supported)" ;;
esac

if [ "$UNINSTALL" = 1 ]; then
  log "Stopping and removing the HostMan service"
  systemctl disable --now deployer.service 2>/dev/null || true
  rm -f "$UNIT"
  systemctl daemon-reload
  rm -rf "$INSTALL_DIR"
  echo
  log "Removed. Your data is still at $DATA_DIR (it holds HostMan's SSH key)."
  log "Delete it with: sudo rm -rf $DATA_DIR && sudo userdel $SERVICE_USER"
  exit 0
fi

command -v systemctl >/dev/null || die "This installer needs systemd."
command -v apt-get >/dev/null || die "This installer expects a Debian or Ubuntu system."

case "$(uname -m)" in
  x86_64|amd64) GO_ARCH=amd64 ;;
  aarch64|arm64) GO_ARCH=arm64 ;;
  armv6l|armv7l) GO_ARCH=armv6l ;;
  *) die "Unsupported architecture: $(uname -m)" ;;
esac

# A bare `[ test ] && cmd` would abort the script under `set -e` whenever the
# test is false, so every conditional below is a full if statement.
UPGRADE=0
if [ -x "$INSTALL_DIR/deployer" ]; then
  UPGRADE=1
fi

# ---------------------------------------------------------------- build tools

export DEBIAN_FRONTEND=noninteractive
missing=""
for tool in git curl; do
  if ! command -v "$tool" >/dev/null; then
    missing="$missing $tool"
  fi
done
if [ -n "$missing" ]; then
  log "Installing build dependencies:$missing"
  apt-get update -qq
  apt-get install -y -qq --no-install-recommends git curl ca-certificates >/dev/null
fi

need_go=1
if command -v go >/dev/null; then
  current="$(go env GOVERSION 2>/dev/null | sed 's/^go//')"
  # A newer Go than we pin is fine; an older one is not.
  if [ -n "$current" ] && [ "$(printf '%s\n%s\n' "$GO_VERSION" "$current" | sort -V | head -1)" = "$GO_VERSION" ]; then
    need_go=0
  fi
fi
if [ "$need_go" = 1 ]; then
  log "Installing Go $GO_VERSION (build-time only)"
  tarball="go${GO_VERSION}.linux-${GO_ARCH}.tar.gz"

  # A download path of this run's own, removed on the way out. A fixed name in
  # /tmp belongs to every copy of this installer at once, and two that overlap
  # — a self-update started again while the first one is still going — had one
  # deleting the tarball the other was about to unpack, which surfaced as tar
  # failing to open a file curl had just reported writing.
  gotar="$(mktemp "/tmp/${tarball}.XXXXXX")" || die "Could not write a temporary file to /tmp."
  trap 'rm -f "$gotar"' EXIT

  # go.dev/dl is a redirector to dl.google.com, so it is the half more likely
  # to be blocked or rate-limited; where it is, the destination still answers.
  got_go=0
  for url in "https://go.dev/dl/${tarball}" "https://dl.google.com/go/${tarball}"; do
    if curl -fsSL --retry 3 --retry-delay 2 "$url" -o "$gotar"; then
      got_go=1
      break
    fi
    warn "Could not download $url"
  done
  if [ "$got_go" = 0 ]; then
    die "Could not download Go $GO_VERSION for linux-$GO_ARCH. Check the host's network and try again."
  fi

  # curl can finish happy having written nothing worth having: a captive
  # portal's login page, a body cut short, a disk with no room on it. What
  # follows replaces a working toolchain, so the archive is proved first.
  [ -s "$gotar" ] || die "The Go download came back empty. Is there space left on /tmp?"
  gzip -t "$gotar" 2>/dev/null ||
    die "The Go download is not a gzip archive — something other than go.dev answered for it."

  # Unpacked beside the toolchain in use and swapped in only once it is known to
  # contain a go binary. Deleting the old one first, as this used to, meant a
  # download that failed for any reason left the machine with no Go at all —
  # and every later run failing on a different line than the one that broke.
  rm -rf "$GO_DIR.new" "$GO_DIR.old"
  mkdir -p "$GO_DIR.new"
  tar -C "$GO_DIR.new" --strip-components=1 -xzf "$gotar" ||
    die "Could not unpack the Go archive."
  [ -x "$GO_DIR.new/bin/go" ] || die "The Go archive unpacked without a go binary in it."
  if [ -d "$GO_DIR" ]; then
    mv "$GO_DIR" "$GO_DIR.old"
  fi
  mv "$GO_DIR.new" "$GO_DIR"
  rm -rf "$GO_DIR.old"

  rm -f "$gotar"
  trap - EXIT
fi
export PATH="$GO_DIR/bin:$PATH"
command -v go >/dev/null || die "Go is still not on PATH."

need_node=1
if command -v node >/dev/null; then
  major="$(node -v | sed 's/^v\([0-9]*\).*/\1/')"
  if [ -n "$major" ] && [ "$major" -ge 20 ] 2>/dev/null; then
    need_node=0
  fi
fi
if [ "$need_node" = 1 ]; then
  log "Installing Node $NODE_MAJOR (build-time only)"
  curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash - >/dev/null
  apt-get install -y -qq nodejs >/dev/null
fi

# --------------------------------------------------------------------- source

# The build needs the whole commit graph, not just the tip: the version number's
# patch component is the repository's commit count, and a shallow clone would
# report that as 1 (see scripts/version.mjs). A partial clone keeps carrying all
# of it cheap — every commit, none of the historical file contents — and a plain
# clone is the fallback for a remote that won't serve one. Either way, never
# shallow.
log "Fetching HostMan ($REF)"
mkdir -p "$INSTALL_DIR"
if [ -d "$BUILD_DIR/.git" ]; then
  git -C "$BUILD_DIR" remote set-url origin "$REPO"
  # A build directory left behind by an older installer is shallow. --unshallow
  # fills it in, and is an error on a repository that is already complete, so
  # only ask for it when it applies.
  unshallow=""
  if [ "$(git -C "$BUILD_DIR" rev-parse --is-shallow-repository)" = true ]; then
    unshallow="--unshallow"
  fi
  # shellcheck disable=SC2086  # $unshallow is one flag or nothing.
  git -C "$BUILD_DIR" fetch $unshallow --filter=blob:none origin "$REF" --quiet 2>/dev/null ||
    git -C "$BUILD_DIR" fetch $unshallow origin "$REF" --quiet ||
    die "Could not fetch $REF from $REPO"
  git -C "$BUILD_DIR" checkout --quiet --force FETCH_HEAD
else
  rm -rf "$BUILD_DIR"
  git clone --filter=blob:none --branch "$REF" "$REPO" "$BUILD_DIR" --quiet 2>/dev/null ||
    { rm -rf "$BUILD_DIR" &&
      git clone --branch "$REF" "$REPO" "$BUILD_DIR" --quiet 2>/dev/null; } ||
    die "Could not clone $REPO at $REF"
fi
REVISION="$(git -C "$BUILD_DIR" rev-parse --short HEAD)"
# vYEAR.MONTH.<commit count>, the one place it's assembled — the same number
# the binary is stamped with below and the PWA build inlines.
VERSION="$(node "$BUILD_DIR/scripts/version.mjs")"
PATCH="$(node "$BUILD_DIR/scripts/version.mjs" --patch)"

# ---------------------------------------------------------------------- build
# Everything is built before the running service is touched, so a compile
# failure leaves the current install alone.

log "Building the web app"
( cd "$BUILD_DIR/apps/web" && npm ci --silent && npm run build --silent ) ||
  die "Web build failed."

log "Building the server $VERSION"
VERSION_PKG=github.com/chinmay28/deployer/server/internal/version
( cd "$BUILD_DIR/server" &&
  go build -trimpath -ldflags "-s -w -X $VERSION_PKG.Patch=$PATCH" \
    -o "$INSTALL_DIR/deployer.new" ./cmd/deployer ) ||
  die "Server build failed."

# ------------------------------------------------------------------- accounts

if ! id "$SERVICE_USER" >/dev/null 2>&1; then
  log "Creating the $SERVICE_USER system user"
  useradd --system --home-dir "$DATA_DIR" --shell /usr/sbin/nologin "$SERVICE_USER"
fi
mkdir -p "$DATA_DIR" "$BACKUP_DIR"
chown -R "$SERVICE_USER:$SERVICE_USER" "$DATA_DIR"
# The database holds HostMan's SSH private key.
chmod 700 "$DATA_DIR" "$BACKUP_DIR"

# --------------------------------------------------------------------- deploy

if [ "$UPGRADE" = 1 ]; then
  log "Stopping HostMan for the upgrade"
  systemctl stop deployer.service || true
fi

BACKUP=""
if [ -f "$DB_PATH" ]; then
  BACKUP="$BACKUP_DIR/deployer-$(date +%Y%m%d-%H%M%S).db"
  log "Snapshotting the database to $BACKUP"
  # The service is stopped, so a plain copy is consistent. The -wal and -shm
  # files are checkpointed into the database on clean shutdown.
  cp "$DB_PATH" "$BACKUP"
  if [ -f "$DB_PATH-wal" ]; then
    cp "$DB_PATH-wal" "$BACKUP-wal"
  fi
  chown -R "$SERVICE_USER:$SERVICE_USER" "$BACKUP_DIR"
fi

PREVIOUS=""
if [ "$UPGRADE" = 1 ]; then
  PREVIOUS="$INSTALL_DIR/deployer.prev"
  cp "$INSTALL_DIR/deployer" "$PREVIOUS"
fi
mv "$INSTALL_DIR/deployer.new" "$INSTALL_DIR/deployer"
chmod 755 "$INSTALL_DIR/deployer"

log "Writing $UNIT"
cat > "$UNIT" <<UNIT_EOF
[Unit]
Description=HostMan — host manager for machines on a network
Documentation=https://github.com/chinmay28/hostman
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SERVICE_USER
Group=$SERVICE_USER
WorkingDirectory=$DATA_DIR
ExecStart=$INSTALL_DIR/deployer -addr :$PORT -db $DB_PATH
Environment=DEPLOYER_PIN=$PIN
# Used to register this machine as a host so HostMan can update itself.
Environment=DEPLOYER_SELF_USER=$SELF_USER
Environment=DEPLOYER_REPO=$REPO
Environment=DEPLOYER_REF=$REF
Restart=on-failure
RestartSec=3
# The database holds an SSH private key: keep it readable only by this user.
UMask=0077

# HostMan only needs to read its own data directory and reach the network.
NoNewPrivileges=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=$DATA_DIR
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
ProtectProc=invisible
RestrictSUIDSGID=yes
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
LockPersonality=yes
CapabilityBoundingSet=
SystemCallFilter=@system-service
SystemCallErrorNumber=EPERM

[Install]
WantedBy=multi-user.target
UNIT_EOF

systemctl daemon-reload
systemctl enable deployer.service >/dev/null 2>&1 || true

log "Starting HostMan"
systemctl restart deployer.service

# ------------------------------------------------------------- health & rollback

healthy=0
for _ in $(seq 1 30); do
  if curl -fsS --max-time 2 "http://127.0.0.1:$PORT/api/health" >/dev/null 2>&1; then
    healthy=1
    break
  fi
  sleep 1
done

if [ "$healthy" = 0 ]; then
  warn "HostMan did not come up. Recent log:"
  journalctl -u deployer.service -n 25 --no-pager || true

  if [ -n "$PREVIOUS" ] && [ -x "$PREVIOUS" ]; then
    warn "Rolling back to the previous version"
    systemctl stop deployer.service || true
    mv "$PREVIOUS" "$INSTALL_DIR/deployer"
    if [ -n "$BACKUP" ] && [ -f "$BACKUP" ]; then
      cp "$BACKUP" "$DB_PATH"
      if [ -f "$BACKUP-wal" ]; then
        cp "$BACKUP-wal" "$DB_PATH-wal"
      else
        rm -f "$DB_PATH-wal"
      fi
      chown -R "$SERVICE_USER:$SERVICE_USER" "$DATA_DIR"
    fi
    systemctl start deployer.service || true
    die "Upgrade failed and was rolled back. The previous version is running again."
  fi
  die "HostMan failed to start. Check: journalctl -u deployer -f"
fi

rm -f "$PREVIOUS"
# Keep the most recent snapshots and drop the rest.
find "$BACKUP_DIR" -maxdepth 1 -name 'deployer-*.db' -printf '%T@ %p\n' 2>/dev/null |
  sort -rn | tail -n +$((KEEP_BACKUPS + 1)) | cut -d' ' -f2- | while read -r old; do
    rm -f "$old" "$old-wal"
  done

ADDRESS="$(hostname -I 2>/dev/null | awk '{print $1}')"
if [ -z "$ADDRESS" ]; then
  ADDRESS="127.0.0.1"
fi

echo
log "HostMan $VERSION ($REVISION) is running"
echo "     http://$ADDRESS:$PORT  (also http://$(hostname).local:$PORT if mDNS is set up)"
echo
if [ -z "$PIN" ]; then
  echo "     No PIN is set: anyone who can reach that address can deploy to your hosts."
  echo "     Keep it on your LAN or Tailscale network. To require a PIN:"
  echo "       curl -fsSL .../quickstart.sh | sudo DEPLOYER_PIN=1234 bash"
  echo
fi
if [ -n "$SELF_USER" ]; then
  echo "     This machine is registered as a host so HostMan can update itself."
  echo "     Authorize its key for $SELF_USER with the first command in Settings."
  echo
fi
echo "     Open Settings in the app for the two commands to run on each host you add."
echo "     Logs:    journalctl -u deployer -f"
echo "     Upgrade: re-run this script"
echo
