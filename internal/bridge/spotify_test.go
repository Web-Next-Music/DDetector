package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type spotifyTransport func(*http.Request) (*http.Response, error)

func (f spotifyTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func spotifyTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := NewServer(nil, "test-key", "http://127.0.0.1:43123", filepath.Join(t.TempDir(), "private", "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.lifecycleCancel)
	return s
}
func spotifyCall(s *Server, method, path, body string, auth bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, s.base+path, strings.NewReader(body))
	if auth {
		r.Header.Set("Authorization", "Bearer "+s.key)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func TestSpotifyOAuthPKCE(t *testing.T) {
	s := spotifyTestServer(t)
	if w := spotifyCall(s, "GET", "/api/spotify/state", "", false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := spotifyCall(s, "POST", "/api/spotify/connect", `{"client_id":"880c84559c2f4ca2b17ad7ac43f2922a"}`, true)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var response struct {
		URL string `json:"authorization_url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(response.URL)
	q := u.Query()
	p := s.spotifyService()
	sum := sha256.Sum256([]byte(p.verifier))
	if q.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(sum[:]) || q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") != spotifyCallbackURI || q.Get("client_id") != spotifyEmbeddedClientID {
		t.Fatal(q)
	}
	verifier := p.verifier
	calls := 0
	p.http.Transport = spotifyTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://accounts.spotify.com/api/token" || r.Header.Get("Authorization") != "" {
			t.Fatal("unexpected credential destination")
		}
		b, _ := io.ReadAll(r.Body)
		v, _ := url.ParseQuery(string(b))
		if v.Get("client_id") != spotifyEmbeddedClientID || v.Get("redirect_uri") != spotifyCallbackURI {
			t.Fatal("token exchange client or redirect mismatch", v)
		}
		if v.Get("code_verifier") != verifier {
			t.Fatal("verifier")
		}
		if v.Get("code_verifier") == "" {
			t.Fatal("missing verifier")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"secret-access","refresh_token":"secret-refresh","expires_in":3600,"token_type":"Bearer"}`)), Header: make(http.Header)}, nil
	})
	if w := spotifyCall(s, "GET", "/api/spotify/callback?state=wrong&code=x", "", false); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if calls != 0 {
		t.Fatal("invalid state exchanged")
	}
	callback := "/api/spotify/callback?state=" + url.QueryEscape(q.Get("state")) + "&code=valid"
	deadline := p.deadline
	s.op.Lock()
	blocked := spotifyCall(s, "GET", callback, "", false)
	s.op.Unlock()
	if blocked.Code != http.StatusConflict {
		t.Fatalf("contended callback: %d %s", blocked.Code, blocked.Body.String())
	}
	if p.state != q.Get("state") || p.verifier != verifier || !p.deadline.Equal(deadline) {
		t.Fatal("operation contention discarded pending authorization")
	}
	if calls != 0 {
		t.Fatal("contended callback exchanged authorization code")
	}
	if w := spotifyCall(s, "GET", callback, "", false); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := spotifyCall(s, "GET", callback, "", false); w.Code != 400 {
		t.Fatal("replay accepted")
	}
	w = spotifyCall(s, "GET", "/api/spotify/state", "", true)
	if strings.Contains(w.Body.String(), "secret-") || !strings.Contains(w.Body.String(), `"connected":true`) {
		t.Fatal(w.Body.String())
	}
	b, err := readPrivateFile(s.path+".spotify.json", maxSessionBytes, true)
	if err != nil || !strings.Contains(string(b), "secret-refresh") {
		t.Fatal(err)
	}
	loaded, err := NewServer(nil, "restart-key", s.base, s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.lifecycleCancel()
	if w := spotifyCall(loaded, "GET", "/api/spotify/state", "", true); w.Code != 200 || !strings.Contains(w.Body.String(), `"connected":true`) {
		t.Fatal("saved authorization not restored after restart", w.Code)
	}
	if loaded.spotifyService().credentials.Refresh != "secret-refresh" {
		t.Fatal("saved refresh token not restored")
	}
	if w := spotifyCall(loaded, "POST", "/api/spotify/connect", "", true); w.Code != 200 {
		t.Fatal("reconnect failed", w.Code)
	}
	loaded.spotifyService().mu.Lock()
	loaded.spotifyService().stopCallback()
	loaded.spotifyService().mu.Unlock()
	b, err = readPrivateFile(s.path+".spotify.json", maxSessionBytes, true)
	if err != nil || !strings.Contains(string(b), "secret-refresh") {
		t.Fatal("unfinished reconnect discarded saved authorization", err)
	}
	if loaded.spotifyService().credentials.Refresh != "secret-refresh" {
		t.Fatal("unfinished reconnect discarded active authorization")
	}
	if w := spotifyCall(s, "POST", "/api/spotify/disconnect", "", true); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	b, err = readPrivateFile(s.path+".spotify.json", maxSessionBytes, true)
	if err != nil || strings.Contains(string(b), "secret-") {
		t.Fatal("disconnect retained tokens", err)
	}
}
func TestSpotifySavedAuthorizationSurvivesRestart(t *testing.T) {
	s := spotifyTestServer(t)
	p := s.spotifyService()
	p.credentials.ClientID = spotifyEmbeddedClientID
	p.http.Transport = spotifyTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"saved-access","refresh_token":"saved-refresh","expires_in":3600,"token_type":"Bearer"}`)), Header: make(http.Header)}, nil
	})
	if err := p.token(context.Background(), s, url.Values{"grant_type": {"authorization_code"}, "code": {"test-code"}}); err != nil {
		t.Fatal(err)
	}
	s.lifecycleCancel()
	loaded, err := NewServer(nil, "restart-key", s.base, s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.lifecycleCancel()
	if w := spotifyCall(loaded, "GET", "/api/spotify/state", "", true); w.Code != 200 || !strings.Contains(w.Body.String(), `"connected":true`) {
		t.Fatal("saved authorization not restored", w.Code)
	}
	if loaded.spotifyService().credentials.Refresh != "saved-refresh" {
		t.Fatal("refresh token not restored")
	}
}

func TestSpotifyRouteGuards(t *testing.T) {
	s := spotifyTestServer(t)
	for _, tc := range []struct {
		name, method, path, host, origin string
		auth                             bool
		status                           int
	}{
		{"callback host checked first", "GET", "/api/spotify/callback", "evil.example", "", false, 403},
		{"callback requires state without bearer", "GET", "/api/spotify/callback", "", "", false, 400},
		{"callback rejects POST", "POST", "/api/spotify/callback", "", "", false, 405},
		{"callback exemption is exact", "GET", "/api/spotify/callback/", "", "", false, 401},
		{"state requires bearer", "GET", "/api/spotify/state", "", "", false, 401},
		{"connect requires bearer", "POST", "/api/spotify/connect", "", "", false, 401},
		{"disconnect requires bearer", "POST", "/api/spotify/disconnect", "", "", false, 401},
		{"protected route rejects browser origin", "GET", "/api/spotify/state", "", "https://evil.example", true, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, s.base+tc.path, nil)
			if tc.host != "" {
				r.Host = tc.host
			}
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.auth {
				r.Header.Set("Authorization", "Bearer "+s.key)
			}
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
		})
	}
}

func TestSpotifyConservativeMatchAndCover(t *testing.T) {
	var c spotifyTrack
	_ = json.Unmarshal([]byte(`{"name":" Song (Live) ","artists":[{"name":"Alice"},{"name":"Bob"}],"album":{"images":[{"url":"https://evil.example/image/x"},{"url":"https://i.scdn.co/image/abc"}]}}`), &c)
	track := Track{Title: "song (live)", Artist: "Bob, Alice"}
	if spotifyMatch(track, []spotifyTrack{c}) == nil {
		t.Fatal("exact normalized match rejected")
	}
	if spotifyMatch(track, []spotifyTrack{c, c}) != nil {
		t.Fatal("ambiguity accepted")
	}
	track.Title = "Song"
	if spotifyMatch(track, []spotifyTrack{c}) != nil {
		t.Fatal("version mismatch accepted")
	}
	if cover := spotifyCover(&c); cover == nil || *cover != "https://i.scdn.co/image/abc" {
		t.Fatal(cover)
	}
	for _, raw := range []string{"http://i.scdn.co/image/x", "https://i.scdn.co.evil/image/x", "https://i.scdn.co:443/image/x", "https://user@i.scdn.co/image/x", "https://i.scdn.co/image/x?q=y"} {
		c.Album.Images[0].URL = raw
		c.Album.Images = c.Album.Images[:1]
		if spotifyCover(&c) != nil {
			t.Fatal(raw)
		}
	}
}
func TestSpotifyFallbackRefreshAndPreserve(t *testing.T) {
	s := spotifyTestServer(t)
	p := s.spotifyService()
	p.credentials = spotifyCredentials{ClientID: spotifyEmbeddedClientID, Refresh: "refresh", Expires: time.Now().Add(-time.Hour)}
	calls := 0
	p.http.Transport = spotifyTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		body := `{"access_token":"new-access","expires_in":3600,"token_type":"Bearer"}`
		if r.URL.Host == "api.spotify.com" {
			if r.Header.Get("Authorization") != "Bearer new-access" {
				t.Fatal("no refreshed token")
			}
			body = `{"tracks":{"items":[{"name":"Song","artists":[{"name":"Artist"}],"album":{"images":[{"url":"https://i.scdn.co/image/abc"}]}}]}}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	track := Track{ID: 123, Title: "Song", Artist: "Artist"}
	got := s.spotifyFallback(context.Background(), track)
	if got.Cover == nil || got.ID != track.ID || got.Title != track.Title || got.Artist != track.Artist || calls != 2 {
		t.Fatal(got, calls)
	}
	_ = s.spotifyFallback(context.Background(), got)
	if calls != 2 {
		t.Fatal("complete track queried")
	}
	if p.credentials.Refresh != "refresh" {
		t.Fatal("refresh token lost")
	}
}
