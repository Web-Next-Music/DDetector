package bridge

import (
	"ddetector-go/internal/censor"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type blockedStateWriter struct {
	*httptest.ResponseRecorder
	at               string
	entered, release chan struct{}
	once             sync.Once
}

func (w *blockedStateWriter) block(at string) {
	if w.at == at {
		w.once.Do(func() { close(w.entered); <-w.release })
	}
}
func (w *blockedStateWriter) Header() http.Header {
	w.block("header")
	return w.ResponseRecorder.Header()
}
func (w *blockedStateWriter) WriteHeader(status int) {
	w.block("status")
	w.ResponseRecorder.WriteHeader(status)
}
func (w *blockedStateWriter) Write(data []byte) (int, error) {
	w.block("body")
	return w.ResponseRecorder.Write(data)
}

func TestStateBlockedWriterDoesNotBlockSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name, at string
		status   int
	}{
		{"headers", "header", 200}, {"body", "body", 200},
		{"conditional", "status", 304}, {"failure", "body", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{state: State{Tracks: []Result{{Track: Track{ID: 1}}}}}
			request := httptest.NewRequest("GET", "/api/state", nil)
			if tc.status == 304 {
				request.Header.Set("If-None-Match", "*")
			}
			if tc.status == 500 {
				s.state.User = &User{Metadata: map[string]any{"bad": math.NaN()}}
			}
			w := &blockedStateWriter{ResponseRecorder: httptest.NewRecorder(), at: tc.at,
				entered: make(chan struct{}), release: make(chan struct{})}
			done := make(chan struct{})
			go func() { defer close(done); s.sendState(w, request) }()
			defer func() {
				close(w.release)
				<-done
				if w.Code != tc.status {
					t.Errorf("first response status = %d, want %d", w.Code, tc.status)
				}
				if tc.status == 200 && !strings.Contains(w.Body.String(), `"id":1`) {
					t.Error("blocked response lost its original snapshot")
				}
			}()
			select {
			case <-w.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("writer was never reached")
			}
			s.mu.Lock()
			s.state.User = nil
			s.state.Tracks[0].ID = 2
			s.tracksRevision++
			s.mu.Unlock()
			other := httptest.NewRecorder()
			otherDone := make(chan struct{})
			go func() { defer close(otherDone); s.sendState(other, httptest.NewRequest("GET", "/api/state", nil)) }()
			select {
			case <-otherDone:
				if other.Code != 200 || !strings.Contains(other.Body.String(), `"id":2`) {
					t.Fatal(other.Code, other.Body)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("blocked writer held the state cache lock")
			}
		})
	}
}

func resetCensorViewCache() {
	censorViews.Lock()
	censorViews.entries = nil
	censorViews.order = nil
	censorViews.Unlock()
}

func TestCensorViewWholePageReusesEncoding(t *testing.T) {
	resetCensorViewCache()
	jobs := make([]CensorJob, censorUIPageSize)
	views := make([]censorUIJob, len(jobs))
	for n := range jobs {
		jobs[n] = CensorJob{ID: fmt.Sprintf("page-%d", n), Result: &censor.Result{Transcript: "small evidence"}}
		views[n] = censorJobView(jobs[n], true)
	}
	for poll := 0; poll < 3; poll++ {
		for n, job := range jobs {
			view := censorJobView(job, true)
			if len(view.encoded) == 0 || &view.encoded[0] != &views[n].encoded[0] {
				t.Fatalf("poll %d job %d serialized again", poll, n)
			}
			detail := censorJobView(job, false)
			if detail.DetailRevision == "" || detail.DetailRevision != view.DetailRevision {
				t.Fatal("summary/detail revision mismatch")
			}
		}
	}
}

