package bridge

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func waitSpotifyCallbackClosed(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		p := s.spotifyService()
		p.mu.Lock()
		closed := p.callback == nil && p.state == "" && p.verifier == ""
		p.mu.Unlock()
		if closed {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("callback listener or pending authorization retained")
}

func TestSpotifyDedicatedCallback(t *testing.T) {
	s := spotifyTestServer(t)
	w := spotifyCall(s, "GET", "/api/spotify/state", "", true)
	if !strings.Contains(w.Body.String(), `"configured":true`) || !strings.Contains(w.Body.String(), spotifyEmbeddedClientID) || !strings.Contains(w.Body.String(), spotifyCallbackURI) {
		t.Fatal(w.Body.String())
	}
	if w := spotifyCall(s, "POST", "/api/spotify/connect", `{}`, true); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	p := s.spotifyService()
	p.mu.Lock()
	state := p.state
	p.mu.Unlock()
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	for _, tc := range []struct {
		host, path string
		status     int
	}{
		{"evil.example", spotifyCallbackPath, 403},
		{spotifyCallbackAddress, spotifyCallbackPath + "/", 403},
		{spotifyCallbackAddress, spotifyCallbackPath + "?state=wrong", 400},
		{spotifyCallbackAddress, spotifyCallbackPath + "?state=" + state + "&error=access_denied", 400},
	} {
		req, _ := http.NewRequest("GET", "http://"+spotifyCallbackAddress+tc.path, nil)
		req.Host = tc.host
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != tc.status {
			t.Fatalf("%s %s: %d", tc.host, tc.path, res.StatusCode)
		}
	}
	waitSpotifyCallbackClosed(t, s)
	listener, err := net.Listen("tcp4", spotifyCallbackAddress)
	if err != nil {
		t.Fatal("callback port not released", err)
	}
	listener.Close()
}

func TestSpotifyCallbackPortConflict(t *testing.T) {
	listener, err := net.Listen("tcp4", spotifyCallbackAddress)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	s := spotifyTestServer(t)
	w := spotifyCall(s, "POST", "/api/spotify/connect", "", true)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "43821") || !strings.Contains(w.Body.String(), "close the application") {
		t.Fatal(w.Code, w.Body.String())
	}
	p := s.spotifyService()
	if p.state != "" || p.callback != nil {
		t.Fatal("failed bind created pending authorization")
	}
}

func TestSpotifyCallbackCleanup(t *testing.T) {
	for _, mode := range []string{"disconnect", "lifecycle", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			s := spotifyTestServer(t)
			if mode == "timeout" {
				ctx, cancel := context.WithTimeout(s.lifecycle, 100*time.Millisecond)
				defer cancel()
				s.lifecycle = ctx
			}
			if w := spotifyCall(s, "POST", "/api/spotify/connect", "", true); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			switch mode {
			case "disconnect":
				if w := spotifyCall(s, "POST", "/api/spotify/disconnect", "", true); w.Code != 200 {
					t.Fatal(w.Body.String())
				}
			case "lifecycle":
				s.lifecycleCancel()
			}
			waitSpotifyCallbackClosed(t, s)
			listener, err := net.Listen("tcp4", spotifyCallbackAddress)
			if err != nil {
				t.Fatal(err)
			}
			listener.Close()
		})
	}
}

func TestSpotifyClientIDSingleSource(t *testing.T) {
	s := spotifyTestServer(t)
	if w := spotifyCall(s, "POST", "/api/spotify/connect", `{"client_id":"0123456789abcdef0123456789abcdef"}`, true); w.Code != 400 {
		t.Fatal(w.Code)
	}
	p := s.spotifyService()
	p.credentials = spotifyCredentials{ClientID: "old-client", Refresh: "old-refresh"}
	if err := p.token(context.Background(), s, nil); err == nil {
		t.Fatal("foreign client refresh accepted")
	}
	if err := s.saveSpotify(p.credentials); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewServer(nil, "test-key", s.base, s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.lifecycleCancel()
	if loaded.spotifyService().credentials.Refresh != "" {
		t.Fatal("foreign saved credentials loaded")
	}
}
