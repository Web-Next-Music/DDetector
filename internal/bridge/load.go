package bridge

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"
)

// Workers never mutate published results. The coordinator owns results and persists
// successful batches, including the last partial batch when a scan is interrupted.
func (s *Server) processLyrics(ctx context.Context, c *yandexClient, path string, cache tracksCache, results []Result, pending []int, stats *FetchResult) (err error) {
	s.mu.Lock()
	s.state.Processing = len(pending) > 0
	s.mu.Unlock()
	s.publishResults(results)
	defer func() {
		s.mu.Lock()
		s.state.Processing = false
		s.mu.Unlock()
	}()
	persisted := cache.Tracks
	// Clone provenance: checkpoints must not mutate the caller's cache snapshot.
	sources := make(map[int64]string, len(cache.LyricsSources))
	for id, version := range cache.LyricsSources {
		sources[id] = version
	}
	provenanceChanged := false
	checkpoint := func() error {
		complete := make([]Result, 0, len(results))
		for _, r := range results {
			if r.Status != "pending" && r.Status != "error" {
				complete = append(complete, r)
			}
		}
		if !provenanceChanged && reflect.DeepEqual(persisted, complete) {
			return nil
		}
		cache.Tracks = complete
		cache.LyricsSources = make(map[int64]string, len(complete))
		for _, r := range complete {
			if version := sources[r.ID]; version != "" {
				cache.LyricsSources[r.ID] = version
			}
		}
		if saveTracksCache(path, cache) != nil {
			return errors.New("unable to persist Yandex cache")
		}
		persisted = complete
		provenanceChanged = false
		return nil
	}
	workCtx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	type completion struct {
		index  int
		lyrics []Lyric
		err    error
	}
	done := make(chan completion, 4)
	defer func() {
		cancel()
		workers.Wait()
		// Retain successful requests completed just before cancellation.
		for len(done) > 0 {
			v := <-done
			if v.err == nil {
				old := results[v.index]
				lyrics := v.lyrics
				if lyrics == nil {
					lyrics = []Lyric{}
				}
				results[v.index] = s.detector.Process(old.Track, lyrics, old.Ignored)
				sources[old.ID] = lyricsSourceVersion
				provenanceChanged = true
				stats.LyricsFetched++
			}
		}
		s.publishResults(results)
		if e := checkpoint(); e != nil {
			err = e
		}
	}()
	jobs := make(chan int)
	for n := 0; n < 4 && n < len(pending); n++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-workCtx.Done():
					return
				case i, ok := <-jobs:
					if !ok {
						return
					}
					if workCtx.Err() != nil {
						return
					}
					lyrics, e := c.lyricsForTrack(workCtx, results[i].Track)
					select {
					case done <- completion{i, lyrics, e}:
					case <-workCtx.Done():
						return
					}
				}
			}
		}()
	}
	// Dispatch from the coordinator to avoid an extra goroutine on cancellation.
	next, completed, unpublished, unsaved := 0, 0, 0, 0
	publishTick := time.NewTicker(100 * time.Millisecond)
	saveTick := time.NewTicker(2 * time.Second)
	defer publishTick.Stop()
	defer saveTick.Stop()
	for completed < len(pending) {
		var dispatch chan int
		var index int
		if next < len(pending) {
			dispatch, index = jobs, pending[next]
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case dispatch <- index:
			next++
		case v := <-done:
			completed++
			if v.err != nil {
				// A failed upgrade must not discard usable legacy display results.
				if results[v.index].Status == "pending" {
					results[v.index].Status = "error"
				}
			} else {
				lyrics := v.lyrics
				if lyrics == nil {
					lyrics = []Lyric{}
				}
				old := results[v.index]
				results[v.index] = s.detector.Process(old.Track, lyrics, old.Ignored)
				sources[old.ID] = lyricsSourceVersion
				provenanceChanged = true
				stats.LyricsFetched++
				unsaved++
			}
			unpublished++
			if unpublished >= 16 {
				s.publishResults(results)
				unpublished = 0
			}
			if unsaved >= 32 {
				if e := checkpoint(); e != nil {
					return e
				}
				unsaved = 0
			}
		case <-publishTick.C:
			if unpublished > 0 {
				s.publishResults(results)
				unpublished = 0
			}
		case <-saveTick.C:
			if unsaved > 0 {
				if e := checkpoint(); e != nil {
					return e
				}
				unsaved = 0
			}
		}
	}
	return ctx.Err()
}
