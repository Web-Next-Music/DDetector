package bridge

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"testing"
)

func TestMembershipPagination(t *testing.T) {
	for _, pageSize := range []int{1000, 2} {
		t.Run(strconv.Itoa(pageSize), func(t *testing.T) {
			calls := 0
			s := testServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/rest/v1/ddetector_tracks" || r.URL.Query().Get("select") != "id,added_at" || r.URL.Query().Get("order") != "added_at.desc,id.asc" || r.Header.Get("Range-Unit") != "items" || r.Header.Get("Authorization") != "Bearer access" {
					t.Error("unexpected membership request", r.URL, r.Header)
				}
				var start, end int
				if _, err := fmt.Sscanf(r.Header.Get("Range"), "%d-%d", &start, &end); err != nil || end != start+999 {
					t.Error("bad range")
				}
				fmt.Fprint(w, "[")
				for i := start; i < start+pageSize && i < 2001; i++ {
					if i > start {
						fmt.Fprint(w, ",")
					}
					fmt.Fprintf(w, `{"id":%d,"added_at":"date"}`, i+1)
				}
				fmt.Fprint(w, "]")
			})
			tracks, err := rows[Track](context.Background(), s.client, "ddetector_tracks", "id,added_at", "added_at.desc,id.asc", "access")
			if err != nil || len(tracks) != 2001 {
				t.Fatal(len(tracks), err)
			}
			for i, track := range tracks {
				if track.ID != int64(i+1) || track.AddedAt == nil || *track.AddedAt != "date" {
					t.Fatal("lost row", i, track)
				}
			}
			if calls != (2001+pageSize-1)/pageSize+1 {
				t.Fatal("incomplete pagination", calls)
			}
		})
	}
}

func TestMissingMembershipMetadata(t *testing.T) {
	s := authorizedTestServer(t)
	install(s)
	c := newYandexClient("secret")
	c.uid = "123"
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/tracks" {
			t.Error("unexpected Yandex request", r.URL.Path)
		}
		return response(200, `{"result":[]}`), nil
	})
	s.yandex = c
	path := s.path + ".yandex-123.tracks-cache.sqlite"
	cache := tracksCache{UserID: "user-1", URL: c.base + "/users/123", Fingerprint: detectorFingerprint(), Tracks: []Result{NewDetector().Process(Track{ID: 2, Title: "Removed later"}, []Lyric{}, false)}}
	if err := saveTracksCache(path, cache); err != nil {
		t.Fatal(err)
	}
	if err := s.load(context.Background(), false); err == nil {
		t.Fatal("missing new metadata accepted")
	}
	saved, err := readTracksCache(path, cache.UserID, cache.URL)
	if err != nil || len(saved.Tracks) != 1 || saved.Tracks[0].ID != 2 || len(s.state.Tracks) != 1 || s.state.Tracks[0].ID != 2 {
		t.Fatal("failed load pruned cache", saved, err)
	}
	cache.Tracks = []Result{NewDetector().Process(Track{ID: 1, Title: "Cached metadata"}, []Lyric{}, false)}
	cache.LyricsSources = map[int64]string{1: lyricsSourceVersion}
	if err := saveTracksCache(path, cache); err != nil {
		t.Fatal(err)
	}
	if err := s.load(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if s.state.Tracks[0].Title != "Cached metadata" || s.state.Tracks[0].Status != "none" {
		t.Fatal("refresh discarded cached metadata", s.state.Tracks)
	}
}
