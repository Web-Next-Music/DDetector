"""Regression tests for the deployment allowlist; no Qt installation required."""

import importlib.util
from pathlib import Path
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location(
    "prune_qt_runtime", Path(__file__).with_name("prune-qt-runtime.py"))
PRUNER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(PRUNER)


class PruneTests(unittest.TestCase):
    def check_layout(self, platform):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            prefix = "usr/" if platform == "linux" else ""
            plugins = prefix + "plugins/" if platform == "linux" else ""
            lib = prefix + "lib/" if platform == "linux" else ""
            qml = prefix + "qml/"
            remove = [qml + "QtQuick/Controls/Material/qmldir",
                      qml + "QtQuick/Controls/Basic/Basic.qmltypes",
                      plugins + "qmltooling/debug-plugin"]
            keep = [qml + "QtQuick/Controls/Basic/qmldir",
                    qml + "QtQuick/Controls/impl/qmldir",
                    qml + "QtQuick/Templates/qmldir",
                    qml + "QtQuick/NativeStyle/qmldir",
                    plugins + "tls/backend", plugins + "multimedia/backend",
                    plugins + "platforms/backend", plugins + "imageformats/webp"]
            if platform == "linux":
                remove += [lib + "libQt6QuickControls2MaterialStyleImpl.so.6",
                           qml + "QtQuick/VirtualKeyboard/Core/qmldir",
                           qml + "QtQuick/Pdf/qmldir", lib + "libQt6Pdf.so.6",
                           lib + "libQt6VirtualKeyboard.so.6",
                           plugins + "platforminputcontexts/libqtvirtualkeyboardplugin.so",
                           plugins + "imageformats/libqpdf.so",
                           prefix + "translations/qtbase_ru.qm"]
                keep += [lib + "libQt6QuickControls2Basic.so.6",
                         lib + "libQt6QuickControls2Impl.so.6",
                         prefix + "translations/application_ru.qm"]
            else:
                remove += ["Qt6QuickControls2Material.dll",
                           "Qt6QuickControls2MaterialStyleImpl.dll"]
                keep += ["Qt6QuickControls2Basic.dll", "Qt6QuickControls2Impl.dll",
                         qml + "QtQuick/Pdf/qmldir", "translations/qtbase_ru.qm"]
            for relative in remove + keep:
                path = root / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(b"fixture")
            self.assertEqual(PRUNER.prune(root, platform), (len(remove), 7 * len(remove)))
            for relative in remove:
                self.assertFalse((root / relative).exists(), relative)
            for relative in keep:
                self.assertTrue((root / relative).exists(), relative)
            self.assertEqual(PRUNER.prune(root, platform), (0, 0))

    def test_linux(self):
        self.check_layout("linux")

    def test_windows(self):
        self.check_layout("windows")

    def test_symlink_ancestor_is_not_followed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "stage"
            outside = Path(directory) / "outside"
            root.mkdir()
            outside.mkdir()
            target = outside / "QtQuick/Controls/Material/file.qmltypes"
            target.parent.mkdir(parents=True)
            target.write_text("preserve")
            try:
                (root / "qml").symlink_to(outside, target_is_directory=True)
            except (OSError, NotImplementedError):
                self.skipTest("directory symlinks unavailable")
            self.assertEqual(PRUNER.prune(root, "windows"), (0, 0))
            self.assertEqual(target.read_text(), "preserve")


if __name__ == "__main__":
    unittest.main()
