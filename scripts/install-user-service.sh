#!/usr/bin/env bash
#
# Install indexarr as a systemd service and start it.
#
# The default is a user service for the current account, without sudo. With
# --system the script installs a system-wide service for a dedicated account.
#
#   ./install.sh                          # user service, build if possible
#   ./install.sh --listen 127.0.0.1:8788  # local only
#   ./install.sh --binary /tmp/indexarr   # prebuilt binary, no Go needed
#   sudo ./install.sh --system            # system service in /var/lib/indexarr
#   ./install.sh --status
#   ./install.sh --uninstall              # data is kept
#
# The data directory (configuration + SQLite database) lives outside the unit
# and is never deleted by --uninstall.
set -euo pipefail
umask 077

NAME="indexarr"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_SRC="${ROOT}/bin/indexarr"
BIN_TARGET=""
DATA_DIR=""
DATA_DIR_EXPLICIT=0
LISTEN=":8788"
MODE="user"
SERVICE_USER="indexarr"
BUILD=1
LINGER=0
DRY_RUN=0
ACTION="install"

usage() {
	cat <<'TXT'
Usage: install.sh [options]

Modes:
  (default)          user service, started by the current account
  --system           system-wide service (requires root/sudo)

Options:
  --name NAME        Service name (default: indexarr)
  --data-dir DIR     Configuration and database directory
                     (default: <repo>/data for the user service,
                     /var/lib/indexarr for --system)
  --listen ADDR      HTTP address, for example :8788 or 127.0.0.1:8788
  --binary FILE      Prebuilt binary to install (skips the build)
  --user USER        Account of the system service (default: indexarr)
  --build            Force a rebuild with make
  --no-build         Use bin/indexarr as-is
  --linger           User service: keep running after logout (loginctl)
  --dry-run          Print the plan and the unit, change nothing
  --status           Show the installed service and exit
  --uninstall        Stop and remove the service (data is kept)
  -h, --help         This help

Without --build/--no-build the script rebuilds when Go and make are available,
and otherwise uses an existing bin/indexarr.
TXT
}

die() {
	echo "error: $*" >&2
	exit 1
}

need_value() {
	[[ $# -ge 2 ]] || die "$1 requires a value"
}

while (($#)); do
	case "$1" in
	--name)
		need_value "$@"
		NAME="$2"
		shift 2
		;;
	--data-dir)
		need_value "$@"
		DATA_DIR="$2"
		DATA_DIR_EXPLICIT=1
		shift 2
		;;
	--listen)
		need_value "$@"
		LISTEN="$2"
		shift 2
		;;
	--binary)
		need_value "$@"
		BIN_SRC="$2"
		BUILD=0
		shift 2
		;;
	--user)
		need_value "$@"
		SERVICE_USER="$2"
		shift 2
		;;
	--system)
		MODE="system"
		shift
		;;
	--build)
		BUILD=1
		shift
		;;
	--no-build)
		BUILD=0
		shift
		;;
	--linger)
		LINGER=1
		shift
		;;
	--dry-run)
		DRY_RUN=1
		shift
		;;
	--status)
		ACTION="status"
		shift
		;;
	--uninstall)
		ACTION="uninstall"
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		die "unknown option: $1 (use --help)"
		;;
	esac
done

[[ "$NAME" =~ ^[A-Za-z0-9_.-]+$ ]] || die "invalid service name: $NAME"
[[ "$LISTEN" =~ ^.*:[0-9]+$ ]] || die "invalid address: $LISTEN (expected host:port)"
if [[ "$MODE" == "system" && "$LINGER" == 1 ]]; then
	echo "warning: --linger only applies to the user service; ignored." >&2
	LINGER=0
fi

if [[ "$MODE" == "system" ]]; then
	if [[ "${EUID}" -ne 0 ]]; then
		die "--system requires root: run it with sudo"
	fi
	if [[ -z "$DATA_DIR" ]]; then
		DATA_DIR="/var/lib/indexarr"
	fi
	BIN_TARGET="/usr/local/bin/indexarr"
	UNIT_DIR="/etc/systemd/system"
	UNIT="${UNIT_DIR}/${NAME}.service"
	SYSTEMCTL=(systemctl)
	JOURNAL=(journalctl)
	LOGS_CMD="journalctl -u ${NAME} -f"
	WANTED_BY="multi-user.target"
	DOCUMENTATION="https://github.com/buzzqw/indexarr"
