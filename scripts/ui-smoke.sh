#!/usr/bin/env bash
# UI route and internal-link smoke test for indexarr 2.
#
# Read-only by design: it sends only HTTP GET requests to an instance that is
# already running. It never starts, stops or reconfigures the service and never
# touches its database.
#
#   ./scripts/ui-smoke.sh
#   INDEXARR_BASE=http://127.0.0.1:8484 ./scripts/ui-smoke.sh
#   UI_SMOKE_STRICT=1 ./scripts/ui-smoke.sh
#
# Exit status: 0 when there are no hard failures; 1 otherwise (with
# UI_SMOKE_STRICT=1 any warning is also a failure). The new /activity routes are
# reported as "pending" while the redesign is in progress and do not fail.
set -euo pipefail

BASE="${INDEXARR_BASE:-http://127.0.0.1:8484}"
BASE="${BASE%/}"
STRICT="${UI_SMOKE_STRICT:-0}"

for bin in curl grep sed awk sort uniq mktemp; do
  if ! command -v "$bin" >/dev/null 2>&1; then
    echo "FAIL: missing required command: $bin" >&2
    exit 1
  fi
done

TMP="$(mktemp -d "${TMPDIR:-/tmp}/indexarr-ui-smoke.XXXXXX")"
cleanup() { rm -rf "$TMP"; }
trap cleanup EXIT

HOSTPORT="${BASE#*://}"
HOSTPORT="${HOSTPORT%%/*}"
HOST_NOAUTH="${HOSTPORT##*@}"

ROUTES=0
ROUTE_OK=0
ROUTE_FAIL=0
ROUTE_PENDING=0
LINKS=0
LINK_OK=0
LINK_FAIL=0
NAV_FAIL=0
WARNS=0
HARD_FAILS=0

HTML_PAGES=0
LANG_MISSING=0
SKIP_MISSING=0
LANG_EXAMPLE=""
SKIP_EXAMPLE=""
ROOT_BODY=""
FIRST_HTML_BODY=""

MATRIX_PATHS="$TMP/matrix-paths"
FOUND_LINKS="$TMP/found-links"
NAV_ENTRIES="$TMP/nav-entries"
: >"$MATRIX_PATHS"
: >"$FOUND_LINKS"
: >"$NAV_ENTRIES"

say() { printf '%s\n' "$*"; }
ok_route() { ROUTE_OK=$((ROUTE_OK + 1)); printf 'ok      %s %s\n' "$1" "$2"; }
ok_link() { LINK_OK=$((LINK_OK + 1)); printf 'ok      %s %s (link)\n' "$1" "$2"; }
fail_route() {
  ROUTE_FAIL=$((ROUTE_FAIL + 1))
  HARD_FAILS=$((HARD_FAILS + 1))
  printf 'FAIL    %s %s\n' "$1" "$2"
}
fail_link() {
  LINK_FAIL=$((LINK_FAIL + 1))
  HARD_FAILS=$((HARD_FAILS + 1))
  printf 'FAIL    %s %s (link)\n' "$1" "$2"
}
fail_nav() {
  NAV_FAIL=$((NAV_FAIL + 1))
  HARD_FAILS=$((HARD_FAILS + 1))
  printf 'FAIL    nav: %s\n' "$*"
}
pending_route() { ROUTE_PENDING=$((ROUTE_PENDING + 1)); printf 'pending %s %s (expected after redesign)\n' "$1" "$2"; }
warn() { WARNS=$((WARNS + 1)); printf 'WARN    %s\n' "$*"; }

# fetch <path> <body-file> -> "status|content-type|redirect-url"
fetch() {
  curl -sS --max-time 20 -o "$2" -w '%{http_code}|%{content_type}|%{redirect_url}' "$BASE$1"
}

# fetch_status <path> -> status, discarding the body (link checks only need it).
fetch_status() {
  curl -sS --max-time 20 -o /dev/null -w '%{http_code}' "$BASE$1"
}

