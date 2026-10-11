package bridge

import (
	"context"
	"ddetector-go/internal/censor"
	"ddetector-go/internal/censorsearch"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type injectedMetadata struct {
	mu      sync.Mutex
	queries []censorsearch.Query
	page    censorsearch.Page
	err     error
	wait    bool
}

func (p *injectedMetadata) Search(ctx context.Context, q censorsearch.Query) (censorsearch.Page, error) {
	p.mu.Lock()
	p.queries = append(p.queries, q)
	p.mu.Unlock()
	if p.wait {
		<-ctx.Done()
		return censorsearch.Page{}, ctx.Err()
	}
	return p.page, p.err
}

type metadataAudioBackend struct {
	manyCensorBackend
	queries     []string
	candidates  []CensorCandidate
	failVariant bool
}

func (b *metadataAudioBackend) Search(_ context.Context, _ CensorIdentity, q string) ([]CensorCandidate, error) {
	b.queries = append(b.queries, q)
	if b.failVariant && q == "ISRC123" {
		return nil, ErrCensorUnavailable
	}
	return b.candidates, nil
}
func metadataFixture() censorsearch.Candidate {
	return censorsearch.Candidate{ID: "42", Title: "Песня", Artist: "Credited", DurationMS: 30000, ISRCs: []string{"ISRC123"}, FullAudio: true, Downloadable: true, URL: "https://evil.example/audio.mp3", ArtistCredits: []censorsearch.ArtistCredit{{Name: "Canonical", CreditedName: "Credited", Aliases: []string{"Алиас", "Alias 2", "Alias 3", "Alias 4", "Alias 5", "Alias 6", "Alias 7"}}}, Releases: []censorsearch.Release{{Title: "Edition", AlbumTitle: "Album"}}}
}
func metadataRuntime(p censorsearch.Provider, b CensorBackend) *censorRuntime {
	return &censorRuntime{metadataProvider: p, backend: b, disk: censorDiskState{Settings: defaultCensorSettings()}}
}

func TestCensorMetadataSeparateLimitAndEnrichment(t *testing.T) {
	p := &injectedMetadata{}
	for n := 0; n < 15; n++ {
		c := metadataFixture()
		c.ID = fmt.Sprintf("%d", n+1)
		p.page.Candidates = append(p.page.Candidates, c)
	}
	b := &metadataAudioBackend{}
	for n := 0; n < 100; n++ {
		b.candidates = append(b.candidates, CensorCandidate{ID: fmt.Sprintf("audio-%d", n), URL: "https://audio.example/full.mp3", FullAudio: true, Downloadable: true})
	}
	rt := metadataRuntime(p, b)
	rt.disk.Settings.CandidateLimit = 100
	j := CensorJob{Request: CensorRequest{Artist: "Original artist", Title: "Original title"}, Result: &censor.Result{DurationSeconds: 30.125}}
	base := censorQueries(j.Request)
	if err := rt.search(context.Background(), CensorIdentity{}, &j, base); err != nil {
		t.Fatal(err)
	}
	if len(j.Candidates) != 110 || !reflect.DeepEqual(j.Queries, base) {
		t.Fatalf("candidates=%d queries=%v", len(j.Candidates), j.Queries)
	}
	if err := validateCensorJobCandidates(j.Candidates); err != nil {
		t.Fatal("separate persisted allowance", err)
	}
	for n, c := range j.Candidates {
		if n < 100 {
			if !censorAudioCandidate(c) {
				t.Fatal("audio evicted", c)
			}
			continue
		}
		if c.FullAudio || c.Downloadable || c.Result != nil || censorAudioCandidate(c) || !strings.HasPrefix(c.ID, "yandex-metadata:") || !strings.HasPrefix(c.URL, "https://music.yandex.ru/track/") || c.Error == "" {
			t.Fatal("metadata gained audio capabilities", c)
		}
		if c.Metadata["isrcs"] != "ISRC123" || !strings.Contains(c.Metadata["artist_credits"], "Алиас") || !strings.Contains(c.Metadata["albums"], "Album") {
			t.Fatal(c.Metadata)
		}
		if rt.validateApproval(CensorJob{Status: "review_required", Candidates: []CensorCandidate{c}}, c.ID) == nil {
			t.Fatal("metadata approved")
		}
	}
	if got := p.queries[0]; got.Artist != "Original artist" || got.Title != "Original title" || got.Duration != 30125*time.Millisecond || got.Limit != 10 {
		t.Fatal(got)
	}
	if !reflect.DeepEqual(b.queries, base) {
		t.Fatal("search must remain strict to the requested artist and title", b.queries)
	}
	if err := rt.search(context.Background(), CensorIdentity{}, &j, j.Queries); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(j.Queries, base) {
		t.Fatal("recheck changed strict query", j.Queries)
	}
	poisoned := append([]CensorCandidate(nil), j.Candidates...)
	poisoned[100].FullAudio = true
	if validateCensorJobCandidates(poisoned) == nil {
		t.Fatal("persisted metadata flags trusted")
	}
}

func TestCensorMetadataUnavailableDoesNotAbortAudio(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider censorsearch.Provider
		warning  string
	}{
		{"missing", nil, censorMetadataMissing},
		{"failure", &injectedMetadata{err: errors.New("private backend details")}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &metadataAudioBackend{candidates: []CensorCandidate{{ID: "audio", URL: "https://audio.example/a.mp3", FullAudio: true, Downloadable: true}}}
			rt := metadataRuntime(tc.provider, b)
			rt.metadataConfigWarning = tc.warning
			j := CensorJob{Request: CensorRequest{Artist: "Artist", Title: "Title"}}
			if err := rt.search(context.Background(), CensorIdentity{}, &j, []string{"manual"}); err != nil {
				t.Fatal(err)
			}
			if len(j.Candidates) != 1 || !reflect.DeepEqual(j.Queries, []string{"manual"}) || j.MetadataWarning == "" || strings.Contains(j.MetadataWarning, "private backend") {
				t.Fatal(j)
			}
			state := rt.snapshot()
			if state.MetadataConfigured != (tc.provider != nil) || (tc.provider == nil && state.MetadataWarning == "") || state.Error != "" {
				t.Fatal(state)
			}
		})
	}
	p := &injectedMetadata{page: censorsearch.Page{Candidates: []censorsearch.Candidate{metadataFixture()}}}
	b := &metadataAudioBackend{failVariant: true, candidates: []CensorCandidate{{ID: "audio", URL: "https://audio.example/a.mp3", FullAudio: true, Downloadable: true}}}
	rt := metadataRuntime(p, b)
	j := CensorJob{Request: CensorRequest{Title: "Title"}}
	if err := rt.search(context.Background(), CensorIdentity{}, &j, []string{"manual"}); err != nil || len(j.Candidates) != 2 || j.MetadataWarning != "" {
		t.Fatal(err, j)
	}
}