else
	if [[ "${EUID}" -eq 0 && -z "${SUDO_USER:-}" ]]; then
		die "a user service must be installed by its owner, not by root; use --system for a system service"
	fi
	if [[ -z "$DATA_DIR" ]]; then
		if [[ "$NAME" == "indexarr" ]]; then
			DATA_DIR="${ROOT}/data"
		else
			DATA_DIR="${ROOT}/data-${NAME}"
		fi
	fi
	BIN_TARGET="$BIN_SRC"
	UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
	UNIT="${UNIT_DIR}/${NAME}.service"
	SYSTEMCTL=(systemctl --user)
	JOURNAL=(journalctl --user)
	LOGS_CMD="journalctl --user -u ${NAME} -f"
	WANTED_BY="default.target"
	DOCUMENTATION="file://${ROOT}/README.md"
fi

# systemctl --user needs the user manager socket. In a bare SSH shell the
# runtime directory is sometimes missing from the environment.
if [[ "$MODE" == "user" && -z "${XDG_RUNTIME_DIR:-}" && -d "/run/user/$(id -u)" ]]; then
	XDG_RUNTIME_DIR="/run/user/$(id -u)"
	export XDG_RUNTIME_DIR
fi
command -v systemctl >/dev/null 2>&1 || die "systemctl not found"
# A dry run only prints the plan and the unit; it must work on a machine where
# the service manager is not running (a container, a rescue shell).
if [[ "$DRY_RUN" == 0 ]]; then
	if [[ "$MODE" == "user" ]]; then
		"${SYSTEMCTL[@]}" show-environment >/dev/null 2>&1 ||
			die "systemd --user is not reachable (XDG_RUNTIME_DIR=${XDG_RUNTIME_DIR:-unset})"
	else
		"${SYSTEMCTL[@]}" show --property=Version >/dev/null 2>&1 ||
			die "systemd is not reachable"
	fi
fi

show_status() {
	"${SYSTEMCTL[@]}" --no-pager --lines=0 status "${NAME}.service" || true
	echo
	echo "Logs: ${LOGS_CMD}"
}

case "$ACTION" in
status)
	show_status
	exit 0
	;;
uninstall)
	# The data directory lives outside the unit, but the unit records it in
	# WorkingDirectory: use that for the final message unless the caller passed
	# --data-dir explicitly.
	if [[ "$DATA_DIR_EXPLICIT" == 0 && -f "$UNIT" ]]; then
		unit_data="$(sed -n 's/^WorkingDirectory=//p' "$UNIT" | head -1)"
		if [[ -n "$unit_data" ]]; then
			DATA_DIR="$unit_data"
		fi
	fi
	if [[ "$DRY_RUN" == 1 ]]; then
		echo "dry run: would stop ${NAME}.service, remove ${UNIT} and keep ${DATA_DIR}"
		exit 0
	fi
	"${SYSTEMCTL[@]}" disable --now "${NAME}.service" >/dev/null 2>&1 || true
	rm -f "$UNIT"
	"${SYSTEMCTL[@]}" daemon-reload
	"${SYSTEMCTL[@]}" reset-failed "${NAME}.service" >/dev/null 2>&1 || true
	echo "Service ${NAME} removed. Data is kept in ${DATA_DIR}."
	exit 0
	;;
esac

# Build by default; --no-build and --binary skip it. When Go or make are
# missing, or the script runs from a release archive without a source tree,
# fall back to the binary shipped next to it instead of failing.
if [[ "$BUILD" == 1 ]]; then
	if command -v make >/dev/null 2>&1 && command -v go >/dev/null 2>&1 && [[ -f "$ROOT/Makefile" ]]; then
		[[ "$DRY_RUN" == 1 ]] || {
			echo "Building ${BIN_SRC} ..."
			make -C "$ROOT" build
		}
		BIN_SRC="${ROOT}/bin/indexarr"
	elif [[ -x "$BIN_SRC" ]]; then
		echo "No source tree or Go toolchain here: using the existing $BIN_SRC (use --build to force a rebuild)."
	elif [[ "$MODE" == "system" && -x "$BIN_TARGET" ]]; then
		echo "Go or make not found: using the installed $BIN_TARGET."
		BIN_SRC="$BIN_TARGET"
	else
		die "Go and make are not available and $BIN_SRC is missing; pass --binary FILE"
	fi
