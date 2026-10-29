package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestNormalizeISRC(t *testing.T) {
	for _, tc := range []struct{ name, raw, want string }{
		{"canonical", "USRC17607839", "USRC17607839"},
		{"lowercase", "usrc17607839", "USRC17607839"},
		{"hyphens and whitespace", " \tus-rc1-76-07839\n", "USRC17607839"},
		{"alphanumeric registrant", "GB1A22345678", "GB1A22345678"},
		{"empty", "", ""},
		{"short", "USRC1760783", ""},
		{"long", "USRC176078390", ""},
		{"country digits", "U1RC17607839", ""},
		{"registrant punctuation", "USRC_7607839", ""},
		{"year letters", "USRC1AA07839", ""},
		{"designation letters", "USRC1760783A", ""},
		{"internal whitespace", "US RC17607839", ""},
		{"unicode", "УSRC17607839", ""},
		{"prefix", "ISRC USRC17607839", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeISRC(tc.raw); got != tc.want {
				t.Fatalf("normalizeISRC(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestYandexMetadataISRC(t *testing.T) {
	for _, tc := range []struct{ name, fields, want string }{
		{"track", `"isrc":"us-rc1-76-07839"`, "USRC17607839"},
		{"missing", `"title":"No ISRC"`, ""},
		{"null", `"isrc":null`, ""},
		{"invalid", `"isrc":"not-an-isrc"`, ""},
		{"album only", `"albums":[{"isrc":"GB1A22345678"}]`, ""},
		{"track over album", `"isrc":"USRC17607839","albums":[{"isrc":"GB1A22345678"}]`, "USRC17607839"},
		{"invalid track ignores album", `"isrc":"invalid","albums":[{"isrc":"GB1A22345678"}]`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newYandexClient("secret")
			calls := 0
			c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/tracks" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				return response(200, `{"result":[{"id":1,"ogImage":"avatars.yandex.net/track/%%",`+tc.fields+`}]}`), nil
			})
			tracks, err := c.metadata(context.Background(), []string{"1"})
			if err != nil {
				t.Fatal(err)
			}
			track := tracks[1]
			if track.ISRC != tc.want {
				t.Fatalf("ISRC = %q, want %q", track.ISRC, tc.want)
			}
			if track.Cover == nil || *track.Cover != "https://avatars.yandex.net/track/200x200" {
				t.Fatalf("cover fallback changed: %v", track.Cover)
			}
			if calls != 1 {
				t.Fatalf("metadata made %d requests, want 1", calls)
			}
			data, err := json.Marshal(track)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			value, present := fields["isrc"]
			if present != (tc.want != "") {
				t.Fatalf("isrc presence = %v, JSON: %s", present, data)
			}
			if present && string(value) != `"`+tc.want+`"` {
				t.Fatalf("serialized isrc = %s, want %q", value, tc.want)
			}
		})
	}
}
