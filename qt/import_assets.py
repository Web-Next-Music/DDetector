#!/usr/bin/env python3
"""Rebuild bundled native fonts from the website's locally cached font subsets.
Requires fonttools and brotli. Never downloads fonts or reads website secrets.
"""
import argparse
import io
import shutil
from pathlib import Path
from fontTools.merge import Merger
from fontTools.ttLib import TTFont
from fontTools.varLib.instancer import instantiateVariableFont

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("source", type=Path, help="Next-Music-Website root")
args = parser.parse_args()
output = Path(__file__).resolve().parent / "assets"
for family, weights in [("Nunito", [600, 700, 800]), ("Quicksand", [600, 700]), ("Syne", [700])]:
    for weight in weights:
        streams = []
        for path in sorted((args.source / ".next/static/media").glob("*.woff2")):
            font = TTFont(path)
            if not font["name"].getDebugName(1).startswith(family):
                continue
            if "fvar" in font:
                font = instantiateVariableFont(font, {"wght": weight}, inplace=True)
            font.flavor = None
            stream = io.BytesIO()
            font.save(stream)
            stream.seek(0)
            streams.append(stream)
        if not streams:
            raise SystemExit(f"No cached {family} fonts found. Build the website first.")
        font = Merger().merge(streams)
        style = {600: "SemiBold", 700: "Bold", 800: "ExtraBold"}[weight]
        # These are modified, subset-merged fonts. Rename to comply with OFL RFNs.
        for record in font["name"].names:
            if record.nameID in (1, 16):
                text = "DDetector " + family
            elif record.nameID in (2, 17):
                text = style
            elif record.nameID in (3, 4, 6):
                text = "DDetector" + family + "-" + style
            else:
                continue
            record.string = text.encode(record.getEncoding())
        font.save(output / f"{family}-{style}.ttf")
        print(f"Bundled {family} {weight}: {len(font.getBestCmap())} characters")
for source, target in [("fonts/luckystar.otf", "luckystar.otf"),
                       ("ui/Kagami.webp", "Kagami.webp"),
                       ("icons/icon-256.png", "icon-256.png")]:
    shutil.copyfile(args.source / "public" / source, output / target)

import re
icons = (args.source / "src/components/profile/ServiceIcon.tsx").read_text()
for provider, path in re.findall(r'(github|discord|spotify):\s*"([M][^"]+)"', icons):
    color = {"github": "#f0f0f4", "discord": "#5865f2", "spotify": "#1ed760"}[provider]
    (output / (provider + "-dark.svg")).write_text(f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="{color}"><path d="{path}"/></svg>\n')
