"""Keep bare red error text aligned with SiteQuote without adding quote chrome."""

import re
import unittest
from pathlib import Path

QML = Path(__file__).resolve().parents[1] / "qml"


class MessageFontTests(unittest.TestCase):
    def test_red_errors_match_quote_font(self):
        quote = (QML / "Components/SiteQuote.qml").read_text()
        properties = ("family", "pixelSize", "weight")
        expected = {
            prop: re.search(r"font\." + prop + r":\s*([^\n]+)", quote).group(1).strip()
            for prop in properties
        }
        messages = (
            ("App/Main.qml", 'text: appUi.backendBridge.spotifyState.error || ""'),
            ("App/Main.qml", 'text: appUi.state.error || ""'),
            ("Git/terminal.qml", 'text: root.terminalSession ? root.terminalSession.error : ""'),
            ("Git/GitWorkspace.qml", 'text: root.available ? root.workspaceBackend.error : ""'),
        )
        for path, marker in messages:
            with self.subTest(path=path, message=marker):
                source = (QML / path).read_text()
                body = source.split(marker, 1)[1].split("}", 1)[0]
                for prop in properties:
                    match = re.search(r"font\." + prop + r":\s*([^\n]+)", body)
                    self.assertIsNotNone(match, prop)
                    self.assertEqual(match.group(1).strip(), expected[prop], prop)
