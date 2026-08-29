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
# Run as root:  make build && sudo make install

set -euo pipefail

BIN_SRC="output/mitia-ops"
BIN_DST="/usr/local/bin/mitia-ops"
PREFIX="/var/lib/mitia-ops"
ETC_DIR="/etc/mitia-ops"
ENV_FILE="$ETC_DIR/env"
KEY_FILE="$ETC_DIR/key"
UNIT_SRC="packaging/mitia-ops.service"
UNIT_DST="/etc/systemd/system/mitia-ops.service"

if [[ "$(id -u)" -ne 0 ]]; then
	echo "run as root (e.g. 'sudo make install')" >&2
	exit 1
fi

if [[ ! -x "$BIN_SRC" ]]; then
	echo "'$BIN_SRC' not found — run 'make build' first" >&2
	exit 1
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
if [[ -f "$ENV_FILE" ]]; then
	: # operator-managed env (MITIAOPS_KEY and/or MITIAOPS_KEY_FILE) — keep it
elif [[ -f "$KEY_FILE" ]]; then
	: # previous install generated one — keep it
elif [[ -e "$PREFIX/data" && -e "$PREFIX/data/mitiaops.db" ]]; then
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
if [[ -d data && ! -e "$PREFIX/data" ]]; then
	cp -a data "$PREFIX/data"
	echo "../data -> $PREFIX/data"
fi
if [[ -d deployments && ! -e "$PREFIX/deployments" ]]; then
	cp -a deployments "$PREFIX/deployments"
	echo "../deployments -> $PREFIX/deployments"
fi

# --- unit ------------------------------------------------------------------
install -m 0644 -o root -g root "$UNIT_SRC" "$UNIT_DST"
if command -v systemd-analyze >/dev/null 2>&1; then
	systemd-analyze verify "$UNIT_DST"
fi

systemctl daemon-reload
systemctl enable mitia-ops >/dev/null

# If an unsupervised instance is already running, do not race it for the listen
# port — stop it first so the unit takes over cleanly.
if command -v pgrep >/dev/null 2>&1; then
	UNSUPERVISED=$(pgrep -f "$BIN_DST" || true)
	if [[ -n "$UNSUPERVISED" && -z "$(systemctl is-active mitia-ops 2>/dev/null || true)" ]]; then
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
echo "lock the dashboard to localhost and reach it over an SSH tunnel:"
echo "  echo MITIAOPS_ADDR=127.0.0.1:8080 >> $ENV_FILE && systemctl restart mitia-ops"
echo "  ssh -L 8080:127.0.0.1:8080 user@vps"
echo "forward other services the same way, e.g. mailcow: ssh -L 2111:127.0.0.1:2111 user@vps"