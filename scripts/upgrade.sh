#!/usr/bin/env bash
# upgrade.sh — update a live indexarr without losing its data.
#
# The default is a dry run: it validates every prerequisite, builds
# bin/indexarr from this checkout into a temporary directory and prints the
# plan. Nothing is stopped or installed.
#
#   ./scripts/upgrade.sh                       # dry run (builds, changes nothing)
#   ./scripts/upgrade.sh --apply --yes         # the production upgrade
#   ./scripts/upgrade.sh --rollback --yes      # restore the latest pre-upgrade backup
#
# What --apply does, in this order:
#   1. builds cmd/indexarr before touching anything;
#   2. writes a coherent copy of the DB with VACUUM INTO to
#      <data>/backups/pre-upgrade-<ts>.db and saves the old binary as
#      <binary>.old-<ts>;
#   3. stops the service, installs the new binary and starts it again;
#   4. checks /healthz (the version must be the one just built), t=caps and one
#      Torznab search authenticated with the API key read from the DB (the key
#      is never printed);
#   5. on any failure after the service stop it restores both the DB and the
#      binary, then starts the old service again.
#
# The upgrade never moves or rewrites the data directory: the live database
# stays in place and the new binary applies only forward, non-destructive
# migrations. --rollback copies the newest pre-upgrade DB back after stopping
# the service, removes stale -wal/-shm files, restores the matching old
# binary and starts the service.
#
# Options:
#   --apply                 execute (default: dry run)
#   --rollback              restore the newest backups/pre-upgrade-*.db
#   --yes                   never prompt (for Claude Code and automation)
#   --data-dir DIR          data directory (default: <repo>/data)
#   --binary FILE           binary to install (default: <repo>/bin/indexarr)
#   --port PORT             listen on 127.0.0.1:PORT for a copy test
#   --listen ADDR           listen address (default: from indexarr.json, else :8484)
#   --no-systemd            manage a local process with <data>/indexarr.pid
#   --unit NAME             systemd unit (default: indexarr)
#   -h, --help              this help
#
# Production upgrades use the systemd unit. The dress rehearsal on a copy uses
# --data-dir, --port 18510, --no-systemd and a temporary --binary.
set -Eeuo pipefail
umask 077
export LC_ALL=C

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DATA_DIR="$ROOT/data"
UNIT="indexarr"
BINARY="$ROOT/bin/indexarr"
LISTEN=""
PORT=""
NO_SYSTEMD=0
MODE="dry-run"
ASSUME_YES=0
STAMP=""
BUILD_DIR=""
EXPECTED_VERSION=""
KEY=""
BACKUP=""
BINARY_OLD=""
ROLLBACK_ARMED=0
ROLLING_BACK=0

usage() {
	cat <<'EOF'
upgrade.sh — update a live indexarr without losing its data.

  ./scripts/upgrade.sh                    dry run: validate, build, print the plan
  ./scripts/upgrade.sh --apply --yes      production upgrade
  ./scripts/upgrade.sh --rollback --yes   restore the latest pre-upgrade backup

Options:
  --apply              execute (default: dry run)
  --rollback           restore backups/pre-upgrade-<ts>.db + the matching binary
  --yes                never prompt
  --data-dir DIR       data directory (default: <repo>/data)
  --binary FILE        binary to install (default: <repo>/bin/indexarr)
  --port PORT          listen on 127.0.0.1:PORT (copy test)
  --listen ADDR        listen address (default: indexarr.json, else :8484)
  --no-systemd         manage a local process with <data>/indexarr.pid
  --unit NAME          systemd unit (default: indexarr)
  -h, --help           this help
EOF
}

