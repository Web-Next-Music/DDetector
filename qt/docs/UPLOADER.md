# Local Send backend

`uploader.h/.cpp` are integrated into the native application through `main.cpp` and `qml/Library/SendDialog.qml`. Reference directories were inspected read-only and were not modified. The backend needs Qt Core/Network, Git, a writable local FckCensorData checkout with an existing HEAD, and a configured Git author identity. It needs neither FCDServer nor a Yandex token. It never pushes or fetches.

## Git library boundary

The workspace optionally uses libgit2 for asynchronous read-only discovery and
status/index snapshots. Repositories with configured external filters and library
errors retain Git CLI fallback. The uploader's staging, commits and rollback
remain the existing Git CLI implementation: hooks, clean/smudge filters and the
transaction contract are not replaced by libgit2. Git remains a runtime dependency.
See `README.md` for backend flags and credential migration limitations.

## Verified reference behavior

Read-only inspection sources:

- `/home/admin/.config/next-music/Addons/FckCensor Uploader/script.js` and `styles.css`
- `/home/admin/Documents/AppData/FCDServer/server.js`
- `/home/admin/Documents/Git/Next-Music-Website/src/app/ddetector/page.tsx`, `page.module.scss`, and `src/app/globals.scss`

Uploader modal fields: editable artist (comma-separated; initially `track.artistNames.join(", ")`), editable title, optional MP3 URL. ID and 64×64 cover are display-only. Empty title retains the original track title in the addon; empty artist becomes an empty array and ultimately `Unknown` at the server. The URL is trimmed. Empty URL means list-only, not a Yandex audio download.

Original operations, in order:

1. If URL is nonempty: fetch the whole response; HEAD `/tracks/:id` to choose saved/overwritten status; PUT the raw bytes to `/tracks/:id`.
2. POST `/list/:id` with JSON `{title, artist}`. Audio is stored as `tracks/<id>`, **without `.mp3`**.
3. Upsert `list.json`'s `tracks[id]` to `https://raw.githubusercontent.com/Hazzz895/FckCensorData/refs/heads/main/tracks/<id>` using text edits. No title/artist/cover is stored in the list.
4. If the URL entry is new or different: append `- [<title> - <artist>](https://music.yandex.ru/track/<id>)` to README, then commit with message `add track «<title> - <artist>»`.
5. If the existing URL is equal: return `skipped`; do not append README or commit, **even if audio was saved/overwritten**. Metadata-only edits therefore do not change README for an unchanged URL.

The server uses `git add --sparse .` and shell-interpolated commit text, catches Git failures and still returns the list action. The local backend intentionally does not copy these unsafe behaviors. Neither implementation pushes. The addon shows success then removes its modal after 1500 ms.

The local backend follows the v2-only publication rules used by
`internal/bridge/censor_native_replacement.go`, rather than the legacy list write
in step 3 above. Send never reads, creates, writes, stages, or commits `list.json`;
existing legacy bytes and staged/unstaged legacy edits are left untouched. A
legacy-only checkout gets a new `list_v2.json` for the sent track; Send does not
migrate the legacy catalog.

## README publication

The legacy server bullet behavior above is historical. Local Send follows the HTML
tracks table in `/run/media/admin/DATA/FckCensorData/README.md`: within
`## Автоматически подменяемые треки`, it appends a three-cell row before
`</tbody>`, or replaces the row linking the same track ID. Existing rows, other
sections (including artists/albums), BOM, surrounding whitespace and line endings
are preserved. Skipped sends leave README untouched.

The title links to the Yandex track and uses the separate Send title label.
Send passes its already available original/player metadata as the optional sixth
`publicationMetadata` argument, separately from the fifth argument's explicit v2
metadata patch. README generation uses that snapshot as a base; fields in the first
source-level `tracks[id]` object in the updated v2 list take precedence, including
metadata overrides from this send. Publication metadata is never persisted to the
v2 list. This avoids losing covers and artist IDs when override editing is disabled
or only some override fields exist.
HTTP(S) covers render at 40×40 pixels; scheme-less covers gain `https://` and
`%%` becomes `100x100`. Missing/unsafe covers leave an empty cell. Artist names
link using their string or numeric IDs; missing/invalid IDs render plain names.
Without artist metadata, the separate Send artist label renders as plain text
(no guessed IDs). Text and attributes are HTML-escaped. No metadata lookup or
image download is performed. Legacy/custom READMEs without an identifiable
tracks table and closing tbody retain the bullet-append fallback rather than
converting existing content or modifying an unrelated table.

## Local API and exact rules

```cpp
Uploader uploader; // No default checkout: configure explicitly.
uploader.setDirectory("/absolute/canonical/path/to/FckCensorData");
```

