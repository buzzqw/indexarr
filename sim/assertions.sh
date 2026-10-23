#!/usr/bin/env bash
# Shared end-to-end assertions for sim/run.sh and sim/run-docker.sh.
#
# run_assertions IDX_BASE MOCK_BASE MOCK_INTERNAL DEF_FILE WORK_DIR
#   IDX_BASE       indexarr HTTP base reachable from the host
#   MOCK_BASE      simmock HTTP base reachable from the host
#   MOCK_INTERNAL  simmock base reachable from indexarr (Docker: http://simmock:19080)
#
# The function returns non-zero when at least one assertion fails.

set -euo pipefail

FAILS=0

ok() { printf 'ok   %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*" >&2; FAILS=$((FAILS + 1)); }

assert_contains() {
    local label=$1 haystack=$2 needle=$3
    if [[ $haystack == *"$needle"* ]]; then
        ok "$label"
    else
        fail "$label (mancante: $needle)"
    fi
}

assert_count() {
    local label=$1 haystack=$2 needle=$3 expected=$4
    local actual
    actual=$(grep -o -- "$needle" <<<"$haystack" | wc -l | tr -d ' ')
    if [[ $actual == "$expected" ]]; then
        ok "$label ($actual)"
    else
        fail "$label (trovati $actual, attesi $expected)"
    fi
}

fetch() {
    local url=$1
    local body
    if ! body=$(curl -fsS "$url" 2>"$WORK_DIR/curl.err"); then
        fail "GET $url: $(tr '\n' ' ' <"$WORK_DIR/curl.err")"
        return 0
    fi
    printf '%s' "$body"
}

request() {
    local method=$1 url=$2 body=${3-}
    local out
    if [[ -n $body ]]; then
        if ! out=$(curl -sS -X "$method" -H 'Content-Type: application/json' \
            --data "$body" -w $'\n%{http_code}' "$url" 2>"$WORK_DIR/curl.err"); then
            fail "$method $url: $(tr '\n' ' ' <"$WORK_DIR/curl.err")"
            return 0
        fi
    else
        if ! out=$(curl -sS -X "$method" -w $'\n%{http_code}' "$url" 2>"$WORK_DIR/curl.err"); then
            fail "$method $url: $(tr '\n' ' ' <"$WORK_DIR/curl.err")"
            return 0
        fi
    fi
    printf '%s' "$out"
}

split_response() {
    RESP_STATUS=${RESP##*$'\n'}
    RESP_BODY=${RESP%$'\n'*}
}

json_id() {
    grep -o '"id":[0-9]*' <<<"$1" | head -n1 | cut -d: -f2
}

run_assertions() {
    local idx=$1 mock=$2 mock_internal=$3 def_file=$4 work=$5
    WORK_DIR=$work
    FAILS=0
    mkdir -p "$WORK_DIR"

    printf '\n== simmock: health, Torznab/Newznab e payload ==\n'
    assert_contains "simmock healthz" "$(fetch "$mock/healthz")" '"status":"ok"'

    local caps search
    caps=$(fetch "$mock/torznab/api?t=caps&apikey=demo-key")
    assert_contains "caps root" "$caps" '<caps>'
    assert_contains "caps TV" "$caps" '<category id="5000"'
    assert_contains "caps modi" "$caps" 'tv-search'

    search=$(fetch "$mock/torznab/api?t=search&apikey=demo-key")
    assert_count "Torznab mock: 3 release" "$search" '<item>' 3
    assert_contains "Torznab mock: infohash" "$search" 'name="infohash"'
    assert_contains "Torznab mock: magnet" "$search" 'name="magneturl"'
    assert_contains "Torznab mock: enclosure nzb" "$search" 'type="application/x-nzb"'

    local code ctype
    code=$(curl -sS -o "$WORK_DIR/mock.torrent" -w '%{http_code}' \
        "$mock/torznab/api?t=get&id=demo-torrent-2&apikey=demo-key")
    ctype=$(curl -sS -o /dev/null -w '%{content_type}' \
        "$mock/torznab/api?t=get&id=demo-torrent-2&apikey=demo-key")
    [[ $code == 200 ]] && ok "mock .torrent HTTP 200" || fail "mock .torrent HTTP $code"
    assert_contains "mock .torrent content type" "$ctype" 'bittorrent'
    if [[ -s $WORK_DIR/mock.torrent ]] && [[ $(head -c1 "$WORK_DIR/mock.torrent") == d ]]; then
        ok "mock .torrent bencoded"
    else
        fail "mock .torrent non valido"
    fi

    code=$(curl -sS -o "$WORK_DIR/mock.nzb" -w '%{http_code}' \
        "$mock/newznab/api?t=get&id=demo-nzb-3&apikey=demo-key")
    ctype=$(curl -sS -o /dev/null -w '%{content_type}' \
        "$mock/newznab/api?t=get&id=demo-nzb-3&apikey=demo-key")
    [[ $code == 200 ]] && ok "mock .nzb HTTP 200" || fail "mock .nzb HTTP $code"
    assert_contains "mock .nzb content type" "$ctype" 'nzb'
    assert_contains "mock .nzb XML" "$(cat "$WORK_DIR/mock.nzb")" '<nzb'

    printf '\n== simmock: tracker HTML ==\n'
    local jar="$WORK_DIR/tracker.jar"
    rm -f "$jar"
    assert_contains "tracker login CSRF" "$(fetch "$mock/tracker/login")" 'name="csrf_token"'
    code=$(curl -sS -c "$jar" -o /dev/null -w '%{http_code}' -X POST \
        --data 'csrf_token=synthetic-csrf-token&username=demo&password=demo' \
        "$mock/tracker/login")
    [[ $code == 200 ]] && ok "tracker login" || fail "tracker login HTTP $code"
    code=$(curl -sS -b "$jar" -o /dev/null -w '%{http_code}' "$mock/tracker/search")
    [[ $code == 200 ]] && ok "tracker search autenticata" || fail "tracker search HTTP $code"
    local tracker
    tracker=$(curl -fsS -b "$jar" "$mock/tracker/search")
    assert_count "tracker: 2 download" "$tracker" 'class="download"' 2
    assert_contains "tracker: dimensione tipizzata" "$tracker" 'class="size"'
    assert_contains "tracker: seeders tipizzati" "$tracker" 'class="seeders"'
    assert_contains "tracker: data tipizzata" "$tracker" 'class="date"'
    assert_contains "tracker: categoria tipizzata" "$tracker" 'class="category"'
    code=$(curl -sS -b "$jar" -o "$WORK_DIR/tracker.torrent" -w '%{http_code}' \
        "$mock/tracker/download/1.torrent")
    [[ $code == 200 && -s $WORK_DIR/tracker.torrent ]] \
        && ok "tracker download protetto da cookie" \
        || fail "tracker download HTTP $code"
    code=$(curl -sS -o /dev/null -w '%{http_code}' "$mock/tracker/download/1.torrent")
    [[ $code == 401 ]] && ok "tracker download senza cookie respinto" \
        || fail "tracker download senza cookie HTTP $code"

    printf '\n== simmock: Prowlarr e Sonarr ==\n'
    local header=(-H 'X-Api-Key: demo-prowlarr')
    assert_contains "Prowlarr indexers" "$(curl -fsS "${header[@]}" "$mock/prowlarr/api/v1/indexer")" '"Torznab"'
    assert_contains "Prowlarr applications" "$(curl -fsS "${header[@]}" "$mock/prowlarr/api/v1/applications")" '"Sonarr"'

    printf '\n== indexarr: definizione e indexer nativo ==\n'
    local yaml_json definition_body
    yaml_json=$(awk '{ gsub(/\\/,"\\\\"); gsub(/"/,"\\\""); printf "%s\\n", $0 }' "$def_file")
    definition_body=$(printf '{"body":"%s"}' "$yaml_json")
    RESP=$(request POST "$idx/api/v2/definitions?apikey=$KEY" "$definition_body")
    split_response
    if [[ $RESP_STATUS == 201 || $RESP_STATUS == 200 ]]; then
        ok "POST /api/v2/definitions ($RESP_STATUS)"
    else
        fail "POST /api/v2/definitions HTTP $RESP_STATUS: $RESP_BODY"
        return 1
    fi

    local indexer_body
    indexer_body=$(printf '{"definition_id":"sim-torznab","name":"Sim Torznab","slug":"sim-torznab","settings":{"url":"%s/torznab/api","apikey":"demo-key","api_key":"demo-key","allow_private":"true","categories":"2040,3040,5040"}}' "$mock_internal")
    RESP=$(request POST "$idx/api/v2/indexers?apikey=$KEY" "$indexer_body")
    split_response
    local indexer_id
    indexer_id=$(json_id "$RESP_BODY")
    if [[ $RESP_STATUS == 201 && -n $indexer_id ]]; then
        ok "POST /api/v2/indexers (id=$indexer_id)"
    else
        fail "POST /api/v2/indexers HTTP $RESP_STATUS: $RESP_BODY"
        return 1
    fi

    RESP=$(request POST "$idx/api/v2/indexers/$indexer_id/run?apikey=$KEY" "")
    split_response
    assert_contains "run indexer HTTP 200" "$RESP_STATUS" '200'
    assert_contains "run indexer: 3 viste" "$RESP_BODY" '"seen":3'
    assert_contains "run indexer: 3 nuove" "$RESP_BODY" '"new":3'

    printf '\n== indexarr: Torznab e download ==\n'
    local torznab
    torznab=$(fetch "$idx/api?t=search&apikey=$KEY")
    assert_contains "Torznab indexarr: total 3" "$torznab" 'total="3"'
    assert_count "Torznab indexarr: 3 item" "$torznab" '<item>' 3
    assert_contains "Torznab indexarr: infohash" "$torznab" 'name="infohash"'

    local link
    link=$(grep -o '/dl/[0-9][0-9]*\.torrent?apikey=[A-Za-z0-9]*' <<<"$torznab" | head -n1)
    if [[ -n $link ]]; then
        code=$(curl -sS -o "$WORK_DIR/idx.torrent" -w '%{http_code}' "$idx$link")
        ctype=$(curl -sS -o /dev/null -w '%{content_type}' "$idx$link")
        [[ $code == 200 && -s $WORK_DIR/idx.torrent ]] \
            && ok "download .torrent da indexarr" \
            || fail "download .torrent da indexarr HTTP $code"
        assert_contains "content type .torrent indexarr" "$ctype" 'bittorrent'
    else
        fail "nessun link .torrent nella risposta indexarr"
    fi

    printf '\n== indexarr: applicazione Sonarr e sync ==\n'
    local app_body
    app_body=$(printf '{"kind":"sonarr","name":"Sim Sonarr","url":"%s/sonarr","api_key":"demo-sonarr","sync_level":"full"}' "$mock_internal")
    RESP=$(request POST "$idx/api/v2/applications?apikey=$KEY" "$app_body")
    split_response
    local app_id
    app_id=$(json_id "$RESP_BODY")
    if [[ $RESP_STATUS == 201 && -n $app_id ]]; then
        ok "POST /api/v2/applications (id=$app_id)"
    else
        fail "POST /api/v2/applications HTTP $RESP_STATUS: $RESP_BODY"
        return 1
    fi

    RESP=$(request POST "$idx/api/v2/applications/$app_id/sync?apikey=$KEY" "")
    split_response
    assert_contains "sync Sonarr HTTP 200" "$RESP_STATUS" '200'
    assert_contains "sync Sonarr: 1 aggiunto" "$RESP_BODY" '"added":1'

    local sonarr_remote
    sonarr_remote=$(curl -fsS -H 'X-Api-Key: demo-sonarr' "$mock/sonarr/api/v3/indexer")
    assert_contains "Sonarr ha l'indexer" "$sonarr_remote" 'indexarr · Sim Torznab'
    assert_contains "Sonarr baseUrl indexarr" "$sonarr_remote" 'sim-torznab/api'

    printf '\n== download client: SABnzbd, NZBGet, Deluge, rTorrent ==\n'
    local sab
    sab=$(curl -fsS -X POST "$mock/sabnzbd/api" \
        --data-urlencode "name=$mock_internal/newznab/api?t=get&id=demo-nzb-3&apikey=demo-key" \
        -d 'mode=addurl' -d 'nzbname=Sim.NZB' -d 'apikey=demo-sab')
    assert_contains "SABnzbd addurl" "$sab" '"status":true'

    local payload nzbget_body nzbget
    payload=$(printf 'sim-nzb-payload' | base64 | tr -d '\n')
    nzbget_body=$(printf '{"method":"append","params":["Sim.NZB","%s","tv",0,false,"",0,"SCORE"],"id":1}' "$payload")
    nzbget=$(curl -fsS -X POST -H 'Content-Type: application/json' --data "$nzbget_body" "$mock/nzbget/jsonrpc")
    assert_contains "NZBGet append" "$nzbget" '"result":1'

    local djar="$WORK_DIR/deluge.jar" deluge magnet
    rm -f "$djar"
    deluge=$(curl -fsS -c "$djar" -X POST -H 'Content-Type: application/json' \
        --data '{"method":"auth.login","params":["demo","demo"],"id":1}' "$mock/deluge/json")
    assert_contains "Deluge auth.login" "$deluge" '"result":true'
    magnet='magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567'
    deluge=$(curl -fsS -b "$djar" -c "$djar" -X POST -H 'Content-Type: application/json' \
        --data "{\"method\":\"core.add_torrent_magnet\",\"params\":[\"$magnet\"],\"id\":1}" "$mock/deluge/json")
    assert_contains "Deluge add_torrent_magnet" "$deluge" 'demo-deluge-hash'

    local rpc list_body load_body
    list_body='<?xml version="1.0"?><methodCall><methodName>system.listMethods</methodName><params></params></methodCall>'
    rpc=$(curl -fsS -X POST -H 'Content-Type: text/xml' --data "$list_body" "$mock/rtorrent/RPC2")
    assert_contains "rTorrent system.listMethods" "$rpc" 'system.listMethods'
    load_body='<?xml version="1.0"?><methodCall><methodName>load.start</methodName><params><param><value><string></string></value></param><param><value><string>magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567</string></value></param></params></methodCall>'
    rpc=$(curl -fsS -X POST -H 'Content-Type: text/xml' --data "$load_body" "$mock/rtorrent/RPC2")
    assert_contains "rTorrent load.start" "$rpc" '<i4>0</i4>'

    printf '\n== stato deterministico di simmock ==\n'
    local state
    state=$(fetch "$mock/debug/state")
    assert_contains "state: Torznab ricerche" "$state" '"torznab_searches":'
    assert_contains "state: tracker download" "$state" '"tracker_downloads":["1.torrent"]'
    assert_contains "state: Sonarr indexer" "$state" 'indexarr · Sim Torznab'
    assert_contains "state: SABnzbd" "$state" '"sabnzbd":[{'
    assert_contains "state: NZBGet" "$state" '"nzbget":[{'
    assert_contains "state: Deluge" "$state" '"deluge":[{'
    assert_contains "state: rTorrent" "$state" '"rtorrent":[{'

    if ((FAILS > 0)); then
        printf '\n%d asserzioni fallite\n' "$FAILS" >&2
        return 1
    fi
    printf '\nTutte le asserzioni sono passate.\n'
    return 0
}
