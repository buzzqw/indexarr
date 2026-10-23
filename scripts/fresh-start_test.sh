#!/usr/bin/env bash
# Offline test for scripts/fresh-start.sh.
#
# It never touches the real data directory, systemd or the network: the fake
# binary creates a disposable SQLite database and answers /healthz, and the
# fake importer records whether the manager keys arrived through the
# environment without printing them. A tiny local HTTP server stands in for
# Jackett only during the import test.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="$ROOT/scripts/fresh-start.sh"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/indexarr-fresh-start-test.XXXXXX")"
PIDS=()

cleanup() {
	local pid
	for pid in "${PIDS[@]:-}"; do
		kill "$pid" 2>/dev/null || true
	done
	if [[ -n "${JACKETT_SERVER_PID:-}" ]]; then
		kill "$JACKETT_SERVER_PID" 2>/dev/null || true
	fi
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

free_port() {
	python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
}

bash -n "$SCRIPT" || fail "bash -n"
ok "bash -n"

# --- fake binaries --------------------------------------------------------

cat >"$TMP/fake-indexarr" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
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
db="$dir/indexarr.db"
if [[ ! -f "$db" ]]; then
	sqlite3 "$db" <<'SQL'
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '');
INSERT INTO settings(key, value) VALUES ('api_key', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb');
SQL
fi
host="${listen%:*}"
port="${listen##*:}"
case "$host" in "" | "*" | 0.0.0.0 | "[::]" | "::") host="127.0.0.1" ;; esac
exec python3 -c '
import http.server, sys
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        code = 200 if self.path == "/healthz" else 404
        body = b"{\"status\":\"ok\"}" if code == 200 else b"no"
        self.send_response(code)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *args):
        pass
http.server.HTTPServer((sys.argv[1], int(sys.argv[2])), H).serve_forever()
' "$host" "$port"
FAKE
chmod +x "$TMP/fake-indexarr"

cat >"$TMP/fake-import" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
prowlarr_sha="$(printf '%s' "${INDEXARR_PROWLARR_KEY:-}" | sha256sum | cut -d' ' -f1)"
jackett_sha="$(printf '%s' "${INDEXARR_JACKETT_KEY:-}" | sha256sum | cut -d' ' -f1)"
printf 'prowlarr=%s jackett=%s tl=%s psha=%s jsha=%s args=%s\n' \
	"${INDEXARR_PROWLARR_KEY:+set}" \
	"${INDEXARR_JACKETT_KEY:+set}" \
	"${INDEXARR_TORRENTLEECH_RSS_URL:+set}" \
	"$prowlarr_sha" \
	"$jackett_sha" \
	"$*" >>"${FAKE_IMPORT_MARKER:?}"
exit 0
FAKE
chmod +x "$TMP/fake-import"

make_data() {
	local dir="$1" key="$2" marker="$3"
	mkdir -p "$dir"
	chmod 700 "$dir"
	cat >"$dir/indexarr.json" <<JSON
{
  "listen": "127.0.0.1:1",
  "db_path": "indexarr.db",
  "log_level": "info"
}
JSON
	chmod 600 "$dir/indexarr.json"
	sqlite3 "$dir/indexarr.db" <<SQL
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '');
CREATE TABLE sources (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,
  kind TEXT NOT NULL DEFAULT 'torznab',
  manager TEXT NOT NULL DEFAULT 'auto',
  url TEXT NOT NULL,
  api_key TEXT NOT NULL DEFAULT ''
);
INSERT INTO settings(key, value) VALUES ('api_key', '$key');
INSERT INTO settings(key, value) VALUES ('marker', '$marker');
INSERT INTO sources(id, name, kind, url, api_key) VALUES (1, 'jackett', 'torznab', 'http://127.0.0.1:9117', 'jackett-source-key');
INSERT INTO sources(id, name, kind, url, api_key) VALUES (2, 'Prowlarr', 'torznab', 'http://127.0.0.1:9696', 'prowlarr-source-key');
INSERT INTO sources(id, name, kind, url, api_key) VALUES (37, 'rss24h.torrentleech.org', 'rss', 'https://rss24h.torrentleech.org/TESTRSSKEY', '');
SQL
}