func TestCensorViewOversizedSummaryNeverSerializesEvidence(t *testing.T) {
	resetCensorViewCache()
	// Legacy evidence can be tens of MiB. A non-finite value also makes a full
	// evidence marshal fail, while the safe summary must still encode/cache.
	result := &censor.Result{Transcript: strings.Repeat("x", 34<<20), DurationSeconds: math.NaN()}
	jobs := make([]CensorJob, censorUIPageSize)
	views := make([]censorUIJob, len(jobs))
	for n := range jobs {
		jobs[n] = CensorJob{ID: fmt.Sprintf("large-%d", n), Result: result}
		if censorViewFits(jobs[n]) {
			t.Fatal("oversized evidence passed admission")
		}
		views[n] = censorJobView(jobs[n], true)
	}
	for poll := 0; poll < 3; poll++ {
		for n, job := range jobs {
			view := censorJobView(job, true)
			if !view.DetailsOmitted || view.DetailRevision != "" || len(view.encoded) == 0 {
				t.Fatal("unsafe oversized summary", n)
			}
			if &view.encoded[0] != &views[n].encoded[0] {
				t.Fatalf("oversized summary re-serialized on poll %d job %d", poll, n)
			}
			if len(view.encoded) > censorUISummaryBytes || view.CensorJob.Result != nil {
				t.Fatal("summary retained evidence")
			}
		}
	}
	// Changes hidden by the summary must never acquire an optimistic detail
	// revision; selected details continue to use the legacy polling path.
	jobs[0].Result = &censor.Result{Transcript: strings.Repeat("y", 1<<20)}
	if censorJobView(jobs[0], true).DetailRevision != "" || censorJobView(jobs[0], false).DetailRevision != "" {
		t.Fatal("oversized detail advertised revision")
	}
	jobs[0].Status = "failed"
	changed := censorJobView(jobs[0], true)
	if changed.Status != "failed" || &changed.encoded[0] == &views[0].encoded[0] {
		t.Fatal("summary metadata mutation missed")
	}
}

func TestCensorViewBoundedDetailMutationInvalidation(t *testing.T) {
	resetCensorViewCache()
	job := CensorJob{ID: "detail-mutations", Status: "running", Queries: []string{},
		Result: &censor.Result{Transcript: "original", ExcerptTranscripts: []censor.ExcerptTranscript{{Text: "old"}},
			Waveform: []censor.WaveformPoint{{Max: .1}}, Fingerprint: &censor.Fingerprint{Values: []uint32{1}}},
		Candidates:    []CensorCandidate{{ID: "candidate", FullAudio: true, Metadata: map[string]string{"source": "old"}}},
		StageProgress: map[string]float64{"analysis": .1}, Replacement: &CensorReplacement{URL: "old"}}
	mutations := []func(){
		func() { job.Result.ExcerptTranscripts[0].Text = "new" },
		func() { job.Result.Transcript = "new" },
		func() { job.Result.Waveform[0].Max = .2 },
		func() { job.Result.Fingerprint.Values[0] = 2 },
		func() { job.Candidates[0].FullAudio = false },
		func() { job.Candidates[0].Metadata["source"] = "new" },
		func() { job.StageProgress["analysis"] = .2 },
		func() { job.Replacement.URL = "new" },
		func() { job.Status = "stopped" },
	}
	previous := censorJobView(job, true)
	for n, mutate := range mutations {
		mutate()
		summary, detail := censorJobView(job, true), censorJobView(job, false)
		if summary.DetailRevision == "" || summary.DetailRevision == previous.DetailRevision || summary.DetailRevision != detail.DetailRevision {
			t.Fatalf("mutation %d missed or mismatched", n)
		}
		again := censorJobView(job, true)
		if &again.encoded[0] != &summary.encoded[0] {
			t.Fatal("unchanged snapshot missed cache")
		}
		previous = summary
	}
}

func TestCensorViewConcurrentSnapshots(t *testing.T) {
	resetCensorViewCache()
	var wg sync.WaitGroup
	for n := 0; n < 16; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			job := CensorJob{ID: "shared-id", Status: fmt.Sprintf("snapshot-%d", n)}
			for poll := 0; poll < 10; poll++ {
				summary, detail := censorJobView(job, true), censorJobView(job, false)
				if summary.DetailRevision != detail.DetailRevision {
					t.Error("concurrent revision mismatch")
				}
				var decoded censorUIJob
				if err := json.Unmarshal(detail.encoded, &decoded); err != nil || decoded.Status != job.Status {
					t.Error("concurrent snapshot mismatch", err)
				}
			}
		}(n)
	}
	wg.Wait()
}

func BenchmarkCensorOversizedSummaryCached(b *testing.B) {
	job := CensorJob{ID: "large-benchmark", Result: &censor.Result{Transcript: strings.Repeat("x", 34<<20)}}
	censorJobView(job, true)
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		censorJobView(job, true)
	}
}
