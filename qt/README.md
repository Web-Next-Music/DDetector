# Native DDetector (Qt6 Quick)

Native C++/QML application; **no WebView, WebEngine, HTML page, or JavaScript HTTP
client**. All implementation and generated build/screenshot files are under `qt/`.
The source website and Go code are not modified.

## Build and run

Requires CMake 3.21+, C++17, and Qt6 6.4+ with Quick, Quick Controls 2, Network, Multimedia,
and Test (for the default test build). Provider icons use Qt's SVG image plugin;
the source WebP artwork needs its WebP image plugin. Both are installed here.

From the `ddetector-go` root:

```sh
cmake -S qt -B qt/build -DCMAKE_BUILD_TYPE=Release
cmake --build qt/build -j 4
ctest --test-dir qt/build --output-on-failure
```

For an application-only build, add `-DBUILD_TESTING=OFF` to configuration.

Launch through the existing Go executable, with its normal Supabase environment
configuration and private session file:

```sh
go run ./cmd/ddetector --qt ./qt/build/ddetector-qt
```

The existing launcher passes `--backend-url http://127.0.0.1:PORT --bridge-key KEY`.
The Qt executable rejects missing keys and non-loopback/backend URLs. Do not
paste the real key into logs or issue reports. It is retained in C++ only, not
registered in QML, saved in QSettings, or included in application logging.
Application-login OAuth access/refresh tokens stay in Go. Go normally opens the provider login in
the system browser; C++ handles the `browser_opened:false` fallback without
forwarding the authorization URL to QML. External site links and Yandex searches
also use the system browser. Covers load from their backend-provided URLs via
Qt's native image loader, not with the bridge bearer key.

## Standalone Yandex Music player

The bottom DDetector panel is native QML, styled after Spotify (not embedded
website components), and always uses `playerSurface: #0c0c0e`. In Settings, enter
a Yandex Music OAuth token and select **Apply token**. Controls and the green
**Play** context-menu action stay disabled until `/account/status` succeeds.
The password field clears after applying; the token stays in C++ memory only
and must be entered again after restart. **Disconnect** clears it and playback.

Track metadata and download information are fetched with OAuth from
`api.music.yandex.net`; audio is played locally using Qt Multimedia. The token
is never sent to media hosts. There are no Ynison connections, device discovery,
remote-player controls, or shared playback sessions. DDetector is a local player
name, not a registered Ynison device. Previous/next traverse tracks played in
this session. Playback availability depends on the account and track rights.
Live authenticated streaming requires manual validation with a real token.

## UI and behavior

- Dark-only, Spotify-style UI: black / `#0c0c0e` / `#171719` surfaces
  with `#2bfef5` accents. The palette is fixed regardless of system appearance;
  there is no theme selector or persisted theme preference.
- Bundled source-derived Nunito (600/700/800), Quicksand (600/700), Syne (700),
  plus the source Lucky Star font and login artwork. Latin/Cyrillic Nunito
  subsets are merged for native use. See `assets/NOTICE.md` for licensing.
- 60px desktop header and equal-width track/results panels; 96px two-row header
  below 950px (utilities/avatar first, search/sort/fetch second),
  full-width track list, and animated 60px-peek results sheet at **<=600px**.
- Numbered 58px track rows, covers/fallbacks, `MM/dd/yyyy` dates, original badges
  (`…`, `found`, `clean`, `no lyrics`), ignored red states, and active selection.
- Default order is backend order. Date is newest first; A–Z uses a locale
  collator; artist sorting counts artists **after filtering**, as in the source.
  Search commits **only on Enter** and matches title, artist, or ID.
  Search/sort apply only to the track list, not result cards.
- Row click copies its ID and scrolls the matching result card into view; on
  narrow screens it opens the results sheet. Cards support highlights,
  timestamps, timestamp suppression on unsynced tracks, expandable full lyrics,
  context-line dimming, Copy ID, and Yandex's `title artist скачать mp3` query.
- Track/card context menus: Yandex, copy, ignore/unignore; full lyrics appears
  only for unsynced result cards.
- Right-side utility buttons reload data and open Settings;
  all have tooltips and accessible names. Reload is disabled without access or
  during loading/fetching. A separate account avatar opens Settings / Sign out.
  The avatar uses HTTPS `avatar_url` or `picture` public metadata, falling back
  to the account name's initial if missing or unavailable. Names use full_name,
  name, preferred_username, or email. Images use native loading without the
  backend bearer key; fetching an avatar contacts its provider.