# normalize_link <raw href/action> -> absolute internal path without query.
# Returns 1 for external URLs, fragments, /dl/, static assets and other files.
normalize_link() {
  local raw="$1" host path
  raw="${raw//&amp;/&}"
  raw="${raw%%#*}"
  case "$raw" in
    ''|'#'|mailto:*|tel:*|javascript:*|data:*|blob:*) return 1 ;;
  esac
  if [[ "$raw" == http://* || "$raw" == https://* ]]; then
    host="${raw#*://}"
    host="${host%%/*}"
    host="${host##*@}"
    if [[ "$host" != "$HOST_NOAUTH" ]]; then
      return 1
    fi
    raw="/${raw#*://*/}"
  elif [[ "$raw" == //* ]]; then
    return 1
  fi
  # Relative links such as "edit" are not part of the matrix: only root paths.
  [[ "$raw" == /* ]] || return 1
  path="${raw%%\?*}"
  case "$path" in
    /dl/*|/static/*|/sw.js|/favicon.ico|/robots.txt) return 1 ;;
    # Downloads that would create or copy a database on a plain GET: keep the
    # smoke test side-effect free. The list page itself is in the route matrix.
    /maintenance/backup|/maintenance/backups/*) return 1 ;;
  esac
  case "$path" in
    *.css|*.js|*.svg|*.png|*.ico|*.webmanifest|*.woff|*.woff2|*.map) return 1 ;;
  esac
  printf '%s\n' "$path"
}

# normalize_nav_href <raw href> -> internal path, keeping the query string so
# that /activity?tab=runs and /activity?tab=logs remain distinguishable.
normalize_nav_href() {
  local raw="$1" host
  raw="${raw//&amp;/&}"
  raw="${raw%%#*}"
  case "$raw" in
    ''|'#'|mailto:*|tel:*|javascript:*) return 1 ;;
  esac
  if [[ "$raw" == http://* || "$raw" == https://* ]]; then
    host="${raw#*://}"
    host="${host%%/*}"
    host="${host##*@}"
    if [[ "$host" != "$HOST_NOAUTH" ]]; then
      return 1
    fi
    raw="/${raw#*://*/}"
  elif [[ "$raw" == //* ]]; then
    return 1
  fi
  [[ "$raw" == /* ]] || return 1
  printf '%s\n' "$raw"
}

is_html_file() {
  grep -qiE '<!doctype[[:space:]]+html|<html([[:space:]>])' "$1"
}

# inspect_html <route> <body-file> scans links and the shared layout markers.
inspect_html() {
  local route="$1" out="$2"
  is_html_file "$out" || return 0
  HTML_PAGES=$((HTML_PAGES + 1))
  scan_links "$out"
  if [[ -z "$ROOT_BODY" && "$route" == "/" ]]; then
    ROOT_BODY="$out"
  fi
  if [[ -z "$FIRST_HTML_BODY" ]]; then
    FIRST_HTML_BODY="$out"
  fi
  if ! grep -qiE '<html[^>]+lang="(en|it|es|fr|de|zh|pl)"' "$out"; then
    LANG_MISSING=$((LANG_MISSING + 1))
    [[ -n "$LANG_EXAMPLE" ]] || LANG_EXAMPLE="$route"
  fi
  if ! grep -qiE '<a[^>]+href="#(main|content)"' "$out" \
    && ! grep -qiE '<a[^>]+class="[^"]*skip' "$out"; then
    SKIP_MISSING=$((SKIP_MISSING + 1))
    [[ -n "$SKIP_EXAMPLE" ]] || SKIP_EXAMPLE="$route"
  fi
}

# scan_links <html-file> collects href/action targets, remembering whether each
# target was a link or a form action (a GET on a POST-only action returns 405).
scan_links() {
  local body="$1" entry attr raw kind path
  while IFS= read -r entry; do
    [[ -n "$entry" ]] || continue
    attr="${entry%%=*}"
    raw="${entry#*=}"
    raw="${raw#\"}"
    raw="${raw%\"}"
    kind="href"
    [[ "$attr" == "action" ]] && kind="action"
    if path="$(normalize_link "$raw")"; then
      printf '%s\t%s\n' "$kind" "$path" >>"$FOUND_LINKS"
    fi
  done < <(grep -oE '(href|action)="[^"]+"' "$body" || true)
}

# check_html_route <route> checks the status (200 or a redirect) and scans the
# body of successful HTML pages for links and layout warnings.
check_html_route() {
  local route="$1" out="$TMP/body.$ROUTES" meta status rest redirect
  ROUTES=$((ROUTES + 1))
  printf '%s\n' "${route%%\?*}" >>"$MATRIX_PATHS"
  if ! meta="$(fetch "$route" "$out")"; then
    fail_route "$route" "curl-error"
    return
  fi
  status="${meta%%|*}"
  rest="${meta#*|}"
  redirect="${rest#*|}"
  case "$status" in
    200|301|302|303)
      ok_route "$route" "$status"
      if [[ -n "$redirect" ]]; then
        printf '        -> %s\n' "$redirect"
      fi
      ;;
    *)
      fail_route "$route" "$status"
      return
      ;;
  esac
  if [[ "$status" != "200" ]]; then
    return
  fi
  inspect_html "$route" "$out"
}

# check_exact_route <route> [needle] requires HTTP 200 and, optionally, that the
# response contains the given text. Used for JSON/XML/assets.
check_exact_route() {
  local route="$1" needle="${2:-}" out="$TMP/exact.$ROUTES" meta status
  ROUTES=$((ROUTES + 1))
  printf '%s\n' "${route%%\?*}" >>"$MATRIX_PATHS"
  if ! meta="$(fetch "$route" "$out")"; then
    fail_route "$route" "curl-error"
    return
  fi
  status="${meta%%|*}"
  if [[ "$status" != "200" ]]; then
    fail_route "$route" "$status"
    return
  fi
  if [[ -n "$needle" ]] && ! grep -q -- "$needle" "$out"; then
    fail_route "$route" "200 (missing $(printf '%q' "$needle"))"
    return
  fi
  if [[ ! -s "$out" ]]; then
    fail_route "$route" "200 (empty body)"
    return
  fi
  ok_route "$route" "$status"
}

# check_activity_route <route> treats 404 as pending until the redesign lands.
check_activity_route() {
  local route="$1" out="$TMP/activity.$ROUTES" meta status
  ROUTES=$((ROUTES + 1))
  printf '%s\n' "${route%%\?*}" >>"$MATRIX_PATHS"
  if ! meta="$(fetch "$route" "$out")"; then
    fail_route "$route" "curl-error"
    return
  fi
  status="${meta%%|*}"
  case "$status" in
    200|301|302|303)
      ok_route "$route" "$status"
      if [[ "$status" == "200" ]]; then
        inspect_html "$route" "$out"
      fi
      ;;
    404)
      pending_route "$route" "$status"
      ;;
    *)
      fail_route "$route" "$status"
      ;;
  esac
}

# check_navigation inspects the first <nav> block (preferring aria-label="Main")
# for duplicated entries in the main navigation.
check_navigation() {
  local src="${ROOT_BODY:-$FIRST_HTML_BODY}" nav flat anchor href label path
  if [[ -z "$src" ]]; then
    warn "navigation: no HTML page available for the <nav> checks"
    return
  fi
  nav="$(sed -n '/<nav[^>]*aria-label="Main"/,/<\/nav>/p' "$src")"
  if [[ -z "$nav" ]]; then
    nav="$(awk '/<nav([[:space:]>])/ { inside = 1 } inside { print } inside && /<\/nav>/ { exit }' "$src")"
  fi
  if [[ -z "$nav" ]]; then
    warn "navigation: no <nav> block found on ${src##*/}"
    return
  fi
  flat="$(tr '\n' ' ' <<<"$nav")"
  : >"$NAV_ENTRIES"
  while IFS= read -r anchor; do
    [[ "$anchor" == *href=* ]] || continue
    href="$(sed -E 's/.*href="([^"]*)".*/\1/' <<<"$anchor")"
    if ! path="$(normalize_nav_href "$href")"; then
      continue
    fi
    label="$(sed -E 's/<[^>]+>//g' <<<"$anchor")"
    label="$(sed -E 's/^[[:space:]]+//; s/[[:space:]]+$//; s/[[:space:]]+/ /g' <<<"$label")"
    label="$(tr '[:upper:]' '[:lower:]' <<<"$label")"
    [[ -n "$label" ]] || label="$path"
    printf '%s\t%s\n' "$label" "$path" >>"$NAV_ENTRIES"
  done < <(grep -oE '<a[^>]*>[^<]*</a>' <<<"$flat" || true)

  if [[ ! -s "$NAV_ENTRIES" ]]; then
    warn "navigation: no links found inside the main <nav>"
    return
  fi

  # Same label pointing at two different places is a duplicated nav entry.
  while IFS= read -r label; do
    [[ -n "$label" ]] || continue
    fail_nav "duplicate label \"$label\" (labels must identify one destination)"
  done < <(cut -f1 "$NAV_ENTRIES" | sort | uniq -d)

  # Same destination under different labels is suspicious but not always wrong
  # (a group label and its first child); report it as a warning.
  while IFS= read -r path; do
    [[ -n "$path" ]] || continue
    label="$(awk -F'\t' -v p="$path" '$2 == p { print $1 }' "$NAV_ENTRIES" | sort -u | tr '\n' ' ')"
    warn "navigation: $path is linked with different labels: $label"
  done < <(cut -f2 "$NAV_ENTRIES" | sort | uniq -d)

}

# check_links checks every distinct internal path once. Paths already covered by
# the route matrix are skipped. Only GET is used; 405 on a form action is fine.
check_links() {
  local unique="$TMP/links-unique" kind path status
  sort -u "$FOUND_LINKS" | awk -F'\t' '
    {
      p = $2
      if (!(p in kind) || $1 == "href") kind[p] = $1
    }
    END {
      for (p in kind) printf "%s\t%s\n", kind[p], p
    }
  ' | sort -t $'\t' -k2,2 >"$unique"

  while IFS=$'\t' read -r kind path; do
    [[ -n "$path" ]] || continue
    if grep -qxF -- "$path" "$MATRIX_PATHS"; then
      continue
    fi
    LINKS=$((LINKS + 1))
    if ! status="$(fetch_status "$path")"; then
      fail_link "$path" "curl-error"
      continue
    fi
    case "$status" in
      200|201|202|204|206|301|302|303|304|307|308)
        ok_link "$path" "$status"
        ;;
      401|403)
        warn "link $path returned $status (authentication protected?)"
        ;;
      405)
        if [[ "$kind" == "action" ]]; then
          ok_link "$path" "405 (POST-only form action)"
        else
          warn "link $path returned 405 (POST-only route used as a link)"
        fi
        ;;
      5??)
        fail_link "$path" "$status"
        ;;
      4??)
        # A form action is POST-only: a GET probe can legitimately return 400
        # (for example when it falls through to a dynamic /releases/{id} route)
        # or 404. Only a 404 is worth a warning; nothing was mutated.
        if [[ "$kind" == "action" ]]; then
          if [[ "$status" == "404" ]]; then
            warn "form action $path returned 404 on the GET probe"
          else
            ok_link "$path" "$status (form action, GET probe)"
          fi
        elif [[ "$status" == "404" ]]; then
          fail_link "$path" "$status"
        else
          warn "link $path returned $status"
        fi
        ;;
      000)
        fail_link "$path" "no response"
        ;;
      *)
        ok_link "$path" "$status"
        ;;
    esac
  done <"$unique"
}

say "==> indexarr UI smoke test"
say "base: $BASE"
if [[ "$STRICT" == "1" ]]; then
  say "mode: strict (warnings are failures)"
else
  say "mode: default (warnings are not failures)"
fi

# Fail fast with a clear message: this script never starts the service.
if ! PREFLIGHT="$(curl -sS --max-time 5 -o /dev/null -w '%{http_code}' "$BASE/healthz" 2>/dev/null)"; then
  say "FAIL    service not reachable at $BASE"
  say "        start indexarr first; this script only reads from a running instance"
  exit 1
fi
if [[ "$PREFLIGHT" == "000" ]]; then
  say "FAIL    service not reachable at $BASE"
  exit 1
fi

say ""
say "==> routes"

# Pages that must render (200) or redirect (301/302/303) after the redesign.
check_html_route "/"
check_html_route "/releases"
check_html_route "/releases?query=test"
check_html_route "/sources"
check_html_route "/sources/new"
check_html_route "/definitions"
check_html_route "/definitions/new"
check_html_route "/definitions/example-html"
check_html_route "/indexers"
check_html_route "/indexers/new"
check_html_route "/indexers/new?type=public"
check_html_route "/indexers/new?type=public&cat=movies&state=verified"
check_html_route "/indexers/new?q=pirate"
check_html_route "/indexers/new?page=2"
check_html_route "/applications"
check_html_route "/applications/new"
check_html_route "/import"
check_html_route "/runs"
check_html_route "/logs"
check_html_route "/rules"
check_html_route "/settings"
check_html_route "/maintenance"

# New information architecture: still pending while the redesign is in progress.
check_activity_route "/activity?tab=runs"
check_activity_route "/activity?tab=logs"
# WP17 tabs: the feed and the requests received from apps.
check_html_route "/activity?tab=feed"
check_html_route "/activity?tab=searches"

say ""
say "==> API and static assets"
check_exact_route "/healthz" '"status":"ok"'
check_exact_route "/api?t=caps" '<caps'
check_exact_route "/newznab/api?t=caps" '<caps'
check_exact_route "/static/app.css"
check_exact_route "/static/app.js"

say ""
say "==> internal links"
check_links

say ""
say "==> navigation and layout"
check_navigation
if [[ "$HTML_PAGES" -gt 0 ]]; then
  if [[ "$LANG_MISSING" -gt 0 ]]; then
    warn "<html lang=\"…\"> missing on $LANG_MISSING/$HTML_PAGES HTML pages (e.g. $LANG_EXAMPLE)"
  fi
  if [[ "$SKIP_MISSING" -gt 0 ]]; then
    warn "skip link missing on $SKIP_MISSING/$HTML_PAGES HTML pages (e.g. $SKIP_EXAMPLE)"
  fi
else
  warn "no HTML page returned 200: layout checks skipped"
fi

say ""
say "==> summary"
printf 'routes:   %d checked: %d ok, %d failed, %d pending\n' \
  "$ROUTES" "$ROUTE_OK" "$ROUTE_FAIL" "$ROUTE_PENDING"
printf 'links:    %d checked: %d ok, %d failed\n' \
  "$LINKS" "$LINK_OK" "$LINK_FAIL"
printf 'nav failures: %d\n' "$NAV_FAIL"
printf 'warnings: %d\n' "$WARNS"
printf 'hard failures: %d\n' "$HARD_FAILS"

if [[ "$HARD_FAILS" -gt 0 ]]; then
  say "UI smoke: FAIL"
  exit 1
fi
if [[ "$STRICT" == "1" && "$WARNS" -gt 0 ]]; then
  say "UI smoke: FAIL (strict mode turns $WARNS warning(s) into failures)"
  exit 1
fi
say "UI smoke: OK"
