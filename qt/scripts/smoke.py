#!/usr/bin/env python3
"""Bounded offscreen UI smoke against a private loopback fixture backend.

No production backend or OAuth is used. Unknown stderr, including QML warnings,
is fatal; narrowly identified multimedia/driver diagnostics are retained in logs.
"""

import argparse
import copy
import hashlib
import http.server
import json
import os
import re
import secrets
import struct
import subprocess
import tempfile
import threading
import zlib
from pathlib import Path

BENIGN_STDERR = (
    re.compile(r"qt\.multimedia\.ffmpeg: Using Qt multimedia with FFmpeg version .+"),
    re.compile(r"Failed to open VDPAU backend libvdpau_nvidia\.so: cannot open shared object file: No such file or directory"),
)


def unexpected_stderr(stderr):
    return [line for line in stderr.splitlines()
            if line.strip() and not any(rule.fullmatch(line) for rule in BENIGN_STDERR)]


def validate_png(path, width, height):
    data = Path(path).read_bytes()
    if not data.startswith(b"\x89PNG\r\n\x1a\n"):
        raise RuntimeError(f"{path}: invalid PNG signature")
    offset, pixels, dimensions, ended = 8, bytearray(), None, False
    while offset + 12 <= len(data):
        size = struct.unpack_from(">I", data, offset)[0]
        kind = data[offset + 4:offset + 8]
        body = data[offset + 8:offset + 8 + size]
        if len(body) != size or offset + size + 12 > len(data):
            raise RuntimeError(f"{path}: truncated PNG")
        crc = struct.unpack_from(">I", data, offset + 8 + size)[0]
        if zlib.crc32(kind + body) & 0xffffffff != crc:
            raise RuntimeError(f"{path}: PNG CRC mismatch")
        if kind == b"IHDR":
            dimensions = struct.unpack_from(">II", body)
        elif kind == b"IDAT":
            pixels.extend(body)
        elif kind == b"IEND":
            ended = True
            break
        offset += size + 12
    if dimensions != (width, height) or not ended:
        raise RuntimeError(f"{path}: dimensions={dimensions}, expected={(width, height)} or missing IEND")
    decoder = zlib.decompressobj()
    raw = decoder.decompress(bytes(pixels), width * height * 8 + height + 1)
    if not decoder.eof or decoder.unconsumed_tail or len(set(raw)) < 16:
        raise RuntimeError(f"{path}: incomplete or blank screenshot")
    return hashlib.sha256(data).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--executable", type=Path, default=Path(__file__).resolve().parents[1] / "build/ddetector-qt")
    parser.add_argument("--output", type=Path, default=Path(__file__).resolve().parents[1] / "build/screenshots")
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    fixture = json.loads((Path(__file__).resolve().parents[1] / "test-fixtures/state.json").read_text())
    key = secrets.token_urlsafe(32)
    state = copy.deepcopy(fixture)
    request_count = 0
    failures = []
    lock = threading.Lock()

    class Handler(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            nonlocal request_count
            with lock:
                if self.path not in ("/api/state", "/api/censor/state") or self.headers.get("Authorization") != "Bearer " + key:
                    failures.append("Unexpected path or missing/invalid bearer")
                    self.send_error(403)
                    return
                if self.path == "/api/state":
                    request_count += 1
                    payload = json.dumps(state).encode()
                else:
                    payload = json.dumps({"jobs": [], "settings": {}, "paused": False,
                                          "configured": False, "error": ""}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

        def log_message(self, format: str, *args: object) -> None:
            pass

    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        with tempfile.TemporaryDirectory(prefix="ddetector-smoke-") as settings:
            env = dict(os.environ, QT_QPA_PLATFORM="offscreen", QT_QUICK_BACKEND="software",
                       XDG_CONFIG_HOME=settings, XDG_CACHE_HOME=settings)

            def run(name, options):
                try:
                    result = subprocess.run([str(args.executable.resolve()), *options], env=env,
                                            capture_output=True, text=True, timeout=12, check=False)
                except subprocess.TimeoutExpired as error:
                    stderr = error.stderr or ""
                    if isinstance(stderr, bytes):
                        stderr = stderr.decode(errors="replace")
                    (args.output / f"{name}.stderr.log").write_text(stderr.replace(key, "<redacted>"))
                    # TimeoutExpired includes command arguments (and the private key).
                    raise RuntimeError(f"{name}: application exceeded 12 seconds") from None
                stderr = result.stderr.replace(key, "<redacted>")
                (args.output / f"{name}.stderr.log").write_text(stderr)
                return result.returncode, stderr

            for name, options, message in [
                ("removed-option", ["--demo"], "Unknown option 'demo'"),
                ("missing-backend", [], "Launch via the Go backend"),
            ]:
                code, stderr = run(name, options)
                if code == 0 or message not in stderr or request_count:
                    raise RuntimeError(f"{name}: launch validation failed\n{stderr}")
            print("PASS launch validation")
            hashes = {}
            scenarios = [
                ("desktop", 800, {}),
                ("login", 900, {"authenticated": False, "user": None, "hasAccess": False, "accessChecked": False, "tracks": []}),
                ("denied", 800, {"hasAccess": False, "accessChecked": True}),
                ("banned", 800, {"isBanned": True, "hasAccess": False}),
                ("checking-access", 800, {"hasAccess": False, "accessChecked": False}),
                ("error", 800, {"hasAccess": False, "accessChecked": False, "error": "Unable to check DDetector access"}),
                ("loading", 800, {"loading": True, "tracks": [], "processed": 0, "foundCount": 0}),
                ("empty", 800, {"tracks": [], "processed": 0, "foundCount": 0}),
            ]
            for name, height, overrides in scenarios:
                with lock:
                    state = copy.deepcopy(fixture)
                    state.update(overrides)
                    before = request_count
                screenshot = args.output / f"{name}.png"
                screenshot.unlink(missing_ok=True)
                code, stderr = run(name, [
                    "--backend-url", f"http://127.0.0.1:{server.server_port}", "--bridge-key", key,
                    "--smoke-test", "--screenshot", str(screenshot.resolve()),
                    "--width", "1200", "--height", str(height),
                ])
                if code or unexpected_stderr(stderr):
                    raise RuntimeError(f"{name}: exit={code}\n{stderr}")
                with lock:
                    if failures or request_count <= before:
                        raise RuntimeError(f"{name}: polling validation failed: {failures}")
                hashes[name] = validate_png(screenshot, 1200, height)
                print(f"PASS {name}: valid nonblank 1200x{height} PNG, authenticated polls, no unexpected stderr")
            if len(set(hashes.values())) != len(hashes):
                raise RuntimeError("Different UI scenarios produced identical screenshots")
            (args.output / "manifest.json").write_text(json.dumps(hashes, indent=2) + "\n")
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=2)


if __name__ == "__main__":
    main()