func TestCensorYandexAuthenticatedMetadata(t *testing.T) {
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "OAuth secret" || r.URL.Path != "/search" || r.URL.Query().Get("type") != "track" || r.URL.Query().Get("text") != "Artist Title" {
			t.Errorf("unexpected metadata request: %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"result":{"tracks":{"results":[{"id":"42","title":"Title","artists":[{"name":"Artist"}],"durationMs":30000,"isrc":"ISRC123","albums":[{"id":"7","title":"Album"}]}]}}}`)
	}))
	defer api.Close()
	s := censorServer(t)
	p := &censorYandexMetadata{server: s}
	rt := metadataRuntime(p, &metadataAudioBackend{})
	if configured, warning := rt.metadataStatus(); configured || warning == "" {
		t.Fatal("missing account not surfaced")
	}
	c := newYandexClient("secret")
	c.base = api.URL
	s.mu.Lock()
	s.yandex = c
	s.mu.Unlock()
	j := CensorJob{Request: CensorRequest{Artist: "Artist", Title: "Title"}}
	candidates, _ := rt.searchMetadata(context.Background(), &j)
	if calls != 1 || len(candidates) != 1 || candidates[0].ID != "yandex-metadata:42" || candidates[0].Metadata["albums"] != "Album" || candidates[0].Metadata["isrcs"] != "ISRC123" || censorAudioCandidate(candidates[0]) || j.MetadataWarning != "" {
		t.Fatal(candidates, j.MetadataWarning, calls)
	}
	s.mu.Lock()
	s.yandex = nil
	s.mu.Unlock()
	if candidates, _ := rt.searchMetadata(context.Background(), &j); len(candidates) != 0 || calls != 1 || j.MetadataWarning == "" {
		t.Fatal("disconnected credential reused")
	}
}

