package bridge

import (
	"context"
	"ddetector-go/internal/censor"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type orchestrationProcessor struct {
	mu       sync.Mutex
	requests []censor.Request
	weak     bool
	block    chan struct{}
	entered  chan struct{}
	once     sync.Once
}

func strongFixture() censor.Result {
	return censor.Result{DurationSeconds: 30, Comparison: &censor.Comparison{Comparable: true, ConfidenceClass: "strong_match", Confidence: .9, Correlation: .95, SpectralSimilarity: .98, SpectralMatchFraction: .95, OverlapSeconds: 30}, SuspiciousIntervals: []censor.SuspiciousInterval{{StartSeconds: 10, EndSeconds: 10.5, Status: "reference_restoration_candidate", Restoration: &censor.RestorationEvidence{ReferenceStartSeconds: 10, ReferenceEndSeconds: 10.5, Confidence: .8}}}}
}
func (p *orchestrationProcessor) Analyze(ctx context.Context, r censor.Request) (censor.Result, error) {
	p.mu.Lock()
	p.requests = append(p.requests, r)
	weak := p.weak
	p.mu.Unlock()
	if p.entered != nil {
		p.once.Do(func() { close(p.entered) })
	}
	if p.block != nil {
		select {
		case <-ctx.Done():
			return censor.Result{}, ctx.Err()
		case <-p.block:
		}
	}
	result := strongFixture()
	if r.Reference == nil {
		result.Comparison = nil
		result.SuspiciousIntervals = nil
	}
	if weak && result.Comparison != nil {
		result.Comparison.Comparable = false
		result.Comparison.ConfidenceClass = "possible_match"
	}
	return result, nil
}

type remoteFixture struct {
	mu                         sync.Mutex
	queries                    []string
	mutations, rollback, saved int
	keys                       []string
	failFirst                  bool
	original                   string
	candidateURL               string
}

func integrationCensor(t *testing.T) (*Server, *remoteFixture, *orchestrationProcessor) {
	t.Helper()
	fixture := &remoteFixture{original: "https://audio.example/original.mp3", candidateURL: "https://audio.example/full.mp3"}
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-access" || r.Header.Get("apikey") != "" {
			t.Error("remote did not use only user JWT")
			w.WriteHeader(403)
			return
		}
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/state":
			if r.Method == "GET" {
				fmt.Fprint(w, `{"rollback":true,"results_persistence":true}`)
			} else {
				fixture.saved++
				fmt.Fprint(w, `{}`)
			}
		case "/requests":
			_ = json.NewEncoder(w).Encode(map[string]any{"requests": []CensorRequest{{ID: "request-1", TrackID: 42, Title: "Title", Artist: "Artist", OriginalURL: fixture.original}}})
		case "/search":
			var in struct {
				Query string `json:"query"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			fixture.queries = append(fixture.queries, in.Query)
			_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []CensorCandidate{{ID: "candidate-1", Title: "Original Title", Artist: "Artist", URL: fixture.candidateURL, Provider: "fixture", FullAudio: true, Downloadable: true, DurationSeconds: 30, Metadata: map[string]string{"format": "mp3"}}}})
		case "/replacements/receipt":
			w.WriteHeader(http.StatusNotFound)
		case "/replacements", "/rollback":
			var selection CensorSelection
			if json.NewDecoder(r.Body).Decode(&selection) != nil || selection.IdempotencyKey == "" || r.Header.Get("Idempotency-Key") != selection.IdempotencyKey {
				t.Error("missing mutation idempotency")
				w.WriteHeader(400)
				return
			}
			url := "https://audio.example/published.mp3"
			if r.URL.Path == "/replacements" {
				fixture.mutations++
				fixture.keys = append(fixture.keys, selection.IdempotencyKey)
				if fixture.failFirst && fixture.mutations == 1 {
					w.WriteHeader(503)
					return
				}
			} else {
				fixture.rollback++
				url = fixture.original
			}
			_ = json.NewEncoder(w).Encode(CensorReplacement{RequestID: selection.RequestID, TrackID: selection.TrackID, URL: url, Durable: true, Atomic: true, IdempotencyKey: selection.IdempotencyKey})
		default:
			t.Error("unexpected remote endpoint", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(remote.Close)
	backend, e := NewCensorRemoteBackend(remote.URL)
	if e != nil {
		t.Fatal(e)
	}
	backend.client = remote.Client()
	s := censorServer(t)
	s.SetCensorBackend(backend)
	p := &orchestrationProcessor{}
	s.censorProcessor = p
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if e := s.Close(ctx); e != nil {
			t.Error(e)
		}
	})
	return s, fixture, p
}
func controlAPI(t *testing.T, s *Server, in any, status int) CensorState {
	t.Helper()
	data, e := json.Marshal(in)
	if e != nil {
		t.Fatal(e)
	}
	w := api(s, "POST", "/api/censor/control", string(data), "bridge-key")
	if w.Code != status {
		t.Fatalf("control %s: %d %s", data, w.Code, w.Body)
	}
	var state CensorState
	if status == 200 {
		// The HTTP view carries numeric canvas samples and native waveform points.
		// Normalize only the view wrapper when decoding the native Go test DTO.
		var view map[string]any
		if json.Unmarshal(w.Body.Bytes(), &view) != nil {
			t.Fatal("invalid state JSON")
		}
		if jobs, ok := view["jobs"].([]any); ok {
			for _, item := range jobs {
				job := item.(map[string]any)
				if result, ok := job["result"].(map[string]any); ok {
					if waveform, ok := result["waveform"].(map[string]any); ok {
						result["waveform"] = waveform["points"]
					}
				}
			}
		}
		data, _ := json.Marshal(view)
		if json.Unmarshal(data, &state) != nil {
			t.Fatal("invalid state", w.Body)
		}
	}
	return state
}
func waitCensor(t *testing.T, s *Server, status string) CensorJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		rt := s.censorRuntime
		s.mu.Unlock()
		state := rt.snapshot()
		if len(state.Jobs) > 0 && state.Jobs[0].Status == status && !state.Running {
			return state.Jobs[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for censor status", status, s.censorRuntime.snapshot())
	return CensorJob{}
}
func fetchStart(t *testing.T, s *Server) CensorJob {
	t.Helper()
	controlAPI(t, s, map[string]any{"action": "fetch"}, 200)
	controlAPI(t, s, map[string]any{"action": "start"}, 200)
	return waitCensor(t, s, "review_required")
}
func TestCensorUIFullPipelineAndManualPublication(t *testing.T) {
	s, f, p := integrationCensor(t)
	state := controlAPI(t, s, map[string]any{"action": "settings", "settings": map[string]any{"workers": 3}}, 200)
	if state.Settings.Workers != 3 {
		t.Fatal(state.Settings)
	}
	j := fetchStart(t, s)
	if len(j.Candidates) != 1 || !strongCensorComparison(j.Candidates[0].Result) || j.Request.OriginalURL == "" {
		t.Fatalf("missing review evidence %+v", j)
	}
	f.mu.Lock()
	count := f.mutations
	queries := append([]string(nil), f.queries...)
	saved := f.saved
	f.mu.Unlock()
	if count != 0 || len(queries) != 1 || saved != 1 {
		t.Fatal("pipeline published or did not search/persist", count, queries, saved)
	}
	p.mu.Lock()
	requests := append([]censor.Request(nil), p.requests...)
	p.mu.Unlock()
	if len(requests) != 2 || requests[0].Source.URL != j.Request.OriginalURL || requests[0].Reference != nil || requests[1].Reference == nil || requests[1].Reference.URL != j.Candidates[0].URL {
		t.Fatalf("wrong engine sources %+v", requests)
	}
	state = controlAPI(t, s, map[string]any{"action": "approve", "id": j.ID, "request_id": j.Request.ID, "track_id": 42, "candidate_id": "candidate-1"}, 200)
	published := waitCensor(t, s, "succeeded")
	if published.Replacement == nil {
		t.Fatal("missing durable receipt")
	}
	controlAPI(t, s, map[string]any{"action": "approve", "id": j.ID, "candidate_id": "candidate-1"}, 200)
	f.mu.Lock()
	count = f.mutations
	f.mu.Unlock()
	if count != 1 {
		t.Fatal("duplicate publication", count)
	}
	p.mu.Lock()
	analyses := len(p.requests)
	p.mu.Unlock()
	if analyses != 3 {
		t.Fatal("approval did not recheck audio", analyses)
	}
	state = controlAPI(t, s, map[string]any{"action": "rollback", "id": j.ID}, 200)
	if state.Jobs[0].Status != "rolled_back" {
		t.Fatal(state)
	}
	controlAPI(t, s, map[string]any{"action": "rollback", "id": j.ID}, 200)
	f.mu.Lock()
	rollbacks := f.rollback
	f.mu.Unlock()
	if rollbacks != 1 {
		t.Fatal(rollbacks)
	}
}
func TestCensorPublicationRetryKeepsKeyAcrossRestart(t *testing.T) {
	s, f, _ := integrationCensor(t)
	j := fetchStart(t, s)
	f.mu.Lock()
	f.failFirst = true
	f.mu.Unlock()
	approve := map[string]any{"action": "approve", "id": j.ID, "candidate_id": "candidate-1"}
	controlAPI(t, s, approve, 200)
	failed := waitCensor(t, s, "review_required")
	if failed.Error == "" {
		t.Fatal("missing publication error")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e := s.closeCensor(ctx, true); e != nil {
		t.Fatal(e)
	}
	controlAPI(t, s, approve, 200)
	waitCensor(t, s, "succeeded")
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.keys) != 2 || f.keys[0] != f.keys[1] {
		t.Fatal("publication key changed after restart", f.keys)
	}
}
func TestCensorApprovalRejectsWeakChangedAndStaleAudio(t *testing.T) {
	for _, mode := range []string{"weak", "request", "candidate", "identity"} {
		t.Run(mode, func(t *testing.T) {
			s, f, p := integrationCensor(t)
			j := fetchStart(t, s)
			switch mode {
			case "weak":
				p.mu.Lock()
				p.weak = true
				p.mu.Unlock()
			case "request":
				f.mu.Lock()
				f.original = "https://audio.example/changed.mp3"
				f.mu.Unlock()
			case "candidate":
				f.mu.Lock()
				f.candidateURL = "https://audio.example/changed.mp3"
				f.mu.Unlock()
			}
			args := map[string]any{"action": "approve", "id": j.ID, "candidate_id": "candidate-1"}
			if mode == "identity" {
				args["request_id"] = "someone-else"
				args["track_id"] = 100
			}
			if mode == "identity" {
				controlAPI(t, s, args, 409)
			} else {
				controlAPI(t, s, args, 200)
				failed := waitCensor(t, s, "review_required")
				if failed.Error == "" {
					t.Fatal("missing failed recheck error")
				}
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.mutations != 0 {
				t.Fatal("unsafe publication")
			}
		})
	}
}
func TestCensorPauseResumeStopAndLogoutCancel(t *testing.T) {
	s, _, p := integrationCensor(t)
	p.block = make(chan struct{})
	p.entered = make(chan struct{})
	controlAPI(t, s, map[string]any{"action": "fetch"}, 200)
	controlAPI(t, s, map[string]any{"action": "start"}, 200)
	select {
	case <-p.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("processor did not start")
	}
	state := controlAPI(t, s, map[string]any{"action": "pause"}, 200)
	if !state.Paused || state.Running || state.Jobs[0].Status != "pending" {
		t.Fatal(state)
	}
	close(p.block)
	controlAPI(t, s, map[string]any{"action": "resume"}, 200)
	j := waitCensor(t, s, "review_required")
	controlAPI(t, s, map[string]any{"action": "defer", "id": j.ID}, 200)
	controlAPI(t, s, map[string]any{"action": "retry", "id": j.ID}, 200)
	j = waitCensor(t, s, "review_required")
	controlAPI(t, s, map[string]any{"action": "reject", "id": j.ID}, 200)
	state = controlAPI(t, s, map[string]any{"action": "stop"}, 200)
	if state.Running || state.Paused {
		t.Fatal(state)
	}
	w := api(s, "POST", "/api/logout", "{}", "bridge-key")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	w = api(s, "GET", "/api/censor/state", "", "bridge-key")
	if w.Code != 200 || strings.Contains(w.Body.String(), j.ID) || strings.Contains(w.Body.String(), "original.mp3") {
		t.Fatal("logout exposed old jobs", w.Code, w.Body)
	}
	if s.censorRuntime != nil {
		t.Fatal("logout retained runtime")
	}
}
func TestCensorLogoutAndCloseCancelActiveQueue(t *testing.T) {
	for _, action := range []string{"logout", "close"} {
		t.Run(action, func(t *testing.T) {
			s, _, p := integrationCensor(t)
			p.block = make(chan struct{})
			p.entered = make(chan struct{})
			controlAPI(t, s, map[string]any{"action": "fetch"}, 200)
			controlAPI(t, s, map[string]any{"action": "start"}, 200)
			select {
			case <-p.entered:
			case <-time.After(time.Second):
				t.Fatal("not started")
			}
			rt := s.censorRuntime
			if action == "logout" {
				w := api(s, "POST", "/api/logout", "{}", "bridge-key")
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body)
				}
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				if e := s.Close(ctx); e != nil {
					t.Fatal(e)
				}
			}
			select {
			case <-rt.done:
			default:
				t.Fatal("queue did not stop")
			}
			if rt.snapshot().Running {
				t.Fatal("runtime still running")
			}
		})
	}
}
func TestCensorUnconfiguredAndLazyState(t *testing.T) {
	t.Setenv("FCKCENSOR_API_URL", "")
	s := censorServer(t)
	root := filepath.Join(filepath.Dir(s.path), "censor")
	for n := 0; n < 2; n++ {
		w := api(s, "GET", "/api/censor/state", "", "bridge-key")
		var state CensorState
		_ = json.Unmarshal(w.Body.Bytes(), &state)
		if w.Code != 200 || !state.Configured || !state.Native || state.RequestSource != "reported_tracks" || len(state.Jobs) != 0 {
			t.Fatal(w.Code, w.Body)
		}
	}
	if _, e := os.Stat(root); !os.IsNotExist(e) {
		t.Fatal("state polling created files", e)
	}
	t.Setenv("FCKCENSOR_API_URL", "https://api.example/protected")
	other := censorServer(t)
	w := api(other, "GET", "/api/censor/state", "", "bridge-key")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if _, ok := other.censorRuntime.backend.(*CensorRemoteBackend); !ok {
		t.Fatal("remote override missing")
	}
}
func TestCensorSettingsAndControlValidation(t *testing.T) {
	s, _, _ := integrationCensor(t)
	for _, body := range []string{
		`{"action":"settings","settings":{"auto_replace":true}}`,
		`{"action":"settings","settings":{"workers":0}}`,
		`{"action":"settings","settings":{"workers":33}}`,
		`{"action":"settings","settings":{"gpu":true}}`,
		`{"action":"settings","settings":{}}`,
		`{"action":"approve","id":"j","candidate_id":"c","url":"file:///etc/passwd"}`,
		`{"action":"search","id":"j","query":" "}`,
		`{"action":"fetch","args":{}}`,
		`{"action":"fetch"} {}`,
	} {
		w := api(s, "POST", "/api/censor/control", body, "bridge-key")
		if w.Code != 400 {
			t.Fatal(body, w.Code, w.Body)
		}
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{{"POST", "/api/censor/state", 405}, {"GET", "/api/censor/control", 405}, {"GET", "/api/censor/state?q=1", 400}} {
		w := api(s, tc.method, tc.path, "{}", "bridge-key")
		if w.Code != tc.status {
			t.Fatal(w.Code, w.Body)
		}
	}
	controlAPI(t, s, map[string]any{"action": "approve", "id": "missing", "candidate_id": "c"}, 404)
}
func TestCensorAccountIsolationAndPersistence(t *testing.T) {
	s, _, _ := integrationCensor(t)
	j := fetchStart(t, s)
	old := s.censorRuntime
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e := s.closeCensor(ctx, true); e != nil {
		t.Fatal(e)
	}
	restored, e := openCensorRuntime(s, "user-1", old.backend)
	if e != nil {
		t.Fatal(e)
	}
	if state := restored.snapshot(); len(state.Jobs) != 1 || state.Jobs[0].ID != j.ID || state.Jobs[0].Status != "review_required" || state.Running {
		t.Fatal(state)
	}
	other, e := openCensorRuntime(s, "other-account", old.backend)
	if e != nil {
		t.Fatal(e)
	}
	if len(other.snapshot().Jobs) != 0 || other.dir == restored.dir {
		t.Fatal("account state shared")
	}
	data, e := readPrivateFile(filepath.Join(old.dir, "runtime.json"), censorStateLimit, true)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(data), "secret-access") {
		t.Fatal("persisted JWT")
	}
}
func TestCensorRemoteRejectsUnsafeConfigAndBoundedResponses(t *testing.T) {
	for _, base := range []string{"http://api.example", "https://u:p@api.example", "https://api.example?key=secret", "https://api.example/#fragment", ""} {
		if _, e := NewCensorRemoteBackend(base); e == nil {
			t.Fatal("accepted config", base)
		}
	}
	for _, mode := range []string{"redirect", "oversized", "credentials", "denied", "unacknowledged"} {
		t.Run(mode, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "redirect":
					http.Redirect(w, r, server.URL+"/elsewhere", 302)
				case "oversized":
					fmt.Fprint(w, strings.Repeat(" ", censorRemoteLimit+1))

				case "credentials":
					fmt.Fprint(w, `{"candidates":[{"id":"c","url":"https://user:secret@audio.example/full.mp3","full_audio":true,"downloadable":true}]}`)
				case "denied":
					w.WriteHeader(403)
				case "unacknowledged":
					fmt.Fprint(w, `{"request_id":"r","track_id":42,"url":"https://audio.example/full.mp3"}`)
				}
			}))
			defer server.Close()
			b, e := NewCensorRemoteBackend(server.URL)
			if e != nil {
				t.Fatal(e)
			}
			b.client = server.Client()
			b.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			i := CensorIdentity{UserID: "user-1", AccessToken: "secret-access"}
			if mode == "unacknowledged" {
				_, e = b.Replace(context.Background(), i, CensorSelection{RequestID: "r", TrackID: 42, CandidateID: "c", IdempotencyKey: "key"})
			} else {
				_, e = b.Search(context.Background(), i, "artist title")
			}
			if e == nil {
				t.Fatal("accepted unsafe remote result")
			}
			if mode == "denied" && !errors.Is(e, ErrCensorDenied) {
				t.Fatal(e)
			}
		})
	}
}