die() {
	printf 'error: %s\n' "$*" >&2
	if [[ "${ROLLBACK_ARMED:-0}" == "1" ]]; then
		rollback_after_failure 1
	fi
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

cleanup() {
	if [[ -n "$BUILD_DIR" && -d "$BUILD_DIR" ]]; then
		rm -rf "$BUILD_DIR"
	fi
}
trap cleanup EXIT

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
	--yes)
		ASSUME_YES=1
		shift
		;;
	--data-dir)
		need_value "$1" "${2:-}"
		DATA_DIR="$2"
		shift 2
		;;
	--binary)
		need_value "$1" "${2:-}"
		BINARY="$2"
		shift 2
		;;
	--port)
		need_value "$1" "${2:-}"
		PORT="$2"
		shift 2
		;;
	--listen)
		need_value "$1" "${2:-}"
		LISTEN="$2"
		shift 2
		;;
	--no-systemd)
		NO_SYSTEMD=1
		shift
		;;
	--unit)
		need_value "$1" "${2:-}"
		UNIT="$2"
		shift 2
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

[[ -n "$DATA_DIR" ]] || die "--data-dir cannot be empty"
DATA_DIR="${DATA_DIR%/}"
[[ -n "$BINARY" ]] || die "--binary cannot be empty"
DB="$DATA_DIR/indexarr.db"
CONFIG="$DATA_DIR/indexarr.json"
PIDFILE="$DATA_DIR/indexarr.pid"
LOG="$DATA_DIR/indexarr.log"
BACKUP_DIR="$DATA_DIR/backups"
BIN_DIR="$(dirname "$BINARY")"

if [[ -n "$PORT" && -n "$LISTEN" ]]; then
	die "--port and --listen are mutually exclusive"
fi
if [[ -n "$PORT" ]]; then
	[[ "$PORT" =~ ^[0-9]+$ ]] || die "--port must be a number, got '$PORT'"
	((PORT >= 1 && PORT <= 65535)) || die "--port out of range: $PORT"
	LISTEN="127.0.0.1:$PORT"
fi

read_config_listen() {
	local file="$1"
	[[ -f "$file" ]] || return 0
	sed -n 's/.*"listen"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$file" | head -n 1
}

if [[ -z "$LISTEN" ]]; then
	LISTEN="$(read_config_listen "$CONFIG")"
fi
[[ -n "$LISTEN" ]] || LISTEN=":8484"

BASE=""
HEALTH_URL=""
set_base_urls() {
	local host="${LISTEN%:*}" port="${LISTEN##*:}"
	case "$host" in
	"" | "*" | "0.0.0.0" | "[::]" | "::") host="127.0.0.1" ;;
	esac
	[[ -n "$port" ]] || die "cannot parse the listen address: $LISTEN"
	BASE="http://$host:$port"
	HEALTH_URL="$BASE/healthz"
}
set_base_urls

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

local_pid_alive() {
	local pid
	[[ -f "$PIDFILE" ]] || return 1
	pid="$(cat "$PIDFILE" 2>/dev/null || true)"
	[[ "$pid" =~ ^[0-9]+$ ]] || return 1
	kill -0 "$pid" 2>/dev/null
}

port_in_use() {
	local host="${LISTEN%:*}" port="${LISTEN##*:}"
	case "$host" in
	"" | "*" | "0.0.0.0" | "[::]" | "::") host="127.0.0.1" ;;
	esac
	(exec 3<>"/dev/tcp/$host/$port") 2>/dev/null
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

wait_health() {
	local i
	for ((i = 0; i < 150; i++)); do
		if curl -fsS --max-time 2 "$HEALTH_URL" >/dev/null 2>&1; then
			return 0
		fi
		sleep 0.2
	done
	return 1
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
		return 1
	fi
}

service_start() {
	if [[ "$NO_SYSTEMD" == "1" ]]; then
		start_local
		return $?
	fi
	$SYSTEMCTL start "$UNIT" || return 1
	if ! wait_health; then
		return 1
	fi
}

# --- prerequisites --------------------------------------------------------

sqlite_ro() {
	local db="$1" query="$2"
	sqlite3 -readonly -cmd '.timeout 10000' "$db" "$query"
}

check_tools() {
	local tool
	for tool in go sqlite3 curl install date mktemp cp mv rm mkdir chmod awk sed tail head seq dirname basename; do
		command -v "$tool" >/dev/null 2>&1 || die "required tool not found: $tool"
	done
}

