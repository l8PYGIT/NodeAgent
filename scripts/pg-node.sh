#!/usr/bin/env bash
#
# pg-node.sh — installer/manager for the PasarGuard multi-tenant node agent (fork).
#
# Installs a PREBUILT binary (per CPU arch) from GitHub Releases + Xray-core and
# runs it as a systemd service — no Docker, no on-server compilation.
#
# One-line install:
#   sudo bash -c "$(curl -sL https://raw.githubusercontent.com/l8PYGIT/NodeAgent/main/scripts/pg-node.sh)" @ install
#
set -euo pipefail

# ---------- globals ----------
GH_REPO="${GH_REPO:-l8PYGIT/NodeAgent}"
RAW_SCRIPT_URL="${RAW_SCRIPT_URL:-https://raw.githubusercontent.com/${GH_REPO}/main/scripts/pg-node.sh}"
BIN_NAME="pg-node-agent"
AUTO_CONFIRM="${AUTO_CONFIRM:-false}"
# Instance name (overridable with --name) decides paths/service/CLI name, so this
# can be installed alongside the official pg-node. Defaults to pg-node-agent.
APP_NAME="pg-node-agent"

# ---------- helpers ----------
colorized_echo() {
    local color="$1" text="$2" code
    case "$color" in
    red) code=31 ;; green) code=32 ;; yellow) code=33 ;;
    blue) code=34 ;; magenta) code=35 ;; cyan) code=36 ;; *) code=0 ;;
    esac
    printf "\033[1;%sm%s\033[0m\n" "$code" "$text"
}
die() { colorized_echo red "$1"; exit 1; }
check_root() { [ "$(id -u)" -eq 0 ] || die "This command must run as root (use sudo)."; }
require_systemd() { command -v systemctl >/dev/null 2>&1 || die "systemd (systemctl) is required."; }

validate_name() { [[ "$1" =~ ^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$ ]]; }

# set_paths derives all instance paths from APP_NAME.
set_paths() {
    APP_DIR="/opt/$APP_NAME"
    DATA_DIR="/var/lib/$APP_NAME"
    ENV_FILE="$APP_DIR/.env"
    SERVICE_UNIT="/etc/systemd/system/$APP_NAME.service"
    BIN_PATH="$APP_DIR/$BIN_NAME"
    SSL_CERT_FILE="$DATA_DIR/certs/ssl_cert.pem"
    SSL_KEY_FILE="$DATA_DIR/certs/ssl_key.pem"
    FIXED_CONFIG_FILE="$DATA_DIR/fixed-config.json"
    XRAY_DIR="$DATA_DIR/xray-core"
}

detect_os() {
    if command -v apt-get >/dev/null 2>&1; then PKG="apt";
    elif command -v dnf >/dev/null 2>&1; then PKG="dnf";
    elif command -v yum >/dev/null 2>&1; then PKG="yum";
    elif command -v pacman >/dev/null 2>&1; then PKG="pacman";
    elif command -v zypper >/dev/null 2>&1; then PKG="zypper";
    elif command -v apk >/dev/null 2>&1; then PKG="apk";
    else PKG=""; fi
}
install_package() {
    local pkg="$1"
    case "$PKG" in
    apt) apt-get update -qq && apt-get install -y "$pkg" ;;
    dnf) dnf install -y "$pkg" ;; yum) yum install -y "$pkg" ;;
    pacman) pacman -Sy --noconfirm "$pkg" ;; zypper) zypper install -y "$pkg" ;;
    apk) apk add --no-cache "$pkg" ;; *) die "Install '$pkg' manually." ;;
    esac
}
need() { command -v "$1" >/dev/null 2>&1 || { colorized_echo yellow "Installing ${2:-$1}..."; install_package "${2:-$1}"; }; }

gen_uuid() { cat /proc/sys/kernel/random/uuid 2>/dev/null || uuidgen 2>/dev/null || die "cannot generate uuid"; }
gen_key() { openssl rand -hex 32 2>/dev/null || gen_uuid; }
public_ip() { curl -s -4 --fail --max-time 5 ifconfig.io 2>/dev/null || curl -s -6 --fail --max-time 5 ifconfig.io 2>/dev/null || echo "127.0.0.1"; }

