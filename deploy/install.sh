#!/bin/sh
# Tori install script.
#
# Agent (server):  curl -fsSL https://raw.githubusercontent.com/thobiasn/tori-cli/main/deploy/install.sh | sudo sh
# Client (local):  curl -fsSL https://raw.githubusercontent.com/thobiasn/tori-cli/main/deploy/install.sh | sh -s -- --client
#
# Options:
#   --client           Install client binary only (no systemd, no root required)
#   --version v1.0.0   Install a specific version
set -eu

REPO="thobiasn/tori-cli"

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

# --- Parse flags ---

VERSION=""
CLIENT_ONLY=false
while [ $# -gt 0 ]; do
    case "$1" in
        --client) CLIENT_ONLY=true; shift ;;
        --version)
            [ $# -ge 2 ] || die "--version requires an argument"
            VERSION="$2"; shift 2 ;;
        --version=*) VERSION="${1#*=}"; shift ;;
        *) die "unknown flag: $1" ;;
    esac
done

# --- Pre-flight ---

OS="$(uname -s)"
case "$OS" in
    Linux)  OS="linux" ;;
    Darwin) OS="darwin" ;;
    *)      die "unsupported OS: $OS" ;;
esac

if [ "$CLIENT_ONLY" = false ] && [ "$OS" != "linux" ]; then
    die "agent install requires Linux (use --client for client-only install)"
fi

if [ "$CLIENT_ONLY" = false ] && [ "$(id -u)" -ne 0 ]; then
    die "agent install must be run as root (use --client for client-only install)"
fi

ARCH="$(uname -m)"
case "$ARCH" in
    x86_64)  ARCH="amd64" ;;
    aarch64) ARCH="arm64" ;;
    arm64)   ARCH="arm64" ;;
    *)       die "unsupported architecture: $ARCH" ;;
esac

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

info "installing tori ${VERSION} (${OS}/${ARCH})"

# --- Download binary ---

# Strip leading v for filename if present.
FILE_VERSION="${VERSION#v}"
DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${VERSION}/tori_${FILE_VERSION}_${OS}_${ARCH}"

info "downloading ${DOWNLOAD_URL}..."
TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT

fetch -o "$TMP" "$DOWNLOAD_URL"
chmod +x "$TMP"

# --- Client-only install ---

if [ "$CLIENT_ONLY" = true ]; then
    # Install to ~/.local/bin (no root needed) or /usr/local/bin (with root).
    if [ "$(id -u)" -eq 0 ]; then
        INSTALL_DIR="/usr/local/bin"
    else
        INSTALL_DIR="${HOME}/.local/bin"
        mkdir -p "$INSTALL_DIR"
    fi

    mv "$TMP" "${INSTALL_DIR}/tori"
    chmod 755 "${INSTALL_DIR}/tori"
    trap - EXIT
    info "installed client to ${INSTALL_DIR}/tori"

    # Check if install dir is in PATH.
    case ":${PATH}:" in
        *":${INSTALL_DIR}:"*) ;;
        *) warn "${INSTALL_DIR} is not in your PATH — add it to your shell profile" ;;
    esac

    echo ""
    info "client installation complete!"
    echo ""
    echo "  Connect to a server:"
    echo "    tori user@your-server"
    echo ""
    echo "  Or add servers to ~/.config/tori/config.toml:"
    echo "    [servers.prod]"
    echo "    host = \"user@prod.example.com\""
    echo ""
    exit 0
fi

# --- Agent install (Linux, root) ---

INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/tori"
DATA_DIR="/var/lib/tori"
RUN_DIR="/run/tori"
SERVICE_FILE="/etc/systemd/system/tori.service"

mv "$TMP" "${INSTALL_DIR}/tori"
chmod 755 "${INSTALL_DIR}/tori"
trap - EXIT
info "installed binary to ${INSTALL_DIR}/tori"

# --- Create system user ---

if ! id tori >/dev/null 2>&1; then
    useradd --system --no-create-home --shell /usr/sbin/nologin tori
    info "created system user 'tori'"
fi

# Add to docker group for socket access.
if getent group docker >/dev/null 2>&1; then
    usermod -aG docker tori
    info "added tori to docker group"
fi

# --- Create directories ---

mkdir -p "$CONFIG_DIR" "$DATA_DIR" "$RUN_DIR"
chown tori:tori "$DATA_DIR" "$RUN_DIR"
info "created directories"

# --- Install systemd service ---

cat > "$SERVICE_FILE" << 'UNIT'
[Unit]
Description=Tori Server Monitoring Agent
After=network.target docker.service
Requires=docker.service

[Service]
Type=simple
User=tori
Group=tori
ExecStart=/usr/local/bin/tori agent --config /etc/tori/config.toml
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=5

# Security hardening
ProtectSystem=strict
ReadWritePaths=/var/lib/tori /run/tori
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
# Tori agent configuration
# See https://github.com/thobiasn/tori-cli for full documentation.

[storage]
# path = "/var/lib/tori/tori.db"
# retention_days = 7

[socket]
# path = "/run/tori/tori.sock"

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
# from = "tori@example.com"
# to = ["you@example.com"]

# [[notify.webhooks]]
# enabled = false
# url = "https://hooks.slack.com/services/..."
CONFIG
    chown tori:tori "${CONFIG_DIR}/config.toml"
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
echo "    2. systemctl enable --now tori"
echo "    3. tori user@this-host"
echo ""
echo "  Useful commands:"
echo "    systemctl status tori       # check agent status"
echo "    journalctl -u tori -f       # follow agent logs"
echo "    systemctl reload tori       # reload config (SIGHUP)"
echo ""