# check_vacuum_support proves that the sqlite3 command can run VACUUM INTO on
# this machine, without touching the live database.
check_vacuum_support() {
	local tmp
	tmp="$(mktemp "${TMPDIR:-/tmp}/indexarr-vacuum-check.XXXXXX")"
	rm -f "$tmp"
	if ! sqlite3 :memory: "VACUUM INTO '$tmp';" >/dev/null 2>&1 || [[ ! -s "$tmp" ]]; then
		rm -f "$tmp"
		die "the sqlite3 tool cannot run VACUUM INTO; upgrade requires SQLite >= 3.27"
	fi
	rm -f "$tmp"
}

check_paths() {
	[[ -d "$DATA_DIR" ]] || die "data directory not found: $DATA_DIR"
	[[ ! -L "$DATA_DIR" ]] || die "refusing a symlinked data directory: $DATA_DIR"
	[[ -d "$BIN_DIR" ]] || die "binary directory not found: $BIN_DIR"
	[[ -w "$BIN_DIR" ]] || die "binary directory is not writable: $BIN_DIR"
	if [[ -e "$BACKUP_DIR" ]]; then
		[[ -d "$BACKUP_DIR" && ! -L "$BACKUP_DIR" ]] ||
			die "backup path is not a plain directory: $BACKUP_DIR"
		[[ -w "$BACKUP_DIR" ]] || die "backup directory is not writable: $BACKUP_DIR"
	else
		[[ -w "$DATA_DIR" ]] || die "data directory is not writable: $DATA_DIR"
	fi
}

read_api_key() {
	local key
	[[ -f "$DB" ]] || die "database not found: $DB"
	[[ -s "$DB" ]] || die "database is empty: $DB"
	sqlite_ro "$DB" "SELECT count(*) FROM settings;" >/dev/null 2>&1 ||
		die "cannot read the database: $DB"
	key="$(sqlite_ro "$DB" "SELECT value FROM settings WHERE key='api_key';")"
	[[ "$key" =~ ^[0-9a-fA-F]{16,128}$ ]] ||
		die "the database has no usable Torznab API key"
	KEY="$key"
}

compute_version() {
	(cd "$ROOT" && git describe --tags --always --dirty 2>/dev/null || echo dev)
}

build_all() {
	[[ -d "$ROOT/cmd/indexarr" ]] || die "source tree not found: $ROOT/cmd/indexarr"
	BUILD_DIR="$(mktemp -d "${TMPDIR:-/tmp}/indexarr-upgrade.XXXXXX")"
	EXPECTED_VERSION="$(compute_version)"
	[[ -n "$EXPECTED_VERSION" ]] || EXPECTED_VERSION="dev"

	step "building indexarr ($EXPECTED_VERSION)"
	if ! (cd "$ROOT" && CGO_ENABLED=0 go build -trimpath \
		-ldflags "-s -w -X main.version=$EXPECTED_VERSION" \
		-o "$BUILD_DIR/indexarr" ./cmd/indexarr); then
		die "build of cmd/indexarr failed; nothing was stopped"
	fi
	[[ -x "$BUILD_DIR/indexarr" ]] || die "the built indexarr is not executable"

	note "build ready (temporary; not installed)"
}

# --- plan and preflight ---------------------------------------------------

plan_stamp() {
	STAMP="$(date +%Y%m%d-%H%M%S)"
	BACKUP="$BACKUP_DIR/pre-upgrade-$STAMP.db"
	BINARY_OLD="$BINARY.old-$STAMP"
	[[ ! -e "$BACKUP" ]] || die "backup already exists: $BACKUP (retry in a second)"
	[[ ! -e "$BINARY_OLD" ]] || die "binary backup already exists: $BINARY_OLD (retry in a second)"
}

