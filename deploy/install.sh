#!/bin/sh
# Rook install script — downloads a release binary and sets up systemd service.
# Usage: curl -fsSL https://raw.githubusercontent.com/thobiasn/rook/main/deploy/install.sh | sh
#   or:  sh install.sh --version v1.0.0
set -eu

REPO="thobiasn/rook"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/rook"
DATA_DIR="/var/lib/rook"
RUN_DIR="/run/rook"
SERVICE_FILE="/etc/systemd/system/rook.service"

# --- Helpers ---

info() { printf '\033[1;34m==>\033[0m %s\n' "$1"; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$1" >&2; }
die()  { printf '\033[1;31merror:\033[0m %s\n' "$1" >&2; exit 1; }

fetch() {
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$@"
    elif command -v wget >/dev/null 2>&1; then
        # Map curl flags to wget: -o FILE -> -qO FILE, bare URL -> -qO-
        case "$1" in
            -o) wget -qO "$2" "$3" ;;
            *)  wget -qO- "$1" ;;
        esac
    else
        die "curl or wget required"
    fi
}

# --- Pre-flight ---

if [ "$(id -u)" -ne 0 ]; then
    die "this script must be run as root"
fi

OS="$(uname -s)"
case "$OS" in
    Linux) OS="linux" ;;
    *)     die "unsupported OS: $OS (only Linux is supported)" ;;
esac

ARCH="$(uname -m)"
case "$ARCH" in
    x86_64)  ARCH="amd64" ;;
    aarch64) ARCH="arm64" ;;
    arm64)   ARCH="arm64" ;;
    *)       die "unsupported architecture: $ARCH" ;;
esac

# --- Parse flags ---

VERSION=""
while [ $# -gt 0 ]; do
    case "$1" in
        --version)
            [ $# -ge 2 ] || die "--version requires an argument"
            VERSION="$2"; shift 2 ;;
        --version=*) VERSION="${1#*=}"; shift ;;
        *) die "unknown flag: $1" ;;
    esac
done

# --- Detect latest version ---

if [ -z "$VERSION" ]; then
    info "detecting latest release..."
    VERSION=$(fetch "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
    if [ -z "$VERSION" ]; then
        die "failed to detect latest version"
    fi
fi

# Validate version format.
case "$VERSION" in
    v[0-9]*) ;;
    *) die "invalid version format: $VERSION (expected v<semver>)" ;;
esac

info "installing rook ${VERSION} (${OS}/${ARCH})"

# --- Download binary ---

# Strip leading v for filename if present.
FILE_VERSION="${VERSION#v}"
DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/rook_${FILE_VERSION}_${OS}_${ARCH}"

info "downloading ${DOWNLOAD_URL}..."
TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT

fetch -o "$TMP" "$DOWNLOAD_URL"

chmod +x "$TMP"
mv "$TMP" "${INSTALL_DIR}/rook"
chmod 755 "${INSTALL_DIR}/rook"
trap - EXIT
info "installed binary to ${INSTALL_DIR}/rook"

# --- Create system user ---

if ! id rook >/dev/null 2>&1; then
    useradd --system --no-create-home --shell /usr/sbin/nologin rook
    info "created system user 'rook'"
fi

# Add to docker group for socket access.
if getent group docker >/dev/null 2>&1; then
    usermod -aG docker rook
    info "added rook to docker group"
fi

# --- Create directories ---

mkdir -p "$CONFIG_DIR" "$DATA_DIR" "$RUN_DIR"
chown rook:rook "$DATA_DIR" "$RUN_DIR"
info "created directories"

# --- Install systemd service ---

cat > "$SERVICE_FILE" << 'UNIT'
[Unit]
Description=Rook Server Monitoring Agent
After=network.target docker.service
Requires=docker.service

[Service]
Type=simple
User=rook
Group=rook
ExecStart=/usr/local/bin/rook agent --config /etc/rook/config.toml
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=5

# Security hardening
ProtectSystem=strict
ReadWritePaths=/var/lib/rook /run/rook
ProtectHome=true
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
UNIT

systemctl daemon-reload
info "installed systemd service"

# --- Generate default config ---

if [ ! -f "${CONFIG_DIR}/config.toml" ]; then
    cat > "${CONFIG_DIR}/config.toml" << 'CONFIG'
# Rook agent configuration
# See https://github.com/thobiasn/rook for full documentation.

[storage]
# path = "/var/lib/rook/rook.db"
# retention_days = 7

[socket]
# path = "/run/rook/rook.sock"

[host]
# proc = "/proc"
# sys = "/sys"

[docker]
# socket = "/var/run/docker.sock"
# include = []
# exclude = []

[collect]
# interval = "10s"

# [alerts.high_cpu]
# condition = "host.cpu_percent > 90"
# for = "1m"
# severity = "critical"
# actions = ["notify"]

# [notify.email]
# enabled = false
# smtp_host = "smtp.example.com"
# smtp_port = 587
# from = "rook@example.com"
# to = ["you@example.com"]

# [[notify.webhooks]]
# enabled = false
# url = "https://hooks.slack.com/services/..."
CONFIG
    chown rook:rook "${CONFIG_DIR}/config.toml"
    info "generated default config at ${CONFIG_DIR}/config.toml"
else
    warn "config already exists at ${CONFIG_DIR}/config.toml, not overwriting"
fi

# --- Done ---

echo ""
info "installation complete!"
echo ""
echo "  Next steps:"
echo "    1. Edit ${CONFIG_DIR}/config.toml"
echo "    2. systemctl enable --now rook"
echo "    3. rook connect user@this-host"
echo ""
echo "  Useful commands:"
echo "    systemctl status rook       # check agent status"
echo "    journalctl -u rook -f       # follow agent logs"
echo "    systemctl reload rook       # reload config (SIGHUP)"
echo ""
