package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T, up http.HandlerFunc) *Server {
	t.Helper()
	remote := httptest.NewServer(up)
	t.Cleanup(remote.Close)
	c, err := NewClient(remote.URL, "public-anon")
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewServer(c, "bridge-key", "http://127.0.0.1:32100", filepath.Join(t.TempDir(), "private", "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.OpenBrowser = func(string) error { return nil }
	return s
}
func api(s *Server, method, path, body, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, s.base+path, strings.NewReader(body))
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func install(s *Server) {
	s.session = &Session{Access: "secret-access", Refresh: "secret-refresh", ExpiresAt: time.Now().Add(time.Hour).Unix(), User: User{ID: "user-1"}}
	s.state.Authenticated = true
	s.state.User = &s.session.User
}
func TestBridgeGuard(t *testing.T) {
	s := testServer(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected upstream call") })
	for _, tc := range []struct {
		method, path, key string
		status            int
	}{{"GET", "/api/state", "", 401}, {"POST", "/api/state", "bridge-key", 405}, {"GET", "/api/state", "bridge-key", 200}, {"POST", "/api/login", "bridge-key", 400}, {"POST", "/api/load", "bridge-key", 401}, {"POST", "/missing", "bridge-key", 404}} {
		if w := api(s, tc.method, tc.path, "{}", tc.key); w.Code != tc.status {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body)
		}
	}
	r := httptest.NewRequest("GET", s.base+"/api/state", nil)
	r.Header.Set("Authorization", "Bearer bridge-key")
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	r.Header.Del("Origin")
	r.Host = "evil.example"
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	install(s)
	w = api(s, "GET", "/api/state", "", "bridge-key")
	if strings.Contains(w.Body.String(), "secret-") {
		t.Fatal("tokens leaked")
	}
}
func TestPKCERefreshLogout(t *testing.T) {
	var verifier string
	refreshes := 0
	s := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/v1/token":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if r.URL.Query().Get("grant_type") == "pkce" {
				verifier = body["code_verifier"]
				if body["auth_code"] != "auth-code" {
					t.Error("code")
				}
			} else {
				refreshes++
				if body["refresh_token"] != "secret-refresh" {
					t.Error("refresh")
				}
			}
			fmt.Fprint(w, `{"access_token":"secret-access","refresh_token":"secret-refresh","expires_in":3600,"user":{"id":"user-1"}}`)
		case "/auth/v1/user":
			fmt.Fprint(w, `{"id":"user-1"}`)
		case "/rest/v1/bans":
			fmt.Fprint(w, `[]`)
		case "/rest/v1/ddetector_users":
			fmt.Fprint(w, `[{"user_id":"user-1"}]`)
		case "/auth/v1/logout":
			w.WriteHeader(204)
		default:
			t.Error(r.URL.Path)
			w.WriteHeader(404)
		}
	})
	for _, provider := range []string{"github", "discord", "spotify"} {
		w := api(s, "POST", "/api/login", `{"provider":"`+provider+`"}`, "bridge-key")
		if w.Code != 200 {
			t.Fatal(w.Body)
		}
		var response map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &response)
		u, _ := url.Parse(response["authorization_url"].(string))
		if u.Query().Get("code_challenge_method") != "s256" || u.Query().Get("provider") != provider || u.Query().Get("redirect_to") != s.base+s.login.Path {
			t.Fatal(u)
		}
	}
	callback := s.login.Path
	w := api(s, "GET", callback+"?code=auth-code", "", "")
	if w.Code != 200 || verifier == "" {
		t.Fatalf("callback %d %s", w.Code, w.Body)
	}
	if w = api(s, "GET", callback+"?code=auth-code", "", ""); w.Code != 400 {
		t.Fatal("callback replay")
	}
	saved, err := ReadSession(s.path)
	if err != nil || saved.User.ID != "user-1" {
		t.Fatal(err)
	}
	info, _ := os.Stat(s.path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("permissions")
	}
	s.session.ExpiresAt = 0
	if _, err = s.authorize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if refreshes != 1 {
		t.Fatal(refreshes)
	}
	w = api(s, "POST", "/api/logout", "", "bridge-key")
	if w.Code != 200 || s.state.Authenticated || s.session != nil {
		t.Fatal(w.Body)
	}
	if _, err = os.Stat(s.path); !os.IsNotExist(err) {
		t.Fatal("session remains")
	}
}
func TestLogoutWaitsForOperation(t *testing.T) {
	s := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("local logout must not contact the auth server")
	})
	install(s)
	s.op.Lock()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- api(s, "POST", "/api/logout", "", "bridge-key") }()
	select {
	case <-done:
		s.op.Unlock()
		t.Fatal("logout did not wait for the active operation")
	case <-time.After(30 * time.Millisecond):
	}
	s.op.Unlock()
	select {
	case w := <-done:
		if w.Code != 200 || s.session != nil || s.state.Authenticated {
			t.Fatal(w.Body)
		}
		if _, err := os.Stat(s.path); !os.IsNotExist(err) {
			t.Fatal("session remains")
		}
	case <-time.After(time.Second):
		t.Fatal("logout did not finish")
	}
}

