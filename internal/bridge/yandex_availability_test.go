package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
)

func TestYandexMetadataAvailability(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		want         *bool
	}{
		{"available", `,"available":true`, availabilityBool(true)},
		{"unavailable", `,"available":false`, availabilityBool(false)},
		{"blocked API response", `,"available":false,"availableForPremiumUsers":true,"availableForOptions":["bookmate"],"error":"no-rights"`, availabilityBool(false)},
		{"no rights", `,"error":"no-rights"`, availabilityBool(false)},
		{"premium alone is unknown", `,"availableForPremiumUsers":false`, nil},
		{"missing", ``, nil},
		{"null", `,"available":null`, nil},
		{"deleted", `,"error":"not-found"`, availabilityBool(false)},
		{"unknown error", `,"error":"temporary-error"`, nil},
		{"structured error", `,"error":{"message":"failed"}`, nil},
		{"explicit boolean", `,"available":true,"error":"not-found"`, availabilityBool(true)},
		{"explicit boolean with rights marker", `,"available":true,"error":"no-rights"`, availabilityBool(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newYandexClient("secret")
			c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return response(200, `{"result":[{"id":"1"`+tc.fields+`}]}`), nil
			})
			tracks, err := c.metadata(context.Background(), []string{"1"})
			if err != nil {
				t.Fatal(err)
			}
			assertAvailability(t, tracks[1].Available, tc.want)
		})
	}
	c := newYandexClient("secret")
	for _, body := range []string{`{"result":[]}`, `{"error":"not-found"}`} {
		c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return response(200, body), nil })
		tracks, err := c.metadata(context.Background(), []string{"1"})
		if len(tracks) != 0 {
			t.Fatal("missing response or request error invented availability", tracks, err)
		}
	}
}

func availabilityBool(v bool) *bool { return &v }
func assertAvailability(t *testing.T, got, want *bool) {
	t.Helper()
	if (got == nil) != (want == nil) || got != nil && want != nil && *got != *want {
		t.Fatalf("availability = %v, want %v", got, want)
	}
}

func TestYandexAvailabilityBackfillsOldCacheOnNormalLoad(t *testing.T) {
	s := authorizedTestServer(t)
	install(s)
	c := newYandexClient("secret")
	c.uid = "123"
	calls := 0
	fields := `,"title":"Cached song","artists":[{"name":"Artist"}]`
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/tracks" {
			t.Errorf("normal load requested audio or lyrics: %s", r.URL.Path)
			return response(404, ""), nil
		}
		calls++
		return response(200, `{"result":[{"id":1`+fields+`}]}`), nil
	})
	s.yandex = c
	if err := s.load(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	assertAvailability(t, s.state.Tracks[0].Available, nil)
	// Old completed caches predate availability; pending results are not persisted.
	path, release, err := acquireTracksStoragePath(s.path, c.uid, s.customSession)
	if err != nil {
		t.Fatal(err)
	}
	completed := s.detector.Process(s.state.Tracks[0].Track, []Lyric{{Text: "ordinary"}}, false)
	cache := tracksCache{UserID: s.session.User.ID, URL: c.base + "/users/" + c.uid, Tracks: []Result{completed}}
	err = saveTracksCache(path, cache)
	release()
	if err != nil {
		t.Fatal(err)
	}
	fields = `,"available":false,"error":"no-rights"`
	if err := s.load(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	assertAvailability(t, s.state.Tracks[0].Available, availabilityBool(false))
	if calls != 2 || s.state.Tracks[0].Title != "Cached song" {
		t.Fatalf("old cache was not backfilled with preserved metadata: calls=%d track=%+v", calls, s.state.Tracks[0])
	}
	if err := s.load(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("known availability was unnecessarily fetched again", calls)
	}
}

func TestYandexAvailabilityCacheRoundTrip(t *testing.T) {
	for _, ext := range []string{".json", ".sqlite"} {
		for _, value := range []*bool{nil, availabilityBool(false), availabilityBool(true)} {
			t.Run(ext+"/"+stringAvailability(value), func(t *testing.T) {
				r := NewDetector().Process(Track{ID: 1, Title: "Song", Available: value}, []Lyric{{Text: "ordinary"}}, false)
				data, err := json.Marshal(r)
				if err != nil {
					t.Fatal(err)
				}
				var public map[string]any
				if err = json.Unmarshal(data, &public); err != nil {
					t.Fatal(err)
				}
				if value == nil {
					if _, ok := public["available"]; ok {
						t.Fatal("unknown must be omitted")
					}
				} else if public["available"] != *value {
					t.Fatal(public)
				}
				path := filepath.Join(t.TempDir(), "private", "cache"+ext)
				cache := tracksCache{UserID: "u", URL: "source", Fingerprint: "old-detector", Tracks: []Result{r}}
				if err = saveTracksCache(path, cache); err != nil {
					t.Fatal(err)
				}
				loaded, err := readTracksCache(path, "u", "source")
				if err != nil || len(loaded.Tracks) != 1 {
					t.Fatal(loaded, err)
				}
				assertAvailability(t, loaded.Tracks[0].Available, value)
			})
		}
	}
}
func stringAvailability(v *bool) string {
	if v == nil {
		return "unknown"
	}
	if *v {
		return "true"
	}
	return "false"
}

func TestYandexAvailabilityDeletedMetadataFallback(t *testing.T) {
	s := authorizedTestServer(t)
	install(s)
	c := newYandexClient("secret")
	c.uid = "123"
	fields := `,"title":"Song","artists":[{"name":"Artist"}],"coverUri":"example.com/%%","available":true`
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/tracks" {
			return response(200, `{"result":[{"id":1`+fields+`}]}`), nil
		}
		if r.URL.Path == "/tracks/1/supplement" {
			return response(200, `{"result":{}}`), nil
		}
		return response(404, ""), nil
	})
	s.yandex = c
	s.exclusionTransport = roundTripFunc(func(*http.Request) (*http.Response, error) { return response(200, `{"tracks":{}}`), nil })
	if err := s.load(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	original := s.state.Tracks[0]
	for _, tc := range []struct {
		fields string
		want   *bool
	}{
		{`,"error":"not-found"`, availabilityBool(false)},
		{``, nil},
		{`,"available":true`, availabilityBool(true)},
	} {
		fields = tc.fields
		if err := s.load(context.Background(), true); err != nil {
			t.Fatal(err)
		}
		got := s.state.Tracks[0]
		assertAvailability(t, got.Available, tc.want)
		if got.Title != original.Title || got.Artist != original.Artist || got.Cover == nil || *got.Cover != *original.Cover {
			t.Fatal("lost cached metadata", got)
		}
		if err := s.load(context.Background(), false); err != nil {
			t.Fatal(err)
		}
		assertAvailability(t, s.state.Tracks[0].Available, tc.want)
	}
}
