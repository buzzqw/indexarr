#!/usr/bin/env bash
# fresh-start.sh — planned clean restart of indexarr (WP8 step 5).
#
# The default is a dry run: it checks the environment and prints every step it
# would take, without touching the data directory, the service or the network.
#
#   ./scripts/fresh-start.sh                       # dry run
#   ./scripts/fresh-start.sh --apply --build      # the real switch (user unit)
#   sudo ./scripts/fresh-start.sh --apply --build # system unit instead
#   ./scripts/fresh-start.sh --rollback            # restore data.old-* and the old binary
#
# What --apply does:
#   1. stops the service (systemd unit or the local process in indexarr.pid);
#   2. moves data/ to data.old-<timestamp>/ (never deletes it);
#   3. creates a fresh data directory and database;
#   4. copies the Torznab API key and the TorrentLeech RSS URL from the old
#      database into the new one (the feed URL is sealed and never printed);
#   5. imports Jackett/Prowlarr indexers as native indexers, reading the
#      manager keys from --env-file (default ~/.config/indexarr-dev/managers.env)
#      or the environment and passing them to the importer through the
#      environment (never on the command line, never printed);
#   6. starts the service again and waits for /healthz.
#
# With --build, step 0 compiles cmd/indexarr from this checkout into a temporary
# file, and the real --apply installs it as the binary after saving the previous
# one as <binary>.old-<timestamp>. A dry run with --build only prints the plan:
# it never compiles, never installs and never touches the current binary.
# --rollback restores <binary>.old-<timestamp> that matches the data.old-*
# directory it is restoring, when that backup exists.
#
# On any error after the data directory has been moved, the script stops the
# new service, moves the failed directory aside as data.failed-<timestamp>/,
# restores data.old-<timestamp>/, restores the previous binary when it was
# replaced, and starts the old service again.
#
# The Jackett import reads the live t=indexers list from --jackett (or
# JACKETT_URL). --jackett-copy is only a fallback used when that API does not
# answer; it is never the primary source. Indexers that fail the read-only caps
# probe are imported disabled with the reason in the summary.
#
# The manager keys come from --env-file (default
# ~/.config/indexarr-dev/managers.env) or from JACKETT_* / PROWLARR_* variables.
# The old database is used only for the Torznab API key of indexarr itself and
# for the personal TorrentLeech RSS URL, which is sealed in the vault.
#
# The script never writes to Jackett or Prowlarr: only GET requests.
#
# Options:
#   --apply                   execute (default: dry run)
#   --rollback                stop, move data/ to data.failed-*, restore data.old-*
#   --build                   build cmd/indexarr from this checkout; install the
#                             binary only with --apply, saving the old one
#   --data-dir DIR            data directory (default: <repo>/data)
#   --unit NAME               systemd unit (default: indexarr)
#   --binary FILE             indexarr binary (default: <repo>/bin/indexarr)
#   --importer FILE           indexarr-import binary (default: <repo>/bin/indexarr-import)
#   --listen ADDR             listen address for the new instance (default: from config, else :8484)
#   --port PORT               shorthand for --listen 127.0.0.1:PORT (test use)
#   --no-systemd              manage a local process with a PID file instead of systemd
#   --env-file FILE           Jackett/Prowlarr keys (default: ~/.config/indexarr-dev/managers.env)
#   --jackett URL             live Jackett base URL (default: JACKETT_URL)
#   --prowlarr URL            live Prowlarr base URL (default: PROWLARR_URL)
#   --jackett-copy DIR        copy of Jackett data, used only if the live API fails
#   --skip-import             do not import from Jackett/Prowlarr
#   --yes                     do not ask for confirmation with --apply/--rollback
#   -h, --help                this help
#
# Safety: production --apply/--rollback is for the coordinator. The dress
# rehearsal uses --data-dir on a copy, --port 18510 and --no-systemd.
set -euo pipefail
umask 077
export LC_ALL=C

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DATA_DIR="$ROOT/data"
UNIT="indexarr"
BINARY="$ROOT/bin/indexarr"
IMPORTER="$ROOT/bin/indexarr-import"
LISTEN=""
PORT=""
NO_SYSTEMD=0
BUILD=0
BUILD_TMP=""
BINARY_OLD=""
BINARY_INSTALLED=0
MODE="dry-run"
JACKETT_URL="${JACKETT_URL:-}"
PROWLARR_URL="${PROWLARR_URL:-}"
JACKETT_COPY=""
ENV_FILE="${HOME}/.config/indexarr-dev/managers.env"
ENV_FILE_EXPLICIT=0
SKIP_IMPORT=0
ASSUME_YES=0

