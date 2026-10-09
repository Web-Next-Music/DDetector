"""UI harness regressions; no native source dependencies or external traffic."""

import importlib.util
import struct
import tempfile
import unittest
import zlib
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def load(name):
    spec = importlib.util.spec_from_file_location(name, ROOT / (name + ".py"))
    if spec is None or spec.loader is None:
        raise ImportError(f"Cannot load {name} from {ROOT / (name + '.py')}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


smoke = load("smoke")
assets = load("import_assets")


def chunk(kind, body):
    return struct.pack(">I", len(body)) + kind + body + struct.pack(">I", zlib.crc32(kind + body) & 0xffffffff)


def png():
    return (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", 16, 1, 8, 2, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(b"\0" + bytes(range(48)))) + chunk(b"IEND", b""))


class SmokeTests(unittest.TestCase):
    def test_narrow_diagnostics_allowlist(self):
        self.assertEqual(smoke.unexpected_stderr("qt.multimedia.ffmpeg: Using Qt multimedia with FFmpeg version 9\n"), [])
        for message in ["qrc:/Main.qml:10: ReferenceError: missing", "Failed to open VDPAU backend other.so", "Unknown hw device type none", "QQmlApplicationEngine failed to load component"]:
            self.assertEqual(smoke.unexpected_stderr(message), [message])

    def test_png_dimensions_crc_and_nonblank(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "test.png"
            path.write_bytes(png())
            self.assertEqual(len(smoke.validate_png(path, 16, 1)), 64)
            with self.assertRaises(RuntimeError):
                smoke.validate_png(path, 17, 1)
            path.write_bytes(png()[:-3])
            with self.assertRaises(RuntimeError):
                smoke.validate_png(path, 16, 1)
            corrupted = bytearray(png())
            corrupted[29] ^= 1
            path.write_bytes(corrupted)
            with self.assertRaises(RuntimeError):
                smoke.validate_png(path, 16, 1)
            blank = (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", 16, 1, 8, 2, 0, 0, 0))
                     + chunk(b"IDAT", zlib.compress(bytes(49))) + chunk(b"IEND", b""))
            path.write_bytes(blank)
            with self.assertRaises(RuntimeError):
                smoke.validate_png(path, 16, 1)

    def test_timeout_does_not_expose_command_arguments(self):
        import subprocess
        from unittest import mock
        with tempfile.TemporaryDirectory() as directory:
            error = subprocess.TimeoutExpired(["application", "--bridge-key", "secret-test-key"], 12)
            with mock.patch.object(smoke.subprocess, "run", side_effect=error), mock.patch("sys.argv", ["smoke.py", "--output", directory]):
                with self.assertRaisesRegex(RuntimeError, "application exceeded 12 seconds") as caught:
                    smoke.main()
                self.assertNotIn("secret-test-key", str(caught.exception))

    def test_protected_icons_are_not_export_targets(self):
        self.assertFalse(assets.PROTECTED.intersection(assets.WINDOW_ICONS.values()))
        self.assertEqual(len(assets.WINDOW_ICONS), 3)

    def test_no_manual_window_canvas_or_dead_titlebar(self):
        source = (ROOT / "Main.qml").read_text()
        self.assertNotIn("id: titleBar", source)
        self.assertNotIn("Canvas {", (ROOT / "WindowButton.qml").read_text())
        self.assertIn('model: ["music", "git-branch"]', source)

    def test_bearded_provenance(self):
        import hashlib
        import json
        provenance = json.loads((ROOT / "assets/bearded-provenance.json").read_text())
        for name, expected in provenance["sha256"].items():
            self.assertEqual(hashlib.sha256((ROOT / "assets" / name).read_bytes()).hexdigest(), expected)

    def test_protected_activity_icon_bytes(self):
        import hashlib
        expected = {"lucide-music.svg": "25d72005128b6fd930d133c4d75e625b0c7b2b0bd01ace787ca90f7ff4ada604",
                    "lucide-git-branch.svg": "aba18380437638ef1e51310163092923658874431853977da7d6511fdff5d109"}
        for name, value in expected.items():
            self.assertEqual(hashlib.sha256((ROOT / "assets" / name).read_bytes()).hexdigest(), value)


if __name__ == "__main__":
    unittest.main()
