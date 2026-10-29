package bridge

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	spotifyEmbeddedClientID = "880c84559c2f4ca2b17ad7ac43f2922a"
	spotifyCallbackAddress  = "127.0.0.1:43821"
	spotifyCallbackPath     = "/api/spotify/callback"
	spotifyCallbackURI      = "http://" + spotifyCallbackAddress + spotifyCallbackPath
)

type spotifyCredentials struct {
	ClientID string    `json:"client_id"`
	Access   string    `json:"access_token"`
	Refresh  string    `json:"refresh_token"`
	Expires  time.Time `json:"expires_at"`
}
type spotifyService struct {
	mu              sync.Mutex
	credentials     spotifyCredentials
	state, verifier string
	deadline        time.Time
	error           string
	http            *http.Client
	coverCache      map[string]coverOutcome
	callback        net.Listener
	callbackCancel  context.CancelFunc
}

func (s *Server) spotifyService() *spotifyService {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.spotify == nil {
		p := &spotifyService{http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
		b, err := readPrivateFile(s.path+".spotify.json", maxSessionBytes, true)
		if err == nil {
			if json.Unmarshal(b, &p.credentials) != nil {
				p.error = "Invalid saved Spotify credentials"
				p.credentials = spotifyCredentials{}
			}
		} else if !os.IsNotExist(err) {
			p.error = "Unable to read private Spotify credentials"
		}
		if p.credentials.ClientID != spotifyEmbeddedClientID {
			p.credentials = spotifyCredentials{}
		}
		s.spotify = p
	}
	return s.spotify
}
func (s *Server) spotifyRedirect() string {
	return spotifyCallbackURI
}

// Called with p.mu held. Closing the listener leaves an accepted callback free
// to finish writing its response.
func (p *spotifyService) stopCallback() {
	if p.callback != nil {
		_ = p.callback.Close()
		p.callbackCancel()
		p.callback, p.callbackCancel = nil, nil
	}
	p.state, p.verifier, p.deadline = "", "", time.Time{}
}

func (s *Server) startSpotifyCallback(p *spotifyService) error {
	if s.lifecycle.Err() != nil {
		return errors.New("Spotify connection canceled: server is shutting down")
	}
	if p.callback != nil {
		return nil
	}
	listener, err := net.Listen("tcp4", spotifyCallbackAddress)
	if err != nil {
		return fmt.Errorf("Unable to open Spotify callback at %s: close the application using port 43821 and retry (%v)", spotifyCallbackURI, err)
	}
	ctx, cancel := context.WithTimeout(s.lifecycle, 10*time.Minute)
	p.callback, p.callbackCancel = listener, cancel
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: time.Second, MaxHeaderBytes: 16 << 10, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Host != spotifyCallbackAddress || r.URL.Path != spotifyCallbackPath || r.URL.EscapedPath() != spotifyCallbackPath {
			failure(w, 403, "Invalid Spotify callback host or path")
			return
		}
		s.spotifyCallback(w, r)
	})}
	go func() { _ = server.Serve(listener) }()
	go func() {
		<-ctx.Done()
		_ = listener.Close()
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.callback == listener {
			p.stopCallback()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				p.error = "Spotify authorization timed out; connect again"
			}
		}
	}()
	return nil
}