fi
[[ "$DRY_RUN" == 1 || -x "$BIN_SRC" ]] || die "binary not found or not executable: $BIN_SRC"

if [[ "$DRY_RUN" == 0 ]]; then
	mkdir -p "$DATA_DIR"
	chmod 700 "$DATA_DIR"
	DATA_DIR="$(cd "$DATA_DIR" && pwd)"
fi
CONFIG="${DATA_DIR}/indexarr.json"
# systemd path and ExecStart options are not quoted here: a path containing
# whitespace would be split. Fail early instead of generating a broken unit.
case "${BIN_SRC}${BIN_TARGET}${DATA_DIR}" in
*[[:space:]]*) die "the binary or data path contains whitespace" ;;
esac

# The app creates a default config on first start; write it here so the file is
# private from the beginning (umask 077).
CONFIG_ACTION="keep the existing ${CONFIG}"
if [[ ! -f "$CONFIG" ]]; then
	CONFIG_ACTION="create ${CONFIG}"
fi

UNIT_CONTENT="[Unit]
Description=indexarr Torznab indexer (${NAME})
Documentation=${DOCUMENTATION}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=${DATA_DIR}
ExecStart=${BIN_TARGET} -config ${CONFIG} -listen ${LISTEN}
Restart=always
RestartSec=5s
TimeoutStopSec=20s
UMask=0077

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ReadWritePaths=${DATA_DIR}

[Install]
WantedBy=${WANTED_BY}
"

if [[ "$MODE" == "system" ]]; then
	UNIT_CONTENT="[Unit]
Description=indexarr Torznab indexer (${NAME})
Documentation=${DOCUMENTATION}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${SERVICE_USER}
Group=${SERVICE_USER}
Environment=HOME=${DATA_DIR}
WorkingDirectory=${DATA_DIR}
ExecStart=${BIN_TARGET} -config ${CONFIG} -listen ${LISTEN}
Restart=always
RestartSec=5s
TimeoutStopSec=20s
UMask=0077

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=${DATA_DIR}

[Install]
WantedBy=${WANTED_BY}
"
fi

if [[ "$DRY_RUN" == 1 ]]; then
	echo "indexarr install plan (dry run, nothing changed)"
	echo "  mode:     ${MODE} systemd service"
	echo "  service:  ${NAME}"
	echo "  binary:   ${BIN_TARGET}"
	echo "  data:     ${DATA_DIR}"
	echo "  config:   ${CONFIG_ACTION}"
	echo "  listen:   ${LISTEN}"
	echo "  unit:     ${UNIT}"
	if [[ "$MODE" == "system" ]]; then
		echo "  account:  ${SERVICE_USER}"
	fi
	echo
	echo "--- ${UNIT} ---"
	printf '%s' "$UNIT_CONTENT"
	exit 0
fi

# A system service needs the dedicated account and its private data directory.
if [[ "$MODE" == "system" ]]; then
	if ! id -u "$SERVICE_USER" >/dev/null 2>&1; then
		command -v useradd >/dev/null 2>&1 || die "useradd not found"
		useradd --system --home "$DATA_DIR" --shell "$(command -v nologin || echo /usr/sbin/nologin)" "$SERVICE_USER"
		echo "Created system user ${SERVICE_USER}."
	fi
	if [[ "$BIN_SRC" != "$BIN_TARGET" ]]; then
		install -m 0755 "$BIN_SRC" "$BIN_TARGET"
		echo "Installed ${BIN_TARGET}."
	fi
	install -d -m 0700 -o "$SERVICE_USER" -g "$SERVICE_USER" "$DATA_DIR"
fi

if [[ ! -f "$CONFIG" ]]; then
	cat >"$CONFIG" <<EOF
{
  "listen": "${LISTEN}",
  "db_path": "indexarr.db",
  "log_level": "info"
}
EOF
	chmod 600 "$CONFIG"
	if [[ "$MODE" == "system" ]]; then
		chown "$SERVICE_USER:$SERVICE_USER" "$CONFIG"
	fi
