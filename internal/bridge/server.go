package bridge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"ddetector-go/internal/censor"
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
	LyricsFailed  int    `json:"lyrics_failed,omitempty"`
	Partial       bool   `json:"partial,omitempty"`
	Error         string `json:"error,omitempty"`
}
type pendingLogin struct {
	Verifier, Path string
	Expires        time.Time
}
type Server struct {
	spotify            *spotifyService
	mu                 sync.Mutex
	op                 sync.Mutex
	refreshMu          sync.Mutex
	client             *Client
	yandex             *yandexClient
	jobCancel          context.CancelFunc
	operationCancel    context.CancelFunc
	playbackCancels    map[chan struct{}]context.CancelFunc
	jobDone            chan struct{}
	lifecycle          context.Context
	lifecycleCancel    context.CancelFunc
	maintenanceDone    chan struct{}
	closing            bool
	logoutPending      int
	yandexFactory      func(string) *yandexClient
	exclusionTransport http.RoundTripper
	key, base, path    string
	customSession      bool
	session            *Session
	state              State
	login              *pendingLogin
	detector           *Detector
	censor             CensorBackend
	censorRuntime      *censorRuntime
	censorProcessor    censor.Processor
	OpenBrowser        func(string) error
	stateBuildMu       sync.Mutex
	tracksJSON         []byte
	tracksTag          string
	tracksRevision     uint64
	cachedRevision     uint64
	cachedState        State
	cachedTracks       []Result
	stateJSON          []byte
	stateETag          string
}