show_plan() {
	if [[ "$MODE" == "apply" ]]; then
		printf 'indexarr upgrade plan (about to apply)\n'
	else
		printf 'indexarr upgrade plan (dry run; nothing will change)\n'
	fi
	printf '  data:     %s\n' "$DATA_DIR"
	printf '  db:       %s\n' "$DB"
	printf '  backup:   %s/backups/pre-upgrade-<timestamp>.db\n' "$DATA_DIR"
	printf '  binary:   %s (old kept as %s.old-<timestamp>)\n' "$BINARY" "$BINARY"
	if [[ "$NO_SYSTEMD" == "1" ]]; then
		printf '  service:  local process, PID file %s\n' "$PIDFILE"
	else
		printf '  service:  %s %s\n' "$SYSTEMCTL" "$UNIT"
	fi
	printf '  listen:   %s\n' "$LISTEN"
	printf '  checks:   healthz %s, t=caps, one Torznab search\n' "$HEALTH_URL"
	printf '\n'
	printf 'steps:\n'
	printf '  1. build cmd/indexarr (already done above)\n'
	printf '  2. VACUUM INTO the DB backup and copy the old binary\n'
	printf '  3. stop, install, start\n'
	printf '  4. verify healthz version, t=caps and a Torznab search\n'
	printf '  5. on failure restore the backup and the old binary automatically\n'
	printf '\n'
	if [[ "$MODE" == "apply" ]]; then
		printf 'Nothing has been stopped or installed yet.\n'
	else
		printf 'No service was stopped and no file was installed. Re-run with --apply.\n'
	fi
}

preflight() {
	check_paths
	if [[ "$NO_SYSTEMD" == "1" ]] && ! local_pid_alive && port_in_use; then
		die "$LISTEN is already in use and no local PID file exists"
	fi
	read_api_key
	build_all
	plan_stamp
}

# --- backups and rollback -------------------------------------------------

create_db_backup() {
	step "backing up the database"
	mkdir -p "$BACKUP_DIR" || die "cannot create the backup directory: $BACKUP_DIR"
	chmod 700 "$BACKUP_DIR" 2>/dev/null || true
	local escaped="${BACKUP//\'/\'\'}"
	if ! sqlite3 -readonly -cmd '.timeout 10000' "$DB" "VACUUM INTO '$escaped';" >/dev/null; then
		rm -f "$BACKUP"
		die "VACUUM INTO failed; nothing was stopped"
	fi
	chmod 600 "$BACKUP" 2>/dev/null || true
	[[ -s "$BACKUP" ]] || die "the database backup is empty"
	local integrity backup_key
	integrity="$(sqlite_ro "$BACKUP" "PRAGMA integrity_check;")"
	[[ "$integrity" == "ok" ]] || die "the database backup failed its integrity check"
	backup_key="$(sqlite_ro "$BACKUP" "SELECT value FROM settings WHERE key='api_key';")"
	[[ "$backup_key" == "$KEY" ]] || die "the backup does not contain the same API key"
	note "db backup: $BACKUP"
}

save_old_binaries() {
	step "saving the old binary"
	if [[ -e "$BINARY" ]]; then
		cp -p "$BINARY" "$BINARY_OLD" || die "cannot save the old binary as $BINARY_OLD"
		note "old binary: $BINARY_OLD"
	else
		note "no old binary at $BINARY (nothing to save)"
	fi
}

install_binaries() {
	step "installing the new binary"
	install -m 0755 "$BUILD_DIR/indexarr" "$BINARY" || die "cannot install $BINARY"
	note "installed $BINARY"
}

restore_database() {
	local backup="$1"
	[[ -s "$backup" ]] || return 1
	[[ "$(sqlite_ro "$backup" "PRAGMA integrity_check;")" == "ok" ]] || return 1
	rm -f "$DB" "$DB-wal" "$DB-shm" "$DB-journal"
	install -m 0600 "$backup" "$DB" || return 1
	return 0
}

