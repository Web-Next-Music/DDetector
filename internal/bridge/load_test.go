package bridge

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLyricsProgressConcurrencyAndCancel(t *testing.T) {
	var active, peak atomic.Int32
	entered := make(chan struct{}, 20)
	release := make(chan struct{}, 1)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/lyrics") {
			w.WriteHeader(404)
			return
		}
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		entered <- struct{}{}
		select {
		case <-release:
			fmt.Fprint(w, `{"result":{"lyrics":{"fullLyrics":"weed"}}}`)
		case <-r.Context().Done():
		}
	}))
	defer remote.Close()
	s := testServer(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected request") })
	c := newYandexClient("token")
	c.base = remote.URL
	results := make([]Result, 12)
	pending := make([]int, len(results))
	for i := range results {
		results[i] = Result{Track: Track{ID: int64(i + 1), Title: "Song"}, Status: "pending"}
		pending[i] = i
	}
	path := filepath.Join(t.TempDir(), "private", "cache.json")
	cache := tracksCache{UserID: "u", URL: c.base, Fingerprint: detectorFingerprint(), Tracks: []Result{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- s.processLyrics(ctx, c, path, cache, results, pending, &FetchResult{}) }()
	for i := 0; i < 4; i++ {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("workers did not start")
		}
	}
	s.mu.Lock()
	snapshot := s.state.Tracks
	revision := s.tracksRevision
	visible := len(snapshot) == 12 && s.state.Processing && s.state.Processed == 0 && snapshot[0].Title == "Song"
	s.mu.Unlock()
	if !visible {
		t.Fatal("metadata list not visible before lyrics")
	}
	release <- struct{}{}
	deadline := time.Now().Add(3 * time.Second)
	progressed := false
	for time.Now().Before(deadline) {
		s.mu.Lock()
		progressed = s.state.Processed == 1 && s.state.FoundCount == 1 && s.tracksRevision > revision && s.state.Processing
		s.mu.Unlock()
		if progressed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !progressed {
		t.Fatal("no intermediate progress")
	}
	for _, r := range snapshot {
		if r.Status != "pending" {
			t.Fatal("published snapshot mutated")
		}
	}
	cancel()
	select {
	case err := <-finished:
		if err != context.Canceled {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not stop workers")
	}
	if peak.Load() != 4 {
		t.Fatal("unexpected concurrency", peak.Load())
	}
	saved, err := readTracksCache(path, "u", c.base)
	if err != nil || len(saved.Tracks) != 1 {
		t.Fatal("interrupted progress not saved", saved, err)
	}
}

func TestInterruptedLyricsSourceMigration(t *testing.T) {
	s := testServer(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected request") })
	c := newYandexClient("secret")
	c.lrclib.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(200, `{"id":1,"trackName":"Song","artistName":"Artist","syncedLyrics":"[00:01]weed"}`), nil
	})
	c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("unexpected Yandex request")
		return response(404, ""), nil
	})
	results := []Result{}
	for i := int64(1); i <= 2; i++ {
		results = append(results, s.detector.Process(Track{ID: i, Title: "Song", Artist: "Artist"}, []Lyric{{Text: "weed"}}, false))
	}
	path := filepath.Join(t.TempDir(), "private", "cache.json")
	cache := tracksCache{UserID: "u", URL: c.base, Fingerprint: detectorFingerprint(), Tracks: append([]Result{}, results...)}
	if err := saveTracksCache(path, cache); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := s.processLyrics(ctx, c, path, cache, results, []int{0, 1}, &FetchResult{}); err != context.DeadlineExceeded {
		t.Fatal(err)
	}
	saved, err := readTracksCache(path, "u", c.base)
	if err != nil || len(saved.Tracks) != 2 {
		t.Fatal(saved, err)
	}
	pending := []int{}
	completed := 0
	for i, r := range saved.Tracks {
		if needsLyricsAcquisition(r, saved) {
			pending = append(pending, i)
			if r.Status != "found" || len(r.Lyrics) != 1 || r.Lyrics[0].TS != nil {
				t.Fatal("legacy display lost", r)
			}
		} else {
			completed++
		}
	}
	if completed != 1 || len(pending) != 1 {
		t.Fatal("partial provenance not checkpointed", saved)
	}
	c.lrclib = newLRCLIBClient()
	calls := 0
	c.lrclib.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return response(200, `{"id":1,"trackName":"Song","artistName":"Artist","instrumental":true}`), nil
	})
	if err := s.processLyrics(context.Background(), c, path, saved, saved.Tracks, pending, &FetchResult{}); err != nil {
		t.Fatal(err)
	}
	saved, err = readTracksCache(path, "u", c.base)
	if err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	for _, r := range saved.Tracks {
		if needsLyricsAcquisition(r, saved) {
			t.Fatal("resume not completed", r)
		}
	}
}

func TestLyricsFailureRetryAndCacheOnlyNoRewrite(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	var calls atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() && strings.HasPrefix(r.URL.Path, "/tracks/1/") {
			fmt.Fprint(w, `invalid response`)
			return
		}
		fmt.Fprint(w, `{"result":{"lyrics":{"fullLyrics":"weed"}}}`)
	}))
	defer remote.Close()
	s := testServer(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected request") })
	c := newYandexClient("token")
	c.base = remote.URL
	path := filepath.Join(t.TempDir(), "private", "cache.json")
	cache := tracksCache{UserID: "u", URL: c.base, Fingerprint: detectorFingerprint(), Tracks: []Result{}}
	results := []Result{{Track: Track{ID: 1}, Status: "pending"}, {Track: Track{ID: 2}, Status: "pending"}}
	if err := s.processLyrics(context.Background(), c, path, cache, results, []int{0, 1}, &FetchResult{}); err != nil {
		t.Fatal(err)
	}
	saved, err := readTracksCache(path, "u", c.base)
	if err != nil || len(saved.Tracks) != 1 || results[0].Status != "error" || results[1].Status != "found" {
		t.Fatal("error aborted scan or cached as missing", saved, err)
	}
	fail.Store(false)
	results[0].Status = "pending"
	if err := s.processLyrics(context.Background(), c, path, saved, results, []int{0}, &FetchResult{}); err != nil {
		t.Fatal(err)
	}
	saved, err = readTracksCache(path, "u", c.base)
	if err != nil || len(saved.Tracks) != 2 {
		t.Fatal("retry failed", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	count := calls.Load()
	if err := s.processLyrics(context.Background(), c, path, saved, saved.Tracks, nil, &FetchResult{}); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || calls.Load() != count {
		t.Fatal("cache-only scan rewrote cache or refetched lyrics")
	}
}
