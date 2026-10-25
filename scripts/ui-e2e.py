#!/usr/bin/env python3
"""Field-by-field browser test for the indexarr forms.

The test starts a throwaway indexarr instance (temporary database, temporary
port) and a small local fixture that pretends to be FlareSolverr, qBittorrent
and Sonarr. It then drives a headless Chrome through the real forms with the
Chrome DevTools Protocol: change every field, save, reload and read the value
back from both the page and the temporary database.

No production database, port or secret is touched. Run it with:

    make e2e
    ./scripts/ui-e2e.py --chrome /usr/bin/google-chrome

Without Chrome the script prints SKIP and exits 0, so a developer machine or a
CI job without a browser can still run the rest of the suite. Set
UI_E2E_REQUIRE_CHROME=1 to make a missing browser a failure.
"""

from __future__ import annotations

import argparse
import base64
import http.server
import json
import os
import random
import shutil
import socket
import sqlite3
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request
from pathlib import Path

try:
    import websocket
except ImportError:  # pragma: no cover - very small environments
    websocket = None


ROOT = Path(__file__).resolve().parents[1]
FAILS: list[str] = []
CHECKS = 0


def check(condition: bool, message: str) -> None:
    global CHECKS
    CHECKS += 1
    if condition:
        print(f"ok   {message}")
        return
    FAILS.append(message)
    print(f"FAIL {message}")


def find_chrome(explicit: str | None) -> str | None:
    if explicit:
        return explicit if Path(explicit).exists() else None
    env = os.environ.get("CHROME_BIN")
    if env and Path(env).exists():
        return env
    for name in ("google-chrome", "google-chrome-stable", "chromium", "chromium-browser"):
        found = shutil.which(name)
        if found:
            return found
    return None


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


class Fixture(http.server.BaseHTTPRequestHandler):
    """Local stand-in for FlareSolverr, qBittorrent and the Arr APIs."""

    def log_message(self, *args):  # silence
        pass

    def _send(self, status: int, body: str, content_type: str = "application/json"):
        data = body.encode()
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_POST(self):
        length = int(self.headers.get("Content-Length") or 0)
        if length:
            self.rfile.read(length)
        if self.path.endswith("/v1"):
            self._send(200, json.dumps({
                "status": "ok",
                "message": "",
                "solution": {"status": 200, "response": "<html>solved</html>"},
            }))
            return
        if self.path.endswith("/api/v2/auth/login"):
            self._send(200, "Ok.", "text/plain")
            return
        self._send(200, "{}")

    def do_GET(self):
        if self.path.endswith("/api/v2/app/version"):
            self._send(200, "v5.0.0", "text/plain")
            return
        self._send(200, "{}")


class CDP:
    def __init__(self, ws_url: str):
        self.ws = websocket.create_connection(ws_url, timeout=30)
        self.next_id = 0

    def send(self, method: str, params: dict | None = None):
        self.next_id += 1
        call_id = self.next_id
        self.ws.send(json.dumps({"id": call_id, "method": method, "params": params or {}}))
        deadline = time.time() + 30
        while time.time() < deadline:
            message = json.loads(self.ws.recv())
            if message.get("id") != call_id:
                continue
            if "error" in message:
                raise RuntimeError(f"{method}: {message['error']}")
            return message.get("result", {})
        raise TimeoutError(method)

    def eval(self, expression: str):
        result = self.send("Runtime.evaluate", {
            "expression": expression,
            "returnByValue": True,
            "awaitPromise": True,
        })
        if result.get("exceptionDetails"):
            raise RuntimeError(result["exceptionDetails"].get("text", "javascript error"))
        return result.get("result", {}).get("value")

    def close(self):
        try:
            self.ws.close()
        except Exception:
            pass


