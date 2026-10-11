package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type playbackCensorBackend struct {
	fakeCensor
	resolve     func(context.Context, CensorIdentity, string) (CensorCandidate, error)
	resolutions int
}

func (b *playbackCensorBackend) Candidate(ctx context.Context, i CensorIdentity, id string) (CensorCandidate, error) {
	b.resolutions++
	return b.resolve(ctx, i, id)
}

func playbackFixture(t *testing.T) (*Server, *playbackCensorBackend, *censorRuntime) {
	t.Helper()
	s := censorServer(t)
	b := &playbackCensorBackend{}
	b.resolve = func(_ context.Context, i CensorIdentity, id string) (CensorCandidate, error) {
		if i.UserID != "user-1" || i.AccessToken != "secret-access" || id != "candidate-1" {
			t.Fatal("incorrect resolver identity or candidate", i.UserID, id)
		}
		return CensorCandidate{ID: id, URL: "https://cdn.example/fresh.mp3", FullAudio: true, Downloadable: true}, nil
	}
	if err := s.SetCensorBackend(b); err != nil {
		t.Fatal(err)
	}
	rt, err := s.currentCensorRuntime(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	result := strongFixture()
	rt.mu.Lock()
	rt.disk.Jobs = []CensorJob{{ID: "job-1", Request: CensorRequest{ID: "request-1", TrackID: 42, OriginalURL: "https://cdn.example/original.mp3"}, Status: "review_required", Result: &result, Candidates: []CensorCandidate{{ID: "candidate-1", URL: "https://cdn.example/stale.mp3", FullAudio: true, Downloadable: true, Result: &result}}, PublicationKey: "existing-key", SelectedCandidate: "candidate-1"}}
	err = rt.persistLocked()
	rt.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return s, b, rt
}

type nativePlaybackTestBackend struct {
	fakeCensor
	source *nativeSource
}

func (b *nativePlaybackTestBackend) Candidate(ctx context.Context, i CensorIdentity, id string) (CensorCandidate, error) {
	return b.source.Candidate(ctx, i, id)
}

func (b *nativePlaybackTestBackend) PlaybackCandidate(ctx context.Context, i CensorIdentity, id string) (CensorCandidate, error) {
	return b.source.PlaybackCandidate(ctx, i, id)
}

func TestCensorPlaybackNativeMissingExplicit(t *testing.T) {
	for _, explicit := range []string{"", `,"explicit":false`} {
		t.Run(fmt.Sprintf("metadata=%q", explicit), func(t *testing.T) {
			s, _, rt := playbackFixture(t)
			downloads := 0
			preview := false
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/tracks/42":
					fmt.Fprintf(w, `{"result":[{"id":42,"available":true%s}]}`, explicit)
				case "/tracks/42/download-info":
					downloads++
					fmt.Fprintf(w, `{"result":[{"codec":"mp3","preview":%t,"downloadInfoUrl":"https://storage.mds.yandex.net/descriptor"}]}`, preview)
				default:
					t.Error("unexpected provider path", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer provider.Close()
			c := newYandexClient("provider-secret")
			c.base = provider.URL
			c.http.Transport = nativeAudioTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "storage.mds.yandex.net" {
					return http.DefaultTransport.RoundTrip(r)
				}
				if r.Header.Get("Authorization") != "" {
					t.Error("provider credentials leaked to descriptor")
				}
				body := `<download-info><host>storage.mds.yandex.net</host><path>/full.mp3</path><ts>1</ts><s>salt</s></download-info>`
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			s.yandex = c
			n := &nativeSource{server: s}
			rt.backend = &nativePlaybackTestBackend{source: n}
			rt.disk.Jobs[0].Candidates[0].ID = "candidate:yandex:42"
			path := "/api/censor/playback?id=job-1&candidate_id=candidate:yandex:42"
			w := api(s, "GET", path, "", "bridge-key")
			var response map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 200 || !strings.HasPrefix(response["url"], "https://storage.mds.yandex.net/get-mp3/") || downloads != 1 {
				t.Fatal("missing explicit prevented full-audio playback", w.Code, w.Body.String(), err, downloads)
			}
			candidate, err := n.PlaybackCandidate(context.Background(), CensorIdentity{}, "candidate:yandex:42")
			if err != nil || candidate.Metadata["explicit"] != "false" || !candidate.FullAudio || !candidate.Downloadable {
				t.Fatal("playback fabricated explicit metadata or lost full audio", candidate, err)
			}
			preview = true
			// A provider audio failure is not a stale explicit-metadata conflict,
			// and must never disclose provider diagnostics or return preview audio.
			w = api(s, "GET", path, "", "bridge-key")
			if w.Code != 502 || strings.Contains(w.Body.String(), "preview") || downloads != 3 {
				t.Fatal(w.Code, w.Body.String(), downloads)
			}
			_, err = n.Candidate(context.Background(), CensorIdentity{}, "candidate:yandex:42")
			if !errors.Is(err, ErrCensorConflict) || downloads != 3 {
				t.Fatal("approval did not reject missing explicit before audio resolution", err, downloads)
			}
		})
	}
}

const playbackPath = "/api/censor/playback?id=job-1&candidate_id=candidate-1"

func TestCensorPlaybackWhileOperationLockHeld(t *testing.T) {
	s, b, _ := playbackFixture(t)
	s.op.Lock()
	defer s.op.Unlock()
	finished := make(chan int, 1)
	go func() { finished <- api(s, "GET", playbackPath, "", "bridge-key").Code }()
	select {
	case status := <-finished:
		if status != 200 || b.resolutions != 1 {
			t.Fatal(status, b.resolutions)
		}
	case <-time.After(time.Second):
		t.Fatal("playback queued behind the held operation lock")
	}
}

func TestCensorPlaybackDuringBlockedPollingAuthorization(t *testing.T) {
	s, _, _ := playbackFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var users atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/v1/user":
			if users.Add(1) == 1 {
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
			fmt.Fprint(w, `{"id":"user-1"}`)
		case "/rest/v1/bans":
			fmt.Fprint(w, `[]`)
		case "/rest/v1/ddetector_users":
			fmt.Fprint(w, `[{"user_id":"user-1"}]`)
		default:
			t.Error("unexpected upstream", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer upstream.Close()
	defer close(release)
	s.client.URL = upstream.URL
	poll := make(chan int, 1)
	go func() { poll <- api(s, "GET", "/api/censor/state", "", "bridge-key").Code }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("poll did not reach authorization")
	}
	if s.op.TryLock() {
		s.op.Unlock()
		t.Fatal("test did not reproduce operation-lock contention")
	}
	finished := make(chan int, 1)
	go func() { finished <- api(s, "GET", playbackPath, "", "bridge-key").Code }()
	select {
	case status := <-finished:
		if status != 200 {
			t.Fatal("playback blocked by polling authorization", status)
		}
	case <-time.After(time.Second):
		t.Fatal("playback waited for blocked polling authorization")
	}
	// Release polling before cleanup and verify it also completes normally.
	release <- struct{}{}
	select {
	case status := <-poll:
		if status != 200 {
			t.Fatal("poll failed", status)
		}
	case <-time.After(time.Second):
		t.Fatal("poll did not finish")
	}
}

func TestCensorWorkerIdentityWhileOperationLockHeld(t *testing.T) {
	s, _, rt := playbackFixture(t)
	s.op.Lock()
	defer s.op.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	identity, err := rt.identity(ctx)
	if err != nil || identity.UserID != "user-1" || identity.AccessToken != "secret-access" {
		t.Fatal("worker authorization queued behind independent operation", identity.UserID, err)
	}
}

func TestCensorPlaybackLogoutCancelsResolutionWithOperationHeld(t *testing.T) {
	s, b, _ := playbackFixture(t)
	entered := make(chan struct{})
	b.resolve = func(ctx context.Context, _ CensorIdentity, _ string) (CensorCandidate, error) {
		close(entered)
		<-ctx.Done()
		return CensorCandidate{}, ErrCensorDenied
	}
	s.op.Lock()
	playback := make(chan int, 1)
	go func() { playback <- api(s, "GET", playbackPath, "", "bridge-key").Code }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		s.op.Unlock()
		t.Fatal("resolution blocked by independent operation")
	}
	logout := make(chan int, 1)
	go func() { logout <- api(s, "POST", "/api/logout", "", "bridge-key").Code }()
	select {
	case status := <-playback:
		if status != 403 {
			t.Error("logout did not deny playback", status)
		}
	case <-time.After(time.Second):
		t.Error("logout did not cancel playback with operation lock held")
	}
	s.op.Unlock()
	select {
	case status := <-logout:
		if status != 200 {
			t.Fatal("logout failed", status)
		}
	case <-time.After(time.Second):
		t.Fatal("logout did not finish")
	}
}

func TestCensorPlaybackDoesNotReplaceOperationCancellation(t *testing.T) {
	s, _, _ := playbackFixture(t)
	s.op.Lock()
	defer s.op.Unlock()
	ctx, cancel := s.operationContext(context.Background(), time.Minute)
	defer cancel()
	if w := api(s, "GET", playbackPath, "", "bridge-key"); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	s.mu.Lock()
	s.operationCancel()
	s.mu.Unlock()
	if ctx.Err() == nil {
		t.Fatal("playback overwrote independent operation cancellation")
	}
}

func TestCensorPlaybackFreshURLWithoutEvidenceMutation(t *testing.T) {
	s, b, rt := playbackFixture(t)
	before, _ := json.Marshal(rt.snapshot())
	path := filepath.Join(rt.dir, "runtime.json")
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	resolve := b.resolve
	b.resolve = func(ctx context.Context, i CensorIdentity, id string) (CensorCandidate, error) {
		if !rt.mu.TryLock() {
			t.Fatal("provider resolution held runtime mutex")
		}
		rt.mu.Unlock()
		return resolve(ctx, i, id)
	}
	w := api(s, "GET", playbackPath, "", "bridge-key")
	var response map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(response) != 1 || response["url"] != "https://cdn.example/fresh.mp3" || b.resolutions != 1 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String(), b.resolutions, w.Header())
	}
	after, _ := json.Marshal(rt.snapshot())
	diskAfter, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) || !bytes.Equal(diskBefore, diskAfter) {
		t.Fatal("playback changed runtime or persisted evidence", err)
	}
}

