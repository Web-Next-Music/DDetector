# Native DDetector (Qt6 Quick)

Native C++/QML application; **no WebView, WebEngine, HTML page, or JavaScript HTTP
client**. All implementation and generated build/screenshot files are under `qt/`.
The source website is not modified; the Go backend owns local library synchronization.

## Spectral visualizer

Enable **Settings → Spectral visualizer** to show the spectrum behind the player
controls. It is off by default and the choice is saved as `visualizerEnabled`.
The 75-band analyser, peak motion, glow, bass pulse and gradients are ported from
`spotifast` (`src/vis.rs` and `src/ui/player_bar.rs`); colours derive from the app
accent, while the existing player background is unchanged. Audio comes from Qt's
pre-volume `QAudioBufferOutput`, so muting does not suppress the spectrum.
The source MIT notice is bundled in `assets/SPOTIFAST-LICENSE.txt`.

## Build and run

Requires CMake 3.21+, C++17, and **Qt6 6.9+** with Quick, Quick Controls 2, Network,
Multimedia and Widgets. `ContextMenu.menu` requires 6.9; `MultiEffect` requires 6.5.
The default test build also needs Qt Test/QuickTest and Python 3.
CMake finds `qmllint` beside this Qt installation's `qmake`, not an older tool on PATH. Unix builds also require the Qt6
QTermWidget development library (`qtermwidget6` CMake package) and its runtime
keyboard/color-scheme data. On Arch Linux this is the `qtermwidget` package. Provider icons use Qt's SVG image plugin;
the source WebP artwork needs its WebP image plugin. Install both runtime plugins on
deployment machines, not only the development machine.

From the `ddetector-go` root:

```sh
cmake -S qt --preset dev
cmake --build qt/build
cmake --build qt/build --target qml-lint
ctest --test-dir qt/build --output-on-failure --timeout 120
```

Presets are defined in `qt/CMakePresets.json`: `dev` is a Debug build with tests in `qt/build`,
`release` is an application-only Release build in `qt/build-release`. The release packaging
scripts and CI use the same `release` preset. `ccache` and `lld` are used automatically when installed.

## Application updates

The Qt client checks the latest stable release of `Web-Next-Music/DDetector` at startup
and every 30 minutes while running (except smoke tests). Scheduled checks are skipped
while an update check, download, or installation is in progress. A green Lucide download button appears above the account avatar
when a newer semantic version is available. Clicking it immediately starts downloading
and installing, without an application confirmation dialog. A circular progress ring
replaces the icon and fills clockwise; unknown progress rotates a partial arc. Status
and percentage appear in the tooltip; only failures use notifications. Availability,
up-to-date results, and successful installation do not produce toasts.
Finish Send and save workspace changes first. System installer/authorization prompts
remain platform-dependent. Development/unknown versions do not pretend to be newer
or older than published versions.

Installation follows the actual installation type: Windows launches the matching NSIS
installer with `/RESTARTAPP` and exits; the updated installer relaunches the installed
Go launcher after successful installation. Previously published installers without this
flag's implementation still require manual reopening until a new installer is released.
AppImage verifies and atomically replaces a safe writable image, then restarts through
AppRun and the Go launcher. Debian/Arch installations use `pkexec` with `dpkg`/`pacman`
and restart `/usr/bin/ddetector` only after a successful install. Launchers receive a
clean environment and no old Qt bridge credentials. Installation/authorization failure
does not restart; restart failure is reported with instructions for manual reopening.
Unsupported platforms/architectures, unmanaged installations in normal mode, unsafe
AppImage paths, and releases without a GitHub SHA-256 asset digest open the release page instead.
Downloads are streamed into private staging and size/hash-checked before installation.
The GitHub digest protects integrity, not against a compromised release publisher.
Windows installer staging is retained because the detached installer still needs it.

Only Debug builds compile the forced latest-release action and bundle `DevTools.qml` and
its Lucide flask icon. The avatar's shared account menu includes a “Dev menu” entry
above Settings, separated from it by a divider. Clicking or right-clicking the avatar
opens that menu; Release builds omit the dev entry and divider.
The update card contains a shared quote and both actions: “Force latest release” on
the left and “Check for updates” on the right. Force fetches real latest stable release
metadata and offers installation regardless of the current version (including the same
version or a development build). Clicking the download icon uses the real platform
installer; there is no simulated download. Ordinary checking resets forced mode and
only offers a newer version. On recognized Debian/Arch hosts, explicit forced mode
also permits installing the native DDetector package from an unmanaged development
checkout via `pkexec`; it does not replace the checkout's binary. Artifact validation,
SHA-256 checks, and AppImage safety remain mandatory. Release resources contain
neither the menu nor its icon.

Minimum versions of `qtermwidget` and `KF6SyntaxHighlighting` and the pinned packaging tools are
defined once in `packaging/deps.env`, which is read by CMake, `packaging/ci/build.sh` and CI.
The packaging tools there are pinned by SHA-256; if an upstream `continuous` build is replaced, the
checksum check fails and the new value must be written to `deps.env`.

### Windows application icons