usage() {
	sed -n '2,69p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

warn() {
	printf 'warning: %s\n' "$*" >&2
}

step() {
	printf '==> %s\n' "$*"
}

note() {
	printf '    %s\n' "$*"
}

cleanup_build() {
	if [[ -n "$BUILD_TMP" ]]; then
		rm -f "$BUILD_TMP" 2>/dev/null || true
	fi
}
trap cleanup_build EXIT

need_value() {
	[[ $# -ge 2 && -n "$2" ]] || die "$1 requires a value"
}

while (($#)); do
	case "$1" in
	--apply)
		MODE="apply"
		shift
		;;
	--rollback)
		MODE="rollback"
		shift
		;;
	--build)
		BUILD=1
		shift
		;;
	--data-dir)
		need_value "$1" "${2:-}"
		DATA_DIR="$2"
		shift 2
		;;
	--unit)
		need_value "$1" "${2:-}"
		UNIT="$2"
		shift 2
		;;
	--binary)
		need_value "$1" "${2:-}"
		BINARY="$2"
		shift 2
		;;
	--importer)
		need_value "$1" "${2:-}"
		IMPORTER="$2"
		shift 2
		;;
	--listen)
		need_value "$1" "${2:-}"
		LISTEN="$2"
		shift 2
		;;
	--port)
		need_value "$1" "${2:-}"
		PORT="$2"
		shift 2
		;;
	--no-systemd)
		NO_SYSTEMD=1
		shift
		;;
	--jackett)
		need_value "$1" "${2:-}"
		JACKETT_URL="$2"
		shift 2
		;;
	--prowlarr)
		need_value "$1" "${2:-}"
		PROWLARR_URL="$2"
		shift 2
		;;
	--env-file)
		need_value "$1" "${2:-}"
		ENV_FILE="$2"
		ENV_FILE_EXPLICIT=1
		shift 2
		;;
	--jackett-copy)
		need_value "$1" "${2:-}"
		JACKETT_COPY="$2"
		shift 2
		;;
	--skip-import)
		SKIP_IMPORT=1
		shift
		;;
	--yes)
		ASSUME_YES=1
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