func RandomKey() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func NewServer(c *Client, key, base, path string, customSession ...bool) (*Server, error) {
	session, err := ReadSession(path)
	if err != nil {
		return nil, err
	}
	lifecycle, lifecycleCancel := context.WithCancel(context.Background())
	s := &Server{lifecycle: lifecycle, lifecycleCancel: lifecycleCancel, client: c, key: key, base: base, path: path, session: session, detector: NewDetector(), state: State{Tracks: []Result{}}}
	s.customSession = len(customSession) > 0 && customSession[0]
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
	if r.URL.Path == "/api/spotify/callback" {
		s.spotifyCallback(w, r)
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
	if strings.HasPrefix(r.URL.Path, "/api/spotify/") {
		s.handleSpotify(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/censor/") {
		s.handleCensor(w, r)
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
		s.mu.Unlock()
		s.sendState(w, r)
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

// stateResponse captures an immutable snapshot under mu, then marshals
// outside the lock. User values are immutable session snapshots;
// LastFetch is copied so even direct scalar writes invalidate the encoding.
// Track replacements (including publishResults) change the backing slice; all
// in-place track edits must increment tracksRevision under mu.
func (s *Server) stateResponse(r *http.Request) ([]byte, string, error) {
	s.stateBuildMu.Lock()
	defer s.stateBuildMu.Unlock()
	s.mu.Lock()
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
	tracksChanged := s.tracksJSON == nil || !sameTracks || s.tracksRevision != s.cachedRevision
	tracks, revision := s.state.Tracks, s.tracksRevision
	var snapshot []Result
	if tracksChanged {
		snapshot = append([]Result(nil), tracks...)
		if tracks != nil && snapshot == nil {
			snapshot = []Result{}
		}
	}
	s.mu.Unlock()
	if tracksChanged {
		data, err := json.Marshal(snapshot)
		if err != nil {
			return nil, "", errors.New("unable to encode tracks")
		}
		s.stateJSON = nil
		s.tracksJSON = data
		s.tracksTag = fmt.Sprintf("%x", sha256.Sum256(data))
		s.cachedTracks, s.cachedRevision = tracks, revision
	}
	// Delta clients receive complete scalar state and only a changed collection.
	// The opaque content revision is safe across backend restarts.
	delta := r.Header.Get("X-State-Delta") == "1"
	var encoded []byte
	var etag string
	if delta {
		scalar, err := json.Marshal(current)
		if err != nil {
			return nil, "", errors.New("unable to encode state")
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(scalar, &fields)
		delete(fields, "tracks")
		fields["tracksRevision"], _ = json.Marshal(s.tracksTag)
		if r.Header.Get("X-Tracks-Revision") != s.tracksTag {
			fields["tracks"] = s.tracksJSON
		}
		encoded, _ = json.Marshal(fields)
		encoded = append(encoded, '\n')
		etag = fmt.Sprintf(`"%x"`, sha256.Sum256(encoded))
	} else {
		if s.stateJSON == nil || tracksChanged || !reflect.DeepEqual(current, s.cachedState) {
			scalar, err := json.Marshal(current)
			if err != nil {
				return nil, "", errors.New("unable to encode state")
			}
			// Reuse the track encoding without sending it through Marshal again.
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(scalar, &fields)
			delete(fields, "tracks")
			scalar, _ = json.Marshal(fields)
			encoded = append(scalar[:len(scalar)-1], []byte(`,"tracks":`)...)
			encoded = append(encoded, s.tracksJSON...)
			s.stateJSON = append(encoded, '}', '\n')
			s.stateETag = fmt.Sprintf(`"%x"`, sha256.Sum256(s.stateJSON))
			s.cachedState = current
		}
		encoded, etag = s.stateJSON, s.stateETag
	}
	return encoded, etag, nil
}

func (s *Server) sendState(w http.ResponseWriter, r *http.Request) {
	// Cached byte slices are immutable. Release both locks before touching the
	// writer, including error and conditional responses, so slow clients cannot
	// hold up other snapshots.
	encoded, etag, err := s.stateResponse(r)
	if err != nil {
		failure(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Vary", "X-State-Delta, X-Tracks-Revision")
	w.Header().Set("ETag", etag)
	for _, tag := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" || strings.TrimPrefix(tag, "W/") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
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
	if !s.tryOperation() {
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
	if !s.tryOperation() {
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
	ctx, cancel := s.operationContext(r.Context(), 30*time.Second)
	defer cancel()
	err := s.client.request(ctx, "POST", "/auth/v1/token?grant_type=pkce", "", map[string]string{"auth_code": r.URL.Query().Get("code"), "code_verifier": p.Verifier}, nil, &sess)
	if err == nil {
		err = sess.normalize()
	}
	if err == nil {
		s.mu.Lock()
		rt := s.censorRuntime
		s.mu.Unlock()
		if rt != nil && rt.uid != sess.User.ID {
			err = s.closeCensor(ctx, true)
		}
	}
	if err == nil {
		// Commit disk and memory together with refresh; a concurrent playback
		// refresh must not overwrite the newly selected account's saved session.
		s.mu.Lock()
		if s.closing || s.logoutPending > 0 || ctx.Err() != nil {
			err = errors.New("login canceled")
		} else {
			err = SaveSession(s.path, &sess)
			if err == nil {
				s.session = &sess
				s.yandex = nil
				u := sess.User
				s.state = State{Authenticated: true, User: &u, Tracks: []Result{}}
			}
		}
		s.mu.Unlock()
	}
	if err != nil {
		s.setError(err)
		failure(w, 502, "Unable to complete login")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("Login complete. You can close this window and return to DDetector"))
}
func (s *Server) setError(err error) { s.mu.Lock(); defer s.mu.Unlock(); s.state.Error = err.Error() }

// Refresh rotation is serialized separately; user/access checks may run concurrently.
// Session guards prevent late network results from reviving a logged-out account.
func (s *Server) authorize(ctx context.Context) (token string, authErr error) {
	var observed *Session
	defer func() {
		if authErr != nil {
			s.mu.Lock()
			if s.session != observed || s.closing || s.logoutPending > 0 {
				s.mu.Unlock()
				return
			}
			s.state.HasAccess = false
			s.state.Tracks = []Result{}
			s.state.Processed = 0
			s.state.FoundCount = 0
			s.mu.Unlock()
		}
	}()
	s.refreshMu.Lock()
	refreshLocked := true
	defer func() {
		if refreshLocked {
			s.refreshMu.Unlock()
		}
	}()
	s.mu.Lock()
	sess := s.session
	observed = sess
	blocked := s.closing || s.logoutPending > 0
	s.mu.Unlock()
	if sess == nil || blocked {
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
		s.mu.Lock()
		if s.session != sess || s.closing || s.logoutPending > 0 || ctx.Err() != nil {
			s.mu.Unlock()
			return "", errors.New("session changed")
		}
		if err = SaveSession(s.path, &next); err != nil {
			s.mu.Unlock()
			return "", err
		}
		s.session = &next
		sess = &next
		observed = sess
		u := next.User
		s.state.User = &u
		s.mu.Unlock()
	}
	s.refreshMu.Unlock()
	refreshLocked = false
	var user User
	if err := s.client.request(ctx, "GET", "/auth/v1/user", sess.Access, nil, nil, &user); err != nil {
		return "", err
	}
	if user.ID != sess.User.ID {
		return "", errors.New("invalid session identity")
	}
	allowed, banned, err := s.client.permitted(ctx, sess.Access, user.ID)
	s.mu.Lock()
	if s.session != sess || s.closing || s.logoutPending > 0 || ctx.Err() != nil {
		s.mu.Unlock()
		return "", errors.New("session changed")
	}
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
	if !s.tryOperation() {
		failure(w, 409, "operation in progress")
		return
	}
	s.mu.Lock()
	if s.closing || s.logoutPending > 0 || s.lifecycle.Err() != nil {
		s.mu.Unlock()
		s.op.Unlock()
		failure(w, 409, "operation in progress")
		return
	}

	if s.session == nil {
		s.mu.Unlock()
		s.op.Unlock()
		failure(w, 401, "login required")
		return
	}
	ctx, cancel := context.WithTimeout(s.lifecycle, 5*time.Minute)
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
func (s *Server) load(ctx context.Context, fetch bool) (loadErr error) {
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
	var excluded map[int64]bool
	if fetch {
		if err := s.client.request(ctx, http.MethodPost, "/functions/v1/sync-ddetector-tracks", token, struct{}{}, nil, nil); err != nil {
			return fmt.Errorf("unable to synchronize track list: %w", err)
		}
		excluded, err = downloadExclusions(ctx, s.exclusionTransport)
		if err != nil {
			return err
		}
	}
	cachePath, releaseStorage, err := acquireTracksStoragePath(s.path, c.uid, s.customSession)
	if err != nil {
		return err
	}
	defer releaseStorage()
	cache, cacheErr := readTracksCacheContext(ctx, cachePath, uid, c.base+"/users/"+c.uid)
	if cacheErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.Print("Yandex cache unavailable; rebuilding")
	}
	purged := 0
	if fetch {
		cache.Excluded = excluded
		beforePurge := len(cache.Tracks)
		filterExcluded(&cache)
		purged = beforePurge - len(cache.Tracks)
		// Commit the new policy and purge before any subsequent fetch can fail.
		if err := saveTracksCache(cachePath, cache); err != nil {
			return fmt.Errorf("unable to persist exclusion list and purge cached tracks: %w", err)
		}
		cache.Dirty, cache.Rebuild = false, false
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
	eligible := make([]Track, 0, len(tracks))
	for _, t := range tracks {
		if !cache.Excluded[t.ID] {
			eligible = append(eligible, t)
		}
	}
	tracks = eligible
	missing := []string{}
	for _, t := range tracks {
		if t.ID <= 0 {
			return errors.New("invalid Supabase library track ID")
		}
		if old, ok := cached[t.ID]; !ok || fetch || old.Available == nil {
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
	// The later membership-removal loop only sees tracks that survived the purge.
	stats := FetchResult{Total: len(tracks), Removed: purged}
	defer func() {
		if loadErr != nil {
			stats.Partial = true
			stats.Error = loadErr.Error()
		}
		s.mu.Lock()
		s.state.LastFetch = &stats
		s.state.Error = stats.Error
		s.mu.Unlock()
	}()
	present := map[int64]bool{}
	coverCtx := withCoverBudget(ctx)
	for _, member := range tracks {
		if err := ctx.Err(); err != nil {
			return err
		}
		id := member.ID
		present[id] = true
		old, ok := cached[id]
		t := old.Track
		if fresh, exists := metadata[id]; exists {
			// Deleted tracks can return only an ID and an availability signal.
			// Keep cached display metadata, but use the fresh tri-state signal.
			if fresh.Title == "" {
				fresh.Title = t.Title
			}
			if fresh.Artist == "" {
				fresh.Artist = t.Artist
			}
			if len(fresh.Artists) == 0 {
				fresh.Artists = t.Artists
			}
			if fresh.Cover == nil {
				fresh.Cover = t.Cover
			}
			t = fresh
		}
		if t.ID == 0 {
			return errors.New("Yandex metadata missing for library track")
		}
		t = s.spotifyFallback(coverCtx, t)
		t.AddedAt = member.AddedAt
		r := old
		r.Track = t
		r.Ignored = im[id]
		if !ok {
			r = Result{Track: t, Ignored: im[id], Status: "pending"}
			if fetch {
				pending = append(pending, len(results))
			}
			stats.Added++
		} else if fetch && needsLyricsAcquisition(r, cache) {
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
	if !s.tryOperation() {
		failure(w, 409, "operation in progress")
		return
	}
	defer s.op.Unlock()
	ctx, cancel := s.operationContext(r.Context(), 90*time.Second)
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
		err = s.client.request(ctx, "POST", "/rest/v1/ddetector_ignored_tracks?on_conflict=user_id,track_id", token, map[string]int64{"track_id": in.ID}, map[string]string{"Prefer": "resolution=ignore-duplicates,return=minimal"}, nil)
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
	// Block new operations before cancellation, while preserving refresh/deletion serialization.
	s.mu.Lock()
	s.logoutPending++
	if s.jobCancel != nil {
		s.jobCancel()
	}
	if s.operationCancel != nil {
		s.operationCancel()
	}
	for _, cancel := range s.playbackCancels {
		cancel()
	}
	s.mu.Unlock()
	s.cancelCensor()
	defer func() { s.mu.Lock(); s.logoutPending--; s.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.closeCensor(ctx, false); err != nil {
		failure(w, 504, "censor cancellation timed out")
		return
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !s.op.TryLock() {
		select {
		case <-ctx.Done():
			failure(w, 504, "logout cancellation timed out")
			return
		case <-ticker.C:
		}
	}
	defer s.op.Unlock()
	if err := s.closeCensor(ctx, true); err != nil {
		failure(w, 500, "unable to close censor runtime")
		return
	}
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

// Start maintains sessions and checks access without loading tracks automatically.
func (s *Server) Start(ctx context.Context) {
	s.mu.Lock()
	if s.closing || s.maintenanceDone != nil {
		s.mu.Unlock()
		return
	}
	done := make(chan struct{})
	s.maintenanceDone = done
	lifecycle := s.lifecycle
	s.mu.Unlock()
	go func() {
		defer close(done)
		// Parent cancellation also cancels API jobs, not just maintenance.
		maintenance, cancelMaintenance := context.WithCancel(lifecycle)
		stopParent := context.AfterFunc(ctx, s.lifecycleCancel)
		defer stopParent()
		defer cancelMaintenance()
		ctx = maintenance
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if !s.tryOperation() {
				continue
			}
			s.mu.Lock()
			sess := s.session
			needsAuthorization := sess != nil && !s.state.AccessChecked
			needsRefresh := sess != nil && sess.ExpiresAt <= time.Now().Add(60*time.Second).Unix()
			s.mu.Unlock()
			if needsAuthorization || needsRefresh {
				work, cancel := context.WithTimeout(ctx, 2*time.Minute)
				s.mu.Lock()
				s.jobCancel = cancel
				if s.logoutPending > 0 || s.closing {
					cancel()
				}
				s.mu.Unlock()
				_, err := s.authorize(work)
				cancel()
				s.mu.Lock()
				s.jobCancel = nil
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

// tryOperation closes the race between a pending logout/shutdown and a new job.
func (s *Server) tryOperation() bool {
	if !s.op.TryLock() {
		return false
	}
	s.mu.Lock()
	blocked := s.closing || s.logoutPending > 0 || (s.lifecycle != nil && s.lifecycle.Err() != nil)
	s.mu.Unlock()
	if blocked {
		s.op.Unlock()
		return false
	}
	return true
}

// Close cancels background work and waits for its final cache checkpoint.
// It is safe to retry with a fresh timeout if the first wait expires.
func (s *Server) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	s.lifecycleCancel()
	if s.jobCancel != nil {
		s.jobCancel()
	}
	if s.operationCancel != nil {
		s.operationCancel()
	}
	for _, cancel := range s.playbackCancels {
		cancel()
	}
	jobDone, maintenanceDone := s.jobDone, s.maintenanceDone
	s.mu.Unlock()
	if err := s.closeCensor(ctx, false); err != nil {
		return err
	}
	for _, done := range []chan struct{}{jobDone, maintenanceDone} {
		if done == nil {
			continue
		}
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !s.op.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	defer s.op.Unlock()
	return s.closeCensor(ctx, true)
}

// Called with op held: logout can cancel an in-flight refresh or mutation too.
func (s *Server) operationContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	stop := context.AfterFunc(s.lifecycle, cancel)
	s.mu.Lock()
	s.operationCancel = cancel
	if s.closing || s.logoutPending > 0 {
		cancel()
	}
	s.mu.Unlock()
	return ctx, func() {
		cancel()
		stop()
		s.mu.Lock()
		s.operationCancel = nil
		s.mu.Unlock()
	}
}
