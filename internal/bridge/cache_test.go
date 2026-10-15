package bridge

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestFetchStopsWhenServerSyncFails(t *testing.T) {
	syncCalls := 0
	s := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/v1/user":
			fmt.Fprint(w, `{"id":"user-1"}`)
		case "/rest/v1/bans":
			fmt.Fprint(w, `[]`)
		case "/rest/v1/ddetector_users":
			fmt.Fprint(w, `[{"user_id":"user-1"}]`)
		case "/functions/v1/sync-ddetector-tracks":
			syncCalls++
			w.WriteHeader(http.StatusBadGateway)
		default:
			t.Error("fetch continued after failed synchronization", r.URL.Path)
		}
	})
	install(s)
	s.yandex = newYandexClient("test-yandex")
	err := s.load(context.Background(), true)
	if err == nil || !strings.Contains(err.Error(), "unable to synchronize track list") || syncCalls != 1 {
		t.Fatalf("expected sync failure, got %v (calls %d)", err, syncCalls)
	}
}

func TestYandexIncrementalCache(t *testing.T) {
	syncCalls := 0
	library := `[{"id":1,"added_at":"now"}]`
	ignored := `[]`
	s := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/functions/v1/sync-ddetector-tracks":
			syncCalls++
			if r.Method != http.MethodPost || r.Header.Get("Authorization") == "" {
				t.Error("sync requires POST with user authorization")
			}
			fmt.Fprint(w, `{"added":0,"total":1}`)
		case "/auth/v1/user":
			fmt.Fprint(w, `{"id":"user-1"}`)
		case "/rest/v1/bans":
			fmt.Fprint(w, `[]`)
		case "/rest/v1/ddetector_ignored_tracks":
			if r.Header.Get("Range") == "0-999" {
				fmt.Fprint(w, ignored)
			} else {
				fmt.Fprint(w, `[]`)
			}
		case "/rest/v1/ddetector_tracks":
			if r.URL.Query().Get("select") != "id,added_at" || r.URL.Query().Get("order") != "added_at.desc,id.asc" {
				t.Error("incorrect membership query", r.URL)
			}
			if r.Header.Get("Range") == "0-999" {
				fmt.Fprint(w, library)
			} else {
				fmt.Fprint(w, `[]`)
			}
		case "/rest/v1/ddetector_users":
			fmt.Fprint(w, `[{"user_id":"user-1"}]`)
		default:
			t.Errorf("unexpected Supabase data request: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	install(s)
	lyricsCalls, metadataCalls := 0, 0

	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "OAuth secret-yandex" {
			t.Error("wrong authorization")
		}
		switch r.URL.Path {
		case "/account/status":
			fmt.Fprint(w, `{"result":{"account":{"uid":123}}}`)

		case "/tracks":
			metadataCalls++
			fmt.Fprint(w, `{"result":[{"id":"1","title":"Song","artists":[{"name":"Artist"}]}]}`)
		case "/tracks/1/lyrics":
			w.WriteHeader(404)
		case "/tracks/1/supplement":
			lyricsCalls++
			fmt.Fprint(w, `{"result":{"lyrics":{"fullLyrics":"weed\nordinary"}}}`)
		default:
			t.Error(r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer remote.Close()
	c := newYandexClient("secret-yandex")
	lrclibCalls := 0
	c.lrclib.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		lrclibCalls++
		if r.URL.Query().Get("track_name") != "Song" || r.URL.Query().Get("artist_name") != "Artist" {
			t.Error("worker did not forward metadata", r.URL)
		}
		return response(404, ""), nil
	})
	c.base = remote.URL
	if err := c.identify(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.yandex = c
	load := func(fetch bool) {
		t.Helper()
		if err := s.load(context.Background(), fetch); err != nil {
			t.Fatal(err)
		}
	}
	load(false)
	library = `[{"id":1,"added_at":"updated"}]`
	ignored = `[{"track_id":1}]`
	load(false)
	if !s.state.Tracks[0].Ignored || s.state.Tracks[0].AddedAt == nil || *s.state.Tracks[0].AddedAt != "updated" {
		t.Fatal("Supabase preferences/date not reconciled", s.state.Tracks)
	}
	if lyricsCalls != 1 || metadataCalls != 1 || s.state.Tracks[0].Status != "found" {
		t.Fatal("not incremental", lyricsCalls, metadataCalls)
	}
	path := s.path + ".yandex-123.tracks-cache.sqlite"
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("cache permissions", err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "secret-") {
		t.Fatal("credential persisted")
	}
	restarted, err := NewServer(s.client, s.key, s.base, s.path)
	if err != nil {
		t.Fatal(err)
	}
	install(restarted)
	restarted.yandex = c
	s = restarted
	load(false)
	if lyricsCalls != 1 {
		t.Fatal("restart refetched lyrics")
	}
	s.exclusionTransport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(200, `{"tracks":{}}`), nil
	})
	if syncCalls != 0 {
		t.Fatal("normal load must not trigger server synchronization")
	}
	load(true)
	if syncCalls != 1 {
		t.Fatal("fetch did not synchronize server track list")
	}
	if metadataCalls != 2 || lyricsCalls != 1 || lrclibCalls != 1 {
		t.Fatal("refresh not incremental")
	}
	library = `[]`
	load(false)
	if len(s.state.Tracks) != 0 || s.state.LastFetch.Removed != 1 {
		t.Fatal("removal failed")
	}
}
func TestLyricsSourceMigration(t *testing.T) {
	for _, absent := range []bool{false, true} {
		t.Run(fmt.Sprint("absent=", absent), func(t *testing.T) {
			s := testServer(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected request") })
			c := newYandexClient("secret")
			calls := 0
			c.lrclib.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return response(200, `{"id":1,"trackName":"Song","artistName":"Artist","syncedLyrics":"[00:01]weed","plainLyrics":"weed"}`), nil
			})
			c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Error("unexpected Yandex request")
				return response(404, ""), nil
			})
			lyrics := []Lyric{{Text: "weed"}}
			if absent {
				lyrics = []Lyric{}
			}
			old := s.detector.Process(Track{ID: 1, Title: "Song", Artist: "Artist"}, lyrics, false)
			path := s.path + ".migration-cache.json"
			cache := tracksCache{UserID: "u", URL: c.base, Fingerprint: "old-detector", Tracks: []Result{old}}
			if err := saveTracksCache(path, cache); err != nil {
				t.Fatal(err)
			}
			// Simulate a genuinely pre-LRCLIB cache with neither schema nor provenance.
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data = []byte(strings.ReplaceAll(string(data), `"lyricsSourceVersion":"lrclib-v1",`, ""))
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			cache, err = readTracksCache(path, "u", c.base)
			if err != nil || !needsLyricsAcquisition(cache.Tracks[0], cache) || hasTimedLyrics(cache.Tracks[0].Lyrics) {
				t.Fatal(cache, err)
			}
			results := append([]Result{}, cache.Tracks...)
			if err := s.processLyrics(context.Background(), c, path, cache, results, []int{0}, &FetchResult{}); err != nil {
				t.Fatal(err)
			}
			warm, err := readTracksCache(path, "u", c.base)
			if err != nil || len(warm.Tracks) != 1 || !hasTimedLyrics(warm.Tracks[0].Lyrics) || needsLyricsAcquisition(warm.Tracks[0], warm) || warm.LyricsSourceVersion != lyricsSourceVersion {
				t.Fatal(warm, err)
			}
			// Detector changes must not reset lyric-source completion.
			warm.Fingerprint = "another-detector"
			if err := saveTracksCache(path, warm); err != nil {
				t.Fatal(err)
			}
			warm, err = readTracksCache(path, "u", c.base)
			if err != nil || needsLyricsAcquisition(warm.Tracks[0], warm) {
				t.Fatal(warm, err)
			}
			if err := s.processLyrics(context.Background(), c, path, warm, warm.Tracks, nil, &FetchResult{}); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatal("migration refetched success", calls)
			}
		})
	}
}

func TestYandexRequestSafety(t *testing.T) {
	calls := 0
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(401)
		fmt.Fprint(w, "secret-yandex")
	}))
	defer remote.Close()
	c := newYandexClient("secret-yandex")
	c.base = remote.URL
	err := c.identify(context.Background())
	if err == nil || strings.Contains(err.Error(), "secret-yandex") || calls != 1 {
		t.Fatal("unsafe error", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if c.identify(ctx) == nil {
		t.Fatal("ignored cancellation")
	}
	s := testServer(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected request") })
	if w := api(s, "POST", "/api/yandex/token", `{"access_token":"bad\nvalue"}`, "bridge-key"); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := api(s, "POST", "/api/yandex/token", `{"access_token":"secret-yandex"}`, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
}
