# Bundled source assets

Unless noted otherwise, these assets originate from `/home/admin/Documents/Git/Next-Music-Website`.
No assets are downloaded at application startup.

## Directory layout

- `icons/lucide/`: Lucide controls, including protected activity icons
  `lucide-music.svg` and `lucide-git-branch.svg`, whose contents remain unchanged.
- `icons/bearded/`: original file-tree SVGs.
- `icons/brands/`: provider logos.
- `icons/app/`: application PNG/ICO icons.
- `fonts/`: native TTF/OTF fonts.
- `images/`: artwork and track placeholder.
- `audio/`: notification sounds.
- `themes/`: terminal color schemes.

Licenses and provenance manifests remain in the asset root. Manifest filenames
and hashes are unchanged. CMake assigns flat `qrc:/assets/<filename>` aliases to
all bundled assets, preserving existing runtime URLs; filenames must be unique
across subdirectories. `../import_assets.py` exports into this layout.

## Editor menu icons (Lucide)

`lucide-undo-2.svg`, `lucide-redo-2.svg`, `lucide-scissors.svg`,
`lucide-trash-2.svg`, and `lucide-text-select.svg` were exported from the
installed `lucide-react` 1.52.0 package's `dist/esm/icons/` icon data.
In that version, `trash-2` aliases `trash` and `text-select` aliases
`square-dashed-text`. SVGs retain the standard 24px view box, 2px stroke,
and round line caps/joins. See `LUCIDE-LICENSE.txt` for the preserved
ISC and MIT license notices. The protected activity-bar Music and Git
branch assets are not changed by these exports.

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

## Access-denied artwork

`Kagami-access-denied.jpg` was downloaded unchanged from the user-specified URL:
https://get.wallhere.com/photo/illustration-anime-cartoon-Lucky-Star-angry-Hiiragi-Kagami-girl-finger-mangaka-threat-702683.jpg

Redistribution rights have not been verified. Verify permission before distributing
this artwork. It is bundled locally; no runtime download is required.

## Source artwork and Lucky Star font

`Kagami.webp` and `luckystar.otf` are copied unchanged from the
website's `public/` directory. Provider SVG paths are from `ServiceIcon.tsx`.

The Lucky Star font identifies itself as:
“Luckystar© dumadi. 2019. All Rights Reserved”. No redistribution license was
present in its font metadata. Source artwork redistribution rights have not
been independently verified either. These are included for the requested local
source-faithful implementation; **verify permission before distributing the
application**, or remove/replace these assets and accept the visual difference.

## Window controls and reproducibility

`lucide-window-minus.svg`, `lucide-window-maximize.svg`, and `lucide-window-x.svg`
are exported from the installed website `lucide-react` 1.52.0 icon data. Source package,
license/module SHA-256 hashes and generated SVG hashes are recorded in
`window-icons-provenance.json`. Run `import_assets.py --icons-only --check` with that
website checkout to verify them. Existing Lucide control SVGs and the protected
activity icons are not rewritten by this mode. Existing `LUCIDE-LICENSE.txt` notices
are retained.

The Bearded file-tree subset is covered by the bundled GPL-3.0 license text and
`bearded-provenance.json`; SVG bytes/colors remain unchanged. Consider GPL obligations
before redistribution. This inventory does not resolve permissions for the artwork
or Lucky Star font listed above.
