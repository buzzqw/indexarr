#!/usr/bin/env bash
# Offline test for scripts/upgrade.sh.
#
# It never touches the production data directory, :8484, bin/indexarr or
# systemd: every case uses a temporary --data-dir, temporary binaries,
# --no-systemd and port 18510. When the production DB is readable, each test
# starts from a VACUUM INTO copy of it (read-only on the original).
#
# Cases:
#   1. a successful upgrade, then the explicit --rollback;
#   2. a broken importer build: the upgrade must fail before stopping anything;
#   3. a wrong /healthz version: the upgrade must roll back automatically.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="$ROOT/scripts/upgrade.sh"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/indexarr-upgrade-test.XXXXXX")"
PORT="${INDEXARR_UPGRADE_TEST_PORT:-18510}"
BASE="http://127.0.0.1:$PORT"
PIDS=()

cleanup() {
	local pid pidfile
	for pid in "${PIDS[@]:-}"; do
		kill "$pid" 2>/dev/null || true
	done
	for pidfile in "$TMP"/*/data/indexarr.pid; do
		[[ -f "$pidfile" ]] || continue
		pid="$(cat "$pidfile" 2>/dev/null || true)"
		if [[ "$pid" =~ ^[0-9]+$ ]]; then
			kill "$pid" 2>/dev/null || true
		fi
	done
	rm -rf "$TMP"
}
trap cleanup EXIT

fail() {
	printf 'FAIL: %s\n' "$*" >&2
	exit 1
}

ok() {
	printf 'ok   %s\n' "$*"
}

port_free() {
	! (exec 3<>"/dev/tcp/127.0.0.1/$PORT") 2>/dev/null
}

wait_for_health() {
	local i
	for ((i = 0; i < 120; i++)); do
		if curl -fsS --max-time 1 "$BASE/healthz" >/dev/null 2>&1; then
			return 0
		fi
		sleep 0.1
	done
	return 1
}

db_value() {
	sqlite3 -readonly "$1" "SELECT value FROM settings WHERE key='$2';"
}

kill_from_pidfile() {
	local pidfile="$1" pid i
	[[ -f "$pidfile" ]] || return 0
	pid="$(cat "$pidfile" 2>/dev/null || true)"
	if [[ "$pid" =~ ^[0-9]+$ ]]; then
		kill -TERM "$pid" 2>/dev/null || true
		for ((i = 0; i < 100; i++)); do
			kill -0 "$pid" 2>/dev/null || break
			sleep 0.05
		done
		kill -KILL "$pid" 2>/dev/null || true
	fi
	rm -f "$pidfile"
}

bash -n "$SCRIPT" || fail "bash -n"
ok "bash -n"

if ! port_free; then
	fail "port $PORT is already in use; stop it or set INDEXARR_UPGRADE_TEST_PORT"
fi

# --- fake indexarr and importer ------------------------------------------

cat >"$TMP/fake-indexarr-source" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "-version" ]]; then
	printf 'indexarr %s\n' "${FAKE_EMBEDDED_VERSION:-dev}"
	exit 0
fi
config=""
listen=""
while (($#)); do
	case "$1" in
	-config) config="$2"; shift 2 ;;
	-listen) listen="$2"; shift 2 ;;
	-db) shift 2 ;;
	*) shift ;;
	esac
done
[[ -n "$config" && -n "$listen" ]] || { echo "fake: missing config/listen" >&2; exit 2; }
dir="$(cd "$(dirname "$config")" && pwd)"
host="${listen%:*}"
port="${listen##*:}"
case "$host" in "" | "*" | 0.0.0.0 | "[::]" | "::") host="127.0.0.1" ;; esac
exec python3 - "$host" "$port" "$dir" "${FAKE_EMBEDDED_VERSION:-dev}" <<'PY'
import http.server
import json
import os
import sqlite3
import sys
import urllib.parse

host, port, dbdir, version = sys.argv[1], int(sys.argv[2]), sys.argv[3], sys.argv[4]
mode = os.environ.get("FAKE_HEALTH_MODE", "ok")


def api_key():
    con = sqlite3.connect(os.path.join(dbdir, "indexarr.db"))
    try:
        row = con.execute("SELECT value FROM settings WHERE key='api_key'").fetchone()
        return row[0] if row else ""
    finally:
        con.close()


class H(http.server.BaseHTTPRequestHandler):
    def send(self, code, body, kind="application/xml"):
        raw = body.encode()
        self.send_response(code)
        self.send_header("Content-Type", kind)
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):
        url = urllib.parse.urlparse(self.path)
        query = urllib.parse.parse_qs(url.query)
        function = (query.get("t") or [""])[0]
        if url.path == "/healthz":
            if mode == "healthfail":
                self.send(500, '{"status":"error"}', "application/json")
                return
            reported = "wrong-version" if mode == "badversion" else version
            self.send(200, json.dumps({"status": "ok", "version": reported}), "application/json")
            return
        if function == "caps":
            self.send(200, '<?xml version="1.0"?><caps><server version="indexarr %s" title="indexarr"/></caps>' % version)
            return
        if url.path in ("/api", "/torznab/api", "/newznab/api"):
            provided = self.headers.get("X-Api-Key", "") or (query.get("apikey") or [""])[0]
            if provided != api_key():
                self.send(401, '<?xml version="1.0"?><error code="100" description="invalid API key"/>')
                return
            if mode == "badtorznab":
                self.send(200, '<?xml version="1.0"?><error code="900" description="fake failure"/>')
                return
            self.send(200, '<?xml version="1.0"?><rss version="2.0"><channel><title>indexarr</title></channel></rss>')
            return
        self.send(404, "no", "text/plain")

    def log_message(self, *args):
        pass


http.server.HTTPServer((host, port), H).serve_forever()
PY
FAKE
chmod +x "$TMP/fake-indexarr-source"

cat >"$TMP/fake-import-source" <<'FAKE'
#!/usr/bin/env bash
printf 'fake importer %s\n' "$*"
FAKE
chmod +x "$TMP/fake-import-source"

mkdir -p "$TMP/fake-go-bin" "$TMP/old-bin"
cat >"$TMP/fake-go-bin/go" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${FAKE_GO_MARKER:?}"
out=""
ldflags=""
prev=""
last=""
for arg in "$@"; do
	case "$prev" in
	-o) out="$arg" ;;
	-ldflags) ldflags="$arg" ;;
	esac
	prev="$arg"
	last="$arg"
done
if [[ "${FAKE_GO_FAIL:-0}" == "1" ]]; then
	echo "fake go: forced failure" >&2
	exit 1
fi
if [[ "${FAKE_GO_FAIL_IMPORT:-0}" == "1" && "$last" == *indexarr-import* ]]; then
	echo "fake go: forced importer failure" >&2
	exit 1
fi
[[ -n "$out" ]] || { echo "fake go: missing -o" >&2; exit 2; }
version="$(printf '%s' "$ldflags" | sed -n 's/.*main\.version=\([^ ]*\).*/\1/p')"
case "$last" in
*indexarr-import*)
	cat >"$out" <<EOF
