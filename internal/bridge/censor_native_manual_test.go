package bridge

import (
	"context"
	"ddetector-go/internal/censor"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func nativeManualFixture(t *testing.T) (*Server, *nativeBackend, *orchestrationProcessor, *atomic.Int64) {
	t.Helper()
	t.Setenv("FCKCENSOR_API_URL", "")
	s := censorServer(t)
	t.Cleanup(func() {
		if err := s.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	processor := &orchestrationProcessor{}
	s.censorProcessor = processor
	var descriptors atomic.Int64
	provider := newYandexClient("private-provider-token")
	provider.http.Transport = nativeAudioTransport(func(r *http.Request) (*http.Response, error) {
		body := ""
		switch {
		case r.URL.Path == "/tracks/42" || r.URL.Path == "/tracks/99":
			id := strings.TrimPrefix(r.URL.Path, "/tracks/")
			body = fmt.Sprintf(`{"result":[{"id":%s,"title":"real title","artists":[{"name":"real artist"}],"available":true,"explicit":true,"durationMs":30000}]}`, id)
		case r.URL.Path == "/search":
			t.Error("manual candidate discovery must not search Yandex")
		case strings.HasSuffix(r.URL.Path, "/download-info"):
			body = `{"result":[{"codec":"mp3","bitrateInKbps":320,"downloadInfoUrl":"https://storage.mds.yandex.net/descriptor"}]}`
		case r.URL.Path == "/descriptor":
			if r.Header.Get("Authorization") != "" {
				t.Error("provider secret leaked")
			}
			body = fmt.Sprintf(`<download-info><host>storage.mds.yandex.net</host><path>/full.mp3</path><ts>%d</ts><s>salt</s></download-info>`, descriptors.Add(1))
		default:
			t.Errorf("unexpected provider path %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	s.yandex = provider
	rt, err := s.currentCensorRuntime(context.Background(), s.session.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	b := rt.backend.(*nativeBackend)
	b.archiveHTTP, _ = archiveFixtureClient(t)
	nativeDirectSiteFixture(t, b.nativeSource)
	calls := &atomic.Int64{}
	queue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(403) }))
	t.Cleanup(queue.Close)
	b.source, _ = NewClient(queue.URL, "sb_publishable_test")
	return s, b, processor, calls
}

func TestNativeUnavailableManualTrackDiscoversWithoutOriginalAudio(t *testing.T) {
	s, b, p, calls := nativeManualFixture(t)
	p.waveform = []censor.WaveformPoint{{}}
	s.yandex.http.Transport = nativeAudioTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/tracks/42" {
			t.Errorf("unavailable target attempted audio resolution: %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"result":[{"id":42,"title":"real title","artists":[{"name":"real artist"},{"name":"second artist"}],"available":false}]}`)), Request: r}, nil
	})
	controlAPI(t, s, map[string]any{"action": "check_track", "track_id": 42}, 200)
	job := waitCensor(t, s, "review_required")
	if !job.Request.OriginalUnavailable || job.Request.OriginalURL != "" || job.Request.Title != "real title" || job.Request.Artist != "real artist, second artist" || job.Result == nil || len(job.Result.Waveform) != 1 || job.Result.DurationSeconds != 30 || job.Result.Comparison != nil || job.Error != "" {
		t.Fatalf("invalid metadata-only review: %+v", job)
	}
	if len(job.Candidates) == 0 || len(job.Queries) != 1 || job.Queries[0] != "real artist, second artist real title" {
		t.Fatalf("discovery not populated: %+v", job)
	}
	for _, c := range job.Candidates {
		if c.Result != nil || !strings.Contains(c.Error, "original Yandex audio is unavailable") {
			t.Fatalf("candidate has fabricated evidence: %+v", c)
		}
	}
	p.mu.Lock()
	analyzed := len(p.requests)
	for _, request := range p.requests {
		if request.Reference != nil || request.Source.URL == "" {
			t.Errorf("waveform request must use candidate audio without reference: %+v", request)
		}
	}
	p.mu.Unlock()
	if analyzed != 1 || calls.Load() != 0 {
		t.Fatal("expected one waveform analysis and no public queue calls")
	}
	if err := b.runtime.compareCandidates(context.Background(), nil, &job); !errors.Is(err, ErrCensorConflict) {
		t.Fatalf("comparison allowed without original: %v", err)
	}
	controlAPI(t, s, map[string]any{"action": "approve", "id": job.ID, "candidate_id": job.Candidates[0].ID}, 409)
	// Audio consumers must still reject unavailable provider tracks.
	if _, err := s.yandex.nativeTrack(context.Background(), 42); err == nil {
		t.Fatal("normal audio resolver accepted unavailable track")
	}
}

func TestNativeCachedUnavailableManualTrackSkipsYandex(t *testing.T) {
	s, b, p, calls := nativeManualFixture(t)
	p.waveform = []censor.WaveformPoint{{}}
	unavailable := false
	s.mu.Lock()
	s.state.Tracks = []Result{{Track: Track{ID: 42, Title: "real title", Artist: "real artist", Available: &unavailable}}}
	s.mu.Unlock()
	var providerCalls atomic.Int64
	s.yandex.http.Transport = nativeAudioTransport(func(r *http.Request) (*http.Response, error) {
		providerCalls.Add(1)
		return nil, errors.New("Yandex network request failed")
	})
	controlAPI(t, s, map[string]any{"action": "check_track", "track_id": 42}, 200)
	job := waitCensor(t, s, "review_required")
	if job.Request.ID != "manual:yandex:42" || job.Request.Origin != "manual" || !job.Request.OriginalUnavailable || job.Request.OriginalURL != "" || job.Request.Title != "real title" || job.Request.Artist != "real artist" || job.Result == nil || len(job.Result.Waveform) != 1 || job.Result.DurationSeconds != 30 || job.Result.Comparison != nil || job.Error != "" {
		t.Fatalf("invalid cached metadata-only review: %+v", job)
	}
	if len(job.Candidates) == 0 || len(job.Queries) != 1 || job.Queries[0] != "real artist real title" {
		t.Fatalf("discovery not populated: %+v", job)
	}
	for _, c := range job.Candidates {
		if c.Result != nil || !strings.Contains(c.Error, "original Yandex audio is unavailable") {
			t.Fatalf("candidate has fabricated evidence: %+v", c)
		}
	}
	if err := b.runtime.compareCandidates(context.Background(), nil, &job); !errors.Is(err, ErrCensorConflict) {
		t.Fatalf("comparison allowed without original: %v", err)
	}
	controlAPI(t, s, map[string]any{"action": "approve", "id": job.ID, "candidate_id": job.Candidates[0].ID}, 409)
	p.mu.Lock()
	analyzed := len(p.requests)
	for _, request := range p.requests {
		if request.Reference != nil || request.Source.URL == "" {
			t.Errorf("waveform request must use candidate audio without reference: %+v", request)
		}
	}
	p.mu.Unlock()
	if providerCalls.Load() != 0 || calls.Load() != 0 || analyzed != 1 {
		t.Fatalf("metadata-only discovery made provider/public queue/audio calls: %d/%d/%d", providerCalls.Load(), calls.Load(), analyzed)
	}
}

func TestNativeManualCachedMetadataRequiresAuthorizedUnavailableTrack(t *testing.T) {
	for _, name := range []string{"available", "unknown", "missing title", "missing artist", "different track", "no access", "different state account", "job metadata only"} {
		t.Run(name, func(t *testing.T) {
			s, b, _, _ := nativeManualFixture(t)
			unavailable := false
			track := Track{ID: 42, Title: "cached title", Artist: "cached artist", Available: &unavailable}
			s.mu.Lock()
			s.state.HasAccess = true
			u := s.session.User
			s.state.User = &u
			switch name {
			case "available":
				available := true
				track.Available = &available
			case "unknown":
				track.Available = nil
			case "missing title":
				track.Title = " "
			case "missing artist":
				track.Artist = " "
			case "different track":
				track.ID = 99
			case "no access":
				s.state.HasAccess = false
			case "different state account":
				s.state.User = &User{ID: "other-user"}
			}
			if name != "job metadata only" {
				s.state.Tracks = []Result{{Track: track}}
			}
			s.mu.Unlock()
			b.runtime.mu.Lock()
			b.runtime.disk.Jobs = []CensorJob{{Request: CensorRequest{ID: "manual:yandex:42", TrackID: 42, Origin: "manual", Title: "untrusted title", Artist: "untrusted artist", OriginalUnavailable: true}}}
			b.runtime.mu.Unlock()
			r, err := b.Request(context.Background(), CensorIdentity{UserID: b.runtime.uid}, "manual:yandex:42")
			if err != nil || r.OriginalUnavailable || r.Title != "real title" || r.Artist != "real artist" || r.OriginalURL == "" {
				t.Fatalf("did not resolve fresh provider audio: %+v, %v", r, err)
			}
		})
	}
}

func TestNativeCheckTrackWithoutPublicQueueAndFreshReanalysis(t *testing.T) {
	s, b, p, calls := nativeManualFixture(t)
	// Queue RLS failures must not disable the explicit user-initiated path.
	b.runtime.mu.Lock()
	b.runtime.sourceUnavailable = true
	b.runtime.mu.Unlock()
	p.block = make(chan struct{})
	p.entered = make(chan struct{})
	controlAPI(t, s, map[string]any{"action": "check_track", "track_id": 42}, 200)
	<-p.entered
	controlAPI(t, s, map[string]any{"action": "check_track", "track_id": 42}, 200)
	if len(b.runtime.snapshot().Jobs) != 1 {
		t.Fatal("in-flight replay duplicated job")
	}
	close(p.block)
	job := waitCensor(t, s, "review_required")
	if job.Request.ID != "manual:yandex:42" || job.Request.Origin != "manual" || job.Request.TrackID != 42 || job.Request.Title != "real title" || job.Request.Artist != "real artist" || !censorHTTPS(job.Request.OriginalURL) || len(job.Candidates) != 1 || job.Candidates[0].Provider != "direct_web" || job.Candidates[0].FullAudio || job.Candidates[0].Result != nil {
		t.Fatal("manual analysis missing real provider evidence", job)
	}
	controlAPI(t, s, map[string]any{"action": "check_track", "track_id": 42}, 200)
	reanalyzed := waitCensor(t, s, "review_required")
	if len(b.runtime.snapshot().Jobs) != 1 || reanalyzed.ID != job.ID {
		t.Fatal("reanalysis duplicated the manual job")
	}
	p.mu.Lock()
	analysisCalls := len(p.requests)
	p.mu.Unlock()
	if analysisCalls != 2 {
		t.Fatal("Analyze again reused persisted analysis", analysisCalls)
	}
	job = reanalyzed
	if calls.Load() != 0 {
		t.Fatal("manual analysis contacted Supabase", calls.Load())
	}
	reopened, err := openCensorRuntime(s, b.runtime.uid, &nativeBackend{nativeSource: NativeSources(s)})
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.disk.Jobs) != 1 || reopened.disk.Jobs[0].Request.Origin != "manual" {
		t.Fatal("manual origin not durable")
	}
	// An interrupted manual analysis can restart without allocating another job.
	interrupted := job
	interrupted.Status = "pending"
	if err := b.runtime.update(interrupted); err != nil {
		t.Fatal(err)
	}
	controlAPI(t, s, map[string]any{"action": "check_track", "track_id": 42}, 200)
	job = waitCensor(t, s, "review_required")
	if job.ID != interrupted.ID || len(b.runtime.snapshot().Jobs) != 1 {
		t.Fatal("restart duplicated manual job")
	}
	controlAPI(t, s, map[string]any{"action": "approve", "id": job.ID, "candidate_id": job.Candidates[0].ID}, 409)
	// A previously stored Archive candidate still passes fresh comparison and
	// selection gates, independently of the current web discovery default.
	job = nativeLegacyArchiveReviewFixture(t, b, job)
	fixture, _, _, _, _, _ := nativePublisherFixture(t)
	controlAPI(t, s, map[string]any{"action": "native_configure", "repository_path": fixture.repo}, 200)
	publisher := b.publisher.(*nativePublisher)
	publisher.validate = func(context.Context, string) error { return nil }
	publisher.client.Transport = nativeAudioTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("full-mp3")), Request: r}, nil
	})
	controlAPI(t, s, map[string]any{"action": "approve", "id": job.ID, "candidate_id": job.Candidates[0].ID}, 200)
	saved := waitCensor(t, s, "local_saved")
	if !saved.Replacement.LocalOnly || saved.Replacement.RequestCompleted || saved.Replacement.GitHubPublished || saved.Request.Origin != "manual" {
		t.Fatal("manual publication claimed public completion", saved)
	}
	if calls.Load() != 0 {
		t.Fatal("manual approval contacted Supabase")
	}
	controlAPI(t, s, map[string]any{"action": "check_track", "track_id": 42}, 200)
	if b.runtime.snapshot().Jobs[0].Status != "local_saved" {
		t.Fatal("replay reset local publication")
	}
}