Windows Qt builds embed `assets/icons/app/app-icon.ico` through `app-icon.rc`, in addition
to the runtime window icon. The ICO contains 16, 24, 32, 48, 64, 128 and 256 px
sizes. The Go launcher embeds the same icon automatically through the checked-in
`cmd/ddetector/app_icon_windows_{amd64,386,arm64}.syso` files; ordinary `go build`
needs no extra flags or resource compiler.

After changing the ICO, regenerate the launcher's resources with LLVM's `llvm-rc`
and `llvm-cvtres` installed:

```sh
python3 qt/generate_windows_resources.py
```

Both `ddetector.exe` and `ddetector-qt.exe` then contain an executable icon for
Explorer and shortcuts. Windows may cache old icons for existing pinned shortcuts.
This does not remove the Windows platform limitations described below.

### Native libraries

`KF6SyntaxHighlighting`, `Qt6Keychain`, and `libgit2 >= 1.5` (via pkg-config)
are required build dependencies. On Arch Linux install `syntax-highlighting`,
`qtkeychain-qt6`, `libgit2`, and `pkgconf`. The former optional-backend switches
are no longer supported; configuration fails when a required library is missing.
No dependency installation or runtime downloads occur.

* KSyntaxHighlighting supplies all language parsing, including Markdown, Shell,
  YAML, Python, JSON/JSONC, JS/TS and other installed definitions. The adapter
  preserves the editor's palette and changes layout formats only. Markdown
  fences use embedded-language definitions; token boundaries follow KDE's
  definitions rather than the former handwritten parser. Unknown `.txt` stays
  unformatted.
* QtKeychain uses asynchronous OS jobs for the **default application config**;
  explicit-path/in-memory configs remain isolated for tests. The `DDetector` service,
  `musicToken` key stores only the music credential, not Go OAuth or bridge secrets.
  Plaintext migration/removal happens only after successful storage of the exact
  current token. Pending writes, unavailable/locked services and shutdown retain
  an owner-only, atomically saved recovery copy. This is intentionally not a
  guarantee of zero plaintext while the OS service is unavailable. Insecure
  QtKeychain fallback is disabled. A newer edit/logout wins over an older read or
  write completion. A successful subsequent write removes the recovery field.
  Transient reads and writes retry up to three times with 1/2/4-second backoff;
  `Config::retryCredentials()` resets the retry budget. An absent credential
  field remains absent when unrelated settings change.
* libgit2 discovers working trees and reads status/index on QtConcurrent workers.
  Repository generation checks discard stale results. Library errors are reported
  without replacing the last successful status. Only status in repositories
  configuring external `filter.*` drivers uses the bounded asynchronous Git CLI,
  because libgit2 does not execute those drivers. Mutations remain Git CLI
  operations to preserve hooks and filters. Library calls cannot be forcibly
  interrupted; unusually slow filesystems may delay a worker and process exit.

Tests cover library highlighting, the migration persistence contract, status,
renames, corrupt-index recovery and existing uploader behavior. They do **not**
exercise a real OS keychain's unlock dialog/service failures; validate migration,
logout and restart against the deployment keychain before redistribution.

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

Settings → Git repository selects a local checkout. The Git page provides split
file-tree/editor panes, a rounded editor frame, repository-relative breadcrumbs, atomic Ctrl+S saves, and
Save/Discard/Cancel protection for unsaved edits. The bottom terminal remains at
least 120 px tall. It uses QTermWidget on a real Unix PTY and starts the default
system shell (`SHELL`, then passwd, then `/bin/sh`) in the Git root. ANSI colors,
alternate-screen applications, Ctrl+C, selection and bracketed paste are supported.
Terminal commands are unrestricted local commands, not sandboxed. Stop the shell
and save edits before changing the checkout. Windows ConPTY is not implemented;
Windows shows an explicit unsupported-platform error rather than a pipe fallback.

Settings and Send share `qml/Components/DialogHeader.qml`. The player's music-note
button opens Send; see `UPLOADER.md` for the local file-update and commit rules.

## Standalone Yandex Music player

The bottom DDetector panel is native QML, styled after Spotify (not embedded
website components), and always uses `playerSurface: #0c0c0e`. In Settings, enter
a Yandex Music OAuth token; it applies automatically after an 800ms debounce or Enter.
The player panel is always visible, including before login and without a Yandex
token. Without access/token it shows a disabled empty-track placeholder; clearing
the token or signing out stops playback without removing the panel.
The token uses QtKeychain when available, with a private recovery copy during
migration or backend failure as described below.

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

Cache: `<session-path>.yandex-<verified-Yandex-UID>.tracks-cache.sqlite`, private
0600 transactional storage, scoped to app and Yandex accounts. Legacy JSON is
migrated on first load and retained as a rollback copy; see the root `README.md`. Cached results appear before library
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
- The right-side activity bar switches between Lyrics and Git. Fetch in the header
  is disabled without access or during loading/fetching. The account avatar opens
  Settings / Sign out; right-clicking the header opens the same menu.
  The avatar uses HTTPS `avatar_url` or `picture` public metadata, falling back
  to the account name's initial if missing or unavailable. Names use full_name,
  name, preferred_username, or email. Images use native loading without the
  backend bearer key; fetching an avatar contacts its provider.
