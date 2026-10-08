package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	LyricsSourceVersion string           `json:"lyricsSourceVersion,omitempty"`
	LyricsSources       map[int64]string `json:"lyricsSources,omitempty"`
	UserID              string           `json:"user_id"`
	URL                 string           `json:"source_url"`
	Fingerprint         string           `json:"fingerprint"`
	Tracks              []Result         `json:"tracks"`
}

func readTracksCache(path, uid, base string) (tracksCache, error) {
	empty := tracksCache{LyricsSourceVersion: lyricsSourceVersion, UserID: uid, URL: base, Fingerprint: detectorFingerprint(), Tracks: []Result{}}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return empty, nil
	}
	if err != nil {
		return empty, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return empty, fmt.Errorf("cache must be a regular private 0600 file")
	}
	if info.Size() > 256<<20 {
		return empty, fmt.Errorf("tracks cache too large")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return empty, err
	}
	var cache tracksCache
	if err = json.Unmarshal(data, &cache); err != nil {
		return empty, fmt.Errorf("invalid tracks cache")
	}
	if cache.UserID != uid || cache.URL != base {
		return empty, nil
	}
	seen := map[int64]bool{}
	for i, r := range cache.Tracks {
		if r.ID <= 0 || seen[r.ID] || (r.Status != "found" && r.Status != "none" && r.Status != "error") {
			return empty, fmt.Errorf("invalid cached result")
		}
		seen[r.ID] = true
		if r.Status == "error" && r.Lyrics == nil {
			// Migrate earlier caches that represented confirmed absence as error.
			cache.Tracks[i].Status = "none"
			cache.Tracks[i].Lyrics = []Lyric{}
		}
	}
	if cache.Fingerprint != empty.Fingerprint {
		d := NewDetector()
		for i, r := range cache.Tracks {
			cache.Tracks[i] = d.Process(r.Track, r.Lyrics, r.Ignored)
		}
		cache.Fingerprint = empty.Fingerprint
	}
	return cache, nil
}

func saveTracksCache(path string, cache tracksCache) error {
	cache.LyricsSourceVersion = lyricsSourceVersion
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("cache directory must be private (0700)")
	}
	f, err := os.CreateTemp(dir, ".tracks-cache-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		err = json.NewEncoder(f).Encode(cache)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
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
