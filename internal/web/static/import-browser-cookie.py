#!/usr/bin/env python3
"""Send the session cookies of a local Firefox profile to indexarr.

indexarr cannot read another site's cookies from a web page: they are
HttpOnly and belong to a different origin. This small helper runs on the
machine that has the browser, reads the Firefox cookie database of the
current user, and posts the Cookie header to indexarr together with the
browser User-Agent. It prints neither the cookie nor the token.

It uses only the Python standard library.
"""

import argparse
import configparser
import glob
import json
import os
import platform
import re
import shutil
import sqlite3
import subprocess
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request

DOMAIN_RE = re.compile(r"^[A-Za-z0-9.-]+$")


def firefox_roots():
    home = os.path.expanduser("~")
    xdg = os.environ.get("XDG_CONFIG_HOME") or os.path.join(home, ".config")
    roots = [
        os.path.join(home, ".mozilla", "firefox"),
        os.path.join(xdg, "mozilla", "firefox"),
    ]
    seen = set()
    out = []
    for root in roots:
        if root not in seen and os.path.isdir(root):
            seen.add(root)
            out.append(root)
    return out


def profile_paths(root):
    ini = os.path.join(root, "profiles.ini")
    paths = []
    if os.path.isfile(ini):
        parser = configparser.ConfigParser()
        try:
            parser.read(ini)
        except configparser.Error:
            parser = None
        if parser is not None:
            for section in parser.sections():
                if not section.startswith("Profile"):
                    continue
                path = parser.get(section, "Path", fallback="").strip()
                if not path:
                    continue
                relative = parser.get(section, "IsRelative", fallback="1").strip() != "0"
                paths.append(os.path.join(root, path) if relative else path)
    if not paths:
        paths = [os.path.dirname(item) for item in glob.glob(os.path.join(root, "*", "cookies.sqlite"))]
    return paths


def copy_cookie_db(profile):
    source = os.path.join(profile, "cookies.sqlite")
    if not os.path.isfile(source):
        return None
    temp = tempfile.mkdtemp(prefix="indexarr-cookie-")
    for suffix in ("", "-wal", "-shm"):
        candidate = source + suffix
        if os.path.isfile(candidate):
            shutil.copy2(candidate, os.path.join(temp, "cookies.sqlite" + suffix))
    return temp


def cookies_for(profile, domain):
    temp = copy_cookie_db(profile)
    if temp is None:
        return []
    try:
        db = sqlite3.connect(os.path.join(temp, "cookies.sqlite"))
        try:
            rows = db.execute(
                "SELECT host, path, name, value FROM moz_cookies "
                "WHERE host = ? OR host = ? OR host LIKE ? "
                "ORDER BY host, path, name",
                (domain, "." + domain, "%." + domain),
            ).fetchall()
        finally:
            db.close()
    finally:
        shutil.rmtree(temp, ignore_errors=True)
    pairs = []
    for _host, _path, name, value in rows:
        if not name or value is None:
            continue
        value = str(value).replace("\r", " ").replace("\n", " ")
        pairs.append(f"{name}={value}")
    # The browser sends the first matching cookie for a name; keep the first
    # occurrence and drop exact duplicates.
    seen = set()
    unique = []
    for pair in pairs:
        if pair in seen:
            continue
        seen.add(pair)
        unique.append(pair)
    return unique


def firefox_version(profile):
    # The running browser is the source of truth: compatibility.ini keeps the
    # version of the last compatibility check and can be one release behind
    # after an upgrade, while the site ties the session to the real header.
    for binary in ("firefox", "firefox-bin"):
        try:
            output = subprocess.check_output(
                [binary, "--version"], stderr=subprocess.DEVNULL, timeout=10
            ).decode("utf-8", "replace")
        except (OSError, subprocess.SubprocessError):
            continue
        match = re.search(r"Firefox\s+(\d+(?:\.\d+)*)", output)
        if match:
            return match.group(1)
    for name in ("application.ini", "compatibility.ini"):
        path = os.path.join(profile, name)
        if not os.path.isfile(path):
            continue
        parser = configparser.ConfigParser()
        try:
            parser.read(path)
        except configparser.Error:
            continue
        for section in parser.sections():
            for key in ("Version", "LastVersion"):
                raw = parser.get(section, key, fallback="").strip()
                if raw:
                    match = re.match(r"(\d+(?:\.\d+)*)", raw)
                    if match:
                        return match.group(1)
    return ""


