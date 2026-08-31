#!/usr/bin/env bash
# Install mitia-ops as a systemd service that starts at boot.
#
# Designed to be idempotent and safe on an existing deploy:
#   * existing /var/lib/mitia-ops data/deployments are left untouched (a
#     freshly-created layout only is migrated from this checkout)
#   * an existing /etc/mitia-ops/env is never clobbered
#   * a master key is only generated when there is no existing encrypted
#     store to keep readable — see MASTER KEY below
#
# Run as root. If a binary was built locally (make build), that is installed;
# otherwise the latest GitHub release binary for this OS/arch is downloaded
# automatically — no extra flags needed:
#   sudo ./scripts/install.sh
#   # or one-liner:
#   curl -fsSL https://raw.githubusercontent.com/jullury/mitia-ops/main/scripts/install.sh | sudo bash

set -euo pipefail

BIN_SRC="output/mitia-ops"
BIN_DST="/usr/local/bin/mitia-ops"
PREFIX="/var/lib/mitia-ops"
ETC_DIR="/etc/mitia-ops"
ENV_FILE="$ETC_DIR/env"
KEY_FILE="$ETC_DIR/key"
UNIT_DST="/etc/systemd/system/mitia-ops.service"
# Pin a specific release (e.g. MITIAOPS_VERSION=v1.2.2) instead of resolving
# `latest`; else the latest GitHub release is installed. Defaults to "latest".
VERSION="${MITIAOPS_VERSION:-latest}"

if [ "$(id -u)" -ne 0 ]; then
	echo "run as root (e.g. 'sudo make install')" >&2
	exit 1
fi

# --- binary ----------------------------------------------------------------
# Prefer a locally-built binary; otherwise download the latest release for this
# OS/arch. This is what makes a fresh machine a one-liner instead of needing a
# Go toolchain.
fetch_binary() {
	url="$1"
	tmp="$(mktemp)"
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$url" -o "$tmp"
	elif command -v wget >/dev/null 2>&1; then
		wget -qO "$tmp" "$url"
	else
		echo "need curl or wget to download the binary" >&2
		return 1
	fi
	mkdir -p "$(dirname "$BIN_SRC")"
	install -m 0755 "$tmp" "$BIN_SRC"
	rm -f "$tmp"
}

if [ ! -x "$BIN_SRC" ]; then
	# Resolve the latest release asset name for this platform.
	local_os="$(uname -s | tr '[:upper:]' '[:lower:]')"
	local_arch="$(uname -m)"
	case "$local_os:$local_arch" in
		linux:x86_64) asset="mitia-ops-linux-amd64" ;;
		linux:aarch64 | linux:arm64) asset="mitia-ops-linux-arm64" ;;
		linux:armv7l | linux:armv6l) asset="mitia-ops-linux-arm" ;;
		darwin:x86_64) asset="mitia-ops-darwin-amd64" ;;
		darwin:arm64) asset="mitia-ops-darwin-arm64" ;;
		*) echo "unsupported platform: $local_os/$local_arch" >&2; exit 1 ;;
	esac
	if [ "$VERSION" = "latest" ]; then
		url="https://github.com/jullury/mitia-ops/releases/latest/download/$asset"
	else
		url="https://github.com/jullury/mitia-ops/releases/download/$VERSION/$asset"
	fi
	echo "downloading $VERSION binary ($url)"
	fetch_binary "$url"
fi

# --- master key ------------------------------------------------------------
# Secrets in data/mitiaops.db are encrypted with the master key chosen via
# MITIAOPS_KEY or MITIAOPS_KEY_FILE. Pointing the service at a different key
# than the one that encrypted an existing DB would make every stored secret
# unreadable, so:
#   * an existing /etc/mitia-ops/env decides everything (never clobbered),
#   * else an existing key file wins,
#   * else, only when nothing encrypted exists to preserve, we generate a key.
mkdir -p "$ETC_DIR"
if [ -f "$ENV_FILE" ]; then
	: # operator-managed env (MITIAOPS_KEY and/or MITIAOPS_KEY_FILE) — keep it