# warn_if_port_in_use warns when a TCP port is already bound (e.g. by the
# official PasarGuard node's Docker container on 62050), which would otherwise
# cause a silent crash-loop.
warn_if_port_in_use() {
    local p="$1" label="$2"
    command -v ss >/dev/null 2>&1 || return 0
    if [ -n "$(ss -ltnH "sport = :$p" 2>/dev/null)" ]; then
        colorized_echo yellow "⚠ پورت $p ($label) از قبل در حال استفاده است (شاید نود/کانتینر دیگری)."
        colorized_echo yellow "  با --http-port / --grpc-port پورت آزاد بده، یا بعداً در $ENV_FILE تغییر بده."
    fi
}

# Map uname -m to our release asset suffix and Xray's arch token.
detect_arch() {
    case "$(uname -m)" in
    x86_64|amd64)  ARCH_SUFFIX="amd64";  XRAY_ARCH="64" ;;
    aarch64|arm64) ARCH_SUFFIX="arm64";  XRAY_ARCH="arm64-v8a" ;;
    armv7l|armv7)  ARCH_SUFFIX="armv7";  XRAY_ARCH="arm32-v7a" ;;
    *) die "Unsupported architecture: $(uname -m)" ;;
    esac
}

latest_tag() {
    curl -fsSL "https://api.github.com/repos/${GH_REPO}/releases/latest" 2>/dev/null \
        | grep -oE '"tag_name":[[:space:]]*"[^"]+"' | head -n1 | sed -E 's/.*"([^"]+)"$/\1/'
}

is_installed() { [ -f "$BIN_PATH" ] && [ -f "$SERVICE_UNIT" ]; }
require_installed() { is_installed || die "node is not installed. Run: $APP_NAME install"; }

# ---------- install steps ----------
download_binary() {
    local tag="$1"
    [ -n "$tag" ] || die "No release found for ${GH_REPO}. Create a release first (push a tag like v0.1.0) so prebuilt binaries exist."
    local asset="${BIN_NAME}_linux_${ARCH_SUFFIX}.tar.gz"
    local url="https://github.com/${GH_REPO}/releases/download/${tag}/${asset}"
    local tmp; tmp="$(mktemp -d)"
    colorized_echo blue "Downloading $asset ($tag)..."
    curl -fsSL "$url" -o "$tmp/$asset" || die "Failed to download $url"
    tar -xzf "$tmp/$asset" -C "$tmp" || die "Failed to extract $asset"
    mkdir -p "$(dirname "$BIN_PATH")"
    install -m 755 "$tmp/$BIN_NAME" "$BIN_PATH"
    rm -rf "$tmp"
    colorized_echo green "✓ Binary installed: $BIN_PATH ($tag, $ARCH_SUFFIX)"
}

install_xray() {
    local version="${1:-latest}"
    need curl curl; need unzip unzip
    if [ "$version" = "latest" ]; then
        version="$(curl -fsSL https://api.github.com/repos/XTLS/Xray-core/releases/latest | grep -oE '"tag_name":[[:space:]]*"[^"]+"' | head -n1 | sed -E 's/.*"([^"]+)"$/\1/')"
    fi
    mkdir -p "$XRAY_DIR"
    local tmp; tmp="$(mktemp -d)"
    colorized_echo blue "Downloading Xray-core $version ($XRAY_ARCH)..."
    curl -fsSL "https://github.com/XTLS/Xray-core/releases/download/${version}/Xray-linux-${XRAY_ARCH}.zip" -o "$tmp/xray.zip" \
        || die "Failed to download Xray-core"
    unzip -o "$tmp/xray.zip" -d "$XRAY_DIR" >/dev/null 2>&1 || die "Failed to extract Xray-core"
    rm -rf "$tmp"
    chmod 755 "$XRAY_DIR/xray"
    colorized_echo green "✓ Xray-core $version installed in $XRAY_DIR"
}

gen_self_signed_cert() {
    need openssl openssl
    local ip; ip="$(public_ip)"
    local san="DNS:localhost,IP:127.0.0.1"
    [ "$ip" != "127.0.0.1" ] && san="$san,IP:$ip"
    [ -n "${INSTALL_SAN_ENTRIES:-}" ] && san="$san,$INSTALL_SAN_ENTRIES"
    mkdir -p "$DATA_DIR/certs"; chmod 700 "$DATA_DIR/certs" 2>/dev/null || true
    colorized_echo blue "Generating self-signed certificate (SAN: $san)..."
    openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 \
        -keyout "$SSL_KEY_FILE" -out "$SSL_CERT_FILE" -days 3650 -nodes \
        -subj "/CN=$ip" -addext "subjectAltName = $san" >/dev/null 2>&1 \
        || die "openssl certificate generation failed"
    chmod 600 "$SSL_KEY_FILE" 2>/dev/null || true
    colorized_echo green "✓ Certificate: $SSL_CERT_FILE"
}

