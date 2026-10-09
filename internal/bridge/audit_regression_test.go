package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLyricsResourceBudgets(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{"stamps", strings.Repeat("[00:01]", maxLRCStampsPerLine+1) + "weed"},
		{"expanded bytes", strings.Repeat(strings.Repeat("[00:01]", maxLRCStampsPerLine)+strings.Repeat("w", maxLyricLineBytes)+"\n", 3)},
		{"long line", "[00:01]" + strings.Repeat("w", maxLyricLineBytes+1)},
		{"line count", strings.Repeat("[00:01]w\n", maxLyricsLines+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseLRCChecked(tc.text); err == nil {
				t.Fatal("unbounded LRC accepted")
			}
		})
	}
	if _, err := parseLyricsTextChecked(strings.Repeat("w", maxLyricLineBytes+1)); err == nil {
		t.Fatal("unbounded plain text")
	}
	c := newYandexClient("secret")
	c.lrclib = nil
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/lyrics") {
			return response(200, `{"result":{"downloadUrl":"https://storage.mds.yandex.net/x"}}`), nil
		}
		if r.URL.Host == "storage.mds.yandex.net" {
			return response(200, strings.Repeat("[00:01]", maxLRCStampsPerLine+1)+strings.Repeat("w", maxLyricLineBytes+1)), nil
		}
		return response(200, `{"result":{}}`), nil
	})
	if lines, err := c.lyrics(context.Background(), 1); err == nil || lines != nil {
		t.Fatal("resource failure became absence", err)
	}
	primary := newLRCLIBClient()
	primary.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		b, _ := json.Marshal(map[string]any{"id": 1, "syncedLyrics": strings.Repeat("[00:01]", maxLRCStampsPerLine+1) + "weed"})
		return response(200, string(b)), nil
	})
	if _, _, err := primary.get(context.Background(), Track{Title: "song", Artist: "artist"}); err == nil {
		t.Fatal("LRCLIB accepted expansion")
	}
}

func TestDetectorCancellationAndBudget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := NewDetector()
	if _, err := d.ProcessContext(ctx, Track{ID: 1}, []Lyric{{Text: "weed"}}, false); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := d.ProcessContext(context.Background(), Track{ID: 1}, []Lyric{{Text: strings.Repeat("w", maxLyricLineBytes+1)}}, false); err == nil {
		t.Fatal("line budget ignored")
	}
	if r := d.Process(Track{ID: 1}, []Lyric{{Text: strings.Repeat("w", maxLyricLineBytes+1)}}, false); r.Status != "error" {
		t.Fatal(r.Status)
	}
}

type blockingStateWriter struct {
	*httptest.ResponseRecorder
	entered, release chan struct{}
}

func (w *blockingStateWriter) Write(p []byte) (int, error) {
	close(w.entered)
	<-w.release
	return w.ResponseRecorder.Write(p)
}
func TestStateWriteDoesNotHoldMutex(t *testing.T) {
	s := testServer(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected upstream") })
	s.publishResults([]Result{{Track: Track{ID: 1, Title: "old"}}})
	w := &blockingStateWriter{httptest.NewRecorder(), make(chan struct{}), make(chan struct{})}
	finished := make(chan struct{})
	go func() {
		r := httptest.NewRequest("GET", s.base+"/api/state", nil)
		r.Header.Set("Authorization", "Bearer bridge-key")
		s.Handler().ServeHTTP(w, r)
		close(finished)
	}()
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("write not started")
	}
	published := make(chan struct{})
	go func() { s.publishResults([]Result{{Track: Track{ID: 2, Title: "new"}}}); close(published) }()
	select {
	case <-published:
	case <-time.After(time.Second):
		close(w.release)
		<-finished
		t.Fatal("network write holds mu")
	}
	// Concurrent re-encoding must not mutate bytes retained by the slow writer.
	fresh := api(s, "GET", "/api/state", "", "bridge-key")
	close(w.release)
	<-finished
	if !strings.Contains(w.Body.String(), "old") || strings.Contains(w.Body.String(), "new") || !strings.Contains(fresh.Body.String(), "new") {
		t.Fatal("snapshot bytes mutated")
	}
}