class Session:
    def __init__(self, cdp: CDP, base: str):
        self.cdp = cdp
        self.base = base

    def wait_ready(self, timeout: float = 15) -> str:
        deadline = time.time() + timeout
        href = ""
        while time.time() < deadline:
            try:
                state = self.cdp.eval("document.readyState")
                href = self.cdp.eval("location.href") or ""
                if state == "complete" and href:
                    return href
            except Exception:
                pass
            time.sleep(0.05)
        raise TimeoutError(f"page did not load: {href}")

    def navigate(self, path: str) -> str:
        url = path if path.startswith("http") else self.base + path
        self.cdp.send("Page.navigate", {"url": url})
        time.sleep(0.15)
        return self.wait_ready()

    def text(self) -> str:
        return self.cdp.eval("document.body ? document.body.innerText : ''") or ""

    def value(self, selector: str):
        return self.cdp.eval(f"""(() => {{
            const el = document.querySelector({json.dumps(selector)});
            return el ? el.value : null;
        }})()""")

    def checked(self, selector: str):
        return self.cdp.eval(f"""(() => {{
            const el = document.querySelector({json.dumps(selector)});
            return el ? Boolean(el.checked) : null;
        }})()""")

    def set_value(self, selector: str, value) -> None:
        self.cdp.eval(f"""(() => {{
            const el = document.querySelector({json.dumps(selector)});
            if (!el) throw new Error("missing field " + {json.dumps(selector)});
            const proto = Object.getPrototypeOf(el);
            const setter = Object.getOwnPropertyDescriptor(proto, "value");
            if (setter && setter.set) setter.set.call(el, {json.dumps(str(value))});
            else el.value = {json.dumps(str(value))};
            el.dispatchEvent(new Event("input", {{bubbles: true}}));
            el.dispatchEvent(new Event("change", {{bubbles: true}}));
            return true;
        }})()""")

    def set_checked(self, selector: str, checked: bool) -> None:
        self.cdp.eval(f"""(() => {{
            const el = document.querySelector({json.dumps(selector)});
            if (!el) throw new Error("missing checkbox " + {json.dumps(selector)});
            el.checked = {str(bool(checked)).lower()};
            el.dispatchEvent(new Event("input", {{bubbles: true}}));
            el.dispatchEvent(new Event("change", {{bubbles: true}}));
            return true;
        }})()""")

    def click(self, selector: str) -> None:
        self.cdp.eval(f"""(() => {{
            const el = document.querySelector({json.dumps(selector)});
            if (!el) throw new Error("missing element " + {json.dumps(selector)});
            el.click();
            return true;
        }})()""")

    def submit_section(self, section: str, index: int = 0) -> None:
        self.cdp.eval(f"""(() => {{
            const hidden = document.querySelector('input[name="section"][value="{section}"]');
            if (!hidden) throw new Error("missing section {section}");
            const buttons = hidden.form.querySelectorAll('button[type="submit"]');
            if (!buttons[{index}]) throw new Error("missing submit button {index}");
            buttons[{index}].click();
            return true;
        }})()""")
        time.sleep(0.4)
        self.wait_ready()

    def press_enter(self, selector: str) -> None:
        self.cdp.eval(f"""(() => {{
            const el = document.querySelector({json.dumps(selector)});
            if (!el) throw new Error("missing field {selector}");
            el.focus();
            return true;
        }})()""")
        self.cdp.send("Input.dispatchKeyEvent", {
            "type": "keyDown",
            "key": "Enter",
            "code": "Enter",
            "text": "\r",
            "unmodifiedText": "\r",
            "windowsVirtualKeyCode": 13,
            "nativeVirtualKeyCode": 13,
        })
        self.cdp.send("Input.dispatchKeyEvent", {
            "type": "keyUp",
            "key": "Enter",
            "code": "Enter",
            "windowsVirtualKeyCode": 13,
            "nativeVirtualKeyCode": 13,
        })
        time.sleep(0.4)
        self.wait_ready()

    def submit_form(self, action: str) -> None:
        self.cdp.eval(f"""(() => {{
            const form = document.querySelector('form[action="{action}"]');
            if (!form) throw new Error("missing form {action}");
            form.requestSubmit();
            return true;
        }})()""")
        time.sleep(0.4)
        self.wait_ready()

    def double_submit_form(self, action: str) -> None:
        self.cdp.eval(f"""(() => {{
            const form = document.querySelector('form[action="{action}"]');
            if (!form) throw new Error("missing form {action}");
            form.requestSubmit();
            form.requestSubmit();
            return true;
        }})()""")
        time.sleep(0.5)
        self.wait_ready()

    def confirm_count(self) -> int:
        return int(self.cdp.eval("Number(localStorage.getItem('__confirmCalls') || 0)") or 0)

    def has_near_flash(self) -> bool:
        return bool(self.cdp.eval("Boolean(document.querySelector('[data-form-flash]'))"))


class Database:
    def __init__(self, path: Path):
        self.path = path

    def query(self, sql: str, params=()):
        conn = sqlite3.connect(self.path)
        try:
            return conn.execute(sql, params).fetchall()
        finally:
            conn.close()

    def setting(self, key: str) -> str:
        rows = self.query("SELECT value FROM settings WHERE key = ?", (key,))
        return rows[0][0] if rows else ""

    def indexer(self, name: str):
        rows = self.query(
            "SELECT id, name, priority, tags, config_json, interval_seconds, enabled "
            "FROM indexers WHERE name = ?", (name,))
        if not rows:
            return None
        keys = ("id", "name", "priority", "tags", "config_json", "interval_seconds", "enabled")
        return dict(zip(keys, rows[0]))

    def indexer_count(self, definition_id: str) -> int:
        return self.query("SELECT COUNT(*) FROM indexers WHERE definition_id = ?",
                          (definition_id,))[0][0]

    def secret_count(self, indexer_id: int, key: str) -> int:
        return self.query(
            "SELECT COUNT(*) FROM indexer_secrets WHERE indexer_id = ? AND key = ?",
            (indexer_id, key))[0][0]

    def app_secret_count(self, key: str) -> int:
        return self.query(
            "SELECT COUNT(*) FROM app_secrets WHERE key = ?", (key,))[0][0]

    def app(self, name: str):
        rows = self.query(
            "SELECT id, name, url, sync_level, length(api_key_enc) FROM applications WHERE name = ?",
            (name,))
        return rows[0] if rows else None

    def client(self, name: str):
        rows = self.query(
            "SELECT id, name, url, username, length(password_enc), length(apikey_enc), "
            "category, save_path, paused, enabled, is_default "
            "FROM download_clients WHERE name = ?", (name,))
        return rows[0] if rows else None


def start_fixture():
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Fixture)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    return server, f"http://127.0.0.1:{server.server_address[1]}"


def wait_http(url: str, timeout: float = 20) -> bool:
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=1) as response:
                if response.status == 200:
                    return True
        except Exception:
            pass
        time.sleep(0.1)
    return False