func TestCensorPlaybackRejectsInputsAndUnauthorizedRequests(t *testing.T) {
	s, b, _ := playbackFixture(t)
	for _, tc := range []struct {
		method, path, key string
		status            int
	}{
		{"POST", playbackPath, "bridge-key", 405},
		{"GET", playbackPath, "wrong-key", 401},
		{"GET", "/api/censor/playback", "bridge-key", 400},
		{"GET", "/api/censor/playback?id=job-1", "bridge-key", 400},
		{"GET", playbackPath + "&id=job-1", "bridge-key", 400},
		{"GET", playbackPath + "&candidate_id=candidate-1", "bridge-key", 400},
		{"GET", playbackPath + "&url=https://evil.example/a.mp3", "bridge-key", 400},
		{"GET", "/api/censor/playback?id=%zz&candidate_id=candidate-1", "bridge-key", 400},
		{"GET", "/api/censor/playback?id=job-1&candidate_id=", "bridge-key", 400},
		{"GET", "/api/censor/playback?id=job-1&candidate_id=%0A", "bridge-key", 400},
		{"GET", "/api/censor/playback?id=other-job&candidate_id=candidate-1", "bridge-key", 404},
		{"GET", "/api/censor/playback?id=job-1&candidate_id=other-candidate", "bridge-key", 404},
	} {
		w := api(s, tc.method, tc.path, "", tc.key)
		if w.Code != tc.status {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body.String())
		}
	}
	s.session = nil
	w := api(s, "GET", playbackPath, "", "bridge-key")
	if w.Code != 403 || b.resolutions != 0 {
		t.Fatal(w.Code, b.resolutions)
	}
}

