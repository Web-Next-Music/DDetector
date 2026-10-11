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
        self.assertFalse(assets.PROTECTED.intersection(assets.CONTROL_ICONS.values()))
        self.assertEqual(len(assets.WINDOW_ICONS), 3)

    def test_settings_icons_are_exported_and_muted(self):
        expected = {"compass", "container", "cpu", "database", "gauge", "globe", "languages",
                    "layers", "link", "list-filter", "navigation", "panel-top", "radar", "sliders-horizontal"}
        self.assertEqual(set(assets.CONTROL_ICONS), expected)
        for name, filename in assets.CONTROL_ICONS.items():
            source = (ROOT / "assets" / filename).read_text()
            self.assertIn("lucide-react 1.52.0", source, name)
            self.assertIn('stroke="#000000"', source, name)
        card = (ROOT / "SiteSettingsCard.qml").read_text()
        self.assertIn('readonly property color leadingGlyphColor: "#999999"', card)
        self.assertIn("colorizationColor: card.leadingGlyphColor", card)

    def test_censor_scroll_and_subtitle_structure(self):
        detector = (ROOT / "CensorDetector.qml").read_text()
        viewport = detector[detector.index("id: detectorScroll"):detector.index("contentWidth: availableWidth")]
        self.assertIn("anchors.fill: parent", viewport)
        self.assertIn("anchors.margins: 16", viewport)
        self.assertIn("anchors.fill: detectorScroll", detector)
        self.assertIn('shadowColor: "#0c0c0e"', detector)
        self.assertIn('ctx.fillStyle = "#D50811"', detector)

        subtitle = (ROOT / "SiteSubtitle.qml").read_text()
        self.assertIn('color: "transparent"', subtitle)
        self.assertIn("border.width: 0", subtitle)
        self.assertIn("radius: 0", subtitle)
        self.assertIn('readonly property color glyphColor: "#999999"', subtitle)

        panel = (ROOT / "CensorSettingsPanel.qml").read_text()
        self.assertEqual(panel.count("SiteSubtitle {"), 0)
        for title in ["Processing resources", "Candidate search"]:
            before_title = panel[:panel.index(f'title: "{title}"')]
            self.assertEqual(before_title.rsplit("{", 1)[0].rstrip().splitlines()[-1].strip(), "SiteSettingsCard")
        self.assertNotIn("censorCleanTemp", panel)
        self.assertNotIn("censorAutoReplace", panel)
        self.assertNotIn("censorVocalAnalysis", panel)
        self.assertNotIn("Hardware acceleration", panel)

    def test_no_manual_window_canvas_or_dead_titlebar(self):
        source = (ROOT / "Main.qml").read_text()
        self.assertNotIn("id: titleBar", source)
        self.assertNotIn("Canvas {", (ROOT / "WindowButton.qml").read_text())
        self.assertIn('model: ["music", "git-branch", "search"]', source)

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