func TestNativeManualResolverBoundToAccountAndExistingJob(t *testing.T) {
	s, b, _, calls := nativeManualFixture(t)
	identity := CensorIdentity{UserID: b.runtime.uid}
	for _, id := range []string{"manual:yandex:42", "manual:yandex:042", "manual:yandex:0"} {
		if _, err := b.Request(context.Background(), identity, id); err == nil {
			t.Fatal("unregistered manual identity resolved", id)
		}
	}
	controlAPI(t, s, map[string]any{"action": "check_track", "track_id": 42}, 200)
	waitCensor(t, s, "review_required")
	if _, err := b.Request(context.Background(), CensorIdentity{UserID: "different-account"}, "manual:yandex:42"); !errors.Is(err, ErrCensorDenied) {
		t.Fatal("foreign account resolved manual job", err)
	}
	if err := b.checkTrack(context.Background(), CensorIdentity{UserID: "different-account"}, 42); !errors.Is(err, ErrCensorDenied) {
		t.Fatal("foreign account replay accepted", err)
	}
	other := censorServer(t)
	other.session.User.ID = "other-user"
	other.yandex = s.yandex
	otherRuntime, err := other.currentCensorRuntime(context.Background(), other.session.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close(context.Background())
	if _, err = otherRuntime.backend.(*nativeBackend).Request(context.Background(), CensorIdentity{UserID: otherRuntime.uid}, "manual:yandex:42"); !errors.Is(err, ErrCensorNotFound) {
		t.Fatal("manual ID bypassed another runtime job association", err)
	}
	b.runtime.mu.Lock()
	b.runtime.disk.Jobs[0].Request.Origin = "reported_tracks"
	b.runtime.mu.Unlock()
	if _, err = b.Request(context.Background(), identity, "manual:yandex:42"); !errors.Is(err, ErrCensorNotFound) {
		t.Fatal("unlabeled job bypassed membership", err)
	}
	if calls.Load() != 0 {
		t.Fatal("manual identity checks contacted Supabase")
	}
	if _, err = b.Request(context.Background(), identity, "request:yandex:42"); err == nil || calls.Load() != 1 {
		t.Fatal("public request skipped real source membership", err)
	}
	s.mu.Lock()
	s.censorRuntime = nil
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.censorRuntime = b.runtime; s.mu.Unlock() }()
	if _, err = b.Request(context.Background(), identity, "manual:yandex:42"); !errors.Is(err, ErrCensorDenied) {
		t.Fatal("detached runtime still resolved manual job", err)
	}
}

func TestNativeManualCommandAdmissionErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		running bool
		active  bool
		failure string
	}{
		{name: "queue running", running: true},
		{name: "command active", active: true},
		{name: "storage unavailable", failure: "Censor storage is unavailable or owned by another process"},
		{name: "persistence failed", failure: "Unable to persist censor state; processing is blocked"},
		{name: "engine queue failed", failure: "Persistent engine queue failed; processing is blocked"},
		{name: "failure takes precedence", active: true, failure: "Unable to persist censor state; processing is blocked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, b, _, calls := nativeManualFixture(t)
			rt := b.runtime
			rt.mu.Lock()
			rt.running, rt.commandActive, rt.failure = tc.running, tc.active, tc.failure
			beforeVersion := rt.stateVersion
			rt.mu.Unlock()
			defer func() {
				rt.mu.Lock()
				rt.running, rt.commandActive, rt.failure = false, false, ""
				rt.mu.Unlock()
			}()
			wantErr, wantStatus, wantMessage := ErrCensorBusy, http.StatusConflict, ErrCensorBusy.Error()
			if tc.failure != "" {
				wantErr, wantStatus, wantMessage = ErrCensorUnavailable, http.StatusServiceUnavailable, tc.failure
			}
			assertError := func(err error) {
				t.Helper()
				if !errors.Is(err, wantErr) || errors.Is(err, ErrCensorConflict) || err.Error() != wantMessage {
					t.Fatalf("admission error = %v; want %v / %q", err, wantErr, wantMessage)
				}
			}
			assertError(b.checkTrack(context.Background(), CensorIdentity{UserID: rt.uid}, 42))
			w := api(s, "POST", "/api/censor/control", `{"action":"check_track","track_id":42}`, "bridge-key")
			if w.Code != wantStatus || !strings.Contains(w.Body.String(), wantMessage) || strings.Contains(w.Body.String(), "selection is stale") {
				t.Fatalf("response = %d %s; want %d / %q", w.Code, w.Body, wantStatus, wantMessage)
			}
			// startCommand has its own admission gate after checkTrack persists.
			if tc.active || tc.failure != "" {
				assertError(rt.startCommand("unused", func(context.Context, CensorIdentity) error {
					t.Error("blocked work started")
					return nil
				}))
			}
			rt.mu.Lock()
			unchanged := len(rt.disk.Jobs) == 0 && rt.stateVersion == beforeVersion && rt.commandDone == nil && rt.failure == tc.failure
			rt.mu.Unlock()
			if !unchanged || calls.Load() != 0 {
				t.Fatal("blocked command mutated state or contacted public source")
			}
		})
	}
}

func TestNativeCheckTrackValidationAndBackend(t *testing.T) {
	s, _, _, calls := nativeManualFixture(t)
	for _, body := range []string{`{"action":"check_track"}`, `{"action":"check_track","track_id":0}`, `{"action":"check_track","track_id":-42}`, `{"action":"check_track","track_id":42,"url":"https://evil.example/audio"}`, `{"action":"check_track","track_id":42,"request_id":"request:yandex:42"}`, `{"action":"check_track","track_id":42,"query":"other"}`, `{"action":"check_track","track_id":42,"repository_path":"/repo"}`} {
		w := api(s, "POST", "/api/censor/control", body, "bridge-key")
		if w.Code != 400 {
			t.Fatal("invalid manual control accepted", w.Code, w.Body)
		}
	}
	if len(s.censorRuntime.snapshot().Jobs) != 0 || calls.Load() != 0 {
		t.Fatal("invalid control reached backend")
	}
	remote, _, _ := integrationCensor(t)
	controlAPI(t, remote, map[string]any{"action": "check_track", "track_id": 42}, 501)
}