func waitFor(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition timed out")
}

func runningCheckpointJob(t *testing.T) (*Server, string, <-chan struct{}) {
	t.Helper()
	s := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/v1/user":
			fmt.Fprint(w, `{"id":"user-1"}`)
		case "/rest/v1/ddetector_users":
			fmt.Fprint(w, `[{"user_id":"user-1"}]`)
		case "/rest/v1/ddetector_tracks":
			if r.Header.Get("Range") == "0-999" {
				fmt.Fprint(w, `[{"id":1},{"id":2}]`)
			} else {
				fmt.Fprint(w, `[]`)
			}
		default:
			fmt.Fprint(w, `[]`)
		}
	})
	install(s)
	if err := SaveSession(s.path, s.session); err != nil {
		t.Fatal(err)
	}
	s.state.AccessChecked = true
	c := newYandexClient("secret")
	c.uid = "123"
	c.lrclib = nil
	blocked := make(chan struct{})
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/tracks":
			return response(200, `{"result":[{"id":1,"title":"one"},{"id":2,"title":"two"}]}`), nil
		case "/tracks/1/supplement":
			return response(200, `{"result":{"lyrics":{"fullLyrics":"weed"}}}`), nil
		case "/tracks/2/supplement":
			close(blocked)
			<-r.Context().Done()
			return nil, r.Context().Err()
		default:
			return response(404, ""), nil
		}
	})
	s.yandex = c
	if w := api(s, "POST", "/api/load", "", "bridge-key"); w.Code != 202 {
		t.Fatal(w.Body)
	}
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("worker not blocked")
	}
	waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.state.Processed == 1 })
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return s, s.path + ".yandex-123.tracks-cache.sqlite", blocked
}

func TestJobLifecycleCheckpoint(t *testing.T) {
	for _, action := range []string{"close", "logout", "parent cancel"} {
		t.Run(action, func(t *testing.T) {
			s, path, _ := runningCheckpointJob(t)
			switch action {
			case "close":
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if err := s.Close(ctx); err != nil {
					t.Fatal(err)
				}
				if w := api(s, "POST", "/api/load", "", "bridge-key"); w.Code != 409 {
					t.Fatal("job accepted after close", w.Code)
				}
			case "logout":
				if w := api(s, "POST", "/api/logout", "", "bridge-key"); w.Code != 200 {
					t.Fatal(w.Body)
				}
				if _, err := os.Stat(s.path); !os.IsNotExist(err) {
					t.Fatal("session retained", err)
				}
				if s.session != nil || s.state.Authenticated || len(s.state.Tracks) != 0 {
					t.Fatal("logout did not clear identity")
				}
			case "parent cancel":
				ctx, cancel := context.WithCancel(context.Background())
				s.Start(ctx)
				cancel()
				waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.jobDone == nil })
			}
			cache, err := readTracksCache(path, "user-1", "https://api.music.yandex.net/users/123")
			if err != nil || len(cache.Tracks) != 1 || cache.Tracks[0].ID != 1 {
				t.Fatal("successful checkpoint lost", cache, err)
			}
		})
	}
}

func TestLogoutCancelsInFlightRefresh(t *testing.T) {
	entered := make(chan struct{})
	s := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/v1/token" {
			t.Error("unexpected request", r.URL.Path)
			w.WriteHeader(500)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
	})
	install(s)
	s.session.ExpiresAt = 0
	s.state.Tracks = []Result{{Track: Track{ID: 1}}}
	if err := SaveSession(s.path, s.session); err != nil {
		t.Fatal(err)
	}
	ignored := make(chan *httptest.ResponseRecorder, 1)
	go func() { ignored <- api(s, "POST", "/api/ignore", `{"track_id":1,"ignored":true}`, "bridge-key") }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("refresh not started")
	}
	if w := api(s, "POST", "/api/logout", "", "bridge-key"); w.Code != 200 {
		t.Fatal("logout did not cancel refresh", w.Body)
	}
	select {
	case w := <-ignored:
		if w.Code != 403 {
			t.Fatal(w.Body)
		}
	case <-time.After(time.Second):
		t.Fatal("refresh handler did not finish")
	}
	if _, err := os.Stat(s.path); !os.IsNotExist(err) {
		t.Fatal("session resurrected", err)
	}
	if s.session != nil || s.state.Authenticated {
		t.Fatal("identity retained")
	}
}