func TestBanFailsClosed(t *testing.T) {
	s := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/v1/user" {
			fmt.Fprint(w, `{"id":"user-1"}`)
		} else if r.URL.Path == "/rest/v1/bans" {
			fmt.Fprint(w, `[{"user_id":"user-1"}]`)
		} else {
			t.Error("banned user accessed data")
		}
	})
	install(s)
	s.state.Tracks = []Result{{Track: Track{ID: 1}}}
	if err := s.load(context.Background(), false); err == nil {
		t.Fatal("ban bypass")
	}
	if !s.state.IsBanned || s.state.HasAccess || len(s.state.Tracks) > 0 {
		t.Fatal("not cleared")
	}
}
func TestPrivateSession(t *testing.T) {
	p := filepath.Join(t.TempDir(), "private", "session.json")
	session := &Session{Access: "a", Refresh: "r", ExpiresAt: time.Now().Unix() + 100, User: User{ID: "u"}}
	if err := SaveSession(p, session); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSession(p); err == nil {
		t.Fatal("accepted public session")
	}
	if err := SaveSession(p, session); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(p), "link")
	if err := os.Symlink(p, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSession(link); err == nil {
		t.Fatal("accepted symlink")
	}
}

func TestLoginFailureAndExpiry(t *testing.T) {
	s := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":"do not echo secret-refresh"}`)
	})
	s.OpenBrowser = func(string) error { return fmt.Errorf("no browser") }
	w := api(s, "POST", "/api/login", `{"provider":"github"}`, "bridge-key")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"browser_opened":false`) {
		t.Fatal(w.Body)
	}
	callback := s.login.Path
	w = api(s, "GET", callback+"?code=bad", "", "")
	if w.Code != 502 || s.session != nil || strings.Contains(s.state.Error, "secret-refresh") {
		t.Fatal("unsafe failed exchange")
	}
	_ = api(s, "POST", "/api/login", `{"provider":"discord"}`, "bridge-key")
	s.login.Expires = time.Now().Add(-time.Minute)
	_ = api(s, "GET", "/api/state", "", "bridge-key")
	if s.login != nil || s.state.LoginPending {
		t.Fatal("expired login remains")
	}
	s.op.Lock()
	w = api(s, "POST", "/api/load", "", "bridge-key")
	s.op.Unlock()
	if w.Code != 409 {
		t.Fatal("overlapping work allowed")
	}
}
func TestUpstreamFailureClearsAccess(t *testing.T) {
	s := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, `{"message":"secret-access"}`)
	})
	install(s)
	s.state.HasAccess = true
	s.state.Tracks = []Result{{Track: Track{ID: 1}}}
	_, err := s.authorize(context.Background())
	if err == nil || s.state.HasAccess || len(s.state.Tracks) != 0 || strings.Contains(err.Error(), "secret-access") {
		t.Fatal("failed auth did not fail closed")
	}
	if _, err := rows[Track](context.Background(), s.client, "ddetector_tracks", "id", "id.asc", "a"); err == nil {
		t.Fatal("upstream failure swallowed")
	}
}
func TestMaintenance(t *testing.T) {
	s := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/v1/user":
			fmt.Fprint(w, `{"id":"user-1"}`)
		case "/rest/v1/ddetector_users":
			fmt.Fprint(w, `[{"user_id":"user-1"}]`)
		default:
			fmt.Fprint(w, `[]`)
		}
	})
	install(s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		loaded := s.state.AccessChecked && !s.state.Loading
		s.mu.Unlock()
		if loaded {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("restored session was not loaded")
}