# load_manager_env reads only the four manager variables from the env file.
# An explicit --env-file must exist; the default one may be absent. Values
# already present in the environment win, so a one-off override never requires
# editing the file. The keys are exported for the importer and never printed.
load_manager_env() {
	local file="$1" explicit="$2" line key value
	if [[ ! -f "$file" ]]; then
		((explicit == 0)) || die "env file not found: $file"
		return 0
	fi
	while IFS= read -r line || [[ -n "$line" ]]; do
		[[ "$line" =~ ^[[:space:]]*# ]] && continue
		[[ "$line" == *=* ]] || continue
		key="${line%%=*}"
		key="${key#export }"
		key="${key//[[:space:]]/}"
		value="${line#*=}"
		case "$value" in
		\"*\") value="${value:1:${#value}-2}" ;;
		\'*\') value="${value:1:${#value}-2}" ;;
		esac
		case "$key" in
		JACKETT_URL | JACKETT_API_KEY | PROWLARR_URL | PROWLARR_API_KEY)
			if [[ -z "${!key:-}" ]]; then
				printf -v "$key" '%s' "$value"
				export "$key"
			fi
			;;
		esac
	done <"$file"
}

load_manager_env "$ENV_FILE" "$ENV_FILE_EXPLICIT"
JACKETT_URL="${JACKETT_URL:-}"
PROWLARR_URL="${PROWLARR_URL:-}"
JACKETT_KEY="${JACKETT_API_KEY:-}"
PROWLARR_KEY="${PROWLARR_API_KEY:-}"

if [[ -n "$PORT" && -n "$LISTEN" ]]; then
	die "--port and --listen are mutually exclusive"
fi
if [[ "$MODE" == "rollback" && "$BUILD" == "1" ]]; then
	die "--build is only for --apply; --rollback restores the matching old binary by itself"
fi
if [[ -n "$PORT" ]]; then
	[[ "$PORT" =~ ^[0-9]+$ ]] || die "--port must be a number, got '$PORT'"
	((PORT >= 1 && PORT <= 65535)) || die "--port out of range: $PORT"
	LISTEN="127.0.0.1:$PORT"
fi

DATA_DIR="${DATA_DIR%/}"
[[ -n "$DATA_DIR" ]] || die "--data-dir cannot be empty"
DB="$DATA_DIR/indexarr.db"
CONFIG="$DATA_DIR/indexarr.json"
PIDFILE="$DATA_DIR/indexarr.pid"
LOG="$DATA_DIR/indexarr.log"

for tool in sqlite3 mv install curl date; do
	command -v "$tool" >/dev/null 2>&1 || die "required tool not found: $tool"
done

# read_config_listen prints the listen value of an indexarr.json without
# requiring jq. It is only a hint: --listen always wins.
read_config_listen() {
	local file="$1"
	[[ -f "$file" ]] || return 0
	sed -n 's/.*"listen"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$file" | head -n 1
}

if [[ -z "$LISTEN" ]]; then
	LISTEN="$(read_config_listen "$CONFIG")"
fi
[[ -n "$LISTEN" ]] || LISTEN=":8484"

health_url() {
	local host="${LISTEN%:*}"
	local port="${LISTEN##*:}"
	case "$host" in
	"" | "*" | "0.0.0.0" | "[::]" | "::")
		host="127.0.0.1"
		;;
	esac
	printf 'http://%s:%s/healthz' "$host" "$port"
}

HEALTH_URL="$(health_url)"

# --- service control ------------------------------------------------------

SYSTEMCTL=""
if [[ "$NO_SYSTEMD" != "1" ]]; then
	if systemctl --user cat "$UNIT" >/dev/null 2>&1; then
		SYSTEMCTL="systemctl --user"
	elif systemctl cat "$UNIT" >/dev/null 2>&1; then
		SYSTEMCTL="systemctl"
	else
		die "systemd unit '$UNIT' not found; use --no-systemd for a local process"
	fi
fi

wait_health() {
	local i
	for i in $(seq 1 150); do
		if curl -fsS --max-time 2 "$HEALTH_URL" >/dev/null 2>&1; then
			return 0
		fi
		sleep 0.2
	done
	return 1
}

local_pid_alive() {
	local pid
	[[ -f "$PIDFILE" ]] || return 1
	pid="$(cat "$PIDFILE" 2>/dev/null || true)"
	[[ "$pid" =~ ^[0-9]+$ ]] || return 1
	kill -0 "$pid" 2>/dev/null
}

stop_local() {
	if local_pid_alive; then
		local pid i
		pid="$(cat "$PIDFILE")"
		kill -TERM "$pid" 2>/dev/null || true
		for i in $(seq 1 100); do
			kill -0 "$pid" 2>/dev/null || break
			sleep 0.1
		done
		if kill -0 "$pid" 2>/dev/null; then
			warn "local process $pid did not stop in 10 s; sending SIGKILL"
			kill -KILL "$pid" 2>/dev/null || true
		fi
	fi
	rm -f "$PIDFILE"
}

service_stop() {
	if [[ "$NO_SYSTEMD" == "1" ]]; then
		stop_local
		return 0
	fi
	$SYSTEMCTL stop "$UNIT" >/dev/null 2>&1 || true
	local i
	for i in $(seq 1 100); do
		if ! $SYSTEMCTL is-active --quiet "$UNIT"; then
			return 0
		fi
		sleep 0.1
	done
	die "service $UNIT is still active after 10 s"
}

start_local() {
	if local_pid_alive; then
		die "a local indexarr process is already running (pid $(cat "$PIDFILE"))"
	fi
	: >"$LOG"
	nohup "$BINARY" -config "$CONFIG" -listen "$LISTEN" >>"$LOG" 2>&1 &
	local pid=$!
	printf '%s\n' "$pid" >"$PIDFILE"
	if ! wait_health; then
		kill -TERM "$pid" 2>/dev/null || true
		die "local instance did not answer $HEALTH_URL"
	fi
}

service_start() {
	if [[ "$NO_SYSTEMD" == "1" ]]; then
		start_local
		return 0
	fi
	$SYSTEMCTL start "$UNIT"
	if ! wait_health; then
		die "service $UNIT did not answer $HEALTH_URL"
	fi
}

# --- database helpers -----------------------------------------------------

sqlite_ro() {
	local db="$1" query="$2"
	sqlite3 -readonly -cmd '.timeout 10000' "$db" "$query"
}

read_setting() {
	local db="$1" key="$2"
	sqlite_ro "$db" "SELECT value FROM settings WHERE key='$key';"
}

# read_torrentleech_feed returns the personal RSS URL of the old TorrentLeech
# source. The URL embeds the rsskey: it is never printed, only handed to the
# importer through the environment and sealed in the vault.
read_torrentleech_feed() {
	sqlite_ro "$DB_OLD" "SELECT url FROM sources
		WHERE lower(name) LIKE '%torrentleech%' OR lower(url) LIKE '%torrentleech%'
		ORDER BY (lower(name) LIKE '%rss%') DESC, id DESC LIMIT 1;" 2>/dev/null || true
}

# --- binary build and rollback -------------------------------------------

# build_binary compiles the current checkout into a temporary file next to the
# target binary. It never installs anything: install_binary does that only in
# the real --apply, after the service has stopped.
build_binary() {
	[[ "$BUILD" == "1" ]] || return 0
	command -v go >/dev/null 2>&1 || die "--build requires go in PATH"
	[[ -d "$ROOT/cmd/indexarr" ]] || die "--build requires the source tree at $ROOT"
	mkdir -p "$(dirname "$BINARY")"
	BUILD_TMP="$(mktemp "$(dirname "$BINARY")/.indexarr-build.XXXXXX")"
	local version
	version="$(cd "$ROOT" && git describe --tags --always --dirty 2>/dev/null || echo dev)"
	step "building indexarr from $ROOT ($version)"
	if ! (cd "$ROOT" && CGO_ENABLED=0 go build -trimpath \
		-ldflags "-s -w -X main.version=$version" -o "$BUILD_TMP" ./cmd/indexarr); then
		rm -f "$BUILD_TMP"
		BUILD_TMP=""
		die "go build failed; nothing was changed"
	fi
	chmod 0755 "$BUILD_TMP"
	note "build ready (temporary; not installed yet)"
}

# install_binary saves the previous binary and installs the compiled one. The
# timestamp is the same as the data backup, so --rollback can pair them.
install_binary() {
	[[ "$BUILD" == "1" ]] || return 0
	[[ -n "$BUILD_TMP" && -f "$BUILD_TMP" ]] || die "the built binary is missing"
	if [[ -e "$BINARY" ]]; then
		[[ ! -e "$BINARY_OLD" ]] || die "binary backup already exists: $BINARY_OLD"
		cp -p "$BINARY" "$BINARY_OLD" || die "cannot save the previous binary as $BINARY_OLD"
		note "previous binary saved as $BINARY_OLD"
	else
		note "no previous binary at $BINARY; nothing to save"
	fi
	install -m 0755 "$BUILD_TMP" "$BINARY" || die "cannot install the new binary at $BINARY"
	BINARY_INSTALLED=1
	rm -f "$BUILD_TMP"
	BUILD_TMP=""
	note "installed the binary built from this checkout"
}

# binary_backup_for_data prints <binary>.old-<timestamp> matching a data.old-*
# backup, or nothing when the data backup was not created by this script.
binary_backup_for_data() {
	local backup="$1" suffix
	case "$backup" in
	"$DATA_DIR".old-*) suffix="${backup#"$DATA_DIR".old-}" ;;
	*) return 0 ;;
	esac
	[[ -n "$suffix" ]] || return 0
	printf '%s.old-%s' "$BINARY" "$suffix"
}