func TestCloseTimeoutCanBeRetried(t *testing.T) {
	s := testServer(t, func(http.ResponseWriter, *http.Request) {})
	s.jobDone = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(s.jobDone)
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLogoutBlocksNewJobs(t *testing.T) {
	s := testServer(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected upstream") })
	install(s)
	s.op.Lock()
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() { finished <- api(s, "POST", "/api/logout", "", "bridge-key") }()
	waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.logoutPending > 0 })
	s.op.Unlock()
	if w := api(s, "POST", "/api/load", "", "bridge-key"); w.Code != 409 && w.Code != 401 {
		t.Fatal("new job passed pending logout", w.Code)
	}
	select {
	case w := <-finished:
		if w.Code != 200 {
			t.Fatal(w.Body)
		}
	case <-time.After(time.Second):
		t.Fatal("logout stalled")
	}
}

func TestCacheSizeFailurePreservesPreviousFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "cache.json")
	cache := tracksCache{UserID: "u", URL: "base", Fingerprint: detectorFingerprint(), Tracks: []Result{NewDetector().Process(Track{ID: 1}, []Lyric{{Text: "weed"}}, false)}}
	if err := saveTracksCache(path, cache); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := saveTracksCacheLimit(path, cache, 32); err == nil {
		t.Fatal("oversized write accepted")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("old cache replaced")
	}
	if _, err := readPrivateFile(path, 32, true); err == nil {
		t.Fatal("reader budget ignored")
	}
	files, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".tracks-cache-*"))
	if err != nil || len(files) != 0 {
		t.Fatal("temporary files retained", files, err)
	}
}

func TestCacheMigrationPersistedWithoutAcquisition(t *testing.T) {
	s := testServer(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected upstream") })
	path := s.path + ".cache.json"
	cache := tracksCache{UserID: "u", URL: "base", Fingerprint: "old", LyricsSources: map[int64]string{1: lyricsSourceVersion}, Tracks: []Result{NewDetector().Process(Track{ID: 1}, []Lyric{{Text: "weed"}}, false)}}
	if err := saveTracksCache(path, cache); err != nil {
		t.Fatal(err)
	}
	migrated, err := readTracksCache(path, "u", "base")
	if err != nil || !migrated.Dirty {
		t.Fatal("migration not marked", err)
	}
	if err := s.processLyrics(context.Background(), newYandexClient("secret"), path, migrated, migrated.Tracks, nil, &FetchResult{}); err != nil {
		t.Fatal(err)
	}
	warm, err := readTracksCache(path, "u", "base")
	if err != nil || warm.Dirty || warm.Fingerprint != detectorFingerprint() {
		t.Fatal("migration not durable", err)
	}
}

func TestPartialLyricsErrorsPreserveLegacyAndReport(t *testing.T) {
	s := testServer(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected upstream") })
	c := newYandexClient("secret")
	c.lrclib = nil
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/tracks/3/supplement" {
			return response(200, `{"result":{"lyrics":{"fullLyrics":"weed"}}}`), nil
		}
		return response(401, "secret response"), nil
	})
	legacy := s.detector.Process(Track{ID: 1}, []Lyric{{Text: "weed"}}, false)
	results := []Result{legacy, {Track: Track{ID: 2}, Status: "pending"}, {Track: Track{ID: 3}, Status: "pending"}}
	stats := FetchResult{}
	path := s.path + ".cache.json"
	cache := tracksCache{UserID: "u", URL: "base", Fingerprint: detectorFingerprint(), Tracks: []Result{legacy}}
	if err := s.processLyrics(context.Background(), c, path, cache, results, []int{0, 1, 2}, &stats); err != nil {
		t.Fatal(err)
	}
	if !stats.Partial || stats.LyricsFailed != 2 || stats.LyricsFetched != 1 || stats.Error == "" || strings.Contains(stats.Error, "secret") {
		t.Fatal(stats)
	}
	if results[0].Status != "found" || results[1].Status != "error" || results[2].Status != "found" {
		t.Fatal("best effort changed")
	}
	saved, err := readTracksCache(path, "u", "base")
	if err != nil || len(saved.Tracks) != 2 || saved.LyricsSources[1] != "" || saved.LyricsSources[3] != lyricsSourceVersion {
		t.Fatal(saved, err)
	}
}

func TestPrivateFileEncodeFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "session.json")
	if err := writePrivateFile(path, ".test-*", 100, func(w io.Writer) error { _, err := io.WriteString(w, "old"); return err }); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("encode failed")
	err := writePrivateFile(path, ".test-*", 100, func(w io.Writer) error { _, _ = io.WriteString(w, "partial"); return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "old" {
		t.Fatal("failed encode replaced file")
	}
}

func TestLoadReportsPartialLyricsFailure(t *testing.T) {
	s := authorizedTestServer(t)
	install(s)
	c := newYandexClient("secret")
	c.uid = "123"
	c.lrclib = nil
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/tracks" {
			return response(200, `{"result":[{"id":1,"title":"song"}]}`), nil
		}
		return response(401, "secret upstream body"), nil
	})
	s.yandex = c
	if err := s.load(context.Background(), false); err != nil {
		t.Fatal("best effort scan aborted", err)
	}
	if s.state.LastFetch == nil || !s.state.LastFetch.Partial || s.state.LastFetch.LyricsFailed != 1 || s.state.Error == "" {
		t.Fatal(s.state.LastFetch, s.state.Error)
	}
	w := api(s, "GET", "/api/state", "", "bridge-key")
	if !strings.Contains(w.Body.String(), `"partial":true`) || strings.Contains(w.Body.String(), "secret") {
		t.Fatal(w.Body)
	}
}

func TestSessionSizeAndSingleJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "session.json")
	session := &Session{Access: "a", Refresh: "r", ExpiresAt: time.Now().Add(time.Hour).Unix(), User: User{ID: "u"}}
	if err := SaveSession(path, session); err != nil {
		t.Fatal(err)
	}
	session.User.Metadata = map[string]any{"large": strings.Repeat("w", int(maxSessionBytes))}
	if err := SaveSession(path, session); err == nil {
		t.Fatal("oversized session accepted")
	}
	if saved, err := ReadSession(path); err != nil || saved.User.ID != "u" || saved.User.Metadata != nil {
		t.Fatal("previous session lost", err)
	}
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(data, []byte(`{}`)...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSession(path); err == nil {
		t.Fatal("multiple JSON values accepted")
	}
}

func FuzzLRCBudgets(f *testing.F) {
	for _, seed := range []string{"[00:01][00:02]weed", "[offset:-100]\n[01:01.12]lyrics", strings.Repeat("[00:01]", maxLRCStampsPerLine+1) + "weed"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		lines, err := parseLRCChecked(text)
		if err != nil {
			if lines != nil {
				t.Fatal("partial lyrics returned on budget error")
			}
			return
		}
		if err := validateLyrics(lines); err != nil {
			t.Fatal("parser produced over-budget lyrics", err)
		}
	})
}

func TestCheckpointBatchScaling(t *testing.T) {
	for _, tc := range []struct{ total, want int }{{0, 32}, {12, 32}, {640, 32}, {1000, 50}, {10000, 500}, {100000, 1024}} {
		if got := checkpointBatchSize(tc.total); got != tc.want {
			t.Fatal(tc.total, got, tc.want)
		}
	}
}

func BenchmarkCacheCheckpoint(b *testing.B) {
	for _, count := range []int{100, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "private", "cache.json")
			cache := tracksCache{UserID: "u", URL: "base", Fingerprint: detectorFingerprint(), Tracks: make([]Result, count)}
			d := NewDetector()
			for i := range cache.Tracks {
				cache.Tracks[i] = d.Process(Track{ID: int64(i + 1)}, []Lyric{{Text: strings.Repeat("ordinary lyrics ", 100)}}, false)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := saveTracksCache(path, cache); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkDetectorOrdinaryLyrics(b *testing.B) {
	d := NewDetector()
	text := strings.Repeat("ordinary lyrics without matches ", 100)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		d.Annotate(text)
	}
}
