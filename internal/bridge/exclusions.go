package bridge

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
)

const maxExclusionListBytes = 8 << 20

// localExclusions reads the configured native repository without modifying it.
func (s *Server) localExclusions(ctx context.Context, uid string) (map[int64]bool, error) {
	s.stateBuildMu.Lock()
	defer s.stateBuildMu.Unlock()
	rt, err := s.currentCensorRuntime(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("exclusion repository unavailable: %w", err)
	}
	if err := s.refreshExclusionProjection(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.censorRuntime != rt {
		return nil, fmt.Errorf("exclusion repository changed during load")
	}
	return s.exclusions, nil
}

// Caller holds stateBuildMu, serializing file reads with policy installation.
func (s *Server) refreshExclusionProjection() error {
	s.mu.Lock()
	rt := s.censorRuntime
	s.mu.Unlock()
	if rt == nil {
		return nil
	}
	rt.mu.Lock()
	repo := rt.disk.RepositoryPath
	_, native := rt.backend.(*nativeBackend)
	var excluded map[int64]bool
	var err error
	if !native || repo == "" {
		excluded = map[int64]bool{}
	} else if !validNativeRepositoryPath(repo) {
		err = fmt.Errorf("invalid native repository path")
	} else {
		var data []byte
		data, err = readLocalExclusionFile(repo)
		if err == nil {
			hash := sha256.Sum256(data)
			if !s.exclusionCacheSet || s.exclusionCachePath != repo || s.exclusionCacheHash != hash {
				s.exclusionCacheData, s.exclusionCacheErr = parseLocalExclusions(data)
				s.exclusionCachePath, s.exclusionCacheHash, s.exclusionCacheSet = repo, hash, true
			}
			excluded, err = s.exclusionCacheData, s.exclusionCacheErr
		}
	}
	if err == nil {
		s.mu.Lock()
		if s.censorRuntime == rt && !reflect.DeepEqual(s.exclusions, excluded) {
			s.exclusions = excluded
			if s.allTracks != nil {
				s.projectResultsLocked()
			}
		}
		s.mu.Unlock()
	}
	rt.mu.Unlock()
	return err
}

func localExclusions(repo string) (map[int64]bool, error) {
	data, err := readLocalExclusionFile(repo)
	if err != nil {
		return nil, err
	}
	return parseLocalExclusions(data)
}

func readLocalExclusionFile(repo string) ([]byte, error) {
	f, err := os.Open(filepath.Join(repo, "list_v2.json"))
	if err != nil {
		return nil, fmt.Errorf("unable to open local exclusion list: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxExclusionListBytes+1))
	if err != nil {
		return nil, fmt.Errorf("unable to read local exclusion list: %w", err)
	}
	if len(data) > maxExclusionListBytes {
		return nil, fmt.Errorf("invalid exclusion list: exceeds 8 MiB")
	}
	return data, nil
}

func parseLocalExclusions(data []byte) (map[int64]bool, error) {
	doc, err := nativeObject(data)
	if err != nil {
		return nil, fmt.Errorf("invalid exclusion list: invalid JSON")
	}
	sources, ok := doc["sources"].([]any)
	if !ok {
		return nil, fmt.Errorf("invalid exclusion list: expected sources array")
	}
	excluded := map[int64]bool{}
	add := func(key string) error {
		id, err := strconv.ParseInt(key, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != key {
			return fmt.Errorf("invalid exclusion list: invalid track ID")
		}
		excluded[id] = true
		return nil
	}
	for _, raw := range sources {
		source, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid exclusion list: expected source object")
		}
		rawStores, has := source["tracksStorages"]
		if !has {
			continue
		}
		stores, ok := rawStores.([]any)
		if !ok {
			return nil, fmt.Errorf("invalid exclusion list: expected tracksStorages array")
		}
		for _, raw := range stores {
			store, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid exclusion list: expected storage object")
			}

			if raw, has := store["trackIds"]; has {
				ids, ok := raw.([]any)
				if !ok {
					return nil, fmt.Errorf("invalid exclusion list: invalid trackIds")
				}
				for _, entry := range ids {
					key := nativeID(entry)
					if err := add(key); err != nil {
						return nil, err
					}

				}
			}
			if raw, has := store["tracks"]; has {
				tracks, ok := raw.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("invalid exclusion list: expected tracks object")
				}
				for key := range tracks {
					if err := add(key); err != nil {
						return nil, err
					}

				}
			}
		}
	}
	return excluded, nil
}