#!/usr/bin/env bash
printf 'fake importer %s\\n' "\$*"
EOF
	;;
*)
	cat >"$out" <<EOF
#!/usr/bin/env bash
export FAKE_EMBEDDED_VERSION='${version:-dev}'
exec '${FAKE_INDEXARR_SOURCE:?}' "\$@"
EOF
	;;
esac
chmod +x "$out"
FAKE
chmod +x "$TMP/fake-go-bin/go"
export FAKE_GO_MARKER="$TMP/go-build.log"
export FAKE_INDEXARR_SOURCE="$TMP/fake-indexarr-source"
: >"$FAKE_GO_MARKER"

make_old_binary() {
	local path="$1" version="$2"
	cat >"$path" <<EOF
#!/usr/bin/env bash
export FAKE_EMBEDDED_VERSION='$version'
exec '$TMP/fake-indexarr-source' "\$@"
EOF
	chmod +x "$path"
}

make_old_importer() {
	local path="$1"
	cat >"$path" <<'EOF'
#!/usr/bin/env bash
printf 'old importer %s\n' "$*"
EOF
	chmod +x "$path"
}

# make_data builds a private copy of the production database when it is
# readable. The original is opened read-only; only the copy gets the marker.
make_data() {
	local dir="$1" key
	mkdir -p "$dir"
	chmod 700 "$dir"
	if [[ -f "$ROOT/data/indexarr.db" ]] &&
		sqlite3 -readonly -cmd '.timeout 10000' "$ROOT/data/indexarr.db" \
			"VACUUM INTO '$dir/indexarr.db';" 2>/dev/null; then
		:
	else
		key="$(printf 'b%.0s' {1..64})"
		sqlite3 "$dir/indexarr.db" <<SQL
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '');
INSERT INTO settings(key, value) VALUES ('api_key', '$key');
SQL
	fi
	sqlite3 "$dir/indexarr.db" \
		"INSERT OR REPLACE INTO settings(key, value) VALUES('test_marker', 'original-marker');"
	cat >"$dir/indexarr.json" <<JSON
{
  "listen": "127.0.0.1:$PORT",
  "db_path": "indexarr.db",
  "log_level": "info"
}
JSON
	chmod 600 "$dir/indexarr.json"
}