func (s *Server) handleSpotify(w http.ResponseWriter, r *http.Request) {
	p := s.spotifyService()
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.URL.Path == "/api/spotify/state" && r.Method == "GET" {
		send(w, 200, map[string]any{"configured": true, "connected": p.credentials.ClientID == spotifyEmbeddedClientID && p.credentials.Refresh != "", "client_id": spotifyEmbeddedClientID, "redirect_uri": s.spotifyRedirect(), "error": p.error})
		return
	}
	if r.Method != "POST" {
		failure(w, 405, "method not allowed")
		return
	}
	if !s.tryOperation() {
		failure(w, 409, "operation in progress")
		return
	}
	defer s.op.Unlock()
	switch r.URL.Path {
	case "/api/spotify/connect":
		var in struct {
			ClientID string `json:"client_id"`
		}
		err := decode(w, r, &in)
		if (err != nil && err != io.EOF) || (in.ClientID != "" && in.ClientID != spotifyEmbeddedClientID) {
			failure(w, 400, "client_id must be omitted or match the embedded Spotify client ID")
			return
		}
		p.stopCallback()
		if err := s.startSpotifyCallback(p); err != nil {
			p.error = err.Error()
			failure(w, 409, p.error)
			return
		}
		// Keep the saved session until a new authorization succeeds.
		p.credentials.ClientID = spotifyEmbeddedClientID
		p.state, p.verifier, p.deadline, p.error = RandomKey(), RandomKey(), time.Now().Add(10*time.Minute), ""
		hash := sha256.Sum256([]byte(p.verifier))
		q := url.Values{"client_id": {spotifyEmbeddedClientID}, "response_type": {"code"}, "redirect_uri": {s.spotifyRedirect()}, "state": {p.state}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}}
		send(w, 200, map[string]string{"authorization_url": "https://accounts.spotify.com/authorize?" + q.Encode()})
	case "/api/spotify/disconnect":
		p.stopCallback()
		if err := s.saveSpotify(spotifyCredentials{}); err != nil {
			failure(w, 500, "Unable to clear private Spotify credentials")
			return
		}
		p.credentials, p.state, p.verifier, p.error = spotifyCredentials{}, "", "", ""
		send(w, 200, map[string]bool{"ok": true})
	default:
		failure(w, 404, "not found")
	}
}
func (s *Server) saveSpotify(c spotifyCredentials) error {
	return writePrivateFile(s.path+".spotify.json", ".spotify-*", maxSessionBytes, func(w io.Writer) error { return json.NewEncoder(w).Encode(c) })
}
func (s *Server) spotifyCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		failure(w, 405, "method not allowed")
		return
	}
	p := s.spotifyService()
	p.mu.Lock()
	defer p.mu.Unlock()
	state := r.URL.Query().Get("state")
	if p.state == "" || time.Now().After(p.deadline) || subtle.ConstantTimeCompare([]byte(state), []byte(p.state)) != 1 {
		failure(w, 400, "Invalid or expired Spotify state")
		return
	}
	if !s.tryOperation() {
		failure(w, 409, "operation in progress")
		return
	}
	defer s.op.Unlock()
	// Keep authorization retryable on contention; consume it only once the
	// operation is owned, while the pending state is still mutex-protected.
	verifier := p.verifier
	p.stopCallback()
	ctx, cancel := s.operationContext(r.Context(), 20*time.Second)
	defer cancel()
	code := r.URL.Query().Get("code")
	if code == "" || len(code) > 4096 || r.URL.Query().Get("error") != "" {
		p.error = "Spotify authorization was not completed"
		failure(w, 400, p.error)
		return
	}
	err := p.token(ctx, s, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {s.spotifyRedirect()}, "code_verifier": {verifier}})
	if err != nil {
		p.error = err.Error()
		failure(w, 502, p.error)
		return
	}
	p.error = ""
	send(w, 200, map[string]string{"message": "Spotify connected. You may close this window."})
}
func (p *spotifyService) request(ctx context.Context, endpoint string, form url.Values, bearer string, out any) error {
	// Endpoints are fixed by callers; redirects never carry credentials elsewhere.
	if endpoint != "https://accounts.spotify.com/api/token" && !strings.HasPrefix(endpoint, "https://api.spotify.com/v1/search?") {
		return errors.New("Invalid Spotify endpoint")
	}
	method := "GET"
	var body io.Reader
	if form != nil {
		method = "POST"
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return errors.New("Invalid Spotify request")
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := p.http.Do(req)
	if err != nil {
		return errors.New("Spotify network request failed")
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if err != nil || len(b) > 2<<20 {
		return errors.New("Invalid Spotify response size")
	}
	if res.StatusCode != 200 {
		return fmt.Errorf("Spotify request failed (HTTP %d)", res.StatusCode)
	}
	if json.Unmarshal(b, out) != nil {
		return errors.New("Invalid Spotify response")
	}
	return nil
}
func (p *spotifyService) token(ctx context.Context, s *Server, form url.Values) error {
	if p.credentials.ClientID != spotifyEmbeddedClientID {
		return errors.New("Saved Spotify credentials belong to another client; reconnect Spotify")
	}
	form.Set("client_id", spotifyEmbeddedClientID)
	var v struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Expires int    `json:"expires_in"`
		Type    string `json:"token_type"`
	}
	if err := p.request(ctx, "https://accounts.spotify.com/api/token", form, "", &v); err != nil {
		return err
	}
	if v.Access == "" || len(v.Access) > 8192 || strings.ContainsAny(v.Access, "\r\n") || v.Expires <= 0 || v.Expires > 86400 || !strings.EqualFold(v.Type, "Bearer") {
		return errors.New("Invalid Spotify token response")
	}
	next := p.credentials
	next.Access = v.Access
	if v.Refresh != "" {
		next.Refresh = v.Refresh
	}
	if next.Refresh == "" {
		return errors.New("Missing Spotify refresh token")
	}
	next.Expires = time.Now().Add(time.Duration(v.Expires) * time.Second)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if s.saveSpotify(next) != nil {
		return errors.New("Unable to save private Spotify credentials")
	}
	p.credentials = next
	return nil
}