elif [ -f "$KEY_FILE" ]; then
	: # previous install generated one — keep it
elif [ -e "$PREFIX/data" ] && [ -e "$PREFIX/data/mitiaops.db" ]; then
	echo "WARNING: existing encrypted store found at $PREFIX/data/mitiaops.db but no" >&2
	echo "master key under $ETC_DIR. Refusing to invent one (it would lock you out)." >&2
	echo "Set your existing key first, e.g.:" >&2
	echo "  sudo sh -c 'echo MITIAOPS_KEY=your-current-key > $ENV_FILE'" >&2
	exit 1
else
	umask 077
	openssl rand -hex 32 > "$KEY_FILE" 2>/dev/null || \
		od -An -N32 -tx1 /dev/urandom | tr -d ' \n' > "$KEY_FILE"
	printf 'MITIAOPS_KEY_FILE=%s\n' "$KEY_FILE" > "$ENV_FILE"
	echo "generated a fresh master key at $KEY_FILE"
fi
chmod 600 "$KEY_FILE" 2>/dev/null || true
chmod 600 "$ENV_FILE"

# --- layout -----------------------------------------------------------------
mkdir -p "$PREFIX"

install -m 0755 -o root -g root "$BIN_SRC" "$BIN_DST"

# Migrate an existing checkout's runtime data only when the target is empty;
# never clobber whatever the service already manages in /var/lib/mitia-ops.
if [ -d data ] && [ ! -e "$PREFIX/data" ]; then
	cp -a data "$PREFIX/data"
	echo "../data -> $PREFIX/data"
fi
if [ -d deployments ] && [ ! -e "$PREFIX/deployments" ]; then
	cp -a deployments "$PREFIX/deployments"
	echo "../deployments -> $PREFIX/deployments"
fi

# --- unit ------------------------------------------------------------------
# Written inline (not copied from the repo) so the one-liner works from any
# working directory — the unit file is self-contained and fixed.
cat > "$UNIT_DST" <<'EOF'
[Unit]
Description=mitia-ops - single-binary dashboard for self-hosted services
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/var/lib/mitia-ops
EnvironmentFile=/etc/mitia-ops/env
ExecStart=/usr/local/bin/mitia-ops
Restart=on-failure
RestartSec=3
TimeoutStopSec=30
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF
chmod 0644 "$UNIT_DST"
if command -v systemd-analyze >/dev/null 2>&1; then
	systemd-analyze verify "$UNIT_DST"
fi

systemctl daemon-reload
systemctl enable mitia-ops >/dev/null

# If an unsupervised instance is already running, do not race it for the listen
# port — stop it first so the unit takes over cleanly.
if command -v pgrep >/dev/null 2>&1; then
	UNSUPERVISED=$(pgrep -f "$BIN_DST" || true)
	if [ -n "$UNSUPERVISED" ] && [ -z "$(systemctl is-active mitia-ops 2>/dev/null || true)" ]; then
		echo "stopping an unsupervised mitia-ops so systemd can take over"
		kill $UNSUPERVISED
	fi
fi

if systemctl is-active --quiet mitia-ops 2>/dev/null; then
	echo "mitia-ops already active; unit re-enabled"
else
	systemctl start mitia-ops
	echo "started mitia-ops"
fi

systemctl --no-pager --full status mitia-ops || true
echo
echo "installed: $BIN_DST, unit=$UNIT_DST, cwd=$PREFIX, env=$ENV_FILE"
echo "binary version: $($BIN_DST --version 2>/dev/null || echo unknown)"
echo "lock the dashboard to localhost and reach it over an SSH tunnel:"
echo "  echo MITIAOPS_ADDR=127.0.0.1:8080 >> $ENV_FILE && systemctl restart mitia-ops"
echo "  ssh -L 8080:127.0.0.1:8080 user@vps"
echo "forward other services the same way, e.g. mailcow: ssh -L 2111:127.0.0.1:2111 user@vps"