func TestCensorLegacyMetadataSafety(t *testing.T) {
	c := CensorCandidate{ID: "musicbrainz:12345678-1234-1234-1234-123456789abc", Provider: "musicbrainz", URL: "https://musicbrainz.org/recording/12345678-1234-1234-1234-123456789abc", Metadata: map[string]string{"kind": "page"}}
	if err := validateCensorJobCandidates([]CensorCandidate{c}); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"audio", "download", "result", "url", "kind"} {
		poisoned := c
		poisoned.Metadata = map[string]string{"kind": "page"}
		switch field {
		case "audio":
			poisoned.FullAudio = true
		case "download":
			poisoned.Downloadable = true
		case "result":
			poisoned.Result = &censor.Result{}
		case "url":
			poisoned.URL = "https://evil.example/audio"
		case "kind":
			poisoned.Metadata["kind"] = "audio"
		}
		if validateCensorJobCandidates([]CensorCandidate{poisoned}) == nil {
			t.Fatal("legacy metadata trusted", field)
		}
	}
}

func TestCensorMetadataCancellationAndMissingDuration(t *testing.T) {
	for _, seconds := range []float64{0, -1, math.NaN(), math.Inf(1), 86401} {
		if q := censorMetadataQuery(CensorJob{Result: &censor.Result{DurationSeconds: seconds}}); q.Duration != 0 {
			t.Fatal(q)
		}
	}
	if censorMetadataQuery(CensorJob{}).Duration != 0 {
		t.Fatal("invented duration")
	}
	b := &metadataAudioBackend{}
	rt := metadataRuntime(&injectedMetadata{wait: true}, b)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	j := CensorJob{Request: CensorRequest{Title: "Title"}}
	if err := rt.search(ctx, CensorIdentity{}, &j, []string{"manual"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if len(b.queries) != 0 {
		t.Fatal("audio search after cancellation")
	}
}

func TestCensorMetadataOnlyManualSearchNeedsNoAudioEngine(t *testing.T) {
	s := censorServer(t)
	backend := &reviewCensorBackend{manyCensorBackend: manyCensorBackend{count: 1}}
	if err := s.SetCensorBackend(backend); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	state := controlAPI(t, s, map[string]any{"action": "fetch"}, 200)
	rt := s.censorRuntime
	rt.metadataProvider = &injectedMetadata{page: censorsearch.Page{Candidates: []censorsearch.Candidate{metadataFixture()}}}
	rt.metadataConfigWarning = ""
	// Deliberately make engine configuration unusable. Metadata review must not
	// depend on creating an engine when there are no audio candidates.
	controlAPI(t, s, map[string]any{"action": "search", "id": state.Jobs[0].ID, "query": "manual"}, 200)
	j := waitCensor(t, s, "review_required")
	if len(j.Candidates) != 1 || j.Result != nil || j.Error != "" || j.Candidates[0].FullAudio || j.Candidates[0].Downloadable {
		t.Fatal(j)
	}
	controlAPI(t, s, map[string]any{"action": "approve", "id": j.ID, "candidate_id": j.Candidates[0].ID}, 409)
}

func TestCensorMetadataInjectedPipelineAndManualReview(t *testing.T) {
	s, _, processor := integrationCensor(t)
	state := controlAPI(t, s, map[string]any{"action": "fetch"}, 200)
	if state.MetadataConfigured || state.MetadataWarning == "" {
		t.Fatal("missing configuration not exposed", state)
	}
	p := &injectedMetadata{page: censorsearch.Page{Candidates: []censorsearch.Candidate{metadataFixture()}}}
	// Inject before any worker starts, without a public network request.
	rt := s.censorRuntime
	rt.metadataProvider = p
	rt.metadataConfigWarning = ""
	controlAPI(t, s, map[string]any{"action": "start"}, 200)
	j := waitCensor(t, s, "review_required")
	if len(j.Candidates) != 2 || j.Candidates[1].Result != nil || j.MetadataWarning != "" {
		t.Fatal(j)
	}
	processor.mu.Lock()
	calls := len(processor.requests)
	processor.mu.Unlock()
	if calls != 2 {
		t.Fatal("metadata sent to DSP", calls)
	}
	controlAPI(t, s, map[string]any{"action": "approve", "id": j.ID, "candidate_id": j.Candidates[1].ID}, 409)
	controlAPI(t, s, map[string]any{"action": "search", "id": j.ID, "query": "manual"}, 200)
	j = waitCensor(t, s, "review_required")
	if len(j.Candidates) != 2 || j.Queries[0] != "manual" || j.Candidates[1].Result != nil {
		t.Fatal(j)
	}
	processor.mu.Lock()
	calls = len(processor.requests)
	processor.mu.Unlock()
	if calls != 3 {
		t.Fatal("manual metadata sent to DSP", calls)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queries) != 2 || p.queries[0].Duration != 30*time.Second || p.queries[1].Duration != 30*time.Second {
		t.Fatal("original DSP duration lost", p.queries)
	}
}
