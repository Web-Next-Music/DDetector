package bridge

import (
	"context"
	"ddetector-go/internal/censor"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestCensorManualSearchAndAnalyzeAreOrchestrated(t *testing.T) {
	s, _, p := integrationCensor(t)
	j := fetchStart(t, s)
	controlAPI(t, s, map[string]any{"action": "search", "id": j.ID, "request_id": "request-1", "track_id": 42, "query": "manual query"}, 200)
	j = waitCensor(t, s, "review_required")
	if len(j.Queries) != 1 || j.Queries[0] != "manual query" || len(j.Candidates) != 1 || !strongCensorComparison(j.Candidates[0].Result) {
		t.Fatal(j)
	}
	p.mu.Lock()
	calls := len(p.requests)
	p.mu.Unlock()
	if calls != 3 {
		t.Fatal("manual search skipped comparison", calls)
	}
	controlAPI(t, s, map[string]any{"action": "analyze", "id": j.ID}, 200)
	waitCensor(t, s, "review_required")
}
func TestCensorApproveReturnsPromptlyAndLogoutCancelsRecheck(t *testing.T) {
	s, f, p := integrationCensor(t)
	j := fetchStart(t, s)
	p.block = make(chan struct{})
	p.entered = make(chan struct{})
	before := time.Now()
	controlAPI(t, s, map[string]any{"action": "approve", "id": j.ID, "candidate_id": "candidate-1"}, 200)
	if time.Since(before) > time.Second {
		t.Fatal("approval blocked HTTP")
	}
	select {
	case <-p.entered:
	case <-time.After(time.Second):
		t.Fatal("recheck not started")
	}
	w := api(s, "GET", "/api/censor/state", "", "bridge-key")
	var state CensorState
	_ = json.Unmarshal(w.Body.Bytes(), &state)
	if w.Code != 200 || !state.Busy {
		t.Fatal("cannot poll ongoing approval", w.Code, w.Body)
	}
	w = api(s, "POST", "/api/logout", "{}", "bridge-key")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mutations != 0 {
		t.Fatal("published despite cancelled recheck")
	}
}

type manyCensorBackend struct{ count int }

func (b *manyCensorBackend) Requests(context.Context, CensorIdentity) ([]CensorRequest, error) {
	out := []CensorRequest{}
	for n := 0; n < b.count; n++ {
		out = append(out, CensorRequest{ID: fmt.Sprintf("request-%d", n), TrackID: int64(n + 1), Title: "Title", Artist: "Artist", OriginalURL: "https://audio.example/original.mp3"})
	}
	return out, nil
}
func (*manyCensorBackend) Search(context.Context, CensorIdentity, string) ([]CensorCandidate, error) {
	return []CensorCandidate{{ID: "c", URL: "https://audio.example/full.mp3", FullAudio: true, Downloadable: true}}, nil
}
func (*manyCensorBackend) Replace(context.Context, CensorIdentity, CensorSelection) (CensorReplacement, error) {
	return CensorReplacement{}, errors.New("must not publish automatically")
}

type concurrencyProcessor struct {
	mu          sync.Mutex
	active, max int
	entered     chan struct{}
	release     chan struct{}
}

func (p *concurrencyProcessor) Analyze(ctx context.Context, r censor.Request) (censor.Result, error) {
	p.mu.Lock()
	p.active++
	if p.active > p.max {
		p.max = p.active
	}
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.active--; p.mu.Unlock() }()
	select {
	case p.entered <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return censor.Result{}, ctx.Err()
	case <-p.release:
	}
	result := strongFixture()
	if r.Reference == nil {
		result.Comparison = nil
	}
	return result, nil
}
func TestCensorWorkerSettingControlsRealQueueConcurrency(t *testing.T) {
	s := censorServer(t)
	_ = s.SetCensorBackend(&manyCensorBackend{count: 8})
	p := &concurrencyProcessor{entered: make(chan struct{}, 16), release: make(chan struct{})}
	s.censorProcessor = p
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})
	controlAPI(t, s, map[string]any{"action": "settings", "settings": map[string]any{"workers": 3}}, 200)
	controlAPI(t, s, map[string]any{"action": "fetch"}, 200)
	controlAPI(t, s, map[string]any{"action": "start"}, 200)
	for n := 0; n < 3; n++ {
		select {
		case <-p.entered:
		case <-time.After(time.Second):
			t.Fatal("workers not applied")
		}
	}
	controlAPI(t, s, map[string]any{"action": "settings", "settings": map[string]any{"workers": 1}}, 409)
	state := controlAPI(t, s, map[string]any{"action": "pause"}, 200)
	if state.Running || !state.Paused {
		t.Fatal(state)
	}
	p.mu.Lock()
	max := p.max
	active := p.active
	p.mu.Unlock()
	if max != 3 || active != 0 {
		t.Fatal("worker count or cancellation", max, active)
	}
	close(p.release)
	controlAPI(t, s, map[string]any{"action": "resume"}, 200)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state = s.censorRuntime.snapshot()
		if !state.Running && state.Stats["review_required"] == 8 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("concurrent jobs did not finish", state)
}
func TestCensorAbsentRemoteSourceIsNotCatalogOrQueue(t *testing.T) {
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer remote.Close()
	b, e := NewCensorRemoteBackend(remote.URL)
	if e != nil {
		t.Fatal(e)
	}
	b.client = remote.Client()
	s := censorServer(t)
	_ = s.SetCensorBackend(b)
	controlAPI(t, s, map[string]any{"action": "fetch"}, 503)
	w := api(s, "GET", "/api/censor/state", "", "bridge-key")
	var state CensorState
	_ = json.Unmarshal(w.Body.Bytes(), &state)
	if w.Code != 200 || state.Configured || len(state.Jobs) != 0 || state.Error == "" {
		t.Fatal(w.Code, w.Body)
	}
}
func TestCensorOptionalRollbackIsExplicit(t *testing.T) {
	s := censorServer(t)
	_ = s.SetCensorBackend(&manyCensorBackend{count: 1})
	s.censorProcessor = &orchestrationProcessor{}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})
	j := fetchStart(t, s)
	controlAPI(t, s, map[string]any{"action": "rollback", "id": j.ID}, 501)
}
func TestCensorStorageOwnershipAndUnsafeRecoveredSources(t *testing.T) {
	s := censorServer(t)
	b := &manyCensorBackend{count: 1}
	_ = s.SetCensorBackend(b)
	controlAPI(t, s, map[string]any{"action": "fetch"}, 200)
	rt := s.censorRuntime
	other, e := openCensorRuntime(s, "user-1", b)
	if e != nil {
		t.Fatal(e)
	}
	if e = other.fetch(context.Background(), CensorIdentity{UserID: "user-1", AccessToken: "secret-access"}); e == nil {
		t.Fatal("second runtime overwrote owned storage")
	}
	wrapped := &censorQueueProcessor{runtime: rt, parent: context.Background(), engine: &orchestrationProcessor{}}
	for _, r := range []censor.Request{
		{Source: censor.Source{Path: "/etc/passwd"}},
		{Source: censor.Source{URL: "https://audio.example/unrelated.mp3"}},
		{Source: censor.Source{URL: "https://audio.example/original.mp3"}, Reference: &censor.Source{URL: "https://audio.example/unrelated.mp3"}},
	} {
		if _, e := wrapped.Analyze(context.Background(), r); e == nil {
			t.Fatal("unprovenanced recovered job accepted", r)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e := s.Close(ctx); e != nil {
		t.Fatal(e)
	}
}
func TestCensorComparisonRequiresFullRestorationEvidence(t *testing.T) {
	for _, mode := range []string{"preview", "no_restoration", "weak", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			result := strongFixture()
			switch mode {
			case "preview":
				result.Comparison.OverlapSeconds = 10
			case "no_restoration":
				result.SuspiciousIntervals = nil
			case "weak":
				result.Comparison.ConfidenceClass = "possible_match"
			case "truncated":
				result.IntervalsTruncated = true
			}
			if strongCensorComparison(&result) {
				t.Fatal("unsafe comparison passed")
			}
		})
	}
}