# restore_binary_for_data puts back the binary saved with the data backup, if
# both exist. It is used by --rollback and by the automatic error path.
restore_binary_for_data() {
	local backup="$1" oldbin
	oldbin="$(binary_backup_for_data "$backup")"
	if [[ -z "$oldbin" || ! -f "$oldbin" ]]; then
		note "no binary backup matches $backup; the binary is left unchanged"
		return 0
	fi
	install -m 0755 "$oldbin" "$BINARY" || die "cannot restore the previous binary from $oldbin"
	note "previous binary restored from $oldbin"
}

# --- preflight ------------------------------------------------------------

# Manager values are resolved once, before anything destructive happens.
JACKETT_OK=0
JACKETT_FALLBACK=0
PROWLARR_OK=0
TL_RSS_URL=""
DB_OLD=""

preflight_common() {
	[[ -d "$DATA_DIR" ]] || die "data directory not found: $DATA_DIR"
	[[ ! -L "$DATA_DIR" ]] || die "refusing to move a symlinked data directory: $DATA_DIR"
	if [[ "$BUILD" != "1" ]]; then
		[[ -x "$BINARY" ]] || die "binary not found or not executable: $BINARY (use --build to compile it)"
	fi
	if [[ "$MODE" == "apply" && "$SKIP_IMPORT" != "1" ]]; then
		[[ -x "$IMPORTER" ]] || die "importer not found or not executable: $IMPORTER"
	fi
	if [[ "$NO_SYSTEMD" == "1" ]]; then
		[[ -n "$LISTEN" ]] || die "no listen address"
	fi
}

