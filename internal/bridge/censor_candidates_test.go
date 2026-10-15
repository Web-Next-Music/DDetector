package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type reviewCensorBackend struct {
	manyCensorBackend
	candidates []CensorCandidate
}

func (b *reviewCensorBackend) Search(context.Context, CensorIdentity, string) ([]CensorCandidate, error) {
	return b.candidates, nil
}
func mixedCensorCandidates() []CensorCandidate {
	return []CensorCandidate{
		{ID: "page", URL: "https://provider.example/track/1", FullAudio: false, Downloadable: false, Metadata: map[string]string{"kind": "page"}},
		{ID: "preview", URL: "https://audio.example/preview.mp3", FullAudio: false, Downloadable: true},
		{ID: "stream", URL: "https://audio.example/stream", FullAudio: true, Downloadable: false},
		{ID: "mislabelled-page", URL: "https://provider.example/page", FullAudio: true, Downloadable: true, Metadata: map[string]string{"kind": "page", "content_type": "text/html"}},
		{ID: "full", URL: "https://audio.example/full.mp3", FullAudio: true, Downloadable: true},
	}
}
func TestCensorMixedCandidatesPreservedAndNeverAnalyzedOrApproved(t *testing.T) {
	s := censorServer(t)
	backend := &reviewCensorBackend{manyCensorBackend: manyCensorBackend{count: 1}, candidates: mixedCensorCandidates()}
	_ = s.SetCensorBackend(backend)
	p := &orchestrationProcessor{}
	s.censorProcessor = p
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})
	j := fetchStart(t, s)
	if len(j.Candidates) != 5 {
		t.Fatal("review candidates dropped", j.Candidates)
	}
	for _, candidate := range j.Candidates {
		if candidate.ID == "full" {
			if candidate.Result == nil || !strongCensorComparison(candidate.Result) {
				t.Fatal(candidate)
			}
			continue
		}
		if candidate.Result != nil || candidate.Error == "" {
			t.Fatal("non-audio candidate was compared or lacks explanation", candidate)
		}
		controlAPI(t, s, map[string]any{"action": "approve", "id": j.ID, "candidate_id": candidate.ID}, 409)
	}
	p.mu.Lock()
	calls := len(p.requests)
	p.mu.Unlock()
	if calls != 2 {
		t.Fatal("pages/previews/streams reached engine", calls)
	}
	state := s.censorRuntime.snapshot()
	if state.Stats["suitable_mp3"] != 1 {
		t.Fatal(state.Stats)
	}
	controlAPI(t, s, map[string]any{"action": "settings", "settings": map[string]any{"min_confidence": .95}}, 200)
	state = s.censorRuntime.snapshot()
	if state.Stats["suitable_mp3"] != 0 {
		t.Fatal("threshold not applied to stats", state.Stats)
	}
	controlAPI(t, s, map[string]any{"action": "approve", "id": j.ID, "candidate_id": "full"}, 409)
	controlAPI(t, s, map[string]any{"action": "settings", "settings": map[string]any{"candidate_limit": 2}}, 200)
	controlAPI(t, s, map[string]any{"action": "search", "id": j.ID, "query": "manual"}, 200)
	j = waitCensor(t, s, "review_required")
	if len(j.Candidates) != 2 || j.Candidates[0].ID != "page" || j.Candidates[1].ID != "preview" {
		t.Fatal("candidate limit did not retain review entries", j.Candidates)
	}
	p.mu.Lock()
	calls = len(p.requests)
	p.mu.Unlock()
	if calls != 2 {
		t.Fatal("manual search analyzed pages", calls)
	}
}
func TestCensorRemoteAcceptsBoundedReviewCandidates(t *testing.T) {
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"candidates": mixedCensorCandidates()})
	}))
	defer remote.Close()
	b, e := NewCensorRemoteBackend(remote.URL)
	if e != nil {
		t.Fatal(e)
	}
	b.client = remote.Client()
	candidates, e := b.Search(context.Background(), CensorIdentity{UserID: "u", AccessToken: "jwt"}, "query")
	if e != nil || len(candidates) != 5 || candidates[0].Metadata["kind"] != "page" {
		t.Fatal(candidates, e)
	}
	for _, url := range []string{"file:///etc/passwd", "javascript:alert(1)", "https://user:secret@provider.example/page"} {
		if e = validateCensorCandidates([]CensorCandidate{{ID: "page", URL: url}}); e == nil {
			t.Fatal("unsafe review URL accepted", url)
		}
	}
}
func TestCensorStatsCountJobsNotMultipleCandidates(t *testing.T) {
	settings := defaultCensorSettings()
	result := strongFixture()
	rt := &censorRuntime{disk: censorDiskState{Settings: settings, Jobs: []CensorJob{
		{ID: "found", Status: "review_required", Result: &result, Candidates: []CensorCandidate{{ID: "c1", URL: "https://audio.example/a.mp3", FullAudio: true, Downloadable: true, Result: &result}, {ID: "c2", URL: "https://audio.example/b.mp3", FullAudio: true, Downloadable: true, Result: &result}}},
		{ID: "page-only", Status: "review_required", Candidates: []CensorCandidate{{ID: "page", URL: "https://provider.example/page", Result: &result}}},
		{ID: "unanalysed", Status: "pending"},
	}}}
	// Untrusted cached page evidence must not contribute to either derived statistic.
	state := rt.snapshot()
	if state.Stats["total"] != 3 || state.Stats["suitable_mp3"] != 1 {
		t.Fatal(state.Stats)
	}
	if _, exists := state.Stats["censorship_found"]; exists {
		t.Fatal("obsolete censorship statistic exposed", state.Stats)
	}
}
func TestCensorCandidateLimitAppliesToGeneratedVariants(t *testing.T) {
	s := censorServer(t)
	b := &reviewCensorBackend{manyCensorBackend: manyCensorBackend{count: 1}}
	for n := 0; n < 10; n++ {
		b.candidates = append(b.candidates, CensorCandidate{ID: fmt.Sprintf("c%d", n), URL: fmt.Sprintf("https://provider.example/page/%d", n)})
	}
	_ = s.SetCensorBackend(b)
	s.censorProcessor = &orchestrationProcessor{}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})
	controlAPI(t, s, map[string]any{"action": "settings", "settings": map[string]any{"candidate_limit": 3}}, 200)
	j := fetchStart(t, s)
	if len(j.Candidates) != 3 {
		t.Fatal(len(j.Candidates))
	}
}

func TestCensorApprovalRevalidatesFreshCandidateAudioFlags(t *testing.T) {
	s := censorServer(t)
	b := &reviewCensorBackend{manyCensorBackend: manyCensorBackend{count: 1}, candidates: []CensorCandidate{{ID: "full", URL: "https://audio.example/full.mp3", FullAudio: true, Downloadable: true}}}
	_ = s.SetCensorBackend(b)
	p := &orchestrationProcessor{}
	s.censorProcessor = p
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})
	j := fetchStart(t, s)
	b.candidates[0].Downloadable = false
	controlAPI(t, s, map[string]any{"action": "approve", "id": j.ID, "candidate_id": "full"}, 200)
	failed := waitCensor(t, s, "review_required")
	if failed.Error == "" {
		t.Fatal("downgraded candidate was not rejected")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) != 2 {
		t.Fatal("non-downloadable candidate reached approval audio recheck")
	}
}