db_value() {
	sqlite3 -readonly "$1" "SELECT value FROM settings WHERE key='$2';"
}

wait_for_health() {
	local base="$1" i
	for i in $(seq 1 100); do
		if curl -fsS --max-time 1 "$base/healthz" >/dev/null 2>&1; then
			return 0
		fi
		sleep 0.1
	done
	return 1
}

kill_from_pidfile() {
	local pidfile="$1" pid
	[[ -f "$pidfile" ]] || return 0
	pid="$(cat "$pidfile" 2>/dev/null || true)"
	if [[ "$pid" =~ ^[0-9]+$ ]]; then
		kill "$pid" 2>/dev/null || true
		wait "$pid" 2>/dev/null || true
	fi
}

# --- 1. dry run is harmless ----------------------------------------------

OLD_KEY="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
DATA="$TMP/dry/data"
make_data "$DATA" "$OLD_KEY" "old-marker"
BEFORE="$(sha256sum "$DATA/indexarr.db" | cut -d' ' -f1)"
OUT="$TMP/dry.out"
PORT="$(free_port)"
"$SCRIPT" --data-dir "$DATA" --binary "$TMP/fake-indexarr" --importer "$TMP/fake-import" \
	--port "$PORT" --no-systemd >"$OUT" 2>&1
AFTER="$(sha256sum "$DATA/indexarr.db" | cut -d' ' -f1)"
[[ "$BEFORE" == "$AFTER" ]] || fail "dry run changed the database"
if compgen -G "$DATA.old-*" >/dev/null; then
	fail "dry run created a backup directory"
fi
grep -q 'dry run' "$OUT" || fail "dry run output missing 'dry run'"
grep -q 'nothing will change' "$OUT" || fail "dry run output missing the warning"
if grep -q -- "$OLD_KEY" "$OUT"; then
	fail "dry run printed the Torznab key"
fi
ok "dry run does not touch the data directory or print the key"

# --- 2. apply + rollback (skip import) -----------------------------------

DATA="$TMP/apply/data"
make_data "$DATA" "$OLD_KEY" "old-marker"
PORT="$(free_port)"
OUT="$TMP/apply.out"
"$SCRIPT" --apply --yes --no-systemd --data-dir "$DATA" \
	--binary "$TMP/fake-indexarr" --importer "$TMP/fake-import" \
	--port "$PORT" --skip-import >"$OUT" 2>&1
grep -q 'fresh start complete' "$OUT" || { cat "$OUT" >&2; fail "apply did not complete"; }
NEW_KEY="$(db_value "$DATA/indexarr.db" api_key)"
[[ "$NEW_KEY" == "$OLD_KEY" ]] || fail "apply did not preserve the Torznab key"
[[ "$(db_value "$DATA/indexarr.db" marker)" == "" ]] || fail "new database is not fresh"
BACKUP="$(compgen -G "$DATA.old-*" | head -n 1)"
[[ -n "$BACKUP" && -d "$BACKUP" ]] || fail "apply did not keep data.old-*"
wait_for_health "http://127.0.0.1:$PORT" || fail "new instance is not healthy"
if grep -q -- "$OLD_KEY" "$OUT"; then
	fail "apply printed the Torznab key"