preflight_dry_run() {
	[[ -d "$DATA_DIR" ]] || die "data directory not found: $DATA_DIR"
	if [[ -f "$DB" ]]; then
		note "old database: $DB"
		note "Torznab API key present: $(if [[ -n "$(read_setting "$DB" api_key 2>/dev/null || true)" ]]; then echo yes; else echo no; fi)"
	else
		note "old database missing: $DB (--apply would stop here)"
	fi
	if [[ "$BUILD" == "1" ]]; then
		note "binary: $BINARY (--build would compile it from $ROOT; nothing is built in a dry run)"
		if command -v go >/dev/null 2>&1; then
			note "build tool: $(command -v go)"
		else
			note "build tool: go not found (--apply --build would stop here)"
		fi
	elif [[ -x "$BINARY" ]]; then
		note "binary: $BINARY"
	else
		note "binary missing: $BINARY (--apply would stop here)"
	fi
	if [[ "$SKIP_IMPORT" == "1" ]]; then
		note "import: skipped (--skip-import)"
	else
		if [[ -f "$ENV_FILE" ]]; then
			note "manager env file: $ENV_FILE"
		else
			note "manager env file: $ENV_FILE (missing; using exported variables)"
		fi
		note "Prowlarr key present: $(if [[ -n "$PROWLARR_KEY" ]]; then echo yes; else echo no; fi)"
		note "Jackett key present: $(if [[ -n "$JACKETT_KEY" ]]; then echo yes; else echo no; fi)"
		[[ -n "$PROWLARR_URL" ]] && note "Prowlarr: $PROWLARR_URL" || note "Prowlarr: not configured"
		[[ -n "$JACKETT_URL" ]] && note "Jackett: $JACKETT_URL" || note "Jackett: not configured"
		[[ -n "$JACKETT_COPY" ]] && note "Jackett fallback copy: $JACKETT_COPY" || note "Jackett fallback copy: not configured"
		note "TorrentLeech RSS: from the old database source (URL never printed)"
	fi
}

