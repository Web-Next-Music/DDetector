package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestYandexArtistIDsAndLegacyCache(t *testing.T) {
	c := newYandexClient("secret")
	c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(200, `{"result":[{"id":"36045842","title":"Отсиди","artists":[{"id":4622996,"name":"Овсянкин"},{"id":"42","name":"Guest"}]}]}`), nil
	})
	tracks, err := c.metadata(context.Background(), []string{"36045842"})
	if err != nil {
		t.Fatal(err)
	}
	track := tracks[36045842]
	if len(track.Artists) != 2 || track.Artists[0].ID != "4622996" || track.Artists[1].ID != "42" {
		t.Fatalf("lost IDs: %+v", track)
	}
	for _, original := range []Track{track, {ID: 1, Title: "Old", Artist: "Name", Artists: []TrackArtist{{Name: "Name"}}}} {
		data, err := json.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		var cached Track
		if err := json.Unmarshal(data, &cached); err != nil {
			t.Fatal(err)
		}
		if cached.Artist != original.Artist || len(cached.Artists) != len(original.Artists) {
			t.Fatal("cache lost artists")
		}
	}
}