start_service() {
	local bin="$1" data="$2"
	: >"$data/indexarr.log"
	nohup "$bin" -config "$data/indexarr.json" -listen "127.0.0.1:$PORT" >>"$data/indexarr.log" 2>&1 &
	printf '%s\n' "$!" >"$data/indexarr.pid"
	wait_for_health || fail "the service at $BASE did not start"
}

run_upgrade() {
	env PATH="$TMP/fake-go-bin:$PATH" "$@"
}

# --- 1. successful upgrade, then explicit rollback -----------------------

SUCCESS="$TMP/success"
mkdir -p "$SUCCESS/bin"
make_data "$SUCCESS/data"
OLD_KEY_LEN="$(sqlite3 -readonly "$SUCCESS/data/indexarr.db" "SELECT length(value) FROM settings WHERE key='api_key';")"
make_old_binary "$SUCCESS/bin/indexarr" "old-version"
make_old_importer "$SUCCESS/bin/indexarr-import"
OLD_SHA="$(sha256sum "$SUCCESS/bin/indexarr" | cut -d' ' -f1)"
OLD_IMPORTER_SHA="$(sha256sum "$SUCCESS/bin/indexarr-import" | cut -d' ' -f1)"
: >"$FAKE_GO_MARKER"
OUT="$TMP/success.out"
run_upgrade FAKE_HEALTH_MODE=ok "$SCRIPT" --apply --yes --no-systemd \
	--data-dir "$SUCCESS/data" --binary "$SUCCESS/bin/indexarr" \
	--importer "$SUCCESS/bin/indexarr-import" --port "$PORT" >"$OUT" 2>&1 ||
	{ cat "$OUT" >&2; fail "successful upgrade exited nonzero"; }
grep -q 'upgrade complete' "$OUT" || { cat "$OUT" >&2; fail "upgrade did not complete"; }
grep -q 'healthz ok' "$OUT" || { cat "$OUT" >&2; fail "healthz was not checked"; }
grep -q 't=caps ok' "$OUT" || { cat "$OUT" >&2; fail "t=caps was not checked"; }
grep -q 'Torznab search ok' "$OUT" || { cat "$OUT" >&2; fail "the Torznab search was not checked"; }
[[ "$(grep -c -- '-o ' "$FAKE_GO_MARKER" || true)" == "2" ]] ||
	fail "expected two builds (indexarr and importer)"
grep -q 'cmd/indexarr-import' "$FAKE_GO_MARKER" || fail "the importer was not built"
BACKUP="$(compgen -G "$SUCCESS/data/backups/pre-upgrade-*.db" | head -n 1)"
[[ -n "$BACKUP" && -s "$BACKUP" ]] || fail "no pre-upgrade DB backup"
[[ "$(sqlite3 -readonly "$BACKUP" 'PRAGMA integrity_check;')" == "ok" ]] ||
	fail "the DB backup is not a valid SQLite file"
[[ "$(db_value "$BACKUP" test_marker)" == "original-marker" ]] ||
	fail "the DB backup does not contain the original data"