fi
if grep -R -q -- "$OLD_KEY" "$DATA"/*.log 2>/dev/null; then
	fail "apply wrote the Torznab key to a log"
fi
kill_from_pidfile "$DATA/indexarr.pid"
ok "apply moves data, creates a fresh DB and preserves the key"

OUT="$TMP/rollback.out"
"$SCRIPT" --rollback --yes --no-systemd --data-dir "$DATA" \
	--binary "$TMP/fake-indexarr" --importer "$TMP/fake-import" \
	--port "$PORT" >"$OUT" 2>&1
grep -q 'rollback complete' "$OUT" || { cat "$OUT" >&2; fail "rollback did not complete"; }
[[ "$(db_value "$DATA/indexarr.db" marker)" == "old-marker" ]] || fail "rollback did not restore the old database"
[[ "$(db_value "$DATA/indexarr.db" api_key)" == "$OLD_KEY" ]] || fail "rollback lost the old key"
compgen -G "$DATA.failed-*" >/dev/null || fail "rollback did not keep data.failed-*"
if compgen -G "$DATA.old-*" >/dev/null; then
	fail "rollback left the old backup in place"
fi
kill_from_pidfile "$DATA/indexarr.pid"
ok "rollback moves the new data to data.failed-* and restores data.old-*"

# --- 3. import path passes keys through the environment ------------------

DATA="$TMP/import/data"
make_data "$DATA" "$OLD_KEY" "old-marker"
PORT="$(free_port)"
JACKETT_PORT="$(free_port)"
python3 -c '
import http.server, sys
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if "t=indexers" not in self.path:
            self.send_response(404); self.end_headers(); return
        if "apikey=jackett-live-key" not in self.path:
            self.send_response(403); self.end_headers(); return
        body = b"<indexers><indexer id=\"site\" configured=\"true\"><title>Site</title><link>https://site.example/</link></indexer></indexers>"
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *args):
        pass
http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
' "$JACKETT_PORT" &
JACKETT_SERVER_PID=$!
PIDS+=("$JACKETT_SERVER_PID")
sleep 0.3
mkdir -p "$TMP/jackett-copy/Indexers"
printf '{"Port": 9117, "APIKey": "stale-copy-key"}\n' >"$TMP/jackett-copy/ServerConfig.json"
cat >"$TMP/managers.env" <<ENV
JACKETT_URL=http://127.0.0.1:$JACKETT_PORT
JACKETT_API_KEY=jackett-live-key
PROWLARR_URL=http://127.0.0.1:$PORT
PROWLARR_API_KEY=prowlarr-live-key
ENV
export FAKE_IMPORT_MARKER="$TMP/import.marker"
: >"$FAKE_IMPORT_MARKER"
OUT="$TMP/import.out"
"$SCRIPT" --apply --yes --no-systemd --data-dir "$DATA" \
	--binary "$TMP/fake-indexarr" --importer "$TMP/fake-import" \
	--port "$PORT" --env-file "$TMP/managers.env" \
	--jackett-copy "$TMP/jackett-copy" >"$OUT" 2>&1
grep -q 'fresh start complete' "$OUT" || { cat "$OUT" >&2; fail "import apply did not complete"; }
APPLY_CALLS="$(grep -c -- '-apply' "$FAKE_IMPORT_MARKER" || true)"
[[ "$APPLY_CALLS" == "1" ]] || { cat "$FAKE_IMPORT_MARKER" >&2; fail "expected one combined importer apply call, got $APPLY_CALLS"; }
grep -q 'prowlarr=set jackett=set tl=set' "$FAKE_IMPORT_MARKER" || fail "manager or TorrentLeech secrets were not passed through the environment"
grep -q -- '-prowlarr http://127.0.0.1:'"$PORT" "$FAKE_IMPORT_MARKER" || fail "Prowlarr URL missing from the import"
grep -q -- '-jackett-url http://127.0.0.1:'"$JACKETT_PORT" "$FAKE_IMPORT_MARKER" || fail "live Jackett URL missing from the import"
if grep -q -- '-jackett /' "$FAKE_IMPORT_MARKER"; then
	fail "the Jackett copy was used even though the live API answered"
fi
PROWLARR_SHA="$(printf '%s' 'prowlarr-live-key' | sha256sum | cut -d' ' -f1)"
JACKETT_SHA="$(printf '%s' 'jackett-live-key' | sha256sum | cut -d' ' -f1)"
grep -q -- "psha=$PROWLARR_SHA" "$FAKE_IMPORT_MARKER" || fail "Prowlarr did not receive the env-file key"
grep -q -- "jsha=$JACKETT_SHA" "$FAKE_IMPORT_MARKER" || fail "Jackett did not receive the env-file key"
if grep -q -- 'jackett-source-key\|prowlarr-source-key\|TESTRSSKEY\|jackett-live-key\|prowlarr-live-key' "$OUT" "$FAKE_IMPORT_MARKER" "$DATA/import.log"; then
	fail "import output, marker or log contains a secret"
fi
kill_from_pidfile "$DATA/indexarr.pid"
ok "import uses the env-file keys and the live Jackett list, never the old source keys"

# --- 3b. Jackett copy is only the fallback when the live API fails --------

DATA="$TMP/fallback/data"
make_data "$DATA" "$OLD_KEY" "old-marker"
PORT="$(free_port)"
DEAD_JACKETT_PORT="$(free_port)"
cat >"$TMP/fallback.env" <<ENV
JACKETT_URL=http://127.0.0.1:$DEAD_JACKETT_PORT
JACKETT_API_KEY=jackett-live-key
PROWLARR_URL=http://127.0.0.1:$PORT
PROWLARR_API_KEY=prowlarr-live-key
ENV
: >"$FAKE_IMPORT_MARKER"
OUT="$TMP/fallback.out"
"$SCRIPT" --apply --yes --no-systemd --data-dir "$DATA" \
	--binary "$TMP/fake-indexarr" --importer "$TMP/fake-import" \
	--port "$PORT" --env-file "$TMP/fallback.env" \
	--jackett-copy "$TMP/jackett-copy" >"$OUT" 2>&1
grep -q 'fresh start complete' "$OUT" || { cat "$OUT" >&2; fail "fallback apply did not complete"; }
grep -q -- '-jackett /' "$FAKE_IMPORT_MARKER" || { cat "$FAKE_IMPORT_MARKER" >&2; fail "fallback did not use the Jackett copy"; }
if grep -q -- '-jackett-url' "$FAKE_IMPORT_MARKER"; then
	fail "fallback still pointed the copy at the dead live URL"
fi
grep -q 'using --jackett-copy as the fallback' "$OUT" || fail "fallback warning missing"
kill_from_pidfile "$DATA/indexarr.pid"
ok "the Jackett copy is used only after the live API fails"

# --- 4. a failed preflight changes nothing -------------------------------

DATA="$TMP/failed/data"
mkdir -p "$DATA"
chmod 700 "$DATA"
printf '{"listen":"127.0.0.1:1","db_path":"indexarr.db"}\n' >"$DATA/indexarr.json"
OUT="$TMP/failed.out"
set +e
"$SCRIPT" --apply --yes --no-systemd --data-dir "$DATA" \
	--binary "$TMP/fake-indexarr" --importer "$TMP/fake-import" \
	--port "$(free_port)" --skip-import >"$OUT" 2>&1
STATUS=$?
set -e
[[ "$STATUS" != "0" ]] || fail "apply with no old database succeeded"
grep -q 'old database not found' "$OUT" || { cat "$OUT" >&2; fail "missing clear error"; }
if compgen -G "$DATA.old-*" >/dev/null; then
	fail "failed preflight created a backup directory"
fi
ok "failed preflight leaves the data directory untouched"

# --- 5. rollback finds a manually created data.old-* ---------------------

DATA="$TMP/manual/data"
make_data "$DATA" "$OLD_KEY" "new-marker"
MANUAL="$DATA.old-20000101-000000"
make_data "$MANUAL" "$OLD_KEY" "manual-old-marker"
PORT="$(free_port)"
OUT="$TMP/manual.out"
"$SCRIPT" --rollback --yes --no-systemd --data-dir "$DATA" \
	--binary "$TMP/fake-indexarr" --importer "$TMP/fake-import" \
	--port "$PORT" >"$OUT" 2>&1
grep -q 'rollback complete' "$OUT" || { cat "$OUT" >&2; fail "manual rollback did not complete"; }
[[ "$(db_value "$DATA/indexarr.db" marker)" == "manual-old-marker" ]] || fail "manual rollback did not pick the old directory"
kill_from_pidfile "$DATA/indexarr.pid"
ok "rollback finds the newest data.old-* directory"

echo "fresh-start test passed"