def user_agent(version):
    if not version:
        return ""
    system = platform.system()
    machine = platform.machine().lower()
    if system == "Linux":
        if machine in ("x86_64", "amd64"):
            platform_token = "X11; Linux x86_64"
        elif machine in ("aarch64", "arm64"):
            platform_token = "X11; Linux aarch64"
        else:
            platform_token = "X11; Linux " + (machine or "x86_64")
    elif system == "Windows":
        platform_token = "Windows NT 10.0; Win64; x64"
    elif system == "Darwin":
        platform_token = "Macintosh; Intel Mac OS X 10.15"
    else:
        platform_token = "X11; Linux x86_64"
    return f"Mozilla/5.0 ({platform_token}; rv:{version}) Gecko/20100101 Firefox/{version}"


def find_profile(domain, requested):
    if requested:
        requested = os.path.expanduser(requested)
        candidates = [requested]
    else:
        candidates = []
        for root in firefox_roots():
            candidates.extend(profile_paths(root))
    best = None
    best_count = 0
    for profile in candidates:
        if not os.path.isfile(os.path.join(profile, "cookies.sqlite")):
            continue
        cookies = cookies_for(profile, domain)
        if len(cookies) > best_count:
            best, best_count = profile, len(cookies)
    return best, best_count


def main():
    parser = argparse.ArgumentParser(
        description="Read the Firefox cookies of a domain and send them to indexarr."
    )
    parser.add_argument("--indexarr", required=True, help="indexarr base URL, for example http://192.168.1.161:8788")
    parser.add_argument("--token", required=True, help="one-time pairing token shown by indexarr")
    parser.add_argument("--domain", default="iptorrents.com", help="cookie domain (default: iptorrents.com)")
    parser.add_argument("--profile", default="", help="Firefox profile directory (auto-detected by default)")
    parser.add_argument("--user-agent", default="", help="override the browser User-Agent")
    parser.add_argument("--dry-run", action="store_true", help="show what would be sent, without sending it")
    args = parser.parse_args()

    if not DOMAIN_RE.fullmatch(args.domain):
        print("invalid --domain", file=sys.stderr)
        return 2
    parsed = urllib.parse.urlparse(args.indexarr)
    if parsed.scheme not in ("http", "https") or not parsed.netloc:
        print("--indexarr must be an http:// or https:// address", file=sys.stderr)
        return 2

    profile, count = find_profile(args.domain, args.profile)
    if profile is None or count == 0:
        print(
            f"no {args.domain} cookies found in the Firefox profiles of this user. "
            f"Log in to {args.domain} with Firefox and run the command again.",
            file=sys.stderr,
        )
        return 1

    cookies = cookies_for(profile, args.domain)
    agent = args.user_agent.strip() or user_agent(firefox_version(profile))
    if not agent:
        print(
            "cannot determine the Firefox User-Agent; pass it with --user-agent",
            file=sys.stderr,
        )
        return 1

    # Only lengths are printed: the cookie is a secret and must never reach the
    # terminal, a log or the shell history.
    print(f"profile: {profile}")
    print(f"cookies: {len(cookies)} ({sum(len(c) for c in cookies)} bytes)")
    print(f"user agent: {agent}")
    if args.dry_run:
        print("dry run: nothing sent")
        return 0

    endpoint = args.indexarr.rstrip("/") + "/pair"
    body = json.dumps({"cookie": "; ".join(cookies), "user_agent": agent}).encode("utf-8")
    request = urllib.request.Request(
        endpoint,
        data=body,
        method="POST",
        headers={
            "Content-Type": "application/json",
            "X-Indexarr-Pairing": args.token,
            "User-Agent": "indexarr-cookie-import/1",
        },
    )
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            payload = response.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as exc:
        detail = exc.read().decode("utf-8", "replace")[:300]
        print(f"indexarr refused the import (HTTP {exc.code}): {detail}", file=sys.stderr)
        return 1
    except urllib.error.URLError as exc:
        print(f"cannot reach indexarr at {args.indexarr}: {exc.reason}", file=sys.stderr)
        return 1
    try:
        result = json.loads(payload)
    except ValueError:
        print(f"unexpected answer from indexarr: {payload[:300]}", file=sys.stderr)
        return 1
    if not result.get("ok"):
        print(f"indexarr refused the import: {result}", file=sys.stderr)
        return 1
    print("cookie sent to indexarr")
    return 0


if __name__ == "__main__":
    sys.exit(main())
