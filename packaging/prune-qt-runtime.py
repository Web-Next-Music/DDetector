#!/usr/bin/env python3
"""Prune optional Qt deployment artifacts from a Linux AppDir or Windows dist."""

import argparse
import re
from pathlib import Path


STYLES = (
    "Fusion", "Imagine", "Material", "Universal", "FluentWinUI3",
    "Windows", "macOS", "iOS",
)
# Only known Qt catalog families; unrelated application catalogs are retained.
QT_CATALOG = re.compile(
    r"(?:qt|qtbase|qtconnectivity|qtdeclarative|qthelp|qtlocation|qtmultimedia|"
    r"qtnetworkauth|qtpositioning|qtquickcontrols|qtquickcontrols2|qtquickdialogs|"
    r"qtremoteobjects|qtscript|qtscxml|qtsensors|qtserialport|qtspeech|"
    r"qttools|qttranslations|qtvirtualkeyboard|qtwayland|qtwebchannel|"
    r"qtwebengine|qtwebsockets|qtxmlpatterns)_[A-Za-z0-9_]+\.qm"
)


def children(path):
    """Walk a staging subtree without following directory symlinks."""
    if not path.is_dir() or path.is_symlink():
        return
    for child in sorted(path.iterdir()):
        yield child
        if child.is_dir() and not child.is_symlink():
            yield from children(child)


def remove(path):
    """Return removed file/link count and bytes (links count their own bytes)."""
    if path.is_symlink() or path.is_file():
        size = path.lstat().st_size
        path.unlink()
        return 1, size
    if path.is_dir():
        files = size = 0
        for child in list(path.iterdir()):
            count, length = remove(child)
            files += count
            size += length
        path.rmdir()
        return files, size
    return 0, 0


def prune(root, platform):
    root = Path(root)
    if not root.is_dir() or root.is_symlink():
        raise ValueError("staging root must be an existing, non-symlink directory")
    prefix = root / "usr" if platform == "linux" else root
    qml = prefix / "qml"
    plugins = prefix / "plugins" if platform == "linux" else root
    libraries = prefix / "lib" if platform == "linux" else root
    targets = [qml / "QtQuick" / "Controls" / style for style in STYLES]
    targets.append(plugins / "qmltooling")
    targets.extend(path for path in children(qml) if path.name.endswith(".qmltypes"))
    patterns = []
    for style in STYLES:
        stem = "Qt6QuickControls2" + style
        patterns.append("lib" + stem + "*.so*" if platform == "linux" else stem + "*.dll")
    if platform == "linux":
        targets.extend([
            qml / "QtQuick" / "VirtualKeyboard",
            qml / "QtQuick" / "Pdf",
            plugins / "platforminputcontexts" / "libqtvirtualkeyboardplugin.so",
            plugins / "imageformats" / "libqpdf.so",
        ])
        patterns.extend(["libQt6VirtualKeyboard*.so*", "libQt6Pdf*.so*"])
        targets.extend(
            path for path in children(prefix / "translations")
            if QT_CATALOG.fullmatch(path.name)
        )
    for pattern in patterns:
        targets.extend(libraries.glob(pattern))

    files = size = 0
    for path in targets:
        # Never traverse an ancestor symlink, even inside a scoped staging path.
        if any(parent.is_symlink() for parent in path.parents if parent != root):
            continue
        count, length = remove(path)
        files += count
        size += length
    return files, size


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("platform", choices=("linux", "windows"))
    parser.add_argument("root", type=Path, help="staging AppDir or Windows dist")
    args = parser.parse_args()
    try:
        files, size = prune(args.root, args.platform)
    except (OSError, ValueError) as error:
        parser.exit(1, "Qt runtime pruning failed: {}\n".format(error))
    print("Qt runtime pruning ({}): removed {} files, {} bytes".format(
        args.platform, files, size))


if __name__ == "__main__":
    main()
