package bridge

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSpotifyYandexCover(t *testing.T) {
	var valid bytes.Buffer
	if err := png.Encode(&valid, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body                 string
		status                     int
		timeout, fail, replacement bool
	}{
		{name: "valid", body: valid.String(), status: 200},
		{name: "404", status: 404, replacement: true},
		{name: "html", body: "<html>not an image</html>", status: 200, replacement: true},
		{name: "invalid", body: "\x89PNG\r\n\x1a\n", status: 200, replacement: true},
		{name: "timeout", timeout: true, replacement: true},
		{name: "spotify failure", status: 404, fail: true},
		{name: "redirect", status: 302, replacement: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := spotifyTestServer(t)
			p := s.spotifyService()
			p.credentials = spotifyCredentials{Refresh: "refresh", Access: "access", Expires: time.Now().Add(time.Hour)}
			probes, searches := 0, 0
			p.http.Transport = spotifyTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "avatars.yandex.net" {
					probes++
					if r.Method != "GET" || r.Header.Get("Authorization") != "" {
						t.Error("unsafe probe")
					}
					if tc.timeout {
						<-r.Context().Done()
						return nil, r.Context().Err()
					}
					res := response(tc.status, tc.body)
					res.Header = make(http.Header)
					res.Header.Set("Location", "http://127.0.0.1/private")
					return res, nil
				}
				if r.URL.Host != "api.spotify.com" {
					t.Fatalf("unsafe request %s", r.URL)
				}
				searches++
				if tc.fail {
					return response(503, `{}`), nil
				}
				return response(200, `{"tracks":{"items":[{"name":"Song","artists":[{"name":"Artist"}],"album":{"images":[{"url":"https://i.scdn.co/image/new"}]}}]}}`), nil
			})
			cover := "https://avatars.yandex.net/get-music-content/fixture/200x200"
			track := Track{ID: 7, Title: "Song", Artist: "Artist", Cover: &cover, Available: availabilityBool(false)}
			got := s.spotifyFallback(context.Background(), track)
			if got.Cover == nil || strings.HasPrefix(*got.Cover, "https://i.scdn.co/") != tc.replacement {
				t.Fatalf("wrong cover: %+v", got)
			}
			if !tc.replacement && *got.Cover != cover {
				t.Fatal("lost prior cover")
			}
			if probes != 1 || (tc.name == "valid" && searches != 0) {
				t.Fatal(probes, searches)
			}
			_ = s.spotifyFallback(context.Background(), track)
			if probes != 1 {
				t.Fatal("probe not cached")
			}
			if got.ID != track.ID || got.Title != track.Title || got.Artist != track.Artist || got.Available != track.Available {
				t.Fatal("metadata changed")
			}
		})
	}
}

func TestCoverURLSafetyAndDisconnected(t *testing.T) {
	for _, raw := range []string{"http://avatars.yandex.net/a", "https://avatars.yandex.net:443/a", "https://avatars.yandex.net.evil/a", "https://user@avatars.yandex.net/a", "https://127.0.0.1/a", "https://i.scdn.co/image/a", "https://avatars.yandex.net/a?x=1"} {
		if safeYandexCover(raw) {
			t.Fatal(raw)
		}
	}
	s := spotifyTestServer(t)
	p := s.spotifyService()
	p.http.Transport = spotifyTransport(func(r *http.Request) (*http.Response, error) { t.Fatal("disconnected probe", r.URL); return nil, nil })
	cover := "https://avatars.yandex.net/a"
	_ = s.spotifyFallback(context.Background(), Track{Cover: &cover})
}