restore_binaries() {
	local rc=0
	if [[ -n "$BINARY_OLD" && -f "$BINARY_OLD" ]]; then
		install -m 0755 "$BINARY_OLD" "$BINARY" || rc=1
	else
		warn "no matching old binary at '$BINARY_OLD'; the current binary is left in place"
	fi
	return "$rc"
}

rollback_after_failure() {
	local code="${1:-1}" health
	if [[ "$ROLLING_BACK" == "1" ]]; then
		exit "$code"
	fi
	ROLLING_BACK=1
	ROLLBACK_ARMED=0
	trap - ERR
	set +e
	printf 'error: upgrade failed after the service stop; rolling back\n' >&2
	service_stop >/dev/null 2>&1 || true
	if restore_database "$BACKUP"; then
		printf 'rollback: db restored from %s\n' "$BACKUP" >&2
	else
		printf 'rollback: could not restore the db from %s\n' "$BACKUP" >&2
	fi
	if restore_binaries; then
		printf 'rollback: old binary restored\n' >&2
	else
		printf 'rollback: binary restore incomplete; check %s\n' "$BIN_DIR" >&2
	fi
	if service_start; then
		if wait_health; then
			health="$(curl -fsS --max-time 3 "$HEALTH_URL" 2>/dev/null || true)"
			printf 'rollback: the old service is healthy again\n' >&2
			printf 'rollback: %s\n' "$health" >&2
		else
			printf 'rollback: the service did not answer %s\n' "$HEALTH_URL" >&2
		fi
	else
		printf 'rollback: the service did not start; inspect %s\n' "$LOG" >&2
	fi
	exit "$code"
}

on_error() {
	local code=$?
	if [[ "$ROLLBACK_ARMED" != "1" ]]; then
		exit "$code"
	fi
	rollback_after_failure "$code"
}

# --- post-install checks --------------------------------------------------

verify_health_version() {
	local json status version
	json="$(curl -fsS --max-time 3 "$HEALTH_URL" 2>/dev/null)" || return 1
	status="$(printf '%s' "$json" | sed -n 's/.*"status"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
	version="$(printf '%s' "$json" | sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')"
	[[ "$status" == "ok" ]] || {
		warn "healthz reported status '${status:-none}'"
		return 1
	}
	[[ -n "$version" ]] || {
		warn "healthz did not report a version"
		return 1
	}
	[[ "$version" == "$EXPECTED_VERSION" ]] || {
		warn "healthz reported version '$version', expected '$EXPECTED_VERSION'"
		return 1
	}
	note "healthz ok (version $version)"
}

verify_caps() {
	local body code
	body="$BUILD_DIR/caps.xml"
	code="$(curl -sS --max-time 10 -o "$body" -w '%{http_code}' "$BASE/api?t=caps" 2>/dev/null || true)"
	[[ "$code" == "200" ]] || {
		warn "t=caps returned HTTP ${code:-none}"
		return 1
	}
	grep -q '<caps' "$body" || {
		warn "t=caps did not return a caps document"
		return 1
	}
	grep -q '<server' "$body" || {
		warn "t=caps is missing the server version"
		return 1
	}
	note "t=caps ok"
}

verify_search() {
	local body code cfg
	body="$BUILD_DIR/search.xml"
	cfg="$BUILD_DIR/curl.conf"
	{
		printf 'silent\nshow-error\nmax-time = 60\n'
		printf 'header = "X-Api-Key: %s"\n' "$KEY"
		printf 'output = "%s"\n' "$body"
		printf 'write-out = "%%{http_code}"\n'
		printf 'url = "%s/api?t=search&q=upgrade-check"\n' "$BASE"
	} >"$cfg"
	code="$(curl --config "$cfg" 2>"$BUILD_DIR/search.err" || true)"
	if [[ "$code" != "200" ]]; then
		warn "Torznab search returned HTTP ${code:-none}: $(tail -n 1 "$BUILD_DIR/search.err" 2>/dev/null)"
		return 1
	fi
	grep -q '<rss' "$body" || {
		warn "Torznab search did not return an RSS document"
		return 1
	}
	if grep -q '<error code=' "$body"; then
		warn "Torznab search returned an error document"
		return 1
	fi
	note "Torznab search ok"
}