preflight_apply() {
	preflight_common
	[[ -f "$DB" ]] || die "old database not found: $DB"
	[[ -s "$DB" ]] || die "old database is empty: $DB"
	sqlite_ro "$DB" "SELECT count(*) FROM settings;" >/dev/null 2>&1 ||
		die "cannot read the old database: $DB"
	DB_OLD="$DB"

	local key
	key="$(read_setting "$DB" api_key)"
	[[ "$key" =~ ^[0-9a-fA-F]{16,128}$ ]] ||
		die "the old database has no usable Torznab API key"

	STAMP="$(date +%Y%m%d-%H%M%S)"
	OLD_DIR="$DATA_DIR.old-$STAMP"
	FAILED_DIR="$DATA_DIR.failed-$STAMP"
	[[ ! -e "$OLD_DIR" ]] || die "backup directory already exists: $OLD_DIR"
	[[ ! -e "$FAILED_DIR" ]] || die "failed directory already exists: $FAILED_DIR"
	if [[ "$BUILD" == "1" ]]; then
		BINARY_OLD="$BINARY.old-$STAMP"
		[[ ! -e "$BINARY_OLD" ]] || die "binary backup already exists: $BINARY_OLD"
	fi

	build_binary

	if [[ "$SKIP_IMPORT" != "1" ]]; then
		TL_RSS_URL="$(read_torrentleech_feed)"
		TL_RSS_URL="${TL_RSS_URL%%$'\n'*}"
		if [[ -n "$TL_RSS_URL" && ! "$TL_RSS_URL" =~ ^https?:// ]]; then
			warn "the TorrentLeech feed URL in the old database is not http(s); it will not be imported"
			TL_RSS_URL=""
		fi

		if [[ -n "$PROWLARR_URL" && -z "$PROWLARR_KEY" ]]; then
			die "PROWLARR_API_KEY is missing (set it in $ENV_FILE or in the environment)"
		fi
		if [[ -n "$JACKETT_URL" && -z "$JACKETT_KEY" ]]; then
			die "JACKETT_API_KEY is missing (set it in $ENV_FILE or in the environment)"
		fi
		if [[ -z "$PROWLARR_URL" && -z "$JACKETT_URL" && -z "$JACKETT_COPY" && -z "$TL_RSS_URL" ]]; then
			die "no Prowlarr URL, no Jackett URL and no TorrentLeech feed: nothing to import (use --skip-import to really skip)"
		fi

		if [[ -n "$PROWLARR_URL" ]]; then
			step "checking Prowlarr read-only: $PROWLARR_URL"
			if ! INDEXARR_PROWLARR_KEY="$PROWLARR_KEY" "$IMPORTER" \
				-prowlarr "$PROWLARR_URL" -timeout 20s >/dev/null 2>"$DATA_DIR/.fresh-start-prowlarr.err"; then
				local err
				err="$(tail -n 3 "$DATA_DIR/.fresh-start-prowlarr.err" 2>/dev/null || true)"
				rm -f "$DATA_DIR/.fresh-start-prowlarr.err"
				die "Prowlarr probe failed: ${err:-see indexarr-import output}"
			fi
			rm -f "$DATA_DIR/.fresh-start-prowlarr.err"
			PROWLARR_OK=1
			note "Prowlarr accepted the key"
		fi
		if [[ -n "$JACKETT_URL" ]]; then
			step "checking Jackett read-only: $JACKETT_URL"
			if probe_jackett; then
				JACKETT_OK=1
				note "Jackett t=indexers list accepted the key"
			elif [[ -n "$JACKETT_COPY" && -d "$JACKETT_COPY" ]]; then
				JACKETT_OK=1
				JACKETT_FALLBACK=1
				warn "Jackett API did not answer; using --jackett-copy as the fallback"
			else
				warn "Jackett API did not answer and no --jackett-copy fallback exists; the Jackett import will be skipped"
			fi
		elif [[ -n "$JACKETT_COPY" ]]; then
			warn "--jackett-copy is only a fallback: set JACKETT_URL so the live API is tried first; the copy is ignored"
		fi
		if [[ "$PROWLARR_OK" != "1" && "$JACKETT_OK" != "1" && -z "$TL_RSS_URL" ]]; then
			die "no manager accepted the import (use --skip-import to really skip)"
		fi
		[[ -n "$TL_RSS_URL" ]] && note "TorrentLeech RSS URL read from the old database (secret, not printed)"
	fi

	if [[ "$NO_SYSTEMD" == "1" ]]; then
		local host="${LISTEN%:*}" port="${LISTEN##*:}"
		case "$host" in "" | "*" | "0.0.0.0" | "[::]" | "::") host="127.0.0.1" ;; esac
		if (exec 3<>"/dev/tcp/$host/$port") 2>/dev/null; then
			die "$host:$port is already in use; stop it before --apply"
		fi
	fi
}

# probe_jackett performs a single GET to the live t=indexers endpoint with the
# key in a curl config streamed on stdin, so the key never appears in the
# process list or in a temporary file.
probe_jackett() {
	local body code
	body="$(mktemp "${TMPDIR:-/tmp}/indexarr-jackett-probe.XXXXXX")"
	code="$({ printf 'url = "%s/api/v2.0/indexers/all/results/torznab/api?t=indexers&configured=true&apikey=%s"\n' "${JACKETT_URL%/}" "$JACKETT_KEY"
		printf 'silent\nshow-error\nmax-time = 15\noutput = "%s"\n' "$body"; } |
		curl --config - -w '%{http_code}' 2>/dev/null || true)"
	local ok=1
	if [[ "$code" != "200" ]]; then
		ok=0
	elif ! grep -q '<indexers' "$body" 2>/dev/null || grep -q '<error' "$body" 2>/dev/null; then
		ok=0
	fi
	rm -f "$body"
	return $((1 - ok))
}

choose_backup() {
	local candidate
	candidate="$(ls -1dt "$DATA_DIR".old-* 2>/dev/null | head -n 1 || true)"
	[[ -n "$candidate" ]] || die "no $DATA_DIR.old-* backup found"
	printf '%s' "$candidate"
}

# --- apply / rollback -----------------------------------------------------

