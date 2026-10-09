package bridge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestExclusionRefreshPurgeAndLoad(t *testing.T) {
	failLibrary := false
	s := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/v1/user":
			fmt.Fprint(w, `{"id":"user-1"}`)
		case "/rest/v1/bans":
			fmt.Fprint(w, `[]`)
		case "/rest/v1/ddetector_users":
			fmt.Fprint(w, `[{"user_id":"user-1"}]`)
		case "/rest/v1/ddetector_tracks":
			if r.Header.Get("Range") != "0-999" {
				fmt.Fprint(w, `[]`)
				return
			}
			if failLibrary {
				w.WriteHeader(400)
				return
			}
			fmt.Fprint(w, `[{"id":1},{"id":2}]`)
		case "/rest/v1/ddetector_ignored_tracks":
			fmt.Fprint(w, `[]`)
		default:
			t.Error("unexpected request", r.URL.Path)
		}
	})
	install(s)
	c := newYandexClient("secret-yandex")
	c.uid = "123"
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/tracks" {
			t.Fatal("excluded track reached lyrics", r.URL)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("track-ids") != "2" {
			t.Fatal("excluded track reached metadata", r.Form)
		}
		return response(200, `{"result":[{"id":"2","title":"Song"}]}`), nil
	})
	s.yandex = c
	path := s.path + ".yandex-123.tracks-cache.sqlite"
	cache := tracksCache{UserID: "user-1", URL: c.base + "/users/123", Fingerprint: detectorFingerprint(), LyricsSources: map[int64]string{1: lyricsSourceVersion, 2: lyricsSourceVersion}, Tracks: []Result{{Track: Track{ID: 1}, Status: "none", Lyrics: []Lyric{}}, {Track: Track{ID: 2}, Status: "none", Lyrics: []Lyric{}}}}
	if err := saveTracksCache(path, cache); err != nil {
		t.Fatal(err)
	}
	s.publishResults(cache.Tracks)
	body := `{"tracks":{}}`
	calls := 0
	s.exclusionTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != exclusionListURL || len(r.Header) != 0 {
			t.Fatal("credentials or wrong endpoint", r)
		}
		return response(200, body), nil
	})
	// A valid updated policy must survive a later membership failure.
	body = `{"tracks":{"1":"https://example.com"}}`
	failLibrary = true
	if err := s.load(context.Background(), true); err == nil {
		t.Fatal("expected membership failure")
	}
	saved, err := readTracksCache(path, cache.UserID, cache.URL)
	if err != nil || len(saved.Tracks) != 1 || saved.Tracks[0].ID != 2 || !saved.Excluded[1] || len(s.state.Tracks) != 1 || s.state.Tracks[0].ID != 2 {
		t.Fatal("purge not committed/published", saved, s.state.Tracks, err)
	}
	db, err := openSQLiteCache(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	err = db.QueryRow(`SELECT count(*) FROM cache_tracks WHERE id=1`).Scan(&count)
	db.Close()
	if err != nil || count != 0 {
		t.Fatal("SQLite row not deleted", count, err)
	}
	failLibrary = false
	// Restart and ordinary load must use the persisted policy, without downloading.
	restarted, err := NewServer(s.client, s.key, s.base, s.path)
	if err != nil {
		t.Fatal(err)
	}
	install(restarted)
	restarted.yandex = c
	if err := restarted.load(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if len(restarted.state.Tracks) != 1 || restarted.state.Tracks[0].ID != 2 || calls != 1 {
		t.Fatal("excluded track reintroduced")
	}
	if err := s.load(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("Fetch did not refresh")
	}
	if s.state.LastFetch == nil || s.state.LastFetch.Removed != 0 {
		t.Fatal("previously purged track counted again", s.state.LastFetch)
	}
	body = `{"tracks":{"1":"url","2":"url"}}`
	if err := s.load(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if len(s.state.Tracks) != 0 || calls != 3 {
		t.Fatal("updated exclusion list did not remove newly excluded track")
	}
	if s.state.LastFetch == nil || s.state.LastFetch.Partial || s.state.LastFetch.Removed != 1 {
		t.Fatal("successful Fetch must count the early purge exactly once", s.state.LastFetch)
	}
	if err := s.load(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if s.state.LastFetch.Removed != 0 {
		t.Fatal("repeated Fetch counted the same exclusion again", s.state.LastFetch)
	}
	// Invalid refreshes preserve both the committed cache and visible UI snapshot.
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{`, `{"tracks":null}`, `{"tracks":{"3":null}}`, `{"tracks":{"bad":"url"}}`} {
		body = bad
		if err := s.load(context.Background(), true); err == nil || !strings.Contains(err.Error(), "invalid exclusion list") {
			t.Fatal("invalid list accepted", err)
		}
		after, _ := os.ReadFile(path)
		if string(before) != string(after) || len(s.state.Tracks) != 0 {
			t.Fatal("invalid list changed cache/UI")
		}
	}
	s.exclusionTransport = roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("network unavailable secret") })
	if err := s.load(context.Background(), true); err == nil || !strings.Contains(err.Error(), "retry Fetch") || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) || len(s.state.Tracks) != 0 {
		t.Fatal("network failure changed cache/UI")
	}
}

func TestExclusionDownloadValidation(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   string
	}{
		{302, ``, "HTTP 302"},
		{503, ``, "HTTP 503"},
		{200, strings.Repeat("x", maxExclusionListBytes+1), "exceeds 8 MiB"},
		{200, `{"tracks":{}} trailing`, "invalid exclusion list"},
		{200, `{"tracks":{"0":"url"}}`, "invalid exclusion list"},
		{200, `{"tracks":{"1":42}}`, "invalid exclusion list"},
	} {
		_, err := downloadExclusions(context.Background(), roundTripFunc(func(*http.Request) (*http.Response, error) { return response(tc.status, tc.body), nil }))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatal(tc.want, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := downloadExclusions(ctx, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if _, ok := r.Context().Deadline(); !ok {
			t.Error("missing bounded deadline")
		}
		return nil, r.Context().Err()
	}))
	if err == nil {
		t.Fatal("cancellation accepted")
	}
}