write_fixed_config() {
    [ -f "$FIXED_CONFIG_FILE" ] && { colorized_echo cyan "Keeping existing $FIXED_CONFIG_FILE"; return; }
    cat >"$FIXED_CONFIG_FILE" <<'JSON'
{
  "log": { "loglevel": "warning" },
  "inbounds": [
    { "tag": "vless-in", "listen": "0.0.0.0", "port": 443, "protocol": "vless",
      "settings": { "clients": [], "decryption": "none" }, "streamSettings": { "network": "tcp" } }
  ],
  "outbounds": [ { "tag": "direct", "protocol": "freedom" } ]
}
JSON
    colorized_echo green "✓ Fixed core config: $FIXED_CONFIG_FILE (edit to taste)"
}

write_env() {
    local master="$1" core="$2" http_port="$3" grpc_port="$4" force_inbounds="$5"
    mkdir -p "$APP_DIR"
    umask 077
    cat >"$ENV_FILE" <<EOF
# API_KEY is unused by the multi-tenant agent; set only to silence a harmless warning.
API_KEY=$(gen_uuid)
PG_AGENT_HTTP_ADDR=:$http_port
PG_AGENT_GRPC_ADDR=:$grpc_port
PG_AGENT_MASTER_KEY=$master
PG_AGENT_CORE_KEY=$core
PG_AGENT_TENANT_DB=$DATA_DIR/tenants.bolt
PG_AGENT_FIXED_CONFIG=$FIXED_CONFIG_FILE
PG_AGENT_ENFORCE_INTERVAL=10s
# Optional override: pin every customer's users to these inbound tags regardless
# of what their panel sends. Leave empty to auto-derive the proxy inbound tags
# from the running core config (recommended: keeps working after you push a new
# config with different inbound tags from your PasarGuard panel).
PG_AGENT_FORCE_INBOUNDS=$force_inbounds
SSL_CERT_FILE=$SSL_CERT_FILE
SSL_KEY_FILE=$SSL_KEY_FILE
XRAY_EXECUTABLE_PATH=$XRAY_DIR/xray
XRAY_ASSETS_PATH=$XRAY_DIR
# Used by the self-update endpoint to restart the right systemd unit.
PG_AGENT_SERVICE_NAME=$APP_NAME
EOF
    umask 022
    colorized_echo green "✓ Wrote $ENV_FILE"
}

write_service() {
    colorized_echo blue "Creating systemd unit $SERVICE_UNIT"
    cat >"$SERVICE_UNIT" <<EOF
[Unit]
Description=PasarGuard multi-tenant node agent ($APP_NAME)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=$ENV_FILE
WorkingDirectory=$DATA_DIR
ExecStart=$BIN_PATH
# Allow binding privileged ports (e.g. 443) and WireGuard kernel ops.
AmbientCapabilities=CAP_NET_BIND_SERVICE CAP_NET_ADMIN
Restart=on-failure
RestartSec=5
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
}

install_node_script() {
    local src="${1:-}"
    [ -z "$src" ] && src="$(command -v "$0" 2>/dev/null || true)"
    if [ -n "$src" ] && [ -f "$src" ]; then
        install -m 755 "$src" "/usr/local/bin/$APP_NAME" 2>/dev/null && \
            { colorized_echo green "✓ CLI installed: /usr/local/bin/$APP_NAME"; return; }
    fi
    # Fallback (e.g. piped via `bash -c "$(curl ...)"` where $0 is not a file):
    # fetch the script from GitHub so the CLI is always installed.
    local tmp; tmp="$(mktemp)"
    if curl -fsSL "$RAW_SCRIPT_URL" -o "$tmp" 2>/dev/null && [ -s "$tmp" ]; then
        install -m 755 "$tmp" "/usr/local/bin/$APP_NAME" 2>/dev/null && \
            colorized_echo green "✓ CLI installed: /usr/local/bin/$APP_NAME"
    fi
    rm -f "$tmp"
}

# self_update_script refreshes the installed CLI itself from GitHub so script
# improvements (not just the binary) reach already-installed hosts on `update`.
self_update_script() {
    local dest="/usr/local/bin/$APP_NAME"
    [ -f "$dest" ] || return 0
    local tmp; tmp="$(mktemp)"
    if curl -fsSL "$RAW_SCRIPT_URL" -o "$tmp" 2>/dev/null && [ -s "$tmp" ]; then
        install -m 755 "$tmp" "$dest" && colorized_echo green "✓ CLI script refreshed: $dest"
    fi
    rm -f "$tmp"
}