[[ "$(sqlite3 -readonly "$SUCCESS/data/indexarr.db" "SELECT length(value) FROM settings WHERE key='api_key';")" == "$OLD_KEY_LEN" ]] ||
	fail "the Torznab API key was not preserved"
[[ "$(sha256sum "$SUCCESS/bin/indexarr" | cut -d' ' -f1)" != "$OLD_SHA" ]] ||
	fail "the new binary was not installed"
[[ "$(sha256sum "$SUCCESS/bin/indexarr-import" | cut -d' ' -f1)" != "$OLD_IMPORTER_SHA" ]] ||
	fail "the new importer was not installed"
OLD_BIN="$(compgen -G "$SUCCESS/bin/indexarr.old-*" | head -n 1)"
[[ -n "$OLD_BIN" && -f "$OLD_BIN" ]] || fail "the old binary was not saved"
[[ "$(sha256sum "$OLD_BIN" | cut -d' ' -f1)" == "$OLD_SHA" ]] ||
	fail "the saved binary is not the old one"
compgen -G "$SUCCESS/bin/indexarr-import.old-*" >/dev/null ||
	fail "the old importer was not saved"
STAMP="${BACKUP##*/pre-upgrade-}"
STAMP="${STAMP%.db}"
[[ "$(basename "$OLD_BIN")" == "indexarr.old-$STAMP" ]] ||
	fail "the binary and DB backups do not share the timestamp"
curl -fsS --max-time 3 "$BASE/healthz" >/dev/null || fail "the new service is not healthy"
KEY_VALUE="$(db_value "$SUCCESS/data/indexarr.db" api_key)"
if grep -qF -f <(printf '%s\n' "$KEY_VALUE") "$OUT"; then
	fail "the output printed the Torznab API key"
fi
ok "successful upgrade builds both binaries, backs up DB and binary, verifies healthz/caps/search"

OUT="$TMP/rollback.out"
"$SCRIPT" --rollback --yes --no-systemd --data-dir "$SUCCESS/data" \
	--binary "$SUCCESS/bin/indexarr" --importer "$SUCCESS/bin/indexarr-import" \
	--port "$PORT" >"$OUT" 2>&1 ||
	{ cat "$OUT" >&2; fail "explicit rollback exited nonzero"; }
grep -q 'rollback complete' "$OUT" || { cat "$OUT" >&2; fail "rollback did not complete"; }
[[ "$(sha256sum "$SUCCESS/bin/indexarr" | cut -d' ' -f1)" == "$OLD_SHA" ]] ||
	fail "rollback did not restore the old binary"
[[ "$(sha256sum "$SUCCESS/bin/indexarr-import" | cut -d' ' -f1)" == "$OLD_IMPORTER_SHA" ]] ||
	fail "rollback did not restore the old importer"
[[ "$(db_value "$SUCCESS/data/indexarr.db" test_marker)" == "original-marker" ]] ||
	fail "rollback did not restore the old database"
[[ -f "$BACKUP" ]] || fail "rollback deleted the pre-upgrade backup"
wait_for_health || fail "the rolled-back service is not healthy"
kill_from_pidfile "$SUCCESS/data/indexarr.pid"
ok "explicit --rollback restores the matching DB and binaries"

# --- 2. broken importer build fails before stopping ---------------------

BROKEN="$TMP/broken"
mkdir -p "$BROKEN/bin"
make_data "$BROKEN/data"
make_old_binary "$BROKEN/bin/indexarr" "old-version"
make_old_importer "$BROKEN/bin/indexarr-import"
OLD_SHA="$(sha256sum "$BROKEN/bin/indexarr" | cut -d' ' -f1)"
start_service "$BROKEN/bin/indexarr" "$BROKEN/data"
OLD_PID="$(cat "$BROKEN/data/indexarr.pid")"
OUT="$TMP/broken.out"
set +e
run_upgrade FAKE_HEALTH_MODE=ok FAKE_GO_FAIL_IMPORT=1 "$SCRIPT" --apply --yes \
	--no-systemd --data-dir "$BROKEN/data" --binary "$BROKEN/bin/indexarr" \
	--importer "$BROKEN/bin/indexarr-import" --port "$PORT" >"$OUT" 2>&1
