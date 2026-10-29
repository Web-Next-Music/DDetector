"""Guard direct panel rendering while retaining local image/control masks."""

import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MAIN = ROOT / "qml/App/Main.qml"


class PanelRenderingTests(unittest.TestCase):
    def test_panels_do_not_flatten_their_subtrees(self):
        source = MAIN.read_text()
        for panel, end in (("rightPanel", "MouseArea {"), ("playerPanel", "TapHandler {")):
            with self.subTest(panel=panel):
                chrome = source.split(f"id: {panel}", 1)[1].split(end, 1)[0]
                self.assertIn("clip: true", chrome)
                self.assertNotIn("layer.enabled:", chrome)
                self.assertNotIn("layer.effect:", chrome)
                self.assertNotIn("MultiEffect", chrome)
                self.assertNotIn(panel + "Mask", source)
                self.assertEqual(chrome.count("SiteCornerCover {"), 1)
                self.assertIn(f"radius: {panel}.radius", chrome)
                self.assertIn("backgroundColor: appUi.bg", chrome)
                self.assertIn("z: 9", chrome)
                self.assertIn("z: 10", chrome)
                self.assertIn("border.color: appUi.borderColor", chrome)

    def test_shared_corners_use_geometry_not_layers(self):
        corners = (ROOT / "qml/Components/SiteCornerCover.qml").read_text()
        self.assertIn("model: 4", corners)
        self.assertIn("preferredRendererType: Shape.GeometryRenderer", corners)
        self.assertIn("fillColor: cover.backgroundColor", corners)
        self.assertIn("rotation: index * 90", corners)
        self.assertIn("Math.max(0, Math.min(radius, width / 2, height / 2))", corners)
        self.assertNotIn("layer.enabled:", corners)
        self.assertNotIn("MultiEffect", corners)

    def test_component_is_registered_as_shared_resource(self):
        cmake = (ROOT / "CMakeLists.txt").read_text()
        shared = cmake.split("set(SHARED_QML", 1)[1].split("set(APP_QML", 1)[0]
        self.assertIn("qml/Components/SiteCornerCover.qml", shared)
        self.assertIn('QT_RESOURCE_ALIAS "${qml_alias}"', cmake)

    def test_specialized_masks_are_preserved(self):
        source = MAIN.read_text()
        for name in ("windowControlsMask", "playerCoverMask"):
            with self.subTest(mask=name):
                self.assertIn(f"maskSource: {name}", source)
                self.assertIn(f"id: {name}", source)


if __name__ == "__main__":
    unittest.main()
