#!/usr/bin/env python3
"""Rebuild assets from a read-only website checkout; never download or read secrets.

All inputs are hashed, generated outputs are validated in memory before any write,
and individual files are atomically replaced. Publication is not a filesystem-wide
transaction. Protected activity icons and Bearded artwork are never output targets.
Reproducible tool versions: fonttools 4.66.1, brotli 1.2.0; see requirements-dev.txt
and README.
"""

import argparse
import hashlib
import io
import json
import os
import re
import tempfile
import xml.etree.ElementTree as ET
from pathlib import Path
from typing import Protocol, cast

PROTECTED = {"lucide-music.svg", "lucide-git-branch.svg"}
WINDOW_ICONS = {
    "minus": "lucide-window-minus.svg",
    "maximize": "lucide-window-maximize.svg",
    "copy": "lucide-window-restore.svg",
    "x": "lucide-window-x.svg",
}
CONTROL_ICONS = {
    "compass": "lucide-compass.svg",
    "container": "lucide-container.svg",
    "cpu": "lucide-cpu.svg",
    "database": "lucide-database.svg",
    "globe": "lucide-globe.svg",
    "gauge": "lucide-gauge.svg",
    "languages": "lucide-languages.svg",
    "layers": "lucide-layers.svg",
    "link": "lucide-link.svg",
    "list-filter": "lucide-list-filter.svg",
    "navigation": "lucide-navigation.svg",
    "panel-top": "lucide-panel-top.svg",
    "radar": "lucide-radar.svg",
    "sliders-horizontal": "lucide-sliders-horizontal.svg",
}


class FontTimestamps(Protocol):
    created: int
    modified: int


def digest(data):
    return hashlib.sha256(data).hexdigest()


def generate(source, icons_only=False):
    inputs, outputs = {}, {}

    def read(relative):
        data = (source / relative).read_bytes()
        inputs[relative] = digest(data)
        return data

    package = "node_modules/lucide-react/"
    version = json.loads(read(package + "package.json"))["version"]
    if version != "1.52.0":
        raise RuntimeError(f"Expected installed lucide-react 1.52.0, found {version}")
    read(package + "LICENSE")
    for name, target in {**WINDOW_ICONS, **CONTROL_ICONS}.items():
        module = read(package + f"dist/esm/icons/{name}.mjs").decode()
        node_marker = module.find("node:")
        start = module.find("[", node_marker)
        depth = 0
        end = -1
        for index in range(start, len(module)):
            if module[index] == "[":
                depth += 1
            elif module[index] == "]":
                depth -= 1
                if depth == 0:
                    end = index + 1
                    break
        if node_marker < 0 or start < 0 or end < 0:
            raise RuntimeError(f"Cannot find installed icon data: {name}")
        try:
            nodes = json.loads(re.sub(r'([{,]\s*)([a-zA-Z]\w*)\s*:', r'\1"\2":', module[start:end]))
        except json.JSONDecodeError as error:
            raise RuntimeError(f"Invalid installed icon data: {name}") from error
        svg = ET.Element("svg", {"xmlns": "http://www.w3.org/2000/svg", "viewBox": "0 0 24 24", "fill": "none", "stroke": "#000000", "stroke-width": "2", "stroke-linecap": "round", "stroke-linejoin": "round"})
        for tag, attributes in nodes:
            ET.SubElement(svg, tag, {k: str(v) for k, v in attributes.items() if k != "key"})
        outputs[target] = (f"<!-- lucide-react {version}; ISC; see LUCIDE-LICENSE.txt -->\n" + ET.tostring(svg, encoding="unicode") + "\n").encode()
    tools = {"lucide-react": version}
    if not icons_only:
        import brotli
        import fontTools
        from fontTools.merge import Merger
        from fontTools.ttLib import TTFont
        from fontTools.varLib.instancer import instantiateVariableFont

        tools.update(fonttools=fontTools.__version__, brotli=brotli.__version__)
        if (fontTools.__version__, brotli.__version__) != ("4.66.1", "1.2.0"):
            raise RuntimeError("Use fonttools==4.66.1 and brotli==1.2.0 for reproducible output")
        cached = []
        for path in sorted((source / ".next/static/media").glob("*.woff2")):
            relative = path.relative_to(source).as_posix()
            data = read(relative)
            with TTFont(io.BytesIO(data)) as font:
                family = font["name"].getDebugName(1) or ""
            if any(family.startswith(name) for name in ("Nunito", "Quicksand", "Syne")):
                cached.append((family, data))
        for family, weights in [("Nunito", [600, 700, 800]), ("Quicksand", [600, 700]), ("Syne", [700])]:
            for weight in weights:
                streams = []
                for font_family, data in cached:
                    if not font_family.startswith(family):
                        continue
                    font = TTFont(io.BytesIO(data), recalcTimestamp=False)
                    if "fvar" not in font:
                        raise RuntimeError(f"Expected variable cached font: {font_family}")
                    font = instantiateVariableFont(font, {"wght": weight}, inplace=True)
                    font.flavor = None
                    stream = io.BytesIO()
                    font.save(stream)
                    stream.seek(0)
                    streams.append(stream)
                if not streams:
                    raise RuntimeError(f"No cached {family} fonts; build the website first")
                font = Merger().merge(streams)
                font.recalcTimestamp = False
                head = cast(FontTimestamps, font["head"])
                head.created = 2082844800
                head.modified = 2082844800  # Fixed OpenType timestamp: 1970-01-01.
                style = {600: "SemiBold", 700: "Bold", 800: "ExtraBold"}[weight]
                for record in font["name"].names:
                    if record.nameID in (1, 16):
                        text = "DDetector " + family
                    elif record.nameID in (2, 17):
                        text = style
                    elif record.nameID in (3, 4, 6):
                        text = "DDetector" + family + "-" + style
                    else:
                        continue
                    encoding = record.getEncoding()
                    if encoding is None:
                        raise RuntimeError(f"Unsupported font name encoding: {family} nameID={record.nameID}")
                    record.string = text.encode(encoding)
                stream = io.BytesIO()
                font.save(stream)
                outputs[f"{family}-{style}.ttf"] = stream.getvalue()
        for relative, target in [("fonts/luckystar.otf", "luckystar.otf"), ("ui/Kagami.webp", "Kagami.webp")]:
            outputs[target] = read("public/" + relative)
        providers = dict(re.findall(r'(github|discord|spotify):\s*"(M[^"\n]+)"', read("src/components/profile/ServiceIcon.tsx").decode()))
        if set(providers) != {"github", "discord", "spotify"}:
            raise RuntimeError("ServiceIcon.tsx must supply exactly three provider paths")
        for provider, path in providers.items():
            color = {"github": "#f0f0f4", "discord": "#5865f2", "spotify": "#1ed760"}[provider]
            svg = ET.Element("svg", {"xmlns": "http://www.w3.org/2000/svg", "viewBox": "0 0 24 24", "fill": color})
            ET.SubElement(svg, "path", {"d": path})
            outputs[provider + "-dark.svg"] = (ET.tostring(svg, encoding="unicode") + "\n").encode()
    if any(name in PROTECTED or name.startswith("bearded-") for name in outputs):
        raise RuntimeError("Attempt to overwrite protected artwork")
    manifest = {"schema": 1, "mode": "window-icons" if icons_only else "website-assets", "tools": tools,
                "inputs": dict(sorted(inputs.items())), "outputs": {name: digest(data) for name, data in sorted(outputs.items())}}
    return outputs, manifest


