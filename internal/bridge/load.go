package bridge

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"
)

const lyricsConcurrency = 50

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
	var checkpointErr error
	checkpoint := func() error {
		if checkpointErr != nil {
			return checkpointErr
		}
		complete := make([]Result, 0, len(results))
		for _, r := range results {
			if r.Status != "pending" && r.Status != "error" {
				complete = append(complete, r)
			}
		}
		if !cache.Dirty && !provenanceChanged && reflect.DeepEqual(persisted, complete) {
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
			checkpointErr = errors.New("unable to persist Yandex cache")
			return checkpointErr
		}
		persisted = complete
		cache.Dirty = false
		cache.Rebuild = false
		provenanceChanged = false
		return nil
	}
	workCtx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	type completion struct {
		index  int
		result Result
		err    error
	}
	done := make(chan completion, lyricsConcurrency)
	defer func() {
		cancel()
		// Drain while workers finish, including completions beyond the buffer.
		// Waiting first would either deadlock or discard successful requests.
		go func() { workers.Wait(); close(done) }()
		for v := range done {
			if v.err == nil {
				results[v.index] = v.result
				sources[v.result.ID] = lyricsSourceVersion
				provenanceChanged = true
				stats.LyricsFetched++
			} else {
				stats.LyricsFailed++
				if results[v.index].Status == "pending" {
					results[v.index].Status = "error"
				}
			}

		}
		if stats.LyricsFailed > 0 {
			stats.Partial = true
			stats.Error = fmt.Sprintf("Lyrics acquisition failed for %d tracks", stats.LyricsFailed)
		}
		s.publishResults(results)
		if e := checkpoint(); e != nil {
			err = e
		}
		if err != nil {
			stats.Partial = true
			stats.Error = err.Error()
		}
	}()
	jobs := make(chan int)
	for n := 0; n < lyricsConcurrency && n < len(pending); n++ {
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
					old := results[i]
					lyrics, e := c.lyricsForTrack(workCtx, old.Track)
					var processed Result
					if e == nil {
						if lyrics == nil {
							lyrics = []Lyric{}
						}
						processed, e = s.detector.ProcessContext(workCtx, old.Track, lyrics, old.Ignored)
					}
					// The coordinator drains this channel on every exit path.
					done <- completion{i, processed, e}
				}
			}
		}()
	}
	// Dispatch from the coordinator to avoid an extra goroutine on cancellation.
	next, completed, unpublished, unsaved := 0, 0, 0, 0
	saveBatch := checkpointBatchSize(len(results))
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
				stats.LyricsFailed++
				// A failed upgrade must not discard usable legacy display results.
				if results[v.index].Status == "pending" {
					results[v.index].Status = "error"
				}
			} else {
				results[v.index] = v.result
				sources[v.result.ID] = lyricsSourceVersion
				provenanceChanged = true
				stats.LyricsFetched++
				unsaved++
			}
			unpublished++
			if unpublished >= 16 {
				s.publishResults(results)
				unpublished = 0
			}
			if unsaved >= saveBatch {
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

// Large fast scans should not rewrite their entire growing cache every 32 tracks.
// Time-based and final checkpoints still bound the durability delay.
func checkpointBatchSize(total int) int {
	batch := total / 20
	if batch < 32 {
		return 32
	}
	if batch > 1024 {
		return 1024
	}
	return batch
}