QML entry point: `send(id, title, artist, mp3Url = "", metadata = {})`. `busy`, `status`, `error`, and `action` notify via `stateChanged`; `completed(action)` is emitted only after successful completion. Actions are `added`, `updated`, `skipped`. A Git failure can leave `action` populated: check `error`, not `action`, for success. While busy, additional sends are ignored. There is no cancellation API; destruction waits for the worker to finish, so shutdown can wait for bounded download/Git operations.

- IDs must be 1–32 ASCII digits, passed as strings. This prevents traversal and option/path injection.
- Title is trimmed, falling back to ID if empty. To match the addon UI, pass the original title when the edited title is blank.
- Artist is split on commas, trimmed, empties discarded, joined with `, `; empty becomes `Unknown`. Title/artist must have no embedded CR/LF or NUL after normalization.
- HTTP/HTTPS only, with no URL user credentials. Normal TLS verification and no-less-safe redirects apply. The whole download has a 30-second deadline and a 128 MiB limit. Empty bodies and non-2xx responses fail. Bytes are saved without transcoding. When creating a new `trackIds` entry, ffmpeg must successfully decode readable audio before any target-file writes; other mapping/replacement/skip paths retain their existing audio handling.
- Each filesystem write uses QSaveFile without direct-write fallback. The operation is **not a multi-file transaction**: later filesystem/Git failures leave earlier changes available for inspection. No automatic reset, rollback, cleanup, or retry discards user work.
- The checkout must exist, be the canonical repository root, and have HEAD. Symlink targets and a symlink `tracks` directory are rejected. A cooperative `.ddetector-upload.lock` prevents other instances of this backend from writing concurrently.
- Rejects preexisting staged, unstaged, or untracked changes in `list_v2.json`, `README.md`, or this track's audio path, and rejects merge/rebase/cherry-pick/revert state. Other paths, including `list.json`, may be dirty or staged. Unsaved workspace edits to these same upload targets also block Send.
- Parses/validates v2 JSON before downloading or saving audio. The root must contain a `sources` array. Reads each source's `tracksStorages`, resolving explicit `tracks` entries (strings, numeric template values, or objects with `url`) and templated `trackIds` (strings, numbers, or objects with `id`). Templates must contain `%%`; HTTP(S) URLs without user credentials are required. Malformed relevant structures or conflicting URLs for the same ID fail without changing files.
- New tracks always target the unique source with exactly `supportedVersions: ">=2.0.0"`, creating it if absent; duplicate matching sources fail closed. Reuse its compatible template storage, appending a `{ "id": <numeric ID>, "durationMs": <measured milliseconds> }` `trackIds` entry (or `tracks[id] = id` for template-map storage). If absent, create a storage there using a compatible existing URL template, without copying its IDs or changing legacy sources. Without a compatible template, create a direct canonical URL mapping in the target source. Conflicting compatible templates or multiple compatible storages within one source fail without writing. Plain sends never create source-level metadata entities. Existing replacements remain in their original sources/version gates: matching entries are removed from their old storage and an explicit `{ "url": "..." }` entry is appended to that source's storages. Unknown entry fields are retained; stale `id` and `durationMs` are removed from the replacement.
- New `trackIds` durations come from actual audio, never source metadata or container duration tags. The existing ffmpeg runtime dependency decodes the first audio stream to 48 kHz mono signed 16-bit PCM; byte/sample count is truncated to integer milliseconds. Startup is bounded to 3 seconds and decoding to 60 seconds, with no new dependency. Downloaded bytes are decoded via an automatically removed temporary file outside the checkout. With an empty URL, the existing local `tracks/<id>` is decoded instead. Missing, invalid or unreadable audio, ffmpeg failure and timeout fail before target-file writes or staging. Existing numeric/string/object entries are not converted on skipped sends.
- Unknown root/source/storage fields and unrelated tracks are retained. Surgical JSON edits preserve unchanged subtrees, BOM, surrounding whitespace, and existing line endings; changed containers may be compactly serialized. A missing v2 list is created; an existing empty/invalid list fails. If all existing mappings already resolve to the canonical URL, the original list bytes, README and HEAD remain unchanged (the existing skipped-audio behavior still applies).
- Only `list_v2.json`, `README.md`, and audio saved by this call are passed to `git add --sparse -- <paths>`, then `git commit --only -m <message> -- <paths>`. Unrelated staged work remains staged and is not included in the upload commit. Skipped entries do not stage audio.
- Git uses QProcess argument arrays, never a shell; inherited `GIT_*` repository/index overrides are removed. Git startup is limited to 3 seconds and each command to 15 seconds; timed-out processes are killed. Hooks and automatic commit signing are disabled to avoid interactive/arbitrary hook execution. Git author identity must already be configured. Errors are surfaced, not silently reported as success.
- Use a trusted checkout: Git configuration/attributes can configure external filters. The cooperative lock does not stop manual Git or filesystem changes by other processes. Do not edit this checkout concurrently with a send. Downloading contacts only the supplied URL/its redirects; it may reveal the client's IP and any query-string credentials to those hosts.

