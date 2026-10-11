package bridge

import (
	"context"
	"ddetector-go/internal/censor"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCensorLargeHistoryPagingAndDetail(t *testing.T) {
	s, _, _ := integrationCensor(t)
	controlAPI(t, s, map[string]any{"action": "fetch"}, 200)
	rt := s.censorRuntime
	rt.mu.Lock()
	template := rt.disk.Jobs[0]
	for n := 0; n < 205; n++ {
		job := template
		job.ID = fmt.Sprintf("job-%03d", n)
		job.Status = "review_required"
		result := strongFixture()
		if n < 20 {
			result.Transcript = strings.Repeat("x", 1<<20)
		}
		job.Result = &result
		job.Candidates = []CensorCandidate{{ID: "full", URL: "https://audio.example/full.mp3", FullAudio: true, Downloadable: true, Result: &result}}
		if n == 204 {
			job.PublicationKey = "preserved-key"
			job.SelectedCandidate = "full"
		}
		if n == 0 {
			rt.disk.Jobs = nil
		}
		rt.disk.Jobs = append(rt.disk.Jobs, job)
	}
	if err := rt.persistLocked(); err != nil {
		t.Fatal(err)
	}
	rt.mu.Unlock()
	info, err := os.Stat(filepath.Join(rt.dir, "runtime.json"))
	if err != nil || info.Size() <= 16<<20 {
		t.Fatal("fixture must exceed UI limit", info, err)
	}
	for _, page := range []struct {
		path                string
		count, offset, next int
	}{
		{"/api/censor/state", 100, 0, 100}, {"/api/censor/jobs?offset=100", 100, 100, 200}, {"/api/censor/jobs?offset=200", 5, 200, -1},
	} {
		w := api(s, "GET", page.path, "", "bridge-key")
		if w.Code != 200 || w.Body.Len() > 16<<20 {
			t.Fatal(w.Code, w.Body.Len(), w.Body.String())
		}
		var view censorUIState
		if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if len(view.Jobs) != page.count || view.Offset != page.offset || view.NextOffset != page.next || view.JobsTotal != 205 || view.Stats["total"] != 205 {
			t.Fatal("bad page", view.Offset, view.NextOffset, len(view.Jobs))
		}
	}
	w := api(s, "GET", "/api/censor/job?id=job-204", "", "bridge-key")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var detail struct {
		Job censorUIJob `json:"job"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Job.ID != "job-204" || detail.Job.PublicationKey != "preserved-key" || !strongCensorComparison(detail.Job.Candidates[0].Result) {
		t.Fatal("detail lost selection evidence")
	}
	for _, path := range []string{"/api/censor/job?id=missing", "/api/censor/job?id=job-000&id=job-001", "/api/censor/job?id=job-000&token=x", "/api/censor/jobs?offset=-1", "/api/censor/job?id=%zz"} {
		w := api(s, "GET", path, "", "bridge-key")
		if w.Code != 400 && w.Code != 404 {
			t.Fatal(path, w.Code)
		}
	}
	if w := api(s, "GET", "/api/censor/job?id=job-000", "", "wrong-key"); w.Code != 401 {
		t.Fatal("detail requires bridge authorization", w.Code)
	}
}

func TestCensorOversizedUpdatePreservesDurableJob(t *testing.T) {
	s, _, _ := integrationCensor(t)
	initial := fetchStart(t, s)
	rt := s.censorRuntime
	job, err := rt.job(initial.ID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(rt.dir, "runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	job.PublicationKey = "must-not-install"
	job.Result.Transcript = strings.Repeat("x", censorJobLimit)
	if err := rt.update(job); !errors.Is(err, ErrCensorCapacity) {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(rt.dir, "runtime.json"))
	retained, _ := rt.job(job.ID)
	if string(before) != string(after) || retained.PublicationKey != "" || retained.Result.Transcript != "" || rt.failure != "" {
		t.Fatal("capacity failure altered durable state or globally blocked review")
	}
	retained.PublicationKey = "receipt-key"
	retained.SelectedCandidate = "candidate-1"
	if err := rt.update(retained); err != nil {
		t.Fatal("receipt update blocked", err)
	}
}

func TestCensorOversizedLegacyDetailProjection(t *testing.T) {
	result := strongFixture()
	result.Transcript = strings.Repeat("x", 17<<20)
	job := CensorJob{ID: "legacy", Status: "review_required", PublicationKey: "receipt", Result: &result, Candidates: []CensorCandidate{{ID: "full", Result: &result}}}
	w := httptest.NewRecorder()
	sendCensorJSON(w, 200, map[string]any{"job": censorJobView(job, false)})
	if w.Code != 200 || w.Body.Len() > 16<<20 {
		t.Fatal(w.Code, w.Body.Len())
	}
	if !strings.Contains(w.Body.String(), "details_omitted\":true") || !strings.Contains(w.Body.String(), "reference_restoration_candidate") {
		t.Fatal("missing omission/evidence")
	}
	if result.Transcript == "" {
		t.Fatal("projection mutated stored transcript")
	}
}

type capacityCensorProcessor struct{}

func (capacityCensorProcessor) Analyze(ctx context.Context, request censor.Request) (censor.Result, error) {
	result := strongFixture()
	result.Transcript = strings.Repeat("x", 1<<20)
	if request.Reference == nil {
		result.Comparison = nil
	}
	return result, nil
}

type capacitySearchBackend struct{ CensorBackend }

func (b capacitySearchBackend) Search(ctx context.Context, identity CensorIdentity, query string) ([]CensorCandidate, error) {
	out := []CensorCandidate{}
	for n := 0; n < 20; n++ {
		out = append(out, CensorCandidate{ID: fmt.Sprintf("candidate-%d", n), URL: fmt.Sprintf("https://audio.example/%d.mp3", n), FullAudio: true, Downloadable: true})
	}
	return out, nil
}
func TestCensorCapacityPausesWorkerWithoutBlockingReview(t *testing.T) {
	s, _, _ := integrationCensor(t)
	s.censor = capacitySearchBackend{s.censor}
	s.censorProcessor = capacityCensorProcessor{}
	controlAPI(t, s, map[string]any{"action": "fetch"}, 200)
	controlAPI(t, s, map[string]any{"action": "start"}, 200)
	deadline := time.Now().Add(30 * time.Second)
	var job CensorJob
	for time.Now().Before(deadline) {
		state := s.censorRuntime.snapshot()
		if len(state.Jobs) > 0 && state.Jobs[0].Status == "failed" && !state.Running {
			job = state.Jobs[0]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if job.ID == "" {
		t.Fatal("capacity worker did not finish")
	}
	state := s.censorRuntime.snapshot()
	if !state.Paused || state.Running || s.censorRuntime.failure != "" || !strings.Contains(job.Error, "storage capacity") {
		t.Fatal("capacity did not leave review usable", job.Error, state.Error)
	}
	if job.Result == nil {
		t.Fatal("previous evidence lost")
	}
	if w := api(s, "GET", "/api/censor/job?id="+job.ID, "", "bridge-key"); w.Code != 200 {
		t.Fatal("review blocked", w.Code)
	}
}

func TestCensorAggregateAdmissionReservesReceiptHeadroom(t *testing.T) {
	rt := &censorRuntime{disk: censorDiskState{Settings: defaultCensorSettings()}}
	for n := 0; n < 62; n++ {
		rt.disk.Jobs = append(rt.disk.Jobs, CensorJob{ID: fmt.Sprintf("job-%d", n), Result: &censor.Result{Transcript: strings.Repeat("x", 1<<20)}})
	}
	proposed := rt.disk
	proposed.Jobs = append([]CensorJob(nil), rt.disk.Jobs...)
	proposed.Jobs[0].Result = &censor.Result{Transcript: strings.Repeat("x", 2<<20)}
	if err := censorAdmissionLimit(proposed, censorStateLimit-(1<<20)); !errors.Is(err, ErrCensorCapacity) {
		t.Fatal("aggregate analysis overflow accepted", err)
	}
	proposed.Jobs = append([]CensorJob(nil), rt.disk.Jobs...)
	proposed.Jobs[0].PublicationKey = "receipt-key"
	if err := censorStateAdmission(proposed); err != nil {
		t.Fatal("receipt headroom unavailable", err)
	}
}

type partialCensorBackend struct {
	CensorBackend
	failures map[string]error
}

func (b partialCensorBackend) Search(ctx context.Context, i CensorIdentity, query string) ([]CensorCandidate, error) {
	if err := b.failures[query]; err != nil {
		return nil, err
	}
	return []CensorCandidate{{ID: query, URL: "https://audio.example/full.mp3", FullAudio: true, Downloadable: true}}, nil
}
func TestCensorPartialBaseSearch(t *testing.T) {
	for _, test := range []struct {
		name, status string
		failures     map[string]error
		wantErr      error
		count        int
	}{
		{"partial", "running", map[string]error{"second": ErrCensorUnavailable}, nil, 1},
		{"first failed", "running", map[string]error{"first": ErrCensorUnavailable}, nil, 1},
		{"all failed", "running", map[string]error{"first": ErrCensorUnavailable, "second": ErrCensorUnavailable}, ErrCensorUnavailable, 0},
		{"approval strict", "review_required", map[string]error{"second": ErrCensorUnavailable}, ErrCensorUnavailable, 1},
		{"denied", "running", map[string]error{"second": ErrCensorDenied}, ErrCensorDenied, 0},
		{"canceled", "running", map[string]error{"second": context.Canceled}, context.Canceled, 0},
		{"query timeout", "running", map[string]error{"second": context.DeadlineExceeded}, nil, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			rt := &censorRuntime{backend: partialCensorBackend{failures: test.failures}, disk: censorDiskState{Settings: defaultCensorSettings()}}
			job := CensorJob{Status: test.status}
			err := rt.search(context.Background(), CensorIdentity{}, &job, []string{"first", "second"})
			if !errors.Is(err, test.wantErr) || len(job.Candidates) != test.count {
				t.Fatal(err, len(job.Candidates))
			}
			if test.count > 0 && (!job.SearchPartial || job.SearchWarning == "") {
				t.Fatal("partial results mislabeled")
			}
		})
	}
}
