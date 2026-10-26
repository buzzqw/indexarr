#!/usr/bin/env bash
#
# Install indexarr as a systemd --user service and start it.
#
#   ./scripts/install-user-service.sh                # build, install, start
#   ./scripts/install-user-service.sh --no-build     # use bin/indexarr as-is
#   ./scripts/install-user-service.sh --listen 0.0.0.0:8788
#   ./scripts/install-user-service.sh --linger       # also survive logout/reboot
#   ./scripts/install-user-service.sh --status
#   ./scripts/install-user-service.sh --uninstall
#
# The data directory (config + SQLite database) is created outside the unit
# and is never deleted by --uninstall.
set -euo pipefail
umask 077

NAME="indexarr"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="${ROOT}/bin/indexarr"
DATA_DIR=""
LISTEN=":8788"
BUILD=1
LINGER=0
ACTION="install"

usage() {
	cat <<'TXT'
Uso: install-user-service.sh [opzioni]

Opzioni:
  --name NOME        Nome del servizio (default: indexarr)
  --data-dir DIR     Directory di config e database (default: <progetto>/data)
  --listen ADDR      Indirizzo HTTP, per esempio :8788 o 127.0.0.1:8788
  --binary FILE      Binario da usare (default: <progetto>/bin/indexarr)
  --no-build         Non esegue make build
  --linger           Abilita loginctl enable-linger per l'utente corrente
  --status           Mostra lo stato del servizio installato ed esce
  --uninstall        Ferma e rimuove il servizio (i dati restano)
  -h, --help         Mostra questo aiuto

Senza --linger il servizio parte con la sessione utente e si ferma al logout.
TXT
}

die() {
	echo "errore: $*" >&2
	exit 1
}

while (($#)); do
	case "$1" in
	--name)
		NAME="${2:?--name richiede un valore}"
		shift 2
		;;
	--data-dir)
		DATA_DIR="${2:?--data-dir richiede un valore}"
		shift 2
		;;
	--listen)
		LISTEN="${2:?--listen richiede un valore}"
		shift 2
		;;
	--binary)
		BIN="${2:?--binary richiede un valore}"
		shift 2
		;;
	--no-build)
		BUILD=0
		shift
		;;
	--linger)
		LINGER=1
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
		die "opzione sconosciuta: $1 (usa --help)"
		;;
	esac
done

[[ "$NAME" =~ ^[A-Za-z0-9_.-]+$ ]] || die "nome servizio non valido: $NAME"
[[ "$LISTEN" =~ ^.*:[0-9]+$ ]] || die "indirizzo non valido: $LISTEN (atteso host:porta)"

if [[ -z "$DATA_DIR" ]]; then
	if [[ "$NAME" == "indexarr" ]]; then
		DATA_DIR="${ROOT}/data"
	else
		DATA_DIR="${ROOT}/data-${NAME}"
	fi
fi

# systemctl --user needs the user manager socket. In a bare SSH shell the
# runtime directory is sometimes missing from the environment.
if [[ -z "${XDG_RUNTIME_DIR:-}" && -d "/run/user/$(id -u)" ]]; then
	runtime_dir="/run/user/$(id -u)"
	export XDG_RUNTIME_DIR="$runtime_dir"
fi
command -v systemctl >/dev/null 2>&1 || die "systemctl non trovato"
systemctl --user show-environment >/dev/null 2>&1 ||
	die "systemd --user non raggiungibile (XDG_RUNTIME_DIR=${XDG_RUNTIME_DIR:-non impostata})"

UNIT_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
UNIT="${UNIT_DIR}/${NAME}.service"

show_status() {
	systemctl --user --no-pager --lines=0 status "$NAME.service" || true
	echo
	echo "Log: journalctl --user -u $NAME -f"
}

case "$ACTION" in
status)
	show_status
	exit 0
	;;
uninstall)
	systemctl --user disable --now "$NAME.service" >/dev/null 2>&1 || true
	rm -f "$UNIT"
	systemctl --user daemon-reload
	systemctl --user reset-failed "$NAME.service" >/dev/null 2>&1 || true
	echo "Servizio $NAME rimosso. I dati restano in $DATA_DIR."
	exit 0
	;;
