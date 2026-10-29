package bridge

import (
	"context"
	"net/http"
	"testing"
)

func TestYandexMetadataCoverFallbacks(t *testing.T) {
	const path = "avatars.yandex.net/get-music-content/123/album/%%"
	const want = "https://avatars.yandex.net/get-music-content/123/album/200x200"
	for _, tc := range []struct{ name, fields, want string }{
		{"track", `"coverUri":"` + path + `","ogImage":"avatars.yandex.net/other"`, want},
		{"absolute", `"coverUri":"https://` + path + `"`, want},
		{"protocol relative", `"coverUri":"//` + path + `"`, want},
		{"og image", `"ogImage":"` + path + `"`, want},
		{"album", `"albums":[{"coverUri":"` + path + `"}]`, want},
		{"album og image", `"albums":[{"ogImage":"` + path + `"}]`, want},
		{"later album", `"albums":[{}, {"coverUri":"` + path + `"}]`, want},
		{"unsafe primary", `"coverUri":"https://secret@avatars.yandex.net/private","ogImage":"` + path + `"`, want},
		{"deleted no artwork", `"albums":[]`, ""},
		{"insecure", `"coverUri":"http://` + path + `"`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newYandexClient("secret")
			calls := 0
			c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Path != "/tracks" {
					t.Errorf("unexpected request: %s", r.URL.Path)
				}
				return response(200, `{"result":[{"id":1,"available":false,`+tc.fields+`}]}`), nil
			})
			tracks, err := c.metadata(context.Background(), []string{"1"})
			if err != nil {
				t.Fatal(err)
			}
			got := ""
			if tracks[1].Cover != nil {
				got = *tracks[1].Cover
			}
			if got != tc.want {
				t.Fatalf("cover=%q want=%q", got, tc.want)
			}
			if calls != 1 {
				t.Fatal("artwork lookup performed extra requests", calls)
			}
			assertAvailability(t, tracks[1].Available, availabilityBool(false))
		})
	}
}