- Settings is a real dialog with a persisted hide-ignored control and player settings.
  Right-clicking the header also opens the account menu.
- Login is a plain centered DDetector page with direct GitHub/Discord/Spotify
  buttons and browser-based OAuth: no 404, artwork, website navigation or footer.
  The compatibility auth popup remains available, without artwork.
  Access checking, errors, access denial and bans have separate messages;
  authenticated users retain Settings and Sign out even without access.
- Go remains the authority for authenticated/accessChecked/hasAccess/isBanned,
  loading/fetching/processing, annotations, and all mutations. The Qt layer never
  performs access elevation or annotation detection.
- Native HTTP calls: bearer on every GET `/api/state` and POST `/api/login`,
  `/api/logout`, `/api/load`, `/api/fetch`, `/api/ignore`. Redirects are not followed
  with the bearer key. Polls are serialized at 750ms; changed tracks are reconciled
  by ID using insert/move/remove/dataChanged, never whole-model resets. Unchanged
  state/model data does not emit change notifications.
- hideIgnored uses QSettings. Demo uses a separate settings namespace.

## Explicit demo and smoke validation

The demo is enabled **only** by `--demo`; it loads `assets/demo.json`, makes no
backend requests, and cannot grant or bypass real backend privileges. Ignore
changes are local in demo; login/load/fetch/logout are no-ops there.

```sh
./qt/build/ddetector-qt --demo
QT_QPA_PLATFORM=offscreen QT_QUICK_BACKEND=software ./qt/build/ddetector-qt \
  --demo --smoke-test --screenshot qt/build/desktop.png
QT_QPA_PLATFORM=offscreen QT_QUICK_BACKEND=software ./qt/build/ddetector-qt \
  --demo --width 390 --height 740 --smoke-test --screenshot qt/build/mobile.png
python3 qt/smoke.py
```

`--smoke-test` exits after approximately 2.1 seconds. `--screenshot FILE` saves
before that exit; it does not itself auto-exit without `--smoke-test`.
`--width` and `--height` set initial window dimensions.

`smoke.py` explicitly starts a temporary test-only loopback backend with a
random key. It verifies the bearer on polls and captures dark-only desktop, mobile,
login (desktop and 320px mobile), compact header, access denied,
banned, access-checking, error, loading, and empty views in
`qt/build/screenshots/`. It never connects to production or starts OAuth.

Qt Test covers ID-preserving model reconciliation, unchanged polling, all bearer
endpoints, token-field exclusion, privilege-state clearing, filter/sort semantics,
QSettings, native QML loading, fixed dark palette/mobile/full-lyrics/selection/clipboard,
direct login/auth modal, account menu, Settings/hide-ignored interactions,
public metadata filtering (including rejection of local-file avatar URLs),
and duplicate-provider-login prevention. To capture interaction
screenshots too (create the directory first):

```sh
DDETECTOR_TEST_SCREENSHOT_DIR=/home/admin/Documents/Git/ddetector-go/qt/build/screenshots \
  QT_QPA_PLATFORM=offscreen QT_QUICK_BACKEND=software ./qt/build/ddetector-qt-tests
```

## Fidelity boundaries

This is a native recreation, not a browser pixel-for-pixel renderer. Qt's text
shaping, native scrolling/menu placement, synthesized typography where no exact
weight exists, and antialiasing differ. Rich-text marks reproduce the source
colors but not CSS's rounded mark corners/3px padding exactly. Album artwork is
clipped rectangularly inside the rounded cover containers. Some icon/menu
ornamentation, CSS entry/shadow/hover animations, footer distribution and the
seasonal remote-logo check are simplified. The original source has no defined
wave styles for its 404 wave markup, so that invisible decoration is omitted.
Other website pages are opened externally, not recreated in this client.

Real provider login, privileged Supabase fetches, and external-browser launching
are not end-to-end validated by offscreen/mock tests. They require configured Go
backend credentials, a user with DDetector access, and a desktop browser session.

To regenerate assets after building the source website (not needed to build Qt):

```sh
python3 qt/import_assets.py /home/admin/Documents/Git/Next-Music-Website
```

This optional helper requires Python `fonttools` and `brotli`, and never reads
source `.env` files or downloads fonts.