## Integration reference

The application already exposes `localUploader`, persists `Config.repositoryPath`, resolves its Git root using `GitWorkspace`, and opens `qml/Library/SendDialog.qml` from the player Send button. The following notes describe the underlying integration API, not outstanding setup steps.

1. The application target already includes uploader sources and Qt Network.
2. The runtime context object is **`localUploader`**, passed into `SendDialog.uploadBackend`.
   QML consumes only `busy`, `error`, `status`, `send(...)`, `stateChanged` and `completed`;
   no new native API is required by the shared UI controls.
3. Send is opened from the player's music-note button, not a track-menu entry.
   `openTrack(id, title, artist, cover)` snapshots the metadata; `artist` accepts an array
   or string. ID and cover are display-only; artist/title/MP3 URL remain editable.
4. The dialog submits through its injected backend:

   ```qml
   dialog.uploadBackend.send(dialog.trackId,
                             titleInput.text.trim() || dialog.originalTitle || dialog.trackId,
                             artistInput.text, urlInput.text.trim())
   ```

5. Fields, submit and close controls are disabled while busy; the busy dialog ignores
   Escape/outside clicks. Successful `completed` closes immediately and emits `sent` to
   the shell notification stack. Error/status remain visible on failure. Cancel before
   submit closes only the modal; there is no cancellation of an in-progress send.
6. The backend does not need or expose credentials and does not download from MusicPlayer. The application persists the checkout in Config's `repositoryPath`; it can be changed when the workspace has no unsaved changes, its console is stopped, and no Send is running. UI access gating follows `appUi.privileged`, but is not a filesystem security boundary.

## Styling findings

The website does not contain a Send/upload modal in the inspected DDetector page. Use its tokens, rather than inventing a website modal reference:

| Token | Light | Dark |
| --- | --- | --- |
| background | `#f9f9f9` | `#000000` |
| surface | `#fdfdfe` | `#0c0c0e` |
| secondary surface | `#f1f1f3` | `#171719` |
| text | `#111116` | `#f0f0f4` |
| muted | `#666` | `#666666` |
| accent | `#00c9c0` | `#2bfef5` |

Website base font is Nunito, heading font Quicksand, logo font Syne. Small/medium/large radii are 8/10/12 px. Borders use translucent neutral HSL via oklab color-mix. Accent surfaces use 8% accent and accent borders 20%. The DDetector page uses a 60 px header, 18 px bold heading, 12 px bold lyric text (1.55 line-height), and 10 px monospaced timestamps.

The addon modal itself uses Nunito; black 60% overlay; 400 px box, 20 px padding, 12 px radius, 10 px gap; 64 px cover with 8 px radius; inputs with 10 px padding and 8 px radius; 13 px status; buttons with 8 px padding/radius. Its colors are Yandex `--ym-*` theme variables, not website constants. For local QML reuse `SiteDialog`, `DialogHeader`, `SiteButton`, and `SiteTextField`,
with bundled `DDetector Nunito`/heading fonts rather than loading Google Fonts.
Qt intentionally raises secondary text to `#999999` for contrast; the table above
records the website reference, not this accessibility adjustment.

## Standalone validation

No changes to the existing application CMake file are required to test this backend:

```sh
cmake -S qt/uploader-tests -B /tmp/ddetector-uploader-build
cmake --build /tmp/ddetector-uploader-build -j2
ctest --test-dir /tmp/ddetector-uploader-build --output-on-failure
```

Tests require ffmpeg and generate tiny valid MP3 fixtures locally. They use temporary repositories and loopback HTTP, never the real FckCensorData checkout. Duration coverage includes local/downloaded audio versus differing source metadata, missing/invalid audio with no writes or index/HEAD changes, matching/conflicting duration patches, legacy numeric preservation, and title edits alongside measured storage durations. They cover v2 add/update/skip, explicit/template/object mappings, version-gate and unknown-field preservation, unchanged legacy bytes/index (and no legacy creation), malformed/conflicting v2 rejection before download, BOM/newline preservation, exact commit paths/text containing shell metacharacters, unrelated staged-index preservation, dirty/concurrently changed target rejection, traversal/symlink rejection, surfaced commit failure, audio save/overwrite on skipped entries without a commit, committing audio for new entries, and non-HTTP URL rejection.

## Optional Send metadata (Qt)

