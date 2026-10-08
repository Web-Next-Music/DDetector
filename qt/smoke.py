#!/usr/bin/env python3
"""Dark-only offscreen native screenshots against a local mock backend, not production.
No OAuth, external traffic, secrets, or privilege bypasses are used by the application.
"""
import argparse
import copy
import http.server
import json
import os
from pathlib import Path
import secrets
import subprocess
import threading

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--executable", type=Path, default=Path(__file__).resolve().parent / "build/ddetector-qt")
parser.add_argument("--output", type=Path, default=Path(__file__).resolve().parent / "build/screenshots")
args = parser.parse_args()
args.output.mkdir(parents=True, exist_ok=True)
fixture = json.loads((Path(__file__).resolve().parent / "assets/demo.json").read_text())
key = secrets.token_urlsafe(32)
state = copy.deepcopy(fixture)
request_count = 0

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        global request_count
        assert self.path == "/api/state"
        assert self.headers.get("Authorization") == "Bearer " + key
        request_count += 1
        payload = json.dumps(state).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def log_message(self, *args):
        pass

server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
thread = threading.Thread(target=server.serve_forever, daemon=True)
thread.start()
env = dict(os.environ, QT_QPA_PLATFORM="offscreen", QT_QUICK_BACKEND="software", XDG_CONFIG_HOME=str(args.output / "settings"))
try:
    scenarios = [
        ("desktop", 1200, 800, {}),
        ("mobile", 390, 740, {}),
        ("login", 1200, 900, {"authenticated": False, "user": None, "hasAccess": False, "accessChecked": False, "tracks": []}),
        ("login-mobile", 320, 480, {"authenticated": False, "user": None, "hasAccess": False, "accessChecked": False, "tracks": []}),
                ("denied", 1200, 800, {"hasAccess": False, "accessChecked": True}),
                ("compact", 700, 600, {}),
                ("banned", 390, 740, {"isBanned": True, "hasAccess": False}),
        ("checking-access", 1200, 800, {"hasAccess": False, "accessChecked": False}),
        ("error", 1200, 800, {"hasAccess": False, "accessChecked": False, "error": "Unable to check DDetector access"}),
        ("loading", 1200, 800, {"loading": True, "tracks": [], "processed": 0, "foundCount": 0}),
        ("empty", 1200, 800, {"tracks": [], "processed": 0, "foundCount": 0}),
    ]
    for name, width, height, overrides in scenarios:
        state = copy.deepcopy(fixture)
        state.update(overrides)
        before = request_count
        result = subprocess.run([
            str(args.executable.resolve()), "--backend-url", f"http://127.0.0.1:{server.server_port}",
            "--bridge-key", key, "--smoke-test", "--screenshot", str((args.output / f"{name}.png").resolve()),
            "--width", str(width), "--height", str(height),
        ], env=env, capture_output=True, text=True, timeout=12)
        if result.returncode or result.stderr.strip():
            raise SystemExit(f"{name}: exit={result.returncode}\n{result.stderr}")
        assert request_count > before, f"{name}: application did not poll"
        screenshot = args.output / f"{name}.png"
        assert screenshot.stat().st_size > 1000
        assert screenshot.read_bytes().startswith(b"\x89PNG\r\n\x1a\n"), f"{name}: invalid PNG screenshot"
        print(f"PASS {name}: dark-only native screenshot, authenticated polling, no QML warnings")
finally:
    server.shutdown()
    server.server_close()
