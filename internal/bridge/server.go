package bridge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

type State struct {
	YandexConnected bool         `json:"yandexConnected"`
	Authenticated   bool         `json:"authenticated"`
	User            *User        `json:"user"`
	AccessChecked   bool         `json:"accessChecked"`
	HasAccess       bool         `json:"hasAccess"`
	IsBanned        bool         `json:"isBanned"`
	LoginPending    bool         `json:"loginPending"`
	Loading         bool         `json:"loading"`
	Fetching        bool         `json:"fetching"`
	Processing      bool         `json:"processing"`
	Processed       int          `json:"processed"`
	FoundCount      int          `json:"foundCount"`
	Tracks          []Result     `json:"tracks"`
	Error           string       `json:"error"`
	LastFetch       *FetchResult `json:"lastFetch,omitempty"`
}
type FetchResult struct {
	Total         int    `json:"total"`
	Added         int    `json:"added"`
	Removed       int    `json:"removed"`
	LyricsFetched int    `json:"lyrics_fetched"`
	Error         string `json:"error,omitempty"`
}
type pendingLogin struct {
	Verifier, Path string
	Expires        time.Time
}
type Server struct {
	mu              sync.Mutex
	op              sync.Mutex
	client          *Client
	yandex          *yandexClient
	jobCancel       context.CancelFunc
	jobDone         chan struct{}
	yandexFactory   func(string) *yandexClient
	key, base, path string
	session         *Session
	state           State
	login           *pendingLogin
	detector        *Detector
	OpenBrowser     func(string) error
	tracksRevision  uint64
	cachedRevision  uint64
	cachedState     State
	cachedTracks    []Result
	stateJSON       []byte
	stateETag       string
}

func RandomKey() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func NewServer(c *Client, key, base, path string) (*Server, error) {
	session, err := ReadSession(path)
	if err != nil {
		return nil, err
	}
	s := &Server{client: c, key: key, base: base, path: path, session: session, detector: NewDetector(), state: State{Tracks: []Result{}}}
	if session != nil {
		s.state.Authenticated = true
		u := session.User
		s.state.User = &u
	}
	s.OpenBrowser = func(u string) error {
		cmd := exec.Command("xdg-open", u)
		if err := cmd.Start(); err != nil {
			return err
		}
		go cmd.Wait()
		return nil
	}
	return s, nil
}
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.handle) }
func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, status int, msg string) {
	send(w, status, map[string]any{"ok": false, "error": msg})
}
func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Host != strings.TrimPrefix(s.base, "http://") {
		failure(w, 403, "invalid host")
		return
	}
	if strings.HasPrefix(r.URL.Path, "/oauth/callback/") {
		s.callback(w, r)
		return
	}
	if r.Header.Get("Origin") != "" {
		failure(w, 403, "browser origins are not allowed")
		return
	}
	expected := "Bearer " + s.key
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(expected)) != 1 {
		failure(w, 401, "bridge authentication required")
		return
	}
	if r.URL.Path == "/api/state" {
		if r.Method != "GET" {
			w.Header().Set("Allow", "GET")
			failure(w, 405, "method not allowed")
			return
		}
		s.mu.Lock()
		if s.login != nil && time.Now().After(s.login.Expires) {
			s.login = nil
			s.state.LoginPending = false
			s.state.Error = "Login expired"
		}
		s.sendState(w, r)
		s.mu.Unlock()
		return
	}
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		failure(w, 405, "method not allowed")
		return
	}
	switch r.URL.Path {
	case "/api/yandex/token":
		s.setYandexToken(w, r)
	case "/api/login":
		s.startLogin(w, r)
	case "/api/logout":
		s.logout(w, r)
	case "/api/load":
		s.startJob(w, false)
	case "/api/fetch":
		s.startJob(w, true)
	case "/api/ignore":
		s.ignore(w, r)
	default:
		failure(w, 404, "not found")
	}
}