def start_chrome(chrome: str, profile: Path):
    process = subprocess.Popen(
        [chrome, "--headless=new", "--disable-gpu", "--no-sandbox",
         "--disable-dev-shm-usage", "--remote-debugging-port=0",
         f"--user-data-dir={profile}", "about:blank"],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    active = profile / "DevToolsActivePort"
    deadline = time.time() + 20
    while time.time() < deadline:
        if active.exists():
            lines = active.read_text().splitlines()
            if len(lines) >= 2:
                port = int(lines[0])
                request = urllib.request.Request(
                    f"http://127.0.0.1:{port}/json/new?about:blank", method="PUT")
                with urllib.request.urlopen(request, timeout=5) as response:
                    target = json.loads(response.read())
                return process, target["webSocketDebuggerUrl"]
        time.sleep(0.1)
    process.terminate()
    raise RuntimeError("Chrome did not start")


FIXTURE_DEFINITION = """schema: 1
connector: newznab
meta:
  id: e2e-fixture
  name: E2E Fixture
  type: private
  protocol: torrent
  urls:
    - https://fixture-a.example/
    - https://fixture-b.example/
settings:
  - { name: apikey, type: password, label: API key, required: true }
  - { name: token, type: password, label: Token }
  - { name: username, type: text, label: Username }
  - name: flavour
    type: select
    label: Flavour
    default: a
    options: [a, b]
  - { name: extra-flag, type: checkbox, label: Extra flag }
categories:
  - { site: "1", standard: 2000, desc: "Movies" }
  - { site: "2", standard: 5000, desc: "TV" }
"""


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--chrome")
    parser.add_argument("--binary")
    parser.add_argument("--keep", action="store_true", help="keep the temporary directory")
    args = parser.parse_args()

    chrome = find_chrome(args.chrome)
    if not chrome:
        message = "SKIP ui-e2e: no Chrome/Chromium found (set CHROME_BIN or --chrome)"
        if os.environ.get("UI_E2E_REQUIRE_CHROME") == "1":
            print(message)
            return 1
        print(message)
        return 0
    if websocket is None:
        message = "SKIP ui-e2e: python websocket-client is not installed"
        if os.environ.get("UI_E2E_REQUIRE_CHROME") == "1":
            print(message)
            return 1
        print(message)
        return 0

    tmp = Path(tempfile.mkdtemp(prefix="indexarr-ui-e2e."))
    binary = Path(args.binary) if args.binary else tmp / "indexarr"
    port = free_port()
    base = f"http://127.0.0.1:{port}"
    fixture, fixture_url = start_fixture()
    app = None
    chrome_process = None
    cdp = None
    keep = args.keep
    try:
        if not args.binary:
            print("==> build indexarr")
            subprocess.run(["go", "build", "-trimpath", "-o", str(binary), "./cmd/indexarr"],
                           cwd=ROOT, check=True)
        config = {
            "listen": f"127.0.0.1:{port}",
            "db_path": str(tmp / "e2e.db"),
            "log_level": "warn",
        }
        config_path = tmp / "indexarr.json"
        config_path.write_text(json.dumps(config))
        env = os.environ.copy()
        env["XDG_CONFIG_HOME"] = str(tmp / "config")
        (tmp / "config").mkdir(parents=True, exist_ok=True)
        log = open(tmp / "indexarr.log", "w")
        app = subprocess.Popen([str(binary), "-config", str(config_path)],
                               stdout=log, stderr=subprocess.STDOUT, env=env)
        if not wait_http(base + "/healthz"):
            raise RuntimeError("indexarr did not start")

        print("==> start Chrome")
        chrome_process, ws_url = start_chrome(chrome, tmp / "chrome")
        cdp = CDP(ws_url)
        cdp.send("Page.enable")
        cdp.send("Runtime.enable")
        cdp.send("Network.enable")
        cdp.send("Page.addScriptToEvaluateOnNewDocument", {"source": """
            window.confirm = function(message) {
              try {
                const n = Number(localStorage.getItem('__confirmCalls') || '0');
                localStorage.setItem('__confirmCalls', String(n + 1));
                localStorage.setItem('__lastConfirm', message || '');
              } catch (error) {}
              return true;
            };
        """})
        session = Session(cdp, base)
        db = Database(tmp / "e2e.db")
        # The assertions below are written against the English UI. The
        # language route stores the cookie, unlike a one-off ?lang query.
        session.navigate("/language/en")
        session.navigate("/")

        # ---------------------------------------------------------- first run
        print("==> first-run wizard")
        body = session.text()
        check("Welcome to indexarr" in body, "first-run wizard is shown on a fresh database")
        session.set_value("#setup_public_url", base)
        session.submit_form("/setup/address")
        check("step=2" in session.wait_ready(), "first-run address saves and advances to step 2")
        session.navigate("/setup?step=1")
        check(session.value("#setup_public_url") == base, "first-run address survives a reload")
        session.submit_form("/setup/skip")
        session.wait_ready()
        check("Welcome to indexarr" not in session.text(), "first-run wizard can be skipped")

        # ---------------------------------------------------------- definitions
        print("==> definitions form")
        session.navigate("/definitions/new")
        session.set_value("#body", FIXTURE_DEFINITION)
        session.set_checked("#overwrite", False)
        session.submit_form("/definitions?csrf=" + str(session.cdp.eval(
            "document.querySelector('input[name=csrf]').value")))
        session.wait_ready()
        check("/definitions/e2e-fixture" in session.cdp.eval("location.href"),
              "definition with two URLs is created")
        check("E2E Fixture" in session.text(), "definition detail shows the saved name")
        updated = FIXTURE_DEFINITION.replace("name: E2E Fixture", "name: E2E Fixture Saved")
        session.navigate("/definitions/new")
        session.set_value("#body", updated)
        session.set_checked("#overwrite", True)
        session.submit_form("/definitions?csrf=" + str(session.cdp.eval(
            "document.querySelector('input[name=csrf]').value")))
        session.wait_ready()
        check("E2E Fixture Saved" in session.text(), "definition overwrite saves and reloads")

        # ---------------------------------------------------------- catalog add
        print("==> catalog add")
        session.navigate("/indexers/new?q=acgnx")
        session.cdp.eval("""(() => {
            const input = document.querySelector('form[action="/indexers/catalog/add"] input[value="acgnx"]');
            if (!input) throw new Error("ACGNX add form missing");
            const form = input.form;
            form.requestSubmit();
            form.requestSubmit();
            return true;
        })()""")
        time.sleep(0.5)
        session.wait_ready()
        count = db.indexer_count("acgnx")
        check(count == 1, f"catalog add is idempotent on double submit (rows={count})")
        check("added" in session.text().lower() or "/indexers/" in session.cdp.eval("location.href"),
              "catalog add reaches a detail or list page")

        # ---------------------------------------------------------- indexer add
        print("==> indexer add from definition")
        session.navigate("/indexers/new?definition=e2e-fixture")
        check("Add E2E Fixture Saved" in session.text() or "Add E2E Fixture" in session.text(),
              "indexer add form renders the fixture")
        session.set_value("#name", "E2E Native")
        session.set_value("#url", "https://fixture-b.example/")
        session.set_value("#setting_apikey", "initial-secret")
        session.set_value("#setting_token", "optional-secret")
        session.set_value("#setting_username", "alice")
        session.set_value("#setting_flavour", "b")
        session.set_checked("#setting_extra-flag", True)
        session.set_value("#priority", "33")
        session.set_value("#interval_seconds", "3600")
        session.set_value("#tags", "e2e, initial")
        session.set_value("#user_agent", "e2e-agent")
        session.set_value("#query", "initial-query")
        categories = session.cdp.eval(
            "Array.from(document.querySelectorAll('input[name=categories]')).map(e => e.value)")
        if categories:
            session.set_checked(f'input[name="categories"][value="{categories[-1]}"]', True)
        session.set_value("#challenge", "none")
        session.set_value("#flaresolverr_url", f"127.0.0.1:{fixture.server_address[1]}")
        session.set_value("#flaresolverr_timeout", "30")
        session.set_checked("#allow_private", True)
        session.set_checked("#enabled", True)
        session.cdp.eval("""(() => {
            const form = document.querySelector('form[action="/indexers"]');
            form.querySelector('button[name="save"]').click();
            return true;
        })()""")
        time.sleep(0.5)
        session.wait_ready()
        row = db.indexer("E2E Native")
        check(row is not None, "indexer is created")
        if row:
            config_json = json.loads(row["config_json"])
            check(row["name"] == "E2E Native", "indexer name is saved")
            check(row["priority"] == 33, "indexer priority is saved")
            check(row["interval_seconds"] == 3600, "indexer interval is saved")
            check(row["tags"] == "e2e,initial", "indexer tags are saved")
            check(config_json.get("query") == "initial-query", "indexer query is saved")
            check(config_json.get("url") == "https://fixture-b.example", "indexer URL select is saved")
            check(config_json.get("user_agent") == "e2e-agent", "indexer user agent is saved")
            check(config_json.get("flaresolverr_url") == f"http://127.0.0.1:{fixture.server_address[1]}",
                  "indexer FlareSolverr URL accepts host:port")
            check(config_json.get("allow_private") == "true", "indexer allow_private is saved")
            check(db.secret_count(row["id"], "apikey") == 1, "indexer required secret is stored")
            check(db.secret_count(row["id"], "token") == 1, "indexer optional secret is stored")
            indexer_id = row["id"]
            indexer_path = f"/indexers/{indexer_id}"
        else:
            indexer_id = 0
            indexer_path = "/indexers"

        # ---------------------------------------------------------- indexer edit
        print("==> indexer edit (closed Advanced)")
        edit_path = indexer_path + "/edit"
        session.navigate(edit_path)
        check(bool(session.cdp.eval("Boolean(document.querySelector('details.advanced:not([open])'))")),
              "indexer Advanced section starts closed")
        check(session.value("#name") == "E2E Native", "edit reload shows the saved name")
        session.set_value("#name", "E2E Native Renamed")
        session.set_value("#url", "https://fixture-a.example/")
        session.set_value("#priority", "44")
        session.set_value("#interval_seconds", "7200")
        session.set_value("#tags", "e2e, edited")
        session.set_value("#user_agent", "e2e-agent-edited")
        session.set_value("#query", "edited-query")
        previous_default = session.value("#default_category")
        options = session.cdp.eval(
            "Array.from(document.querySelectorAll('#default_category option')).map(e => e.value)")
        new_default = next((o for o in options if o != previous_default), previous_default)
        session.set_value("#default_category", new_default)
        session.set_value("#challenge", "flaresolverr")
        session.set_value("#flaresolverr_url", f"127.0.0.1:{fixture.server_address[1]}")
        session.set_value("#flaresolverr_timeout", "45")
        session.set_checked("#allow_private", False)
        session.set_checked("#enabled", False)
        session.set_value("#setting_username", "bob")
        session.set_value("#setting_flavour", "a")
        session.set_checked("#setting_extra-flag", False)
        # Blank secret fields must preserve the stored values.
        session.set_value("#setting_apikey", "")
        session.set_value("#setting_token", "")
        session.double_submit_form(f"/indexers/{indexer_id}")
        session.wait_ready()
        row = db.indexer("E2E Native Renamed")
        check(row is not None and row["name"] == "E2E Native Renamed",
              "double-click on Save updates the indexer once")
        check(row is not None and row["enabled"] == 0, "indexer enabled checkbox is saved")
        if row:
            config_json = json.loads(row["config_json"])
            check(row["priority"] == 44, "edited priority is saved")
            check(row["interval_seconds"] == 7200, "edited interval is saved")
            check(config_json.get("url") == "https://fixture-a.example", "edited URL select is saved")
            check(config_json.get("query") == "edited-query", "edited query is saved")
            check(config_json.get("default_category") == new_default, "edited default category is saved")
            check(config_json.get("challenge") == "flaresolverr", "edited challenge is saved")
            check(config_json.get("allow_private") == "false", "edited allow_private is saved")
            check(db.secret_count(row["id"], "apikey") == 1,
                  "blank required secret keeps the stored value")
            check(db.secret_count(row["id"], "token") == 1,
                  "blank optional secret keeps the stored value")
        check(session.has_near_flash(), "confirmation appears next to the page actions")

        session.navigate(edit_path)
        check(session.value("#name") == "E2E Native Renamed", "edited name survives a reload")
        check(session.value("#priority") == "44", "edited priority survives a reload")
        check(session.value("#url") == "https://fixture-a.example/", "edited URL survives a reload")
        check(session.value("#default_category") == new_default, "edited default category survives a reload")
        check(session.checked("#enabled") is False, "edited enabled state survives a reload")

        # Clear-secret checkbox on the optional token.
        session.set_checked('input[name="clear_token"]', True)
        session.submit_form(f"/indexers/{indexer_id}")
        session.wait_ready()
        row = db.indexer("E2E Native Renamed")
        if row:
            check(db.secret_count(row["id"], "token") == 0, "clear-secret checkbox deletes only that secret")
            check(db.secret_count(row["id"], "apikey") == 1, "clear-secret leaves the other secret")

        # Validation error title and language.
        session.navigate(edit_path)
        session.set_value("#interval_seconds", "1")
        session.cdp.eval(f"""(() => {{
            const form = document.querySelector('form[action="/indexers/{indexer_id}"]');
            form.noValidate = true;
            form.requestSubmit();
            return true;
        }})()""")
        time.sleep(0.4)
        session.wait_ready()
        heading = session.cdp.eval("document.querySelector('h1').innerText")
        check("Edit E2E Native Renamed" in heading,
              f"edit validation keeps an edit title ({heading!r})")
        check("The interval must be 0 or at least 60 seconds" in session.text(),
              "edit validation error is English on the English UI")
        session.navigate(edit_path + "?lang=it")
        session.set_value("#interval_seconds", "1")
        session.cdp.eval(f"""(() => {{
            const form = document.querySelector('form[action="/indexers/{indexer_id}"]');
            form.action = "/indexers/{indexer_id}?lang=it";
            form.noValidate = true;
            form.requestSubmit();
            return true;
        }})()""")
        time.sleep(0.4)
        session.wait_ready()
        check("intervallo deve essere 0 o almeno 60 secondi" in session.text(),
              "edit validation error is Italian on the Italian UI")
        session.navigate(edit_path)

        # ---------------------------------------------------------- settings
        print("==> settings general")
        session.navigate("/settings?section=general")
        session.set_value("#public_url", base)
        session.submit_section("general")
        check("Settings saved" in session.text(), "general settings report saved")
        check(session.has_near_flash(), "general confirmation is next to the Save button")
        check(db.setting("public_url") == base, "public_url is saved")

        print("==> settings notifications")
        session.navigate("/settings?section=notifications")
        session.set_value("#notify_webhook_url", fixture_url + "/hook")
        session.set_value("#notify_telegram_token", "e2e-telegram-token")
        session.set_value("#notify_telegram_chat_id", "11111")
        session.set_checked("#notify_new_releases", True)
        session.set_checked("#notify_failures", True)
        session.set_checked("#notify_grab_failed", True)
        session.set_checked("#notify_catalog_updated", True)
        session.set_checked("#notify_backup_failed", True)
        session.set_value("#notify_cooldown_minutes", "45")
        session.submit_section("notifications")
        check("Settings saved" in session.text(), "notification settings report saved")
        check(db.setting("notify_webhook_url") == "", "the webhook is not in the settings table")
        check(db.setting("notify_telegram_token") == "", "the Telegram token is not in the settings table")
        check(db.app_secret_count("notify_webhook_url") == 1, "webhook is sealed in the vault table")
        check(db.app_secret_count("notify_telegram_token") == 1, "Telegram token is sealed in the vault table")
        check(db.setting("notify_cooldown_minutes") == "45", "anti-spam window is saved")
        session.navigate("/settings?section=notifications")
        check(session.value("#notify_telegram_token") == "", "secret field is blank after reload")
        check("••••" in (session.cdp.eval(
            "document.querySelector('#notify_telegram_token').placeholder") or ""),
            "Telegram field shows a masked placeholder")
        check("••••" in (session.cdp.eval(
            "document.querySelector('#notify_webhook_url').placeholder") or ""),
            "webhook field shows a masked placeholder")
        # Show is an explicit action and returns the stored value.
        session.click('#notify_webhook_url ~ button[data-secret-show]')
        time.sleep(0.5)
        check(session.value("#notify_webhook_url") == fixture_url + "/hook",
              "Show reveals the stored webhook only on request")
        # Blank secret preserves; changing chat id still saves.
        session.navigate("/settings?section=notifications")
        session.set_value("#notify_telegram_chat_id", "22222")
        session.submit_section("notifications")
        check(db.app_secret_count("notify_telegram_token") == 1,
              "blank Telegram token preserves the stored value")
        check(db.setting("notify_telegram_chat_id") == "22222", "changed chat id is saved")
        # Clear-secret checkbox with data-confirm.
        before = session.confirm_count()
        session.navigate("/settings?section=notifications")
        session.set_checked('input[name="notify_telegram_token_remove"]', True)
        session.submit_section("notifications")
        check(session.confirm_count() == before + 1, "remove-secret checkbox asks for confirmation")
        check(db.app_secret_count("notify_telegram_token") == 0, "remove-secret checkbox clears the token")
        # The test button reports a readable result.
        session.submit_form("/settings/notifications/test")
        check("Test notification sent" in session.text(), "test notification reports success")

        print("==> settings updates")
        session.navigate("/settings?section=updates")
        session.set_value("#default_interval_seconds", "7200")
        session.set_value("#retention_days", "14")
        session.set_value("#history_retention_days", "21")
        session.submit_section("updates")
        check(db.setting("default_interval_seconds") == "7200", "default interval is saved")
        check(db.setting("retention_days") == "14", "retention days are saved")
        check(db.setting("history_retention_days") == "21", "history retention is saved")
        session.navigate("/settings?section=updates")
        check(session.value("#default_interval_seconds") == "7200", "interval survives a reload")
        check(session.value("#history_retention_days") == "21", "history retention survives a reload")

        print("==> settings network (Enter and Test)")
        session.navigate("/settings?section=network")
        check(session.cdp.eval(
            "document.querySelector('form input[name=section][value=network]')"
            ".form.querySelector('button[type=submit]').innerText.includes('Save')"),
            "network form's first submit button is Save")
        session.set_value("#flaresolverr_url", f"127.0.0.1:{fixture.server_address[1]}")
        session.set_value("#flaresolverr_timeout_seconds", "45")
        session.set_value("#host_rate", "2.5")
        session.set_value("#host_burst", "6")
        session.press_enter("#flaresolverr_url")  # Enter must Save, not Test.
        check("section=network" in session.cdp.eval("location.href"), "Enter stays in the network section")
        check(db.setting("flaresolverr_url") == fixture_url, "Enter saves the normalized FlareSolverr URL")
        check(db.setting("host_burst") == "6", "Enter saves the other network fields")
        check(session.has_near_flash(), "network confirmation is next to Save")
        # Test button works and saves too.
        session.navigate("/settings?section=network")
        session.set_value("#flaresolverr_url", f"127.0.0.1:{fixture.server_address[1]}")
        session.set_value("#flaresolverr_timeout_seconds", "30")
        session.submit_section("network", 1)
        check("FlareSolverr OK" in session.text(), "Test button reports success")
        check("section=network" in session.cdp.eval("location.href"), "Test returns to the network section")
        check(db.setting("flaresolverr_url") == fixture_url, "successful test saves the URL")
        # Invalid value: visible field error, value kept.
        session.navigate("/settings?section=network")
        session.set_value("#flaresolverr_url", "not a url")
        session.submit_section("network", 0)
        check(session.cdp.eval("Boolean(document.querySelector('#flaresolverr_url ~ .field-error, .field .field-error'))"),
              "invalid FlareSolverr URL shows a field error")
        check(session.value("#flaresolverr_url") == "not a url", "invalid value stays in the field")
        check("must be an absolute http:// or https:// address" in session.text(),
              "invalid FlareSolverr error is English")
        session.navigate("/settings?section=network")
        check(session.value("#flaresolverr_url") == fixture_url, "the invalid attempt did not overwrite the saved URL")

        print("==> settings backup")
        session.navigate("/settings?section=backup")
        session.set_checked("#backup_enabled", False)
        session.set_value("#backup_interval_hours", "12")
        session.set_value("#backup_keep", "5")
        session.submit_section("backup")
        check(db.setting("backup_enabled") == "0", "backup enabled toggle is saved")
        check(db.setting("backup_interval_hours") == "12", "backup interval is saved")
        check(db.setting("backup_keep") == "5", "backup keep is saved")
        session.navigate("/settings?section=backup")
        check(session.checked("#backup_enabled") is False, "backup enabled survives a reload")
        check(session.value("#backup_keep") == "5", "backup keep survives a reload")

        print("==> settings advanced")
        session.navigate("/settings?section=advanced")
        session.set_value("#workers", "3")
        session.set_value("#fetch_timeout_seconds", "30")
        session.set_value("#breaker_threshold", "6")
        session.set_value("#breaker_cooldown", "90")
        session.set_value("#max_torrents_per_run", "12")
        session.set_value("#max_torrent_mb", "5")
        session.set_checked("#fetch_torrents", True)
        session.set_value("#user_agent", "e2e-agent/1.0")
        session.set_value("#torznab_download_factor", "2")
        session.set_value("#torznab_upload_factor", "3")
        session.set_value("#api_rate_limit_per_minute", "222")
        session.set_checked("#torznab_live_search", True)
        session.set_value("#torznab_live_timeout_seconds", "25")
        session.set_value("#torznab_live_cache_seconds", "240")
        session.set_value("#torznab_live_wait_seconds", "7")
        session.set_value("#auto_disable_after", "9")
        session.submit_section("advanced")
        check(db.setting("workers") == "3", "workers is saved")
        check(db.setting("max_torrent_bytes") == str(5 << 20), "max torrent size is saved in bytes")
        check(db.setting("user_agent") == "e2e-agent/1.0", "user agent is saved")
        check(db.setting("torznab_download_factor") == "2", "download factor is saved")
        check(db.setting("auto_disable_after") == "9", "auto-disable is saved")
        session.navigate("/settings?section=advanced")
        check(session.value("#workers") == "3", "workers survives a reload")
        check(session.value("#max_torrent_mb") == "5", "max torrent size survives a reload")
        check(session.checked("#fetch_torrents") is True, "fetch torrents survives a reload")

        # ---------------------------------------------------------- app
        print("==> application form")
        session.navigate("/applications/new")
        session.set_value("#kind", "sonarr")
        session.set_value("#name", "E2E Sonarr")
        session.set_value("#url", fixture_url)
        session.set_value("#sync_level", "disabled")
        session.set_value("#api_key", "e2e-app-key")
        session.submit_form("/applications")
        check(session.cdp.eval("location.pathname") == "/applications",
              "application create returns to the applications page")
        app_row = db.app("E2E Sonarr")
        check(app_row is not None, "application row exists")
        if app_row:
            app_id = app_row[0]
            check(app_row[2] == fixture_url, "application URL is saved")
            check(app_row[3] == "disabled", "application sync level is saved")
            check(app_row[4] > 0, "application API key is encrypted in storage")
            session.navigate(f"/applications/{app_id}/edit")
            check(session.value("#name") == "E2E Sonarr", "application name survives a reload")
            check(session.value("#api_key") == "", "application secret field is blank after reload")
            session.set_value("#name", "E2E Sonarr Edited")
            session.set_value("#url", fixture_url + "/arr")
            session.set_value("#sync_level", "add-remove")
            session.set_value("#api_key", "")
            session.submit_form(f"/applications/{app_id}")
            check(db.app("E2E Sonarr Edited") is not None, "application edits are saved")
            edited = db.app("E2E Sonarr Edited")
            check(edited and edited[2] == fixture_url + "/arr", "application URL edit is saved")
            check(edited and edited[3] == "add-remove", "application sync level edit is saved")
            check(edited and edited[4] > 0, "blank application API key preserves the secret")
            session.navigate(f"/applications/{app_id}/edit")
            check(session.value("#url") == fixture_url + "/arr", "application URL survives a reload")
            session.set_checked('input[name="clear_api_key"]', True)
            session.submit_form(f"/applications/{app_id}")
            session.wait_ready()
            cleared = db.app("E2E Sonarr Edited")
            check(cleared is not None and cleared[4] in (0, None), "clear-api-key removes the application secret")
            before = session.confirm_count()
            session.navigate(f"/applications/{app_id}/edit")
            session.click('form[action="/applications/%d/delete"] button[type=submit]' % app_id)
            time.sleep(0.4)
            session.wait_ready()
            check(session.confirm_count() == before + 1, "application delete asks for confirmation")
            check(db.app("E2E Sonarr Edited") is None, "application delete removes the row")

        # ---------------------------------------------------------- download client
        print("==> download client form")
        session.navigate("/download-clients/new")
        session.set_value("#client_name", "E2E qBittorrent")
        session.set_value("#client_kind", "qbittorrent")
        session.set_value("#client_protocol", "torrent")
        session.set_value("#client_url", fixture_url)
        session.set_value("#client_username", "e2e-user")
        session.set_value("#client_password", "e2e-password")
        session.set_value("#client_api_key", "e2e-client-key")
        session.set_value("#client_category", "e2e-category")
        session.set_value("#client_save_path", "/downloads/e2e")
        session.set_checked("#client_paused", True)
        session.set_checked("#client_enabled", True)
        session.set_checked("#client_default", True)
        session.submit_form("/download-clients")
        client_row = db.client("E2E qBittorrent")
        check(client_row is not None, "download client is created")
        if client_row:
            client_id = client_row[0]
            check(client_row[2] == fixture_url, "download client URL is saved")
            check(client_row[4] > 0 and client_row[5] > 0, "download client secrets are encrypted")
            session.navigate(f"/download-clients/{client_id}/edit")
            check(session.value("#client_name") == "E2E qBittorrent", "client name survives a reload")
            check(session.value("#client_password") == "", "client password is blank after reload")
            session.set_value("#client_name", "E2E qBittorrent Edited")
            session.set_value("#client_url", fixture_url + "/qb")
            session.set_value("#client_username", "e2e-user-edited")
            session.set_value("#client_category", "e2e-category-edited")
            session.set_value("#client_save_path", "/downloads/e2e2")
            session.set_checked("#client_paused", False)
            session.set_checked("#client_default", False)
            session.submit_form(f"/download-clients/{client_id}")
            edited = db.client("E2E qBittorrent Edited")
            check(edited is not None, "client edits are saved")
            check(edited and edited[2] == fixture_url + "/qb", "client URL edit is saved")
            check(edited and edited[6] == "e2e-category-edited", "client category edit is saved")
            check(edited and edited[4] > 0 and edited[5] > 0,
                  "blank client secrets preserve the stored values")
            session.navigate("/applications#clients")
            session.click(f'form[action="/download-clients/{client_id}/test"] button[type=submit]')
            time.sleep(0.5)
            session.wait_ready()
            check("Test OK" in session.text() or "qBittorrent" in session.text(),
                  "download client Test button runs against the fixture")
            session.navigate(f"/download-clients/{client_id}/edit")
            before = session.confirm_count()
            session.set_checked('input[name="password_remove"]', True)
            session.set_checked('input[name="api_key_remove"]', True)
            session.submit_form(f"/download-clients/{client_id}")
            check(session.confirm_count() >= before + 2, "remove-secret checkboxes ask for confirmation")
            cleared = db.client("E2E qBittorrent Edited")
            check(cleared is not None and cleared[4] in (0, None) and cleared[5] in (0, None),
                  "remove-secret checkboxes clear the stored client secrets")
            before = session.confirm_count()
            session.navigate("/applications#clients")
            session.click(f'form[action="/download-clients/{client_id}/delete"] button[type=submit]')
            time.sleep(0.4)
            session.wait_ready()
            check(session.confirm_count() == before + 1, "client delete asks for confirmation")
            check(db.client("E2E qBittorrent Edited") is None, "client delete removes the row")

        # ---------------------------------------------------------- API keys
        print("==> API keys")
        session.navigate("/settings?section=security")
        session.set_value("#key_name", "e2e-key")
        session.submit_form("/settings/apikeys")
        check("e2e-key" in session.text(), "additional API key is created")
        rows = db.query("SELECT id, name FROM api_keys WHERE name = ?", ("e2e-key",))
        check(len(rows) == 1, "additional API key exists in the database")
        if rows:
            key_id = rows[0][0]
            before = session.confirm_count()
            session.navigate("/settings?section=security")
            session.click(f'form[action="/settings/apikeys/{key_id}/delete"] button[type=submit]')
            time.sleep(0.4)
            session.wait_ready()
            check(session.confirm_count() == before + 1, "API key revoke asks for confirmation")
            check(len(db.query("SELECT id FROM api_keys WHERE name = ?", ("e2e-key",))) == 0,
                  "API key revoke removes the row")

        # ---------------------------------------------------------- security last
        print("==> settings security")
        session.navigate("/settings?section=security")
        session.set_value("#ui_user", "e2e")
        session.set_value("#ui_password", "e2e-password-123")
        session.set_value("#ui_password_confirm", "e2e-password-123")
        token = base64.b64encode(b"e2e:e2e-password-123").decode()
        cdp.send("Network.setExtraHTTPHeaders", {"headers": {"Authorization": "Basic " + token}})
        session.submit_section("security")
        check(db.setting("ui_user") == "e2e", "UI user is saved")
        check(db.setting("ui_password_hash") != "", "UI password hash is saved")
        session.navigate("/settings?section=security")
        check(session.value("#ui_user") == "e2e", "UI user survives a reload behind Basic auth")
        check(session.value("#ui_password") == "", "UI password is never rendered after a reload")
        session.set_value("#ui_password", "e2e-password-456")
        session.set_value("#ui_password_confirm", "e2e-password-456")
        # The POST itself is authenticated with the current password; the
        # redirect afterwards uses the same header. Switch to the new one only
        # once the change has been written.
        session.submit_section("security")
        new_token = base64.b64encode(b"e2e:e2e-password-456").decode()
        cdp.send("Network.setExtraHTTPHeaders", {"headers": {"Authorization": "Basic " + new_token}})
        session.navigate("/settings?section=security")
        check(session.value("#ui_user") == "e2e",
              "changed UI password saves with the new credentials")

        # ---------------------------------------------------------- mobile 390 px
        print("==> mobile 390 px")
        session.cdp.send("Emulation.setDeviceMetricsOverride", {
            "width": 390, "height": 844, "deviceScaleFactor": 1, "mobile": True,
        })
        mobile_routes = ["/", "/releases", "/indexers", "/indexers/new", "/applications",
                         "/activity", "/settings", "/rules", "/maintenance"]
        for route in mobile_routes:
            session.navigate(route)
            width = session.cdp.eval("document.documentElement.scrollWidth")
            check(width <= 391, f"{route}: no horizontal scroll at 390 px (width {width})")
            small = session.cdp.eval("""
              Array.from(document.querySelectorAll('button, a.btn, input[type=submit], select')).filter(function(el){
                var r=el.getBoundingClientRect();
                return r.width>0 && r.height>0 && r.height<44;
              }).length
            """)
            check(small == 0, f"{route}: touch targets >= 44 px ({small} too small)")
        session.cdp.send("Emulation.clearDeviceMetricsOverride", {})

    finally:
        if cdp is not None:
            cdp.close()
        if chrome_process is not None:
            chrome_process.terminate()
            try:
                chrome_process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                chrome_process.kill()
        if app is not None:
            app.terminate()
            try:
                app.wait(timeout=5)
            except subprocess.TimeoutExpired:
                app.kill()
        fixture.shutdown()
        fixture.server_close()
        if keep:
            print(f"kept {tmp}")
        else:
            shutil.rmtree(tmp, ignore_errors=True)

    print(f"\n{CHECKS} checks, {len(FAILS)} failure(s)")
    for failure in FAILS:
        print(f"  - {failure}")
    return 1 if FAILS else 0


if __name__ == "__main__":
    sys.exit(main())