func TestCensorPlaybackRequiresStoredFullAudio(t *testing.T) {
	for _, kind := range []string{"full", "downloadable", "page", "preview", "html", "unsafe"} {
		t.Run(kind, func(t *testing.T) {
			s, b, rt := playbackFixture(t)
			rt.mu.Lock()
			c := &rt.disk.Jobs[0].Candidates[0]
			switch kind {
			case "full":
				c.FullAudio = false
			case "downloadable":
				c.Downloadable = false
			case "page", "preview":
				c.Metadata = map[string]string{"kind": kind}
			case "html":
				c.Metadata = map[string]string{"content_type": "text/html"}
			case "unsafe":
				c.URL = "file:///tmp/audio.mp3"
			}
			rt.mu.Unlock()
			w := api(s, "GET", playbackPath, "", "bridge-key")
			if w.Code != 409 || b.resolutions != 0 {
				t.Fatal(w.Code, b.resolutions)
			}
		})
	}
}

func TestCensorPlaybackValidatesResolvedCandidate(t *testing.T) {
	for _, kind := range []string{"id", "full", "downloadable", "page", "preview", "html", "http", "file", "credentials", "fragment", "metadata"} {
		t.Run(kind, func(t *testing.T) {
			s, b, _ := playbackFixture(t)
			resolve := b.resolve
			b.resolve = func(ctx context.Context, i CensorIdentity, id string) (CensorCandidate, error) {
				c, err := resolve(ctx, i, id)
				switch kind {
				case "id":
					c.ID = "other"
				case "full":
					c.FullAudio = false
				case "downloadable":
					c.Downloadable = false
				case "page", "preview":
					c.Metadata = map[string]string{"kind": kind}
				case "html":
					c.Metadata = map[string]string{"content_type": "text/html"}
				case "http":
					c.URL = "http://cdn.example/a.mp3"
				case "file":
					c.URL = "file:///tmp/a.mp3"
				case "credentials":
					c.URL = "https://user:secret@cdn.example/a.mp3"
				case "fragment":
					c.URL += "#fragment"
				case "metadata":
					c.Metadata = map[string]string{"value": strings.Repeat("x", 2049)}
				}
				return c, err
			}
			w := api(s, "GET", playbackPath, "", "bridge-key")
			if w.Code != 409 || strings.Contains(w.Body.String(), "https://") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestCensorPlaybackResolverErrorsAndUnsupported(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{
		{ErrCensorDenied, 403}, {ErrCensorNotFound, 404}, {ErrCensorConflict, 409}, {ErrCensorUnavailable, 503}, {ErrCensorUnsupported, 501}, {errors.New("provider-secret"), 502},
	} {
		s, b, _ := playbackFixture(t)
		b.resolve = func(context.Context, CensorIdentity, string) (CensorCandidate, error) {
			return CensorCandidate{}, tc.err
		}
		w := api(s, "GET", playbackPath, "", "bridge-key")
		if w.Code != tc.status || strings.Contains(w.Body.String(), "provider-secret") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	s, _, rt := playbackFixture(t)
	rt.backend = &fakeCensor{}
	w := api(s, "GET", playbackPath, "", "bridge-key")
	if w.Code != 501 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestCensorPlaybackRechecksMembershipAndAccount(t *testing.T) {
	for _, change := range []string{"candidate", "audio", "account", "runtime", "cancel"} {
		t.Run(change, func(t *testing.T) {
			s, b, rt := playbackFixture(t)
			resolve := b.resolve
			b.resolve = func(ctx context.Context, i CensorIdentity, id string) (CensorCandidate, error) {
				c, err := resolve(ctx, i, id)
				switch change {
				case "candidate", "audio":
					rt.mu.Lock()
					if change == "candidate" {
						rt.disk.Jobs[0].Candidates = nil
					} else {
						rt.disk.Jobs[0].Candidates[0].FullAudio = false
					}
					rt.mu.Unlock()
				case "account":
					s.mu.Lock()
					s.session.User.ID = "other-account"
					s.mu.Unlock()
				case "runtime":
					s.mu.Lock()
					s.censorRuntime = nil
					s.mu.Unlock()
				case "cancel":
					s.mu.Lock()
					for _, cancel := range s.playbackCancels {
						cancel()
					}
					s.mu.Unlock()
				}
				return c, err
			}
			w := api(s, "GET", playbackPath, "", "bridge-key")
			expected := 403
			if change == "candidate" {
				expected = 404
			}
			if change == "audio" {
				expected = 409
			}
			if w.Code != expected || strings.Contains(w.Body.String(), "fresh.mp3") {
				t.Fatal(w.Code, w.Body.String())
			}
			if change == "runtime" {
				s.mu.Lock()
				s.censorRuntime = rt
				s.mu.Unlock()
			}
		})
	}
}