// sendState is called with mu held. User values are immutable session snapshots;
// LastFetch is copied so even direct scalar writes invalidate the encoding.
// Track replacements (including publishResults) change the backing slice; all
// in-place track edits must increment tracksRevision under mu.
func (s *Server) sendState(w http.ResponseWriter, r *http.Request) {
	current := s.state
	current.Tracks = nil
	if current.LastFetch != nil {
		fetch := *current.LastFetch
		current.LastFetch = &fetch
	}
	sameTracks := len(s.state.Tracks) == len(s.cachedTracks) && (s.state.Tracks == nil) == (s.cachedTracks == nil)
	if sameTracks && len(s.state.Tracks) > 0 {
		sameTracks = &s.state.Tracks[0] == &s.cachedTracks[0]
	}
	if s.stateJSON == nil || !sameTracks || s.tracksRevision != s.cachedRevision || !reflect.DeepEqual(current, s.cachedState) {
		encoded, err := json.Marshal(s.state)
		if err != nil {
			failure(w, 500, "unable to encode state")
			return
		}
		s.stateJSON = append(encoded, '\n')
		s.stateETag = fmt.Sprintf(`"%x"`, sha256.Sum256(s.stateJSON))
		s.cachedState = current
		s.cachedTracks = s.state.Tracks
		s.cachedRevision = s.tracksRevision
	}
	w.Header().Set("ETag", s.stateETag)
	for _, tag := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" || strings.TrimPrefix(tag, "W/") == s.stateETag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(s.stateJSON)
}

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON object")
	}
	return nil
}
func (s *Server) startLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Provider string `json:"provider"`
	}
	if decode(w, r, &in) != nil {
		failure(w, 400, "expected provider JSON")
		return
	}
	if in.Provider != "github" && in.Provider != "discord" && in.Provider != "spotify" {
		failure(w, 400, "unsupported provider")
		return
	}
	if !s.op.TryLock() {
		failure(w, 409, "operation in progress")
		return
	}
	defer s.op.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session != nil {
		failure(w, 409, "logout before changing accounts")
		return
	}
	verifier := RandomKey()
	path := "/oauth/callback/" + RandomKey()
	challenge := sha256.Sum256([]byte(verifier))
	q := url.Values{"provider": {in.Provider}, "redirect_to": {s.base + path}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"s256"}}
	authURL := s.client.URL + "/auth/v1/authorize?" + q.Encode()
	s.login = &pendingLogin{verifier, path, time.Now().Add(10 * time.Minute)}
	s.state.LoginPending = true
	s.state.Error = ""
	err := s.OpenBrowser(authURL)
	send(w, 200, map[string]any{"ok": true, "authorization_url": authURL, "browser_opened": err == nil, "expires_in": 600})
}
func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		failure(w, 405, "method not allowed")
		return
	}
	if !s.op.TryLock() {
		failure(w, 409, "operation in progress")
		return
	}
	defer s.op.Unlock()
	s.mu.Lock()
	p := s.login
	if p == nil || r.URL.Path != p.Path || time.Now().After(p.Expires) {
		s.mu.Unlock()
		failure(w, 400, "invalid or expired login callback")
		return
	}
	s.login = nil
	s.state.LoginPending = false
	s.mu.Unlock()
	if r.URL.Query().Get("error") != "" || r.URL.Query().Get("code") == "" {
		s.setError(errors.New("OAuth login was denied or missing code"))
		failure(w, 400, "OAuth login failed")
		return
	}
	var sess Session
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	err := s.client.request(ctx, "POST", "/auth/v1/token?grant_type=pkce", "", map[string]string{"auth_code": r.URL.Query().Get("code"), "code_verifier": p.Verifier}, nil, &sess)
	if err == nil {
		err = sess.normalize()
	}
	if err == nil {
		err = SaveSession(s.path, &sess)
	}
	if err != nil {
		s.setError(err)
		failure(w, 502, "Unable to complete login")
		return
	}
	s.mu.Lock()
	s.session = &sess
	s.yandex = nil
	u := sess.User
	s.state = State{Authenticated: true, User: &u, Tracks: []Result{}}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("Login complete. You can close this window and return to DDetector."))
}
func (s *Server) setError(err error) { s.mu.Lock(); defer s.mu.Unlock(); s.state.Error = err.Error() }

