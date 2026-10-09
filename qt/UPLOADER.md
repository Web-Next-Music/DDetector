# Local Send backend

`uploader.h/.cpp` are integrated into the native application through `main.cpp` and `SendDialog.qml`. Reference directories were inspected read-only and were not modified. The backend needs Qt Core/Network, Git, a writable local FckCensorData checkout with an existing HEAD, and a configured Git author identity. It needs neither FCDServer nor a Yandex token. It never pushes or fetches.

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

## Local API and exact rules

```cpp
Uploader uploader; // No default checkout: configure explicitly.
uploader.setDirectory("/absolute/canonical/path/to/FckCensorData");
```

QML entry point: `send(id, title, artist, mp3Url = "")`. `busy`, `status`, `error`, and `action` notify via `stateChanged`; `completed(action)` is emitted only after successful completion. Actions are `added`, `updated`, `skipped`. A Git failure can leave `action` populated: check `error`, not `action`, for success. While busy, additional sends are ignored. There is no cancellation API; destruction waits for the worker to finish, so shutdown can wait for bounded download/Git operations.

- IDs must be 1–32 ASCII digits, passed as strings. This prevents traversal and option/path injection.
- Title is trimmed, falling back to ID if empty. To match the addon UI, pass the original title when the edited title is blank.
- Artist is split on commas, trimmed, empties discarded, joined with `, `; empty becomes `Unknown`. Title/artist must have no embedded CR/LF or NUL after normalization.
- HTTP/HTTPS only, with no URL user credentials. Normal TLS verification and no-less-safe redirects apply. The whole download has a 30-second deadline and a 128 MiB limit. Empty bodies and non-2xx responses fail. As in the reference, bytes are not transcoded or checked for MP3 encoding; the caller must supply the intended audio, not an HTML download page.
- Each filesystem write uses QSaveFile without direct-write fallback. The operation is **not a multi-file transaction**: later filesystem/Git failures leave earlier changes available for inspection. No automatic reset, rollback, cleanup, or retry discards user work.
- The checkout must exist, be the canonical repository root, and have HEAD. Symlink targets and a symlink `tracks` directory are rejected. A cooperative `.ddetector-upload.lock` prevents other instances of this backend from writing concurrently.
- Rejects preexisting staged, unstaged, or untracked changes in `list.json`, `README.md`, or this track's audio path, and rejects merge/rebase/cherry-pick/revert state. Other paths may be dirty or staged.
- Parses/validates list JSON before saving audio. Existing canonical formatting is preserved with surgical edits; unsupported edits fail instead of reformatting or corrupting the file. Missing list is created using the reference indentation; an existing empty/invalid list fails. For a matching URL, no textual edit is needed.
- Only `list.json`, `README.md`, and audio saved by this call are passed to `git add --sparse -- <paths>`, then `git commit --only -m <message> -- <paths>`. Unrelated staged work remains staged and is not included in the upload commit. Skipped entries do not stage audio.
- Git uses QProcess argument arrays, never a shell; inherited `GIT_*` repository/index overrides are removed. Git startup is limited to 3 seconds and each command to 15 seconds; timed-out processes are killed. Hooks and automatic commit signing are disabled to avoid interactive/arbitrary hook execution. Git author identity must already be configured. Errors are surfaced, not silently reported as success.
- Use a trusted checkout: Git configuration/attributes can configure external filters. The cooperative lock does not stop manual Git or filesystem changes by other processes. Do not edit this checkout concurrently with a send. Downloading contacts only the supplied URL/its redirects; it may reveal the client's IP and any query-string credentials to those hosts.

## Integration reference

The application already exposes `localUploader`, persists `Config.repositoryPath`, resolves its Git root using `GitWorkspace`, and opens `SendDialog.qml` from the player Send button. The following notes describe the underlying integration API, not outstanding setup steps.

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

Tests use temporary repositories and loopback HTTP, never the real FckCensorData checkout. They cover add/update/skip, exact commit text containing shell metacharacters, unrelated staged-index preservation, dirty target rejection, traversal/symlink rejection, surfaced commit failure, audio save/overwrite on skipped entries without a commit, committing audio for new entries, and non-HTTP URL rejection.
