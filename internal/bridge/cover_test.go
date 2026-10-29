package bridge

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"reflect"
	"testing"
	"time"
)

func TestGeniURLCoverFallback(t *testing.T) {
	var valid bytes.Buffer
	if err := png.Encode(&valid, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	const artwork = "https://images.genius.com/cover.1000x1000x1.png"
	for _, tc := range []struct {
		name, cover, body string
		status            int
		want              bool
	}{
		{"missing", "", `{"matches":1,"resources":{"image":"` + artwork + `"}}`, 200, true},
		{"broken", "https://avatars.yandex.net/broken", `{"matches":1,"resources":{"image":"` + artwork + `"}}`, 200, true},
		{"valid", "https://avatars.yandex.net/valid", ``, 200, false},
		{"failure", "https://avatars.yandex.net/broken", `{}`, 503, false},
		{"no match", "", `{"matches":0,"resources":{"image":"` + artwork + `"}}`, 200, false},
		{"api error", "", `{"error":true,"matches":1,"resources":{"image":"` + artwork + `"}}`, 200, false},
		{"unsafe", "", `{"matches":1,"resources":{"image":"http://127.0.0.1/private"}}`, 200, false},
		{"thumbnail", "", `{"matches":1,"resources":{"image":"http://bad","thumbnail":"` + artwork + `"}}`, 200, true},
		{"invalid JSON", "", `{`, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{}
			p := s.coverService()
			probes, searches := 0, 0
			p.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" || r.Header.Get("Authorization") != "" {
					t.Fatal("unsafe request", r)
				}
				if r.URL.Host == "avatars.yandex.net" {
					probes++
					if r.URL.Path == "/valid" {
						return response(200, valid.String()), nil
					}
					return response(404, "missing"), nil
				}
				searches++
				if r.URL.Host != "api.sv443.net" || r.URL.Path != "/geniurl/search/top" || r.URL.Query().Get("artist") != "Artist & Guest" || r.URL.Query().Get("song") != "Song + Title" {
					t.Fatal(r.URL)
				}
				return response(tc.status, tc.body), nil
			})
			track := Track{ID: 7, Title: "Song + Title", Artist: "Artist & Guest"}
			if tc.cover != "" {
				cover := tc.cover
				track.Cover = &cover
			}
			got := s.coverFallback(context.Background(), track)
			expected := track
			if tc.want {
				cover := artwork
				expected.Cover = &cover
			}
			if !reflect.DeepEqual(got, expected) {
				t.Fatalf("got %+v, expected %+v", got, expected)
			}
			_ = s.coverFallback(context.Background(), track)
			if searches > 1 || probes > 1 {
				t.Fatalf("not cached: searches=%d probes=%d", searches, probes)
			}
		})
	}
}

func TestCoverProbeAllTracksAndExpiry(t *testing.T) {
	p := (&Server{}).coverService()
	calls := 0
	p.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return response(404, "missing"), nil })
	ctx := context.Background()
	for i := 0; i < 40; i++ {
		p.brokenYandexCover(ctx, fmt.Sprintf("https://avatars.yandex.net/%d", i))
	}
	if calls != 40 {
		t.Fatal(calls)
	}
	raw := "https://avatars.yandex.net/0"
	hit := p.coverCache[raw]
	hit.expires = time.Now().Add(-time.Second)
	p.coverCache[raw] = hit
	p.brokenYandexCover(context.Background(), raw)
	if calls != 41 {
		t.Fatal(calls)
	}
}

func TestGeniusCoverSafety(t *testing.T) {
	for _, raw := range []string{"http://images.genius.com/a", "https://images.genius.com:443/a", "https://images.genius.com.evil/a", "https://user@images.genius.com/a", "https://images.genius.com/a?x=1", "https://images.genius.com/a#frag", "https://127.0.0.1/a", "https://images.genius.com/"} {
		if safeGeniusCover(raw) {
			t.Fatal(raw)
		}
	}
	for _, raw := range []string{"https://images.genius.com/a.png", "https://images.rapgenius.com/a.jpg"} {
		if !safeGeniusCover(raw) {
			t.Fatal(raw)
		}
	}
}

// A library taking longer than five seconds must still resolve its last cover.
func TestCoverFallbackProcessesWholeLibrary(t *testing.T) {
	s := &Server{}
	p := s.coverService()
	probes, searches := 0, 0
	p.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "avatars.yandex.net" {
			probes++
			return response(404, "missing"), nil
		}
		searches++
		timer := time.NewTimer(140 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return response(200, `{"matches":1,"resources":{"image":"https://images.genius.com/cover.png"}}`), nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for i := 0; i < 40; i++ {
		cover := fmt.Sprintf("https://avatars.yandex.net/broken/%d", i)
		track := Track{ID: int64(i + 1), Title: fmt.Sprintf("Song %d", i), Artist: "Artist", Cover: &cover}
		got := s.coverFallback(ctx, track)
		if got.Cover == nil || *got.Cover != "https://images.genius.com/cover.png" {
			t.Fatalf("track %d skipped: %+v", i, got)
		}
	}
	if probes != 40 || searches != 40 {
		t.Fatalf("incomplete library: probes=%d searches=%d", probes, searches)
	}
}

func TestCoverFallbackCancellation(t *testing.T) {
	s := &Server{}
	p := s.coverService()
	calls := 0
	p.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	track := Track{Title: "Song", Artist: "Artist"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if got := s.coverFallback(ctx, track); !reflect.DeepEqual(got, track) {
		t.Fatal("cancellation changed metadata")
	}
	if len(p.searchCache) != 0 {
		t.Fatal("cancellation cached as a miss")
	}
	_ = s.coverFallback(ctx, track)
	if calls != 1 {
		t.Fatal("request made after cancellation")
	}
}
