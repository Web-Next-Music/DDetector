package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Bump when annotation or detection semantics change.
const detectorAlgorithmVersion = "1"

const lyricsSourceVersion = "lrclib-v1"

// Per-track provenance is separate from detector output and public display data.
func needsLyricsAcquisition(r Result, cache tracksCache) bool {
	return r.Status == "error" || cache.LyricsSources[r.ID] != lyricsSourceVersion
}

func detectorFingerprint() string {
	sum := sha256.Sum256([]byte(detectorAlgorithmVersion + "\x00" + keywordJSON))
	return hex.EncodeToString(sum[:])
}

type tracksCache struct {
	Excluded            map[int64]bool   `json:"excludedTrackIDs,omitempty"`
	Dirty               bool             `json:"-"`
	Rebuild             bool             `json:"-"`
	LyricsSourceVersion string           `json:"lyricsSourceVersion,omitempty"`
	LyricsSources       map[int64]string `json:"lyricsSources,omitempty"`
	UserID              string           `json:"user_id"`
	URL                 string           `json:"source_url"`
	Fingerprint         string           `json:"fingerprint"`
	Tracks              []Result         `json:"tracks"`
}

func readTracksCache(path, uid, base string) (tracksCache, error) {
	return readTracksCacheContext(context.Background(), path, uid, base)
}
func readTracksCacheContext(ctx context.Context, path, uid, base string) (tracksCache, error) {
	empty := tracksCache{LyricsSourceVersion: lyricsSourceVersion, UserID: uid, URL: base, Fingerprint: detectorFingerprint(), Tracks: []Result{}}
	if strings.HasSuffix(path, ".sqlite") {
		cache, err := readSQLiteCache(ctx, path, uid, base)
		if err != nil && ctx.Err() == nil && !errors.Is(err, errSQLiteCacheVersion) {
			cache.Dirty = true
			cache.Rebuild = true
		}
		return cache, err
	}
	data, err := readPrivateFile(path, maxTracksCacheBytes, true)
	if os.IsNotExist(err) {
		return empty, nil
	}
	if err != nil {
		return empty, err
	}
	return decodeTracksCache(ctx, data, uid, base)
}

func decodeTracksCache(ctx context.Context, data []byte, uid, base string) (tracksCache, error) {
	empty := tracksCache{LyricsSourceVersion: lyricsSourceVersion, UserID: uid, URL: base, Fingerprint: detectorFingerprint(), Tracks: []Result{}}
	var cache tracksCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return empty, fmt.Errorf("invalid tracks cache")
	}
	return validateTracksCache(ctx, cache, uid, base)
}

func validateTracksCache(ctx context.Context, cache tracksCache, uid, base string) (tracksCache, error) {
	empty := tracksCache{LyricsSourceVersion: lyricsSourceVersion, UserID: uid, URL: base, Fingerprint: detectorFingerprint(), Tracks: []Result{}}
	if cache.UserID != uid || cache.URL != base {
		return empty, nil
	}
	cache.Dirty = cache.LyricsSourceVersion != lyricsSourceVersion
	filterExcluded(&cache)
	seen := map[int64]bool{}
	for i, r := range cache.Tracks {
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		if err := validateLyrics(r.Lyrics); err != nil {
			return empty, err
		}
		if r.ID <= 0 || seen[r.ID] || (r.Status != "found" && r.Status != "none" && r.Status != "error") {
			return empty, fmt.Errorf("invalid cached result")
		}
		seen[r.ID] = true
		if r.Status == "error" && r.Lyrics == nil {
			// Migrate earlier caches that represented confirmed absence as error.
			cache.Dirty = true
			cache.Tracks[i].Status = "none"
			cache.Tracks[i].Lyrics = []Lyric{}
		}
	}
	if cache.Fingerprint != empty.Fingerprint {
		d := NewDetector()
		for i, r := range cache.Tracks {
			processed, err := d.ProcessContext(ctx, r.Track, r.Lyrics, r.Ignored)
			if err != nil {
				return empty, err
			}
			cache.Tracks[i] = processed
		}
		cache.Fingerprint = empty.Fingerprint
		cache.Dirty = true
	}
	return cache, nil
}

func saveTracksCache(path string, cache tracksCache) error {
	if strings.HasSuffix(path, ".sqlite") {
		return saveSQLiteCache(context.Background(), path, cache)
	}
	return saveTracksCacheLimit(path, cache, maxTracksCacheBytes)
}

func saveTracksCacheLimit(path string, cache tracksCache, limit int64) error {
	return writePrivateFile(path, ".tracks-cache-*", limit, func(w io.Writer) error {
		// Encode one track at a time: the full cache need not fit in an encoder buffer.
		header, err := json.Marshal(map[string]any{
			"lyricsSourceVersion": lyricsSourceVersion, "lyricsSources": cache.LyricsSources,
			"excludedTrackIDs": cache.Excluded,
			"user_id":          cache.UserID, "source_url": cache.URL, "fingerprint": cache.Fingerprint,
		})
		if err != nil {
			return err
		}
		if _, err = w.Write(header[:len(header)-1]); err != nil {
			return err
		}
		if _, err = io.WriteString(w, `,"tracks":[`); err != nil {
			return err
		}
		encoder := json.NewEncoder(w)
		for i, track := range cache.Tracks {
			if err := validateLyrics(track.Lyrics); err != nil {
				return err
			}
			if i > 0 {
				if _, err = io.WriteString(w, ","); err != nil {
					return err
				}
			}
			if err = encoder.Encode(track); err != nil {
				return err
			}
		}
		_, err = io.WriteString(w, "]}\n")
		return err
	})
}

func (s *Server) publishResults(results []Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Tracks = append([]Result{}, results...)
	s.tracksRevision++
	s.state.Processed, s.state.FoundCount = 0, 0
	for _, r := range results {
		if r.Status != "pending" {
			s.state.Processed++
		}
		if r.Status == "found" {
			s.state.FoundCount++
		}
	}
}