The Send dialog keeps **README / commit labels** separate from entity metadata.
Expand **Edit metadata · optional**, check individual overrides, then change their
values. Prefilled, unchanged inputs and unchecked fields are omitted; disabling
the section sends an empty patch. Opening another track resets all opt-ins, except
for the duration default described below.
Existing source-level metadata is prefilled where available; display labels and
thumbnail URLs are never automatically copied into overrides.

The duration placeholder shows the decoded replacement audio duration, not the
Yandex Music duration. `requestDuration(id, mp3Url)` performs bounded HTTP(S)
download and the same ffmpeg measurement as Send on a worker thread; without a URL
it measures local `tracks/<id>`. It emits `durationReady(request, durationMs)`
(`-1` on failure) without writes or changing Send's busy/error state. Unknown
replacement duration displays `Unknown` in the placeholder. With an empty URL,
Send is list-only: missing or undecodable local audio does not block it, and new
storage entries omit `durationMs` when measurement is unavailable. Existing local
audio is never attached or overwritten by a list-only send. With a supplied URL,
download validation and required duration decoding retain their existing behavior.
URL edits invalidate the old
result and debounce a new request; stale results and results after manual duration
edits cannot replace user input. Previewing a URL downloads the audio; Send downloads
it again so its normal validation and concurrency checks remain authoritative.
When a known replacement duration differs from a valid Yandex Music duration,
both duration override and Edit metadata default to enabled. The measured value
is included even if it equals the existing metadata baseline. Users can disable
either toggle to omit it. Other overrides and placeholders are unchanged.

Supported overrides:

- `title`: nonempty, single-line text, at most 4096 characters.
- `artists`: 1–100 real artist IDs plus names. Enter `123: Name; 456: Other name`.
  IDs use WebHost's `[A-Za-z0-9_-]{1,128}` contract and must be unique. This is
  manual ID entry, not an artist lookup service; the app cannot verify identity.
  Artist objects already present on the track are matched by ID and retain their
  other fields. An existing `__fckCensor.insertionIndex` is preserved; otherwise
  artist edits set it to `null` to prevent automatic artist-profile insertion.
- `coverUri`: HTTP(S) address, or scheme-less host/path. HTTPS prefixes are
  explicitly removed on save to match the schema; `%%` image-size placeholders
  are supported. No cover uploads or downloads are performed.
- `durationMs`: non-negative integer milliseconds, up to JavaScript's maximum
  safe integer. Consumer WebHost metadata projection may not expose duration in
  every view; the source entity stores it without changing audio duration.

Structured API example (the final argument is a QVariantMap / QML object):

```js
uploader.send("123", "README title", "README artist", "", {
    title: "Custom title",
    artists: [{id: "42", name: "Artist"}],
    coverUri: "example.org/art/%%",
    durationMs: 180000
})
```

An empty patch preserves the original audio/list-only path exactly. Nonempty
patches merge only supplied fields into `sources[i].tracks["originalId"]`, never
storage `tracks`. Null, empty text, empty artists, unknown patch keys and invalid
values fail validation before downloads or target-file writes. There is no
metadata deletion/reset API. `trackMetadata(id)` is a read-only best-effort
prefill from the first source-level track object, not a full version-aware
consumer simulation.

Placement deliberately fails closed: edits require a unique source with a
compatible audio mapping after routing (which may create its storage). Multiple matching sources or metadata for that
ID in any other source are rejected. A `durationMs` patch is also rejected if the ID already has a conflicting storage duration (before routing); matching values are allowed; title, artist and cover edits remain allowed. When this send creates a new measured storage duration, an explicitly requested metadata duration must equal the measured value or the send fails before writes. Existing source metadata durations are not used to calculate audio duration and are retained unless explicitly patched. These rules apply even when some configurations might work. Source version gates are accepted only
when absent, `>0.0.0`, or `>=2.0.0`; other gates require manual review. New IDs use the same `>=2.0.0` routing with or without metadata; an existing legacy mapping is not automatically migrated. Sources are never reordered and
existing metadata is never relocated. These restrictions protect against the
installed PulseSync consumer's first-source-wins **whole-object** selection and
storage-duration-created entries shadowing later source metadata.

Changed metadata is committed even when the audio URL is unchanged. Such a send
returns `updated` and updates the README tracks row as described above; commit
text continues to use the separate labels. JSON patching preserves untouched properties, unknown data,
indentation, line endings and BOM. Existing legacy isolation, dirty-target,
concurrency and failure handling remain unchanged; this does not add automatic
rollback or turn the existing write sequence into a transaction. The Go bridge
is a separate publication flow and has no new metadata API in this change.

Read-only consumer/schema references for this contract:
`/home/admin/.config/PulseSync/addons/fckcensor/script.js` and
`/run/media/admin/DATA/FckCensorData/list_v2.schema.json`.
