package bridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

// SQLite pages, indexes and reusable free pages are not JSON payload. Limit them
// separately, and enforce the physical budget inside SQLite before committing.
const maxSQLiteCacheBytes = 2*maxTracksCacheBytes + 16*1024*1024

var errSQLiteCacheVersion = errors.New("unsupported SQLite cache version")

// The private directory is the trust boundary, just as for session JSON. DELETE
// journaling avoids long-lived WAL sidecars. Never open a symlink or public file.
func openSQLiteCache(ctx context.Context, path string) (*sql.DB, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || !storagePrivate(dir, info, false) {
		return nil, fmt.Errorf("cache directory must be private and non-symlink")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		err = f.Close()
	} else if os.IsExist(err) {
		info, err = os.Lstat(path)
		if err == nil && (!info.Mode().IsRegular() || !storagePrivate(path, info, true) || info.Size() > maxSQLiteCacheBytes) {
			err = fmt.Errorf("invalid private SQLite cache")
		}
	}
	if err != nil {
		return nil, err
	}
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		info, err := os.Lstat(path + suffix)
		if err == nil && (!info.Mode().IsRegular() || !storagePrivate(path+suffix, info, true)) {
			return nil, fmt.Errorf("invalid private SQLite sidecar")
		}
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, `PRAGMA busy_timeout=5000; PRAGMA journal_mode=DELETE; PRAGMA synchronous=FULL;`); err != nil {
		db.Close()
		return nil, err
	}
	var pageSize int64
	if err = db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil || pageSize <= 0 {
		db.Close()
		return nil, fmt.Errorf("invalid SQLite page size: %w", err)
	}
	if _, err = db.ExecContext(ctx, fmt.Sprintf("PRAGMA max_page_count=%d", maxSQLiteCacheBytes/pageSize)); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func saveSQLiteCache(ctx context.Context, path string, cache tracksCache) error {
	info, statErr := os.Lstat(path)
	if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	if statErr == nil && (!info.Mode().IsRegular() || !storagePrivate(path, info, true)) {
		return fmt.Errorf("invalid private SQLite cache")
	}
	if os.IsNotExist(statErr) || cache.Rebuild || info.Size() > maxSQLiteCacheBytes {
		// A hot journal belongs to the old database. It must not be replayed onto
		// an atomically replaced database; leave unresolved sidecars untouched.
		for _, suffix := range []string{"-journal", "-wal", "-shm"} {
			if _, err := os.Lstat(path + suffix); err == nil {
				return fmt.Errorf("SQLite sidecar requires recovery before replacement")
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		// Build a new or rejected database privately. Failed recovery preserves the
		// old file; only a complete committed database can replace it.
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		f, err := os.CreateTemp(dir, ".sqlite-cache-*")
		if err != nil {
			return err
		}
		temp := f.Name()
		if err := f.Close(); err != nil {
			os.Remove(temp)
			return err
		}
		defer os.Remove(temp)
		if err := saveSQLiteCacheExisting(ctx, temp, cache); err != nil {
			return err
		}
		if err := os.Rename(temp, path); err != nil {
			return err
		}
		d, err := os.Open(dir)
		if err != nil {
			return err
		}
		defer d.Close()
		return d.Sync()
	}
	return saveSQLiteCacheExisting(ctx, path, cache)
}

func saveSQLiteCacheExisting(ctx context.Context, path string, cache tracksCache) error {
	// Validate first, then encode one row at a time inside the transaction. Peak
	// encoder memory is bounded by one track, not the complete cached library.
	seen := make(map[int64]bool, len(cache.Tracks))
	for _, r := range cache.Tracks {
		if err := ctx.Err(); err != nil {
			return err
		}
		if r.ID <= 0 || seen[r.ID] || (r.Status != "found" && r.Status != "none" && r.Status != "error") {
			return fmt.Errorf("invalid cached result")
		}
		seen[r.ID] = true
		if err := validateLyrics(r.Lyrics); err != nil {
			return err
		}

	}
	meta := cache
	meta.Tracks = nil
	meta.LyricsSources = nil
	meta.LyricsSourceVersion = lyricsSourceVersion
	header, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	size := int64(len(header))
	if size > maxTracksCacheBytes {
		return fmt.Errorf("tracks cache too large")
	}
	db, err := openSQLiteCache(ctx, path)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS cache_meta (id INTEGER PRIMARY KEY CHECK(id=1), version INTEGER NOT NULL, payload BLOB NOT NULL); CREATE TABLE IF NOT EXISTS cache_tracks (id INTEGER PRIMARY KEY, position INTEGER NOT NULL, payload BLOB NOT NULL); CREATE INDEX IF NOT EXISTS cache_tracks_position ON cache_tracks(position); CREATE TABLE IF NOT EXISTS cache_lyrics_sources (id INTEGER PRIMARY KEY, version TEXT NOT NULL);`); err != nil {
		return err
	}
	var version int
	err = tx.QueryRowContext(ctx, `SELECT version FROM cache_meta WHERE id=1`).Scan(&version)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil && version != 1 {
		return errSQLiteCacheVersion
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO cache_meta VALUES(1,1,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload WHERE payload!=excluded.payload`, header); err != nil {
		return err
	}
	// Keep unchanged payloads untouched; a temporary membership table permits
	// pruning without a parameter-count limit or overwriting the whole database.
	if _, err = tx.ExecContext(ctx, `CREATE TEMP TABLE keep_tracks(id INTEGER PRIMARY KEY)`); err != nil {
		return err
	}
	keep, err := tx.PrepareContext(ctx, `INSERT INTO keep_tracks VALUES(?)`)
	if err != nil {
		return err
	}
	defer keep.Close()
	upsert, err := tx.PrepareContext(ctx, `INSERT INTO cache_tracks VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET position=excluded.position,payload=excluded.payload WHERE position!=excluded.position OR payload!=excluded.payload`)
	if err != nil {
		return err
	}
	defer upsert.Close()
	for i, r := range cache.Tracks {
		if err := ctx.Err(); err != nil {
			return err
		}
		payload, err := json.Marshal(r)
		if err != nil {
			return err
		}
		size += int64(len(payload))
		if size > maxTracksCacheBytes {
			return fmt.Errorf("tracks cache too large")
		}
		if _, err = keep.ExecContext(ctx, r.ID); err != nil {
			return err
		}
		if _, err = upsert.ExecContext(ctx, r.ID, i, payload); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM cache_tracks WHERE id NOT IN (SELECT id FROM keep_tracks)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM cache_lyrics_sources`); err != nil {
		return err
	}
	source, err := tx.PrepareContext(ctx, `INSERT INTO cache_lyrics_sources VALUES(?,?)`)
	if err != nil {
		return err
	}
	defer source.Close()
	for id, version := range cache.LyricsSources {
		if _, ok := seen[id]; ok && version != "" {
			size += int64(len(version))
			if size > maxTracksCacheBytes {
				return fmt.Errorf("tracks cache too large")
			}
			if _, err = source.ExecContext(ctx, id, version); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// saveSQLiteCacheDelta applies a processLyrics checkpoint without encoding or
// issuing UPSERTs for unchanged tracks. Track positions are their indices in the
// reconciled result list; gaps are harmless and preserve the same read order.
func saveSQLiteCacheDelta(ctx context.Context, path string, cache tracksCache, results []Result, dirty map[int]bool, removed map[int64]bool, sources map[int64]string) error {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		initial := cache
		initial.Tracks = make([]Result, 0, len(results))
		initial.LyricsSources = make(map[int64]string, len(sources))
		for _, r := range results {
			if r.Status == "pending" || r.Status == "error" {
				continue
			}
			initial.Tracks = append(initial.Tracks, r)
			if version := sources[r.ID]; version != "" {
				initial.LyricsSources[r.ID] = version
			}
		}
		return saveSQLiteCache(ctx, path, initial)
	} else if err != nil {
		return err
	}
	meta := cache
	meta.Tracks = nil
	meta.LyricsSources = nil
	meta.LyricsSourceVersion = lyricsSourceVersion
	header, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	db, err := openSQLiteCache(ctx, path)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS cache_lyrics_sources (id INTEGER PRIMARY KEY, version TEXT NOT NULL)`); err != nil {
		return err
	}
	var version int
	if err = tx.QueryRowContext(ctx, `SELECT version FROM cache_meta WHERE id=1`).Scan(&version); err != nil {
		return err
	}
	if version != 1 {
		return errSQLiteCacheVersion
	}
	// Migrate provenance embedded by older version-1 databases before replacing
	// the metadata payload with its compact form.
	var oldHeader []byte
	if err = tx.QueryRowContext(ctx, `SELECT payload FROM cache_meta WHERE id=1`).Scan(&oldHeader); err != nil {
		return err
	}
	var oldMeta tracksCache
	if err = json.Unmarshal(oldHeader, &oldMeta); err != nil {
		return fmt.Errorf("invalid SQLite cache metadata")
	}
	for id, sourceVersion := range oldMeta.LyricsSources {
		if sourceVersion != "" {
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO cache_lyrics_sources VALUES(?,?)`, id, sourceVersion); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE cache_meta SET payload=? WHERE id=1 AND payload!=?`, header, header); err != nil {
		return err
	}
	upsert, err := tx.PrepareContext(ctx, `INSERT INTO cache_tracks VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET position=excluded.position,payload=excluded.payload WHERE position!=excluded.position OR payload!=excluded.payload`)
	if err != nil {
		return err
	}
	defer upsert.Close()
	setSource, err := tx.PrepareContext(ctx, `INSERT INTO cache_lyrics_sources VALUES(?,?) ON CONFLICT(id) DO UPDATE SET version=excluded.version WHERE version!=excluded.version`)
	if err != nil {
		return err
	}
	defer setSource.Close()
	for i := range dirty {
		if i < 0 || i >= len(results) {
			return fmt.Errorf("invalid cache delta")
		}
		r := results[i]
		if r.ID <= 0 || r.Status == "pending" || r.Status == "error" {
			return fmt.Errorf("invalid cached result")
		}
		if err = validateLyrics(r.Lyrics); err != nil {
			return err
		}
		payload, marshalErr := json.Marshal(r)
		if marshalErr != nil {
			return marshalErr
		}
		if _, err = upsert.ExecContext(ctx, r.ID, i, payload); err != nil {
			return err
		}
		if sourceVersion := sources[r.ID]; sourceVersion != "" {
			if _, err = setSource.ExecContext(ctx, r.ID, sourceVersion); err != nil {
				return err
			}
		} else if _, err = tx.ExecContext(ctx, `DELETE FROM cache_lyrics_sources WHERE id=?`, r.ID); err != nil {
			return err
		}
	}
	for id := range removed {
		if _, err = tx.ExecContext(ctx, `DELETE FROM cache_tracks WHERE id=?`, id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM cache_lyrics_sources WHERE id=?`, id); err != nil {
			return err
		}
	}
	var payloadSize int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(payload)),0) + (SELECT length(payload) FROM cache_meta WHERE id=1) + (SELECT COALESCE(SUM(length(version)),0) FROM cache_lyrics_sources) FROM cache_tracks`).Scan(&payloadSize); err != nil {
		return err
	}
	if payloadSize > maxTracksCacheBytes {
		return fmt.Errorf("tracks cache too large")
	}
	return tx.Commit()
}

func readSQLiteCache(ctx context.Context, path, uid, base string) (tracksCache, error) {
	empty := tracksCache{LyricsSourceVersion: lyricsSourceVersion, UserID: uid, URL: base, Fingerprint: detectorFingerprint(), Tracks: []Result{}}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		legacy := strings.TrimSuffix(path, ".sqlite") + ".json"
		cache, err := readTracksCacheContext(ctx, legacy, uid, base)
		if err != nil {
			return empty, err
		}
		if _, err := os.Lstat(legacy); os.IsNotExist(err) {
			return cache, nil
		}
		if err := saveSQLiteCache(ctx, path, cache); err != nil {
			return empty, err
		}
		// Keep legacy JSON as a rollback copy; never remove it before durable commit.
		return cache, nil
	} else if err != nil {
		return empty, err
	}
	db, err := openSQLiteCache(ctx, path)
	if err != nil {
		return empty, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRowContext(ctx, `SELECT version FROM cache_meta WHERE id=1`).Scan(&version); err != nil {
		return empty, err
	}
	if version != 1 {
		return empty, errSQLiteCacheVersion
	}
	var hasSources bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='cache_lyrics_sources')`).Scan(&hasSources); err != nil {
		return empty, err
	}
	var payloadSize int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(payload)),0) + (SELECT length(payload) FROM cache_meta WHERE id=1) FROM cache_tracks`).Scan(&payloadSize); err != nil {
		return empty, err
	}
	if hasSources {
		var sourcesSize int64
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(version)),0) FROM cache_lyrics_sources`).Scan(&sourcesSize); err != nil {
			return empty, err
		}
		payloadSize += sourcesSize
	}
	if payloadSize > maxTracksCacheBytes {
		return empty, fmt.Errorf("tracks cache too large")
	}
	var header []byte
	if err = tx.QueryRowContext(ctx, `SELECT payload FROM cache_meta WHERE id=1`).Scan(&header); err != nil {
		return empty, err
	}
	var cache tracksCache
	if err = json.Unmarshal(header, &cache); err != nil {
		return empty, fmt.Errorf("invalid SQLite cache metadata")
	}
	if hasSources {
		sourceRows, queryErr := tx.QueryContext(ctx, `SELECT id,version FROM cache_lyrics_sources`)
		if queryErr != nil {
			return empty, queryErr
		}
		if cache.LyricsSources == nil {
			cache.LyricsSources = map[int64]string{}
		}
		for sourceRows.Next() {
			var id int64
			var version string
			if err = sourceRows.Scan(&id, &version); err != nil {
				sourceRows.Close()
				return empty, err
			}
			cache.LyricsSources[id] = version
		}
		if err = sourceRows.Err(); err != nil {
			sourceRows.Close()
			return empty, err
		}
		if err = sourceRows.Close(); err != nil {
			return empty, err
		}
	}
	cache.Tracks = []Result{}
	rows, err := tx.QueryContext(ctx, `SELECT id,payload FROM cache_tracks ORDER BY position`)
	if err != nil {
		return empty, err
	}
	defer rows.Close()
	size := int64(len(header))
	for rows.Next() {
		var id int64
		var b []byte
		if err = rows.Scan(&id, &b); err != nil {
			return empty, err
		}
		size += int64(len(b))
		if size > maxTracksCacheBytes {
			return empty, fmt.Errorf("tracks cache too large")
		}
		var r Result
		if json.Unmarshal(b, &r) != nil || r.ID != id {
			return empty, fmt.Errorf("invalid SQLite cached result")
		}
		cache.Tracks = append(cache.Tracks, r)
	}
	if err = rows.Err(); err != nil {
		return empty, err
	}
	return validateTracksCache(ctx, cache, uid, base)
}
