# Native DDetector (Qt6 Quick)

Native C++/QML application; **no WebView, WebEngine, HTML page, or JavaScript HTTP
client**. All implementation and generated build/screenshot files are under `qt/`.
The source website is not modified; the Go backend owns local library synchronization.

## Build and run

Requires CMake 3.21+, C++17, and Qt6 6.4+ with Quick, Quick Controls 2, Network, Multimedia,
Widgets, and Test (for the default test build). Unix builds also require the Qt6
QTermWidget development library (`qtermwidget6` CMake package) and its runtime
keyboard/color-scheme data. On Arch Linux this is the `qtermwidget` package. Provider icons use Qt's SVG image plugin;
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
also use the system browser. Covers use an asynchronous native image provider and a
128 MiB disk cache under Qt's CacheLocation/track-covers. OAuth and bridge credentials
are never sent to the public Yandex artwork CDN.

## Git workspace and embedded terminal

Settings → Git repository selects a local checkout. The Git page provides rounded
file-tree/editor cards, repository-relative breadcrumbs, atomic Ctrl+S saves, and
Save/Discard/Cancel protection for unsaved edits. The bottom terminal remains at
least 120 px tall. It uses QTermWidget on a real Unix PTY and starts the default
system shell (`SHELL`, then passwd, then `/bin/sh`) in the Git root. ANSI colors,
alternate-screen applications, Ctrl+C, selection and bracketed paste are supported.
Terminal commands are unrestricted local commands, not sandboxed. Stop the shell
and save edits before changing the checkout. Windows ConPTY is not implemented;
Windows shows an explicit unsupported-platform error rather than a pipe fallback.

Settings and Send share `DialogHeader.qml`. The player's music-note button opens
Send; see `UPLOADER.md` for the local file-update and commit rules.

## Standalone Yandex Music player

The bottom DDetector panel is native QML, styled after Spotify (not embedded
website components), and always uses `playerSurface: #0c0c0e`. In Settings, enter
a Yandex Music OAuth token; it applies automatically after an 800ms debounce or Enter.
The panel is hidden until app login/access and token validation succeed. Signing out
immediately stops playback and hides the panel; clearing the token does the same.
The token persists in the private Config.json described below.

Track metadata and download information are fetched with OAuth from
`api.music.yandex.net`; audio is played locally using Qt Multimedia. The token
is never sent to media hosts. There are no Ynison connections, device discovery,
remote-player controls, or shared playback sessions. DDetector is a local player
name, not a registered Ynison device. Previous/next traverse tracks played in
this session. Playback availability depends on the account and track rights.
Live authenticated streaming requires manual validation with a real token.

## Local Yandex library and lyrics

The Go backend retrieves the complete track ID list and added_at timestamps from
Supabase's ddetector_tracks with pagination. Metadata comes directly from
Yandex Music using the configured OAuth token, supplied through the authenticated
loopback bridge. Lyrics use public LRCLIB (syncedLyrics preferred, plainLyrics fallback),
then signed Yandex LRC/TEXT and supplement endpoints if needed. LRCLIB receives the
track title and artist and sees the client IP, but receives no OAuth credentials. Supabase also handles application login/access and ignore preferences,
but supplies no track titles, covers or lyrics. This is not an entirely offline application.

Cache: `<session-path>.yandex-<verified-Yandex-UID>.tracks-cache.json`, private atomic
0600 writes, scoped to app and Yandex accounts. Cached results appear before library
refresh; only unseen tracks need metadata/lyrics and detection. Confirmed missing lyrics
are cached too; transient errors are retried. Removed library IDs are pruned. Fetch refreshes
metadata, but reuses successful cached lyrics. LRC lyrics preserve timestamps for
click-to-play/seek; plain lyrics have no timestamps. First-run acquisition
requires network and is checkpointed as tracks finish. Legacy lyrics are upgraded once
through LRCLIB in the background, keeping old results visible and checkpointing per-track
source versions. To force another full rebuild, remove that account's cache while stopped.

## UI and behavior

- Dark-only, Spotify-style UI: black / `#0c0c0e` / `#171719` surfaces
  with `#2bfef5` accents. The palette is fixed regardless of system appearance;
  there is no theme selector or persisted theme preference.
- Bundled source-derived Nunito (600/700/800), Quicksand (600/700), Syne (700),
  plus the source Lucky Star font and login artwork. Latin/Cyrillic Nunito
  subsets are merged for native use. See `assets/NOTICE.md` for licensing.
- Desktop-only layout with a 48px single-row header and side-by-side track/results
  panels (40% / 60%). The minimum window size remains 1200×750; there are no
  mobile/tablet layouts, compact headers, or results sheets.
- Numbered 58px track rows, covers/fallbacks, `MM/dd/yyyy` dates, original badges
  (`…`, `found`, `clean`, `no lyrics`), ignored red states, and active selection.
- Default order is backend order. Date is newest first; A–Z uses a locale
  collator; artist sorting counts artists **after filtering**, as in the source.
  Search commits **only on Enter** and matches title, artist, or ID.
  Search/sort apply only to the track list, not result cards.
- Row click copies its ID and scrolls the matching result card into view.
  Cards support highlights,
  timestamps, timestamp suppression on unsynced tracks, expandable full lyrics,
  context-line dimming, Copy ID, and Yandex's `title artist download mp3` query.
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
- Settings use `appConfig`, a shared QObject storing `musicToken`, `hideIgnored`, `hideWindowControls`, and
  `volume` in `QStandardPaths::GenericConfigLocation/DDetector/Config.json` (normally
  `~/.config/DDetector/Config.json` on Linux). Existing nested Config.json is moved automatically. The Yandex token is
  plaintext, not encrypted: the directory is owner-only (0700), and atomic
  QSaveFile writes create an owner-readable/writable file (0600). Keep backups
  private. Safe persistence errors are exposed as `appConfig.error`; malformed
  configurations are preserved and block writes until repaired and the app restarted.
  Legacy Desktop.conf is removed and is no longer created or used.

  After app login/access verification, the saved token is applied to the player and
  local library backend automatically. Subsequent token changes update both.

## Smoke validation

The application always requires a loopback backend URL and private bearer key;
launch it through the Go backend. There is no standalone fixture launch mode.
UI tests and the smoke harness use `test-fixtures/state.json` with mock loopback
backends. This fixture is bundled only into the test executable, not the application.

```sh
python3 qt/smoke.py
```

`--smoke-test` exits after approximately 2.1 seconds. `--screenshot FILE` saves
before that exit; it does not itself auto-exit without `--smoke-test`.
`--width` and `--height` set initial window dimensions.

`smoke.py` explicitly starts a temporary test-only loopback backend with a
random key. It verifies the bearer on polls and captures desktop-sized, dark-only
library, login, access-denied, banned, access-checking, error, loading, and empty views in
`qt/build/screenshots/`. It never connects to production or starts OAuth.

Qt Test covers ID-preserving model reconciliation, unchanged polling, all bearer
endpoints, token-field exclusion, privilege-state clearing, filter/sort semantics,
JSON persistence/permissions/malformed-file protection, native QML loading, fixed dark
palette, full lyrics, selection/clipboard, desktop header/panel geometry and auth
styling retained at programmatically narrowed widths (without changing the window minimum),
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