apply_error() {
	local code=$?
	trap - ERR
	set +e
	printf 'error: apply failed (exit %s); attempting automatic rollback\n' "$code" >&2
	service_stop >/dev/null 2>&1 || true
	if [[ "$BINARY_INSTALLED" == "1" && -n "${BINARY_OLD:-}" && -f "${BINARY_OLD:-}" ]]; then
		if install -m 0755 "$BINARY_OLD" "$BINARY"; then
			printf 'rollback: restored the previous binary %s\n' "$BINARY_OLD" >&2
			BINARY_INSTALLED=0
		fi
	fi
	if [[ -n "${OLD_DIR:-}" && -d "${OLD_DIR:-}" ]]; then
		local failed
		failed="$DATA_DIR.failed-$(date +%Y%m%d-%H%M%S)"
		if [[ -d "$DATA_DIR" ]]; then
			mv "$DATA_DIR" "$failed"
			printf 'rollback: new data moved to %s\n' "$failed" >&2
		fi
		mv "$OLD_DIR" "$DATA_DIR"
		printf 'rollback: restored %s\n' "$DATA_DIR" >&2
		service_start >/dev/null 2>&1 || true
	fi
	exit "$code"
}

set_api_key() {
	local old_key new_key
	old_key="$(read_setting "$DB_OLD" api_key)"
	[[ "$old_key" =~ ^[0-9a-fA-F]{16,128}$ ]] || die "old Torznab API key is not valid"
	# The key travels on stdin, never on the command line. It is hex, so the
	# SQL quoting is unambiguous.
	printf "INSERT INTO settings(key, value) VALUES('api_key', '%s') ON CONFLICT(key) DO UPDATE SET value=excluded.value;\n" "$old_key" |
		sqlite3 -cmd '.timeout 10000' "$DB"
	new_key="$(read_setting "$DB" api_key)"
	[[ "$new_key" == "$old_key" ]] || die "could not preserve the Torznab API key"
	note "Torznab API key preserved (${#old_key} characters)"
}

run_import() {
	local log="$DATA_DIR/import.log"
	: >"$log"
	local -a args=(-db "$DB" -config "$CONFIG" -apply)
	if [[ "$PROWLARR_OK" == "1" ]]; then
		args+=(-prowlarr "$PROWLARR_URL")
	fi
	if [[ "$JACKETT_OK" == "1" ]]; then
		if [[ "$JACKETT_FALLBACK" == "1" ]]; then
			args+=(-jackett "$JACKETT_COPY")
			note "using the Jackett copy as the fallback"
		else
			args+=(-jackett-url "$JACKETT_URL")
		fi
	fi
	if [[ "${#args[@]}" -le 4 ]]; then
		note "no manager to import"
	fi
	step "importing live managers and the TorrentLeech feed"
	if ! INDEXARR_PROWLARR_KEY="$PROWLARR_KEY" \
		INDEXARR_JACKETT_KEY="$JACKETT_KEY" \
		INDEXARR_TORRENTLEECH_RSS_URL="$TL_RSS_URL" \
		"$IMPORTER" "${args[@]}" >>"$log" 2>&1; then
		tail -n 20 "$log" >&2 || true
		return 1
	fi
	note "import written to the new database (one indexer per site)"
	if [[ -s "$log" ]]; then
		note "import log: $log"
	fi
}

write_config() {
	cat >"$CONFIG" <<JSON
{
  "listen": "$LISTEN",
  "db_path": "indexarr.db",
  "log_level": "info"
}
JSON
	chmod 600 "$CONFIG"
}

do_apply() {
	preflight_apply
	if [[ "$ASSUME_YES" != "1" ]]; then
		printf 'This will move %s to %s and restart the service. Type "yes" to continue: ' "$DATA_DIR" "$OLD_DIR" >&2
		local answer
		read -r answer || true
		[[ "$answer" == "yes" ]] || die "aborted"
	fi

	trap apply_error ERR
	step "stopping the service"
	service_stop

	step "moving $DATA_DIR to $OLD_DIR"
	mv "$DATA_DIR" "$OLD_DIR"
	DB_OLD="$OLD_DIR/indexarr.db"
	[[ ! -e "$DB" ]] || die "the new database path still exists after moving the old data"
	install -d -m 0700 "$DATA_DIR"
	write_config

	install_binary

	step "creating the new database"
	service_start
	wait_health >/dev/null || die "new instance is not healthy"
	service_stop
	[[ -s "$DB" ]] || die "the new database was not created"

	step "preserving the Torznab API key"
	set_api_key

	if [[ "$SKIP_IMPORT" != "1" ]]; then
		run_import || die "import failed"
	fi

	step "starting the new instance"
	service_start
	if ! wait_health; then
		die "new instance is not healthy at $HEALTH_URL"
	fi
	trap - ERR

	printf '\n'
	printf 'fresh start complete\n'
	printf '  data:    %s\n' "$DATA_DIR"
	printf '  old:     %s (kept; delete it after a week)\n' "$OLD_DIR"
	if [[ "$BINARY_INSTALLED" == "1" && -n "$BINARY_OLD" ]]; then
		printf '  binary:  %s (previous kept as %s)\n' "$BINARY" "$BINARY_OLD"
	fi
	printf '  listen:  %s\n' "$LISTEN"
	printf '  health:  %s\n' "$HEALTH_URL"
	printf '  next:    open the UI, enable the sites you need and verify Sonarr/Radarr\n'
}