verify_new_service() {
	wait_health || {
		warn "the new service did not answer $HEALTH_URL"
		return 1
	}
	verify_health_version || return 1
	verify_caps || return 1
	verify_search || return 1
}

# --- modes ----------------------------------------------------------------

do_apply() {
	preflight
	show_plan
	if [[ "$ASSUME_YES" != "1" ]]; then
		printf 'Apply the upgrade to %s? Type "yes" to continue: ' "$DATA_DIR" >&2
		local answer
		read -r answer || true
		[[ "$answer" == "yes" ]] || die "aborted"
	fi

	create_db_backup
	save_old_binaries

	trap on_error ERR
	ROLLBACK_ARMED=1

	step "stopping the service"
	service_stop

	install_binaries

	step "starting the new service"
	if ! service_start; then
		die "the new service did not start"
	fi

	step "verifying the new service"
	verify_new_service || die "post-install checks failed"

	ROLLBACK_ARMED=0
	trap - ERR

	printf '\nupgrade complete\n'
	printf '  version: %s\n' "$EXPECTED_VERSION"
	printf '  data:    %s (unchanged)\n' "$DATA_DIR"
	printf '  backup:  %s\n' "$BACKUP"
	printf '  old:     %s\n' "$BINARY_OLD"
	printf '  health:  %s\n' "$HEALTH_URL"
	printf '  rollback: %s --rollback --yes\n' "$0"
}

do_rollback() {
	check_paths
	check_vacuum_support
	[[ -d "$BACKUP_DIR" ]] || die "no backup directory: $BACKUP_DIR"
	shopt -s nullglob
	local backups=("$BACKUP_DIR"/pre-upgrade-*.db)
	shopt -u nullglob
	((${#backups[@]} > 0)) || die "no pre-upgrade backup in $BACKUP_DIR"
	BACKUP="$(printf '%s\n' "${backups[@]}" | LC_ALL=C sort | tail -n 1)"
	[[ -s "$BACKUP" ]] || die "the newest backup is empty: $BACKUP"
	[[ "$(sqlite_ro "$BACKUP" "PRAGMA integrity_check;")" == "ok" ]] ||
		die "the newest backup failed its integrity check: $BACKUP"
	STAMP="${BACKUP##*/pre-upgrade-}"
	STAMP="${STAMP%.db}"
	BINARY_OLD="$BINARY.old-$STAMP"
	[[ -f "$BINARY_OLD" ]] || die "the matching old binary is missing: $BINARY_OLD"

	if [[ "$ASSUME_YES" != "1" ]]; then
		printf 'Restore %s and %s, then restart %s? Type "yes" to continue: ' \
			"$BACKUP" "$BINARY_OLD" "$DATA_DIR" >&2
		local answer
		read -r answer || true
		[[ "$answer" == "yes" ]] || die "aborted"
	fi

	trap 'printf "error: rollback failed\n" >&2' ERR

	step "stopping the service"
	service_stop

	step "restoring the database"
	restore_database "$BACKUP" || die "cannot restore the database from $BACKUP"
	note "db restored from $BACKUP"

	step "restoring the old binary"
	restore_binaries || die "cannot restore the old binary"

	step "starting the restored service"
	if ! service_start; then
		die "the restored service did not start"
	fi
	wait_health || die "the restored service is not healthy at $HEALTH_URL"

	trap - ERR
	printf '\nrollback complete\n'
	printf '  data:   %s\n' "$DATA_DIR"
	printf '  backup: %s (kept)\n' "$BACKUP"
	printf '  binary: %s\n' "$BINARY"
	printf '  health: %s\n' "$HEALTH_URL"
}

do_dry_run() {
	preflight
	show_plan
}

check_tools
case "$MODE" in
dry-run)
	check_vacuum_support
	do_dry_run
	;;
apply)
	check_vacuum_support
	do_apply
	;;
rollback)
	do_rollback
	;;
esac
