package bridge

import (
	"context"
	"net/http"
	"reflect"
	"testing"
)

func TestLyricsFormatFallbacks(t *testing.T) {
	for _, tc := range []struct {
		name, lrc, text, supplement string
		lrcStatus, textStatus       int
		want                        string
		calls                       []string
	}{
		{"TEXT after missing LRC", "", "\ufeffweed\r\n\r\nordinary", "", 404, 200, "weed", []string{"LRC", "TEXT"}},
		{"TEXT after untimed LRC", "older text", "weed", "", 200, 200, "weed", []string{"LRC", "TEXT"}},
		{"TEXT after empty LRC", "[ti:Title]", "weed", "", 200, 200, "weed", []string{"LRC", "TEXT"}},
		{"TEXT after LRC failure", "", "weed", "", 500, 200, "weed", []string{"LRC", "LRC", "LRC", "TEXT"}},
		{"keep untimed LRC", "weed", "", "", 200, 404, "weed", []string{"LRC", "TEXT"}},
		{"supplement last", "", "", "weed", 404, 404, "weed", []string{"LRC", "TEXT", "supplement"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newYandexClient("secret")
			var calls []string
			c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "storage.mds.yandex.net" {
					if r.Header.Get("Authorization") != "" {
						t.Error("download credentials")
					}
					if r.URL.Path == "/LRC" {
						return response(200, tc.lrc), nil
					}
					return response(200, tc.text), nil
				}
				if r.URL.Path == "/tracks/1/supplement" {
					calls = append(calls, "supplement")
					return response(200, `{"result":{"lyrics":{"fullLyrics":"`+tc.supplement+`"}}}`), nil
				}
				q := r.URL.Query()
				format := q.Get("format")
				calls = append(calls, format)
				if q.Get("sign") != lyricsSignature("1", q.Get("timeStamp")) {
					t.Error("bad signature")
				}
				status := tc.lrcStatus
				if format == "TEXT" {
					status = tc.textStatus
				}
				return response(status, `{"result":{"downloadUrl":"https://storage.mds.yandex.net/`+format+`"}}`), nil
			})
			lines, err := c.lyrics(context.Background(), 1)
			if err != nil || len(lines) == 0 || lines[0].Text != tc.want || lines[0].TS != nil {
				t.Fatal(lines, err)
			}
			if !reflect.DeepEqual(calls, tc.calls) {
				t.Fatal(calls)
			}
			result := NewDetector().Process(Track{ID: 1}, lines, false)
			if result.Status != "found" || len(result.AllLines) != len(lines) {
				t.Fatal(result)
			}
		})
	}
}

func TestLRCParsingContract(t *testing.T) {
	for _, tc := range []struct {
		name, input   string
		stamps, texts []string
	}{
		{"repeated timestamps and positive offset", "\ufeff[ti:Title]\r\n[ar:Artist]\r\n[offset:+100]\r\n[01:02.34][00:03.5]weed\r\n[00:04]ordinary", []string{"00:03.600", "00:04.100", "01:02.440"}, []string{"weed", "ordinary", "weed"}},
		{"negative offset clamps at zero", "[offset:-1500]\n[00:01.25]first\n[00:02.001]second", []string{"00:00.000", "00:00.501"}, []string{"first", "second"}},
		{"fraction precision and colon separator", "[00:01.1]one\n[00:02.12]two\n[00:03.123]three\n[00:04:45]four\n[00:05]five", []string{"00:01.100", "00:02.120", "00:03.123", "00:04.450", "00:05.000"}, []string{"one", "two", "three", "four", "five"}},
		{"metadata excluded", "[ar:Artist]\n[al:Album]\n[ti:Title]\n[au:Author]\n[by:Editor]\n[re:Tool]\n[ve:1]\n[length:03:00]\n[offset:0]", []string{}, []string{}},
		{"stable equal timestamps and empty cue", "[00:02]later\n[00:01]first\n[00:01]second\n[00:03]", []string{"00:01.000", "00:01.000", "00:02.000", "00:03.000"}, []string{"first", "second", "later", ""}},
		{"untimed content retained", "[ti:Title]\n[Chorus]\nweed", []string{"", ""}, []string{"[Chorus]", "weed"}},
		{"excessive offset ignored", "[offset:86400001]\n[00:01]weed", []string{"00:01.000"}, []string{"weed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseLRC(tc.input)
			stamps, texts := []string{}, []string{}
			for _, line := range got {
				stamp := ""
				if line.TS != nil {
					stamp = *line.TS
				}
				stamps = append(stamps, stamp)
				texts = append(texts, line.Text)
			}
			if !reflect.DeepEqual(stamps, tc.stamps) || !reflect.DeepEqual(texts, tc.texts) {
				t.Fatalf("got timestamps %q, text %q; want %q, %q", stamps, texts, tc.stamps, tc.texts)
			}
		})
	}
}

func TestLyricsTextDoesNotInterpretLRC(t *testing.T) {
	lines := parseLyricsText("\ufeff[00:01.00]literal\r\n[Chorus]\n\nweed")
	if len(lines) != 3 || lines[0].TS != nil || lines[0].Text != "[00:01.00]literal" || lines[1].Text != "[Chorus]" {
		t.Fatal(lines)
	}
}
