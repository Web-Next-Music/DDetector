# Bundled source assets

These assets originate from `/home/admin/Documents/Git/Next-Music-Website`.
No assets are downloaded at application startup.

## Native font derivatives (SIL Open Font License 1.1)

The website's cached Google Fonts WOFF2 subsets were instantiated at the used
weights, merged (including Cyrillic for Nunito), and converted to TrueType with
OpenType layout tables preserved. Their native family names are prefixed with
`DDetector` because these are modified fonts, respecting reserved font names.
The designs remain Nunito, Quicksand, and Syne, not substitute system fonts.

- Nunito: Copyright 2014 The Nunito Project Authors
  (https://github.com/googlefonts/nunito)
- Quicksand: Copyright 2019 The Quicksand Project Authors
  (https://github.com/andrew-paglinawan/QuicksandFamily.git),
  with Reserved Font Name "Quicksand"
- Syne: Copyright 2019 The Syne Project Authors
  (https://gitlab.com/bonjour-monde/fonderie/syne-typeface)

See `OFL-1.1.txt` for the complete license.
Use `../import_assets.py` to reproduce the conversions from the source build.

## Lucide icons

`lucide-*.svg` are exported from the website's installed `lucide-react` 1.52.0
icon data, using the same Lucide library rather than substitute glyphs.
Qt renders the SVGs natively; React is not needed at runtime.
See `LUCIDE-LICENSE.txt` for ISC and applicable Feather MIT notices.

## Source artwork and Lucky Star font

`Kagami.webp`, `icon-256.png`, and `luckystar.otf` are copied unchanged from the
website's `public/` directory. Provider SVG paths are from `ServiceIcon.tsx`.

The Lucky Star font identifies itself as:
“Luckystar© dumadi. 2019. All Rights Reserved”. No redistribution license was
present in its font metadata. Source artwork redistribution rights have not
been independently verified either. These are included for the requested local
source-faithful implementation; **verify permission before distributing the
application**, or remove/replace these assets and accept the visual difference.