esac

if [[ "$BUILD" == 1 ]]; then
	command -v make >/dev/null 2>&1 || die "make non trovato (usa --no-build)"
	echo "Compilazione in $ROOT ..."
	make -C "$ROOT" build
fi
[[ -x "$BIN" ]] || die "binario non trovato o non eseguibile: $BIN (esegui make build)"

mkdir -p "$DATA_DIR"
chmod 700 "$DATA_DIR"
DATA_DIR="$(cd "$DATA_DIR" && pwd)"
CONFIG="${DATA_DIR}/indexarr.json"
# systemd path and ExecStart options are not quoted here: a path containing
# whitespace would be split. Fail early instead of generating a broken unit.
case "$BIN$DATA_DIR" in
*[[:space:]]*) die "il percorso del binario o della directory dati contiene spazi" ;;
esac

# The app creates a default config on first start; write it here so the file is
# private from the beginning (umask 077).
if [[ ! -f "$CONFIG" ]]; then
	cat >"$CONFIG" <<EOF
{
  "listen": "${LISTEN}",
  "db_path": "indexarr.db",
  "log_level": "info"
}
EOF
	chmod 600 "$CONFIG"
fi

mkdir -p "$UNIT_DIR"

# Paths contain no whitespace (checked above), so they can stay unquoted:
# WorkingDirectory= and ReadWritePaths= do not accept shell-style quotes.
cat >"$UNIT" <<EOF
[Unit]
Description=indexarr Torznab indexer (${NAME})
Documentation=file://${ROOT}/README.md
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=${DATA_DIR}
ExecStart=${BIN} -config ${CONFIG} -listen ${LISTEN}
Restart=always
RestartSec=5s
TimeoutStopSec=20s
UMask=0077

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ReadWritePaths=${DATA_DIR}

[Install]
WantedBy=default.target
EOF

echo "Unità scritta in $UNIT"
systemctl --user daemon-reload
systemctl --user enable "$NAME.service" >/dev/null
systemctl --user restart "$NAME.service"

if [[ "$LINGER" == 1 ]]; then
	current="$(loginctl show-user "$USER" -p Linger --value 2>/dev/null || echo unknown)"
	if [[ "$current" == "yes" ]]; then
		echo "Linger già attivo per $USER."
	else
		if loginctl enable-linger "$USER" 2>/dev/null; then
			echo "Linger attivato per $USER: il servizio resta attivo dopo il logout."
		else
			echo "avviso: impossibile attivare linger (serve autorizzazione)." >&2
			echo "        Esegui a mano: loginctl enable-linger $USER" >&2
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
		systemctl --user is-active --quiet "$NAME.service"
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
		echo "Servizio $NAME installato e avviato."
		echo "  unit:   $UNIT"
		echo "  dati:   $DATA_DIR"
		echo "  locale: http://${HEALTH_HOST}:${PORT}/"
		if [[ -n "$LAN_IP" ]]; then
			echo "  LAN:    http://${LAN_IP}:${PORT}/"
		fi
		echo "  log:    journalctl --user -u $NAME -f"
		echo
		echo "La API key Torznab e l'endpoint per Sonarr/Radarr sono in Settings."
		echo "Nota: la UI è senza autenticazione finché non imposti utente e password in Settings."
		if [[ -n "$LAN_IP" ]]; then
			if check_lan_health; then
				echo "Verifica LAN: la porta risponde su http://${LAN_IP}:${PORT}/."
			else
				echo "avviso: la porta risponde in locale ma non su ${LAN_IP} (firewall?)." >&2
			fi
		fi
		show_status
		exit 0
	fi
	sleep 0.5
done

echo "avviso: il servizio non risponde su $HEALTH_URL" >&2
systemctl --user --no-pager --lines=20 status "$NAME.service" >&2 || true
echo >&2
echo "Ultime righe di log:" >&2
journalctl --user -u "$NAME.service" -n 20 --no-pager >&2 || true
exit 1