fi

mkdir -p "$UNIT_DIR"
printf '%s' "$UNIT_CONTENT" >"$UNIT"

echo "Unit written to ${UNIT}"
"${SYSTEMCTL[@]}" daemon-reload
"${SYSTEMCTL[@]}" enable "${NAME}.service" >/dev/null
if ! "${SYSTEMCTL[@]}" restart "${NAME}.service"; then
	show_status
	exit 1
fi

if [[ "$LINGER" == 1 && "$MODE" == "user" ]]; then
	current="$(loginctl show-user "$(id -un)" -p Linger --value 2>/dev/null || echo unknown)"
	if [[ "$current" == "yes" ]]; then
		echo "Linger is already enabled for $(id -un)."
	else
		if loginctl enable-linger "$(id -un)" 2>/dev/null; then
			echo "Linger enabled for $(id -un): the service stays up after logout."
		else
			echo "warning: cannot enable linger (authorization?)." >&2
			echo "         Run manually: loginctl enable-linger $(id -un)" >&2
		fi
	fi
fi

HEALTH_HOST="${LISTEN%:*}"
PORT="${LISTEN##*:}"
ALL_INTERFACES=0
case "$HEALTH_HOST" in
"" | "0.0.0.0" | "[::]" | "::" | "*")
	ALL_INTERFACES=1
	HEALTH_HOST="127.0.0.1"
	;;
esac
HEALTH_URL="http://${HEALTH_HOST}:${PORT}/healthz"
LAN_IP=""
if [[ "$ALL_INTERFACES" == 1 ]]; then
	# The source address of the default route is the address other LAN hosts
	# will use; fall back to the first non-loopback IPv4 of hostname -I.
	LAN_IP="$(ip route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p' | head -1 || true)"
	if [[ -z "$LAN_IP" ]]; then
		LAN_IP="$(hostname -I 2>/dev/null | tr ' ' '\n' | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' | grep -v '^127\.' | head -1 || true)"
	fi
fi

check_health() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsS --max-time 2 "$HEALTH_URL" >/dev/null 2>&1
	elif command -v wget >/dev/null 2>&1; then
		wget -q -T 2 -O /dev/null "$HEALTH_URL" >/dev/null 2>&1
	else
		"${SYSTEMCTL[@]}" is-active --quiet "${NAME}.service"
	fi
}

# shellcheck disable=SC2329 # called conditionally after the local health check
check_lan_health() {
	local url="http://${LAN_IP}:${PORT}/healthz"
	if command -v curl >/dev/null 2>&1; then
		curl -fsS --max-time 2 "$url" >/dev/null 2>&1
	elif command -v wget >/dev/null 2>&1; then
		wget -q -T 2 -O /dev/null "$url" >/dev/null 2>&1
	else
		return 0
	fi
}

for _ in $(seq 1 60); do
	if check_health; then
		echo
		echo "Service ${NAME} installed and running."
		echo "  unit:   ${UNIT}"
		echo "  data:   ${DATA_DIR}"
		echo "  local:  http://${HEALTH_HOST}:${PORT}/"
		if [[ -n "$LAN_IP" ]]; then
			echo "  LAN:    http://${LAN_IP}:${PORT}/"
		fi
		echo "  logs:   ${LOGS_CMD}"
		echo
		echo "The Torznab API key and the Sonarr/Radarr endpoint are in Settings."
		echo "Note: the UI has no authentication until you set a username and password in Settings."
		if [[ -n "$LAN_IP" ]]; then
			if check_lan_health; then
				echo "LAN check: the port answers on http://${LAN_IP}:${PORT}/."
			else
				echo "warning: the port answers locally but not on ${LAN_IP} (firewall?)." >&2
			fi
		fi
		show_status
		exit 0
	fi
	sleep 0.5
done

echo "warning: the service does not answer on ${HEALTH_URL}" >&2
"${SYSTEMCTL[@]}" --no-pager --lines=20 status "${NAME}.service" >&2 || true
echo >&2
echo "Last log lines:" >&2
"${JOURNAL[@]}" -u "$NAME" -n 20 --no-pager >&2 || true
exit 1