# ---------- commands ----------
install_command() {
    check_root; require_systemd
    # force_inbounds is empty by default: the agent auto-derives the proxy inbound
    # tags from the running core config, so users land on the real inbound(s) even
    # after the operator pushes a custom config from their panel. Set it only to
    # pin users to a subset of the node's proxy inbounds.
    local master="" core="" http_port="8090" grpc_port="62050" version="latest" force_inbounds=""
    while [[ $# -gt 0 ]]; do case "$1" in
        --master-key) master="$2"; shift 2 ;;
        --core-key) core="$2"; shift 2 ;;
        --http-port) http_port="$2"; shift 2 ;;
        --grpc-port) grpc_port="$2"; shift 2 ;;
        --version) version="$2"; shift 2 ;;
        --force-inbounds) force_inbounds="$2"; shift 2 ;;
        --san-entries) INSTALL_SAN_ENTRIES="$2"; shift 2 ;;
        -y|--yes) shift ;;
        *) die "Unknown install option: $1" ;;
    esac; done

    # Preserve keys on reinstall.
    if [ -f "$ENV_FILE" ]; then
        [ -z "$master" ] && master="$(grep -E '^PG_AGENT_MASTER_KEY=' "$ENV_FILE" | cut -d= -f2-)"
        [ -z "$core" ] && core="$(grep -E '^PG_AGENT_CORE_KEY=' "$ENV_FILE" | cut -d= -f2-)"
    fi
    [ -z "$master" ] && master="$(gen_key)"
    # Generate a core key by default so the operator can manage the Xray core
    # from their own PasarGuard panel out of the box (only this key may push config).
    # It MUST be a UUID: a PasarGuard panel validates a node's API key as a UUID
    # and rejects anything else, so a hex key can't be used to add the node there.
    [ -z "$core" ] && core="$(gen_uuid)"

    detect_os; detect_arch
    need curl curl; need openssl openssl; need tar tar

    mkdir -p "$DATA_DIR"
    local tag="$version"
    [ "$version" = "latest" ] && tag="$(latest_tag)"
    download_binary "$tag"
    [ -x "$XRAY_DIR/xray" ] || install_xray latest
    [ -f "$SSL_CERT_FILE" ] || gen_self_signed_cert
    write_fixed_config
    write_env "$master" "$core" "$http_port" "$grpc_port" "$force_inbounds"
    write_service
    install_node_script || true

    warn_if_port_in_use "$http_port" "HTTP کنترل"
    warn_if_port_in_use "$grpc_port" "gRPC"

    systemctl enable --now "$APP_NAME"

    local ip; ip="$(public_ip)"
    colorized_echo blue "================================"
    colorized_echo green "PasarGuard node agent is running (systemd: $APP_NAME)."
    colorized_echo magenta "  Address (HTTPS control):  https://$ip:$http_port"
    [ -n "$core" ] && colorized_echo magenta "  gRPC (PasarGuard-compat): $ip:$grpc_port"
    colorized_echo magenta "  Master key (register in main panel):"
    colorized_echo red "    $master"
    [ -n "$core" ] && { colorized_echo magenta "  Core key (PasarGuard core management):"; colorized_echo red "    $core"; }
    colorized_echo magenta "  Node certificate (paste into the main panel when adding the node):"
    cat "$SSL_CERT_FILE"
    colorized_echo blue "================================"
}

up_command()      { check_root; require_installed; systemctl start "$APP_NAME"; colorized_echo green "started."; }
down_command()    { check_root; require_installed; systemctl stop "$APP_NAME"; colorized_echo green "stopped."; }
restart_command() { check_root; require_installed; systemctl restart "$APP_NAME"; colorized_echo green "restarted."; }
status_command()  { require_installed; systemctl status --no-pager "$APP_NAME"; }
logs_command()    { require_installed; journalctl -u "$APP_NAME" -f --no-pager; }
edit_env_command(){ require_installed; "${EDITOR:-nano}" "$ENV_FILE"; systemctl restart "$APP_NAME"; }
edit_command()    { require_installed; "${EDITOR:-nano}" "$FIXED_CONFIG_FILE"; colorized_echo yellow "Apply via the main panel (PUT /nodes/{id}/config) or 'restart'."; }

