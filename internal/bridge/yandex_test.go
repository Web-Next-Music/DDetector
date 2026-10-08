package bridge

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func TestSignedLyrics(t *testing.T) {
	if got := lyricsSignature("123", "1700000000"); got != "0Vud0sSibSPxZ9g889XHJBtHLa1dj/Cn39Hi8FCKB3g=" {
		t.Fatal(got)
	}
	c := newYandexClient("secret-token")
	downloads := 0
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "api.music.yandex.net" {
			q := r.URL.Query()
			if r.URL.Path != "/tracks/123/lyrics" || q.Get("format") != "LRC" || q.Get("sign") != lyricsSignature("123", q.Get("timeStamp")) || r.Header.Get("Authorization") != "OAuth secret-token" {
				t.Error("bad signed API request")
			}
			return response(200, `{"result":{"downloadUrl":"https://storage.mds.yandex.net/lyrics?signature=private"}}`), nil
		}
		downloads++
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("credential sent to download")
		}
		return response(200, "[ti:Title]\n[offset:100]\n[01:02.34][00:03.5]weed\n[00:04]ordinary"), nil
	})
	lines, err := c.lyrics(context.Background(), 123)
	if err != nil || downloads != 1 || len(lines) != 3 {
		t.Fatal(lines, err)
	}
	if *lines[0].TS != "00:03.600" || *lines[1].TS != "00:04.100" || *lines[2].TS != "01:02.440" {
		t.Fatal(lines)
	}
	result := NewDetector().Process(Track{ID: 123}, lines, false)
	if result.Status != "found" || len(result.Lines) != 2 || *result.Lines[0].TS != "00:03.600" {
		t.Fatal(result)
	}
}
func TestLyricsDownloadSafety(t *testing.T) {
	for _, raw := range []string{"http://storage.mds.yandex.net/x", "https://storage.mds.yandex.net.evil.test/x", "https://evil.test/x", "https://user:pass@storage.mds.yandex.net/x", "https://127.0.0.1/x", "https://storage.mds.yandex.net:444/x"} {
		if trustedLyricsURL(raw) {
			t.Fatal("trusted", raw)
		}
	}
	c := newYandexClient("secret-token")
	calls := 0
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		res := response(302, "")
		res.Header.Set("Location", "https://evil.test/secret")
		return res, nil
	})
	if _, err := c.downloadLyrics(context.Background(), "https://storage.mds.yandex.net/x"); err == nil || calls != 1 {
		t.Fatal("redirect followed", err, calls)
	}
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return response(200, strings.Repeat("x", (2<<20)+1)), nil
	})
	if _, err := c.downloadLyrics(context.Background(), "https://storage.mds.yandex.net/x"); err == nil {
		t.Fatal("unbounded body")
	}
}
func TestLyricsFailureNotAbsence(t *testing.T) {
	c := newYandexClient("secret")
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/lyrics") {
			return response(200, `{"result":{"downloadUrl":"https://evil.test/private-signature"}}`), nil
		}
		return response(200, `{"result":{}}`), nil
	})
	lines, err := c.lyrics(context.Background(), 1)
	if err == nil || lines != nil || strings.Contains(err.Error(), "private-signature") {
		t.Fatal("download failure became cached absence or leaked URL", lines, err)
	}
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/lyrics") {
			return response(404, ""), nil
		}
		return response(200, `{"result":{"lyrics":{"fullLyrics":"fallback weed"}}}`), nil
	})
	lines, err = c.lyrics(context.Background(), 1)
	if err != nil || len(lines) != 1 || lines[0].TS != nil || lines[0].Text != "fallback weed" {
		t.Fatal("supplement fallback failed", lines, err)
	}
}

func TestMissingLyricsCached(t *testing.T) {
	s := authorizedTestServer(t)
	install(s)
	c := newYandexClient("secret")
	c.uid = "123"
	calls := 0
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {

		case "/tracks":
			return response(200, `{"result":[{"id":1,"title":"Song"}]}`), nil
		case "/tracks/1/lyrics":
			calls++
			return response(404, ""), nil
		case "/tracks/1/supplement":
			calls++
			return response(200, `{"result":{}}`), nil
		}
		t.Error(r.URL.Path)
		return response(500, ""), nil
	})
	s.yandex = c
	if err := s.load(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if s.state.Tracks[0].Status != "none" || s.state.Tracks[0].Lyrics == nil {
		t.Fatal(s.state.Tracks)
	}
	restarted, err := NewServer(s.client, s.key, s.base, s.path)
	if err != nil {
		t.Fatal(err)
	}
	install(restarted)
	restarted.yandex = c
	if err := restarted.load(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatal("absence refetched", calls)
	}
}
func authorizedTestServer(t *testing.T) *Server {
	return testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/v1/user":
			fmt.Fprint(w, `{"id":"user-1"}`)
		case "/rest/v1/bans", "/rest/v1/ddetector_ignored_tracks":
			fmt.Fprint(w, `[]`)
		case "/rest/v1/ddetector_users":
			fmt.Fprint(w, `[{"user_id":"user-1"}]`)
		case "/rest/v1/ddetector_tracks":
			if r.Header.Get("Range") == "0-999" {
				fmt.Fprint(w, `[{"id":1}]`)
			} else {
				fmt.Fprint(w, `[]`)
			}
		default:
			t.Error(r.URL.Path)
			w.WriteHeader(404)
		}
	})
}
func TestTokenCancelsLoad(t *testing.T) {
	for _, replacement := range []string{"", "replacement"} {
		t.Run("token-"+replacement, func(t *testing.T) {
			s := authorizedTestServer(t)
			install(s)
			started := make(chan struct{})
			canceled := make(chan struct{})
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				close(started)
				<-r.Context().Done()
				close(canceled)
			}))
			defer remote.Close()
			c := newYandexClient("old-secret")
			c.uid = "123"
			c.base = remote.URL
			s.yandex = c
			s.state.YandexConnected = true
			s.yandexFactory = func(token string) *yandexClient {
				next := newYandexClient(token)
				next.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
					return response(200, `{"result":{"account":{"uid":456}}}`), nil
				})
				return next
			}
			if w := api(s, "POST", "/api/load", "", "bridge-key"); w.Code != 202 {
				t.Fatal(w.Body)
			}
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("load did not start")
			}
			w := api(s, "POST", "/api/yandex/token", fmt.Sprintf(`{"access_token":%q}`, replacement), "bridge-key")
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body)
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("request not canceled")
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			if len(s.state.Tracks) != 0 || s.state.Loading || s.state.Error != "" || s.state.YandexConnected != (replacement != "") {
				t.Fatal(s.state)
			}
			if replacement == "" && s.yandex != nil {
				t.Fatal("token retained")
			}
			if replacement != "" && s.yandex.uid != "456" {
				t.Fatal("not replaced")
			}
		})
	}
}
func TestEmptyTokenRevokesWithoutSession(t *testing.T) {
	s := testServer(t, func(w http.ResponseWriter, r *http.Request) { t.Error("upstream revoke call") })
	s.yandex = newYandexClient("secret")
	s.state.YandexConnected = true
	s.state.Tracks = []Result{{Track: Track{ID: 1}}}
	if w := api(s, "POST", "/api/yandex/token", `{"access_token":""}`, "bridge-key"); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if s.yandex != nil || s.state.YandexConnected || len(s.state.Tracks) != 0 {
		t.Fatal("not revoked")
	}
	if w := api(s, "POST", "/api/yandex/token", `{}`, "bridge-key"); w.Code != 400 {
		t.Fatal("missing token accepted")
	}
}