do_rollback() {
	preflight_common
	local backup failed
	backup="$(choose_backup)"
	[[ -d "$backup" ]] || die "backup is not a directory: $backup"
	failed="$DATA_DIR.failed-$(date +%Y%m%d-%H%M%S)"
	[[ ! -e "$failed" ]] || die "failed directory already exists: $failed"

	if [[ "$ASSUME_YES" != "1" ]]; then
		printf 'This will move %s to %s and restore %s. Type "yes" to continue: ' "$DATA_DIR" "$failed" "$backup" >&2
		local answer
		read -r answer || true
		[[ "$answer" == "yes" ]] || die "aborted"
	fi

	trap 'printf "error: rollback failed\n" >&2' ERR
	step "stopping the service"
	service_stop

	step "restoring the binary that matches $backup"
	restore_binary_for_data "$backup"

	step "moving $DATA_DIR to $failed"
	mv "$DATA_DIR" "$failed"
	step "restoring $backup to $DATA_DIR"
	mv "$backup" "$DATA_DIR"

	step "starting the restored instance"
	service_start
	wait_health >/dev/null || die "restored instance is not healthy at $HEALTH_URL"
	trap - ERR

	printf '\nrollback complete\n'
	printf '  data:   %s\n' "$DATA_DIR"
	printf '  failed: %s (kept for inspection)\n' "$failed"
	printf '  health: %s\n' "$HEALTH_URL"
}

show_plan() {
	printf 'fresh-start plan (dry run; nothing will change)\n'
	printf '  mode:       apply\n'
	printf '  data:       %s\n' "$DATA_DIR"
	if [[ "$BUILD" == "1" ]]; then
		printf '  binary:     %s (built from %s; dry run does not compile or install)\n' "$BINARY" "$ROOT"
	else
		printf '  binary:     %s\n' "$BINARY"
	fi
	printf '  importer:   %s\n' "$IMPORTER"
	if [[ "$NO_SYSTEMD" == "1" ]]; then
		printf '  service:    local process, PID file %s\n' "$PIDFILE"
	else
		printf '  service:    %s %s\n' "$SYSTEMCTL" "$UNIT"
	fi
	printf '  listen:     %s\n' "$LISTEN"
	printf '  health:     %s\n' "$HEALTH_URL"
	printf '\n'
	preflight_dry_run
	printf '\nsteps:\n'
	if [[ "$BUILD" == "1" ]]; then
		printf '  0. build %s from %s; save the old one as %s.old-<timestamp>\n' "$BINARY" "$ROOT" "$BINARY"
		printf '     (only --apply installs it; this dry run does neither)\n'
	fi
	printf '  1. stop the service\n'
	printf '  2. move %s to %s.old-<timestamp>/\n' "$DATA_DIR" "$DATA_DIR"
	printf '  3. create a fresh %s and database\n' "$DATA_DIR"
	printf '  4. copy the Torznab API key from the old database\n'
	if [[ "$SKIP_IMPORT" == "1" ]]; then
		printf '  5. import skipped (--skip-import)\n'
	else
		printf '  5. import Jackett/Prowlarr as native indexers (keys from %s, GET only)\n' "$ENV_FILE"
		printf '  5b. add the native torrentleech-rss indexer with the old feed URL\n'
	fi
	printf '  6. start the service and wait for %s\n' "$HEALTH_URL"
	printf '\nNo command was executed. Re-run with --apply to perform the switch.\n'
}

case "$MODE" in
dry-run)
	show_plan
	;;
apply)
	do_apply
	;;
rollback)
	do_rollback
	;;
esac