update_command() {
    check_root; require_installed; detect_arch
    self_update_script
    local version="${1:-latest}" tag
    tag="$version"; [ "$version" = "latest" ] && tag="$(latest_tag)"
    download_binary "$tag"
    systemctl restart "$APP_NAME"
    colorized_echo green "node updated to $tag and restarted."
}

core_update_command() {
    check_root; require_installed; detect_arch
    install_xray "${1:-latest}"
    systemctl restart "$APP_NAME"
    colorized_echo green "Xray-core updated and node restarted."
}

renew_cert_command() {
    check_root; require_installed
    gen_self_signed_cert
    systemctl restart "$APP_NAME"
    colorized_echo magenta "New certificate (re-pin in the main panel):"; cat "$SSL_CERT_FILE"
}

uninstall_command() {
    check_root
    systemctl disable --now "$APP_NAME" 2>/dev/null || true
    rm -f "$SERVICE_UNIT"; systemctl daemon-reload 2>/dev/null || true
    rm -f "$BIN_PATH" "/usr/local/bin/$APP_NAME"
    rm -rf "$APP_DIR"
    colorized_echo green "node uninstalled. Data kept at $DATA_DIR (remove manually if desired)."
}

completion_command() {
    check_root
    local f="/etc/bash_completion.d/$APP_NAME"
    cat >"$f" <<EOF
_pgnode(){ COMPREPLY=( \$(compgen -W "install update uninstall up down restart status logs core-update renew-cert edit edit-env install-script completion help" -- "\${COMP_WORDS[COMP_CWORD]}") ); }
complete -F _pgnode $APP_NAME
EOF
    colorized_echo green "✓ Bash completion installed: $f"
}

usage() {
    colorized_echo cyan "pg-node — multi-tenant node agent (prebuilt binary + systemd)"
    echo "Usage: $APP_NAME <command> [options]"
    echo
    echo "Commands:"
    echo "  install            Download binary + Xray and start the systemd service"
    echo "  update [VER]       Download a newer agent binary and restart (default: latest)"
    echo "  uninstall          Stop and remove the node"
    echo "  up | down | restart | status | logs"
    echo "  core-update [VER]  Install/replace Xray-core (default: latest)"
    echo "  renew-cert         Regenerate the self-signed certificate"
    echo "  edit | edit-env    Edit fixed config / env"
    echo "  install-script | completion"
    echo
    echo "Install options:"
    echo "  --master-key KEY   Master key (auto-generated if omitted)"
    echo "  --core-key KEY     Enable PasarGuard-compat gRPC (core config) with this key"
    echo "  --http-port PORT   HTTP control port (default 8090)"
    echo "  --grpc-port PORT   gRPC compat port (default 62050)"
    echo "  --version vX.Y.Z   Install a specific release (default: latest)"
    echo "  --san-entries CSV  Extra cert SANs (e.g. DNS:node.example.com)"
    echo
    echo "Global options:"
    echo "  --name NAME        Instance name (paths/service/CLI). Default: pg-node-agent"
    echo "  -y, --yes          Non-interactive"
}

# Parse global flags (--name, -y) anywhere on the line; keep the rest in ARGS.
ARGS=()
while [[ $# -gt 0 ]]; do
    case "$1" in
    --name) APP_NAME="$2"; shift 2 ;;
    --name=*) APP_NAME="${1#*=}"; shift ;;
    -y|--yes) AUTO_CONFIRM=true; shift ;;
    *) ARGS+=("$1"); shift ;;
    esac
done
set -- "${ARGS[@]+"${ARGS[@]}"}"
validate_name "$APP_NAME" || die "invalid --name '$APP_NAME' (1-63 chars: letters/digits/_/-, starting alnum)."
set_paths

cmd="${1:-help}"; shift || true
case "$cmd" in
    install) install_command "$@" ;;
    update) update_command "$@" ;;
    uninstall) uninstall_command ;;
    up) up_command ;;
    down) down_command ;;
    restart) restart_command ;;
    status) status_command ;;
    logs) logs_command ;;
    core-update) core_update_command "$@" ;;
    renew-cert) renew_cert_command ;;
    edit) edit_command ;;
    edit-env) edit_env_command ;;
    install-script) check_root; install_node_script "$(pwd)/scripts/pg-node.sh" ;;
    completion) completion_command ;;
    help|-h|--help) usage ;;
    *) colorized_echo red "Unknown command: $cmd"; usage; exit 1 ;;
esac