def asset_path(name):
    """Resolve export filenames without changing the provenance manifest format."""
    if name.startswith("lucide-"):
        return Path("icons/lucide") / name
    if name.endswith((".ttf", ".otf")):
        return Path("fonts") / name
    if name in {"github-dark.svg", "discord-dark.svg", "spotify-dark.svg"}:
        return Path("icons/brands") / name

    if name == "Kagami.webp":
        return Path("images") / name
    return Path(name)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path)
    parser.add_argument("--output", type=Path, default=Path(__file__).resolve().parents[1] / "assets")
    parser.add_argument("--icons-only", action="store_true", help="Export only new window icons; never regenerate existing control/artwork assets")
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--dry-run", action="store_true", help="Generate/validate and print hashes; write nothing")
    mode.add_argument("--check", action="store_true", help="Compare regenerated outputs and provenance with output directory; write nothing")
    args = parser.parse_args()
    source = args.source.resolve()
    output = args.output.resolve()
    if output == source or source in output.parents:
        raise RuntimeError("Output must not be inside the read-only website")
    outputs, manifest = generate(source, args.icons_only)
    manifest_name = "window-icons-provenance.json" if args.icons_only else "import-manifest.json"
    outputs[manifest_name] = (json.dumps(manifest, indent=2, sort_keys=True) + "\n").encode()
    if args.check:
        mismatches = [name for name, data in outputs.items() if not (output / asset_path(name)).is_file() or (output / asset_path(name)).read_bytes() != data]
        if mismatches:
            raise RuntimeError("Asset/provenance mismatch: " + ", ".join(mismatches))
        print(f"PASS reproducible assets: {len(outputs)} files")
    elif args.dry_run:
        print(json.dumps(manifest, indent=2, sort_keys=True))
    else:
        output.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(prefix=".asset-stage-", dir=output) as stage:
            for name, data in outputs.items():
                (Path(stage) / name).write_bytes(data)
            for name in outputs:
                target = output / asset_path(name)
                target.parent.mkdir(parents=True, exist_ok=True)
                os.replace(Path(stage) / name, target)
        print(f"Published {len(outputs)} validated files to {output}")


if __name__ == "__main__":
    main()