// Called only while holding op, so refresh rotation and identity changes cannot race.
func (s *Server) authorize(ctx context.Context) (token string, authErr error) {
	defer func() {
		if authErr != nil {
			s.mu.Lock()
			s.state.HasAccess = false
			s.state.Tracks = []Result{}
			s.state.Processed = 0
			s.state.FoundCount = 0
			s.mu.Unlock()
		}
	}()
	s.mu.Lock()
	sess := s.session
	s.mu.Unlock()
	if sess == nil {
		return "", errors.New("login required")
	}
	if sess.ExpiresAt <= time.Now().Add(60*time.Second).Unix() {
		var next Session
		err := s.client.request(ctx, "POST", "/auth/v1/token?grant_type=refresh_token", "", map[string]string{"refresh_token": sess.Refresh}, nil, &next)
		if err != nil {
			return "", err
		}
		if err = next.normalize(); err != nil {
			return "", err
		}
		if next.User.ID != sess.User.ID {
			return "", errors.New("refresh changed user identity")
		}
		if err = SaveSession(s.path, &next); err != nil {
			return "", err
		}
		s.mu.Lock()
		s.session = &next
		sess = &next
		u := next.User
		s.state.User = &u
		s.mu.Unlock()
	}
	var user User
	if err := s.client.request(ctx, "GET", "/auth/v1/user", sess.Access, nil, nil, &user); err != nil {
		return "", err
	}
	if user.ID != sess.User.ID {
		return "", errors.New("invalid session identity")
	}
	allowed, banned, err := s.client.permitted(ctx, sess.Access, user.ID)
	s.mu.Lock()
	s.state.User = &user
	s.state.AccessChecked = err == nil
	s.state.HasAccess = err == nil && allowed
	s.state.IsBanned = banned
	if !s.state.HasAccess {
		s.state.Tracks = []Result{}
		s.state.Processed = 0
		s.state.FoundCount = 0
	}
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	if !allowed {
		return "", errors.New("DDetector access denied")
	}
	return sess.Access, nil
}
func (s *Server) startJob(w http.ResponseWriter, fetch bool) {
	if !s.op.TryLock() {
		failure(w, 409, "operation in progress")
		return
	}
	s.mu.Lock()
	if s.session == nil {
		s.mu.Unlock()
		s.op.Unlock()
		failure(w, 401, "login required")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	done := make(chan struct{})
	s.jobCancel, s.jobDone = cancel, done
	s.state.Loading = true
	s.state.Fetching = fetch
	s.state.Error = ""
	s.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			s.mu.Lock()
			s.jobCancel, s.jobDone = nil, nil
			s.op.Unlock()
			close(done)
			s.mu.Unlock()
		}()
		err := s.load(ctx, fetch)
		s.mu.Lock()
		s.state.Loading = false
		s.state.Fetching = false
		s.state.Processing = false
		if err != nil {
			s.state.Error = err.Error()
		}
		s.mu.Unlock()
	}()
	send(w, 202, map[string]any{"ok": true, "accepted": true})
}
func (s *Server) load(ctx context.Context, fetch bool) error {
	token, err := s.authorize(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	c := s.yandex
	uid := s.session.User.ID
	s.mu.Unlock()
	if c == nil {
		return errors.New("Yandex Music token required")
	}
	cachePath := s.path + ".yandex-" + c.uid + ".tracks-cache.json"
	cache, cacheErr := readTracksCache(cachePath, uid, c.base+"/users/"+c.uid)
	if cacheErr != nil {
		log.Print("Yandex cache unavailable; rebuilding")
	}
	s.publishResults(cache.Tracks)
	cached := map[int64]Result{}
	for _, r := range cache.Tracks {
		cached[r.ID] = r
	}
	tracks, err := rows[Track](ctx, s.client, "ddetector_tracks", "id,added_at", "added_at.desc,id.asc", token)
	if err != nil {
		return err
	}
	missing := []string{}
	for _, t := range tracks {
		if t.ID <= 0 {
			return errors.New("invalid Supabase library track ID")
		}
		if _, ok := cached[t.ID]; !ok || fetch {
			missing = append(missing, strconv.FormatInt(t.ID, 10))
		}
	}
	metadata, err := c.metadata(ctx, missing)
	if err != nil {
		return err
	}
	ignored, err := rows[struct {
		ID int64 `json:"track_id"`
	}](ctx, s.client, "ddetector_ignored_tracks", "track_id", "track_id.asc", token)
	if err != nil {
		return err
	}
	im := map[int64]bool{}
	for _, v := range ignored {
		im[v.ID] = true
	}
	results := []Result{}
	pending := []int{}
	stats := FetchResult{Total: len(tracks)}
	present := map[int64]bool{}
	for _, member := range tracks {
		if err := ctx.Err(); err != nil {
			return err
		}
		id := member.ID
		present[id] = true
		old, ok := cached[id]
		t := old.Track
		if fresh, exists := metadata[id]; exists {
			t = fresh
		}
		if t.ID == 0 {
			return errors.New("Yandex metadata missing for library track")
		}
		t.AddedAt = member.AddedAt
		r := old
		r.Track = t
		r.Ignored = im[id]
		if !ok {
			r = Result{Track: t, Ignored: im[id], Status: "pending"}
			pending = append(pending, len(results))
			stats.Added++
		} else if needsLyricsAcquisition(r, cache) {
			pending = append(pending, len(results))
		}
		results = append(results, r)
	}
	for _, r := range cache.Tracks {
		if !present[r.ID] {
			stats.Removed++
		}
	}
	if err := s.processLyrics(ctx, c, cachePath, cache, results, pending, &stats); err != nil {
		return err
	}
	s.mu.Lock()
	s.state.LastFetch = &stats
	s.mu.Unlock()
	return nil
}
func (s *Server) ignore(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID      int64 `json:"track_id"`
		Ignored *bool `json:"ignored"`
	}
	if decode(w, r, &in) != nil || in.ID <= 0 || in.Ignored == nil {
		failure(w, 400, "expected positive track_id and ignored boolean")
		return
	}
	if !s.op.TryLock() {
		failure(w, 409, "operation in progress")
		return
	}
	defer s.op.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	token, err := s.authorize(ctx)
	if err != nil {
		failure(w, 403, err.Error())
		return
	}
	s.mu.Lock()
	exists := false
	for _, t := range s.state.Tracks {
		if t.ID == in.ID {
			exists = true
			break
		}
	}
	s.mu.Unlock()
	if !exists {
		failure(w, 404, "load a known track before changing ignore status")
		return
	}
	if *in.Ignored {
		err = s.client.request(ctx, "POST", "/rest/v1/ddetector_ignored_tracks?on_conflict=track_id", token, map[string]int64{"track_id": in.ID}, map[string]string{"Prefer": "resolution=ignore-duplicates,return=minimal"}, nil)
	} else {
		err = s.client.request(ctx, "DELETE", "/rest/v1/ddetector_ignored_tracks?track_id=eq."+strconv.FormatInt(in.ID, 10), token, nil, nil, nil)
	}
	if err != nil {
		s.setError(err)
		failure(w, 502, err.Error())
		return
	}
	s.mu.Lock()
	for i := range s.state.Tracks {
		if s.state.Tracks[i].ID == in.ID {
			if s.state.Tracks[i].Ignored != *in.Ignored {
				s.state.Tracks[i].Ignored = *in.Ignored
				s.tracksRevision++
			}
		}
	}
	s.mu.Unlock()
	send(w, 200, map[string]any{"ok": true, "track_id": in.ID, "ignored": *in.Ignored})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	// Serialize deletion with operations that can refresh and persist the session.
	for !s.op.TryLock() {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer s.op.Unlock()
	err := os.Remove(s.path)
	if err != nil && !os.IsNotExist(err) {
		failure(w, 500, "unable to remove saved session")
		return
	}
	s.mu.Lock()
	s.session = nil
	s.yandex = nil
	s.login = nil
	s.state = State{Tracks: []Result{}}
	s.mu.Unlock()
	send(w, 200, map[string]any{"ok": true})
}
func Listen() (net.Listener, error) { return net.Listen("tcp4", "127.0.0.1:0") }

// Start maintains restored sessions and loads newly authenticated accounts.
func (s *Server) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if !s.op.TryLock() {
				continue
			}
			s.mu.Lock()
			sess := s.session
			needsLoad := sess != nil && !s.state.AccessChecked
			needsRefresh := sess != nil && sess.ExpiresAt <= time.Now().Add(60*time.Second).Unix()
			s.mu.Unlock()
			if needsLoad || needsRefresh {
				work, cancel := context.WithTimeout(ctx, 2*time.Minute)
				var err error
				if needsLoad {
					s.mu.Lock()
					s.state.Loading = true
					s.mu.Unlock()
					err = s.load(work, false)
				} else {
					_, err = s.authorize(work)
				}
				cancel()
				s.mu.Lock()
				s.state.Loading = false
				s.state.Processing = false
				if err != nil {
					s.state.Error = err.Error()
				}
				s.mu.Unlock()
				if err != nil {
					s.op.Unlock()
					select {
					case <-ctx.Done():
						return
					case <-time.After(30 * time.Second):
					}
					continue
				}
			}
			s.op.Unlock()
		}
	}()
}
