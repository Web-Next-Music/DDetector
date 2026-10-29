package bridge

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"testing"
)

func TestLibraryGeniURLExistingCache(t *testing.T) {
	for _, fetch := range []bool{false, true} {
		t.Run(fmt.Sprint("fetch=", fetch), func(t *testing.T) {
			s := authorizedTestServer(t)
			install(s)
			c := newYandexClient("secret")
			c.uid = "123"
			c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/tracks" {
					t.Errorf("unexpected Yandex request: %s", r.URL.Path)
				}
				return response(200, `{"result":[{"id":1,"title":"Отсиди","artists":[{"id":7,"name":"Овсянкин"}],"available":false,"coverUri":"avatars.yandex.net/get-music-content/broken/200x200"}]}`), nil
			})
			s.yandex = c
			s.exclusionTransport = roundTripFunc(func(*http.Request) (*http.Response, error) { return response(200, `{"tracks":{}}`), nil })
			calls := 0
			p := s.coverService()
			p.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "avatars.yandex.net" {
					return response(404, "missing"), nil
				}
				calls++
				if r.URL.Host != "api.sv443.net" || r.Header.Get("Authorization") != "" {
					t.Errorf("unexpected artwork request: %s", r.URL)
				}
				return response(200, `{"matches":1,"resources":{"image":"https://images.genius.com/fixture.png"}}`), nil
			})
			path, release, err := acquireTracksStoragePath(s.path, c.uid, s.customSession)
			if err != nil {
				t.Fatal(err)
			}
			cover := "https://avatars.yandex.net/get-music-content/broken/200x200"
			result := s.detector.Process(Track{Cover: &cover, ID: 1, Title: "Отсиди", Artist: "Овсянкин", Artists: []TrackArtist{{ID: "7", Name: "Овсянкин"}}, Available: availabilityBool(false)}, []Lyric{{Text: "ordinary"}}, false)
			cache := tracksCache{UserID: s.session.User.ID, URL: c.base + "/users/" + c.uid, Fingerprint: detectorFingerprint(), LyricsSources: map[int64]string{1: lyricsSourceVersion}, Tracks: []Result{result}}
			err = saveTracksCache(path, cache)
			release()
			if err != nil {
				t.Fatal(err)
			}
			if err := s.load(context.Background(), fetch); err != nil {
				t.Fatal(err)
			}
			saved, err := readTracksCache(path, cache.UserID, cache.URL)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || len(saved.Tracks) != 1 || saved.Tracks[0].Cover == nil || *saved.Tracks[0].Cover != "https://images.genius.com/fixture.png" {
				t.Fatalf("fallback not persisted: calls=%d saved=%+v", calls, saved)
			}
			got := saved.Tracks[0]
			if got.Status != result.Status || got.Ignored != result.Ignored || !reflect.DeepEqual(got.Lyrics, result.Lyrics) {
				t.Fatal("library state changed", got)
			}
		})
	}
}
