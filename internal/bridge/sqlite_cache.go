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
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("cache directory must be private and non-symlink")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err == nil {
		err = f.Close()
	} else if os.IsExist(err) {
		info, err = os.Lstat(path)
		if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > maxSQLiteCacheBytes) {
			err = fmt.Errorf("invalid private SQLite cache")
		}
	}
	if err != nil {
		return nil, err
	}
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		info, err := os.Lstat(path + suffix)
		if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm() != 0600) {
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
	if statErr == nil && (!info.Mode().IsRegular() || info.Mode().Perm() != 0600) {
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
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS cache_meta (id INTEGER PRIMARY KEY CHECK(id=1), version INTEGER NOT NULL, payload BLOB NOT NULL); CREATE TABLE IF NOT EXISTS cache_tracks (id INTEGER PRIMARY KEY, position INTEGER NOT NULL, payload BLOB NOT NULL); CREATE INDEX IF NOT EXISTS cache_tracks_position ON cache_tracks(position);`); err != nil {
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
	var payloadSize int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(length(payload)),0) + (SELECT length(payload) FROM cache_meta WHERE id=1) FROM cache_tracks`).Scan(&payloadSize); err != nil {
		return empty, err
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