STATUS=$?
set -e
[[ "$STATUS" != "0" ]] || { cat "$OUT" >&2; fail "the broken build was accepted"; }
grep -q 'build of cmd/indexarr-import failed' "$OUT" ||
	{ cat "$OUT" >&2; fail "the importer build error is missing"; }
grep -q 'nothing was stopped' "$OUT" ||
	{ cat "$OUT" >&2; fail "the output does not say that nothing was stopped"; }
kill -0 "$OLD_PID" 2>/dev/null || fail "the running service was stopped by the failed build"
curl -fsS --max-time 2 "$BASE/healthz" >/dev/null ||
	fail "the running service stopped answering after the failed build"
[[ "$(sha256sum "$BROKEN/bin/indexarr" | cut -d' ' -f1)" == "$OLD_SHA" ]] ||
	fail "the binary changed despite the broken build"
if compgen -G "$BROKEN/data/backups/pre-upgrade-*.db" >/dev/null; then
	fail "the broken build created a DB backup after stopping"
fi
if compgen -G "$BROKEN/bin/indexarr.old-*" >/dev/null; then
	fail "the broken build saved a binary backup"
fi
kill_from_pidfile "$BROKEN/data/indexarr.pid"
ok "a broken importer build fails before stopping or backing up anything"

# --- 3. wrong healthz version triggers an automatic rollback ------------

FAILHEALTH="$TMP/failhealth"
mkdir -p "$FAILHEALTH/bin"
make_data "$FAILHEALTH/data"
make_old_binary "$FAILHEALTH/bin/indexarr" "old-version"
make_old_importer "$FAILHEALTH/bin/indexarr-import"
OLD_SHA="$(sha256sum "$FAILHEALTH/bin/indexarr" | cut -d' ' -f1)"
OLD_IMPORTER_SHA="$(sha256sum "$FAILHEALTH/bin/indexarr-import" | cut -d' ' -f1)"
OUT="$TMP/failhealth.out"
set +e
run_upgrade FAKE_HEALTH_MODE=badversion "$SCRIPT" --apply --yes --no-systemd \
	--data-dir "$FAILHEALTH/data" --binary "$FAILHEALTH/bin/indexarr" \
	--importer "$FAILHEALTH/bin/indexarr-import" --port "$PORT" >"$OUT" 2>&1
STATUS=$?
set -e
[[ "$STATUS" != "0" ]] || { cat "$OUT" >&2; fail "the wrong healthz version was accepted"; }
grep -q 'rolling back' "$OUT" || { cat "$OUT" >&2; fail "no automatic rollback"; }
grep -q 'db restored from' "$OUT" || { cat "$OUT" >&2; fail "the DB was not restored"; }
grep -q 'old binaries restored' "$OUT" || { cat "$OUT" >&2; fail "the binaries were not restored"; }
grep -q 'old service is healthy again' "$OUT" ||
	{ cat "$OUT" >&2; fail "the old service was not restarted"; }
[[ "$(sha256sum "$FAILHEALTH/bin/indexarr" | cut -d' ' -f1)" == "$OLD_SHA" ]] ||
	fail "the new binary was left in place after the failed health check"
[[ "$(sha256sum "$FAILHEALTH/bin/indexarr-import" | cut -d' ' -f1)" == "$OLD_IMPORTER_SHA" ]] ||
	fail "the new importer was left in place after the failed health check"
[[ "$(db_value "$FAILHEALTH/data/indexarr.db" test_marker)" == "original-marker" ]] ||
	fail "the old database was not restored"
compgen -G "$FAILHEALTH/data/backups/pre-upgrade-*.db" >/dev/null ||
	fail "the pre-upgrade backup was lost during the rollback"
wait_for_health || fail "the automatically rolled-back service is not healthy"
KEY_VALUE="$(db_value "$FAILHEALTH/data/indexarr.db" api_key)"
if grep -qF -f <(printf '%s\n' "$KEY_VALUE") "$OUT"; then
	fail "the rollback output printed the Torznab API key"
fi
kill_from_pidfile "$FAILHEALTH/data/indexarr.pid"
ok "a wrong healthz version rolls the DB and binaries back automatically"

echo "upgrade test passed"