- Settings is a real dialog with a persisted hide-ignored control and player settings.
  Right-clicking the header also opens the account menu.
- Login is a modal over the disabled desktop shell, with bundled Kagami artwork,
  GitHub/Discord/Spotify buttons and browser-based OAuth; no website navigation or footer.
  Access errors/denials use a separate artwork modal.
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
- Settings use `appConfig`, a shared QObject storing `musicToken`, `hideIgnored`, `hideWindowControls`,
  `approveSoundEnabled`, and `volume` in `QStandardPaths::GenericConfigLocation/DDetector/Config.json` (normally
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
colors with rounded highlight rectangles and horizontal padding. Result-card covers
use a rounded shader mask on GPU renderers; other cover containers retain rectangular
image clipping. On the software renderer shader masks/shadows are disabled, keeping
modal contents readable instead of rendering an empty masked layer. Some icon/menu
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

This optional helper never reads source `.env` files or downloads fonts. For deterministic
font output use `fonttools==4.66.1` and `brotli==1.2.0` in an isolated Python environment.
It requires the website's cached **variable** WOFF2 subsets and installed
`lucide-react` **1.52.0**. Input/tool/output hashes are recorded in `import-manifest.json`.
Generation is fully validated before publication; each file is replaced atomically,
but publication is not a multi-file filesystem transaction.

```sh
# Validate without changing either checkout:
python3 qt/import_assets.py /path/to/Next-Music-Website --dry-run
# Generate and subsequently verify a separate reproducible output set:
python3 qt/import_assets.py /path/to/Next-Music-Website --output /tmp/ddetector-assets
python3 qt/import_assets.py /path/to/Next-Music-Website --output /tmp/ddetector-assets --check
# Only the three new window-control SVGs and their provenance:
python3 qt/import_assets.py /path/to/Next-Music-Website --icons-only --check
```

`--check` compares regenerated bytes and the manifest; it does not claim older bundled
font bytes were produced with the current deterministic timestamp policy. Neither
mode emits Bearded artwork or the protected `lucide-music.svg` / `lucide-git-branch.svg`.
Window SVGs use separate `lucide-window-*.svg` filenames; existing control assets are
not regenerated by `--icons-only`. Licenses/artwork are not deleted or relicensed.
See `assets/NOTICE.md` for unresolved redistribution permissions.

## Shared UI and regression coverage

`SiteButton` centralizes icon/text, disabled/pressed/hover and visible keyboard-focus
states. `SiteTextField` and `SiteToggleRow` share field and setting-row behavior.
`WindowButton` and `NotificationStack` are extracted from Main; native entry URL and
context-object names remain unchanged. Main remains the shell/state coordinator.
The permanently zero-height legacy title bar is removed; the 48px header still handles
window dragging/maximizing. Window controls are installed-library Lucide exports.

Secondary text is intentionally `#999999` instead of the website's `#666666` (about
6.3:1 on `#171719`); meaningful dates and empty messages no longer have extra opacity.
This is a contrast correction, not a new theme. Surfaces, accent, artwork and layout
are preserved. Metadata/messages use PlainText; only escaped lyrics and the fixed
logo use rich text.

Track rows support Enter/Space and Up/Down; Menu/Shift+F10 opens track actions.
A lyrics card is a single keyboard stop for its lines: Up/Down selects a line,
Enter/Space plays its valid timestamp, and Menu/Shift+F10 opens actions. Its header
buttons remain standard keyboard controls. Focus is distinct from track selection.
Tooltips also appear on focus; Qt Dialog restores focus to the opener.

CTest includes `qml-controls` (Qt Quick Test), `ui-harness` (Python unit tests), and
`ui-smoke` in addition to native tests. Smoke uses fresh temporary settings/cache,
explicit checks even under Python -O, authenticated polling, PNG CRC/dimension/
nonblank checks, and distinct scenario hashes. Full stderr is retained with keys
redacted; only the specific FFmpeg initialization and missing NVIDIA VDPAU-library
messages are allowed. Unknown driver errors and QML warnings still fail the run.
Screenshots are sanity checks, not a pixel-perfect or screen-reader test.

`qml-lint` is strict for the new standalone controls and reports all application QML.
Application warnings from runtime context objects/native registrations remain visible;
fully strict application lint needs native `.qmltypes` metadata. `qt_add_resources`
uses a single APP_QML list for app/UI tests. Migration to `qt_add_qml_module` is deferred
until native registration and qrc entry URLs can be coordinated safely.

`.github/workflows/qt-ui.yml` performs a clean Release build/lint/CTest, including smoke,
on rolling Arch Qt6 and uploads diagnostics. This is not yet a Qt 6.9 minimum-version
matrix or a deployment-package test. GPU effects, live playback/OAuth, large-document
latency and screen-reader behavior need separate desktop validation. Lyrics still use
per-card Repeaters and the editor still indexes full text; no unmeasured performance
improvement is claimed.
