package bridge

import (
	"context"
	"ddetector-go/internal/censor"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type progressSearchBackend struct {
	CensorBackend
	rt     *censorRuntime
	id     string
	calls  int
	cancel context.CancelFunc
}

func (b *progressSearchBackend) Search(ctx context.Context, i CensorIdentity, query string) ([]CensorCandidate, error) {
	j, err := b.rt.job(b.id)
	if err != nil {
		return nil, err
	}
	if j.StageProgress["search"] != float64(b.calls)/2 {
		return nil, errors.New("search progress not persisted between queries")
	}
	b.calls++
	if b.cancel != nil && b.calls == 2 {
		b.cancel()
		return nil, ctx.Err()
	}
	if b.calls == 1 {
		return nil, ErrCensorUnavailable
	}
	return nil, nil
}

type progressProcessor struct {
	rt    *censorRuntime
	id    string
	calls int
	t     *testing.T
}

func (p *progressProcessor) Analyze(ctx context.Context, r censor.Request) (censor.Result, error) {
	p.t.Helper()
	j, err := p.rt.job(p.id)
	if err != nil {
		p.t.Fatal(err)
	}
	if j.Stage != "comparison" || j.StageProgress["comparison"] != float64(p.calls)/2 {
		p.t.Fatalf("unexpected progress: %+v", j)
	}
	p.calls++
	if p.calls == 1 {
		return censor.Result{}, errors.New("unavailable audio")
	}
	return strongFixture(), nil
}

func TestCensorStageProgressPersistence(t *testing.T) {
	s, _, _ := integrationCensor(t)
	controlAPI(t, s, map[string]any{"action": "fetch"}, 200)
	rt := s.censorRuntime
	j := rt.snapshot().Jobs[0]
	j.Status = "running"
	j.StageProgress = newStageProgress()
	if err := rt.update(j); err != nil {
		t.Fatal(err)
	}
	b := &progressSearchBackend{CensorBackend: rt.backend, rt: rt, id: j.ID}
	rt.backend = b
	if err := rt.search(context.Background(), CensorIdentity{}, &j, []string{"first", "second"}); err != nil {
		t.Fatal(err)
	}
	if j.StageProgress["search"] != 1 || !j.SearchPartial {
		t.Fatal(j.StageProgress, j.SearchPartial)
	}
	j.Candidates = []CensorCandidate{{ID: "page", URL: "https://example.com/page"}, {ID: "a", URL: "https://example.com/a.mp3", FullAudio: true, Downloadable: true}, {ID: "b", URL: "https://example.com/b.mp3", FullAudio: true, Downloadable: true}}
	rt.processor = &progressProcessor{rt: rt, id: j.ID, t: t}
	if err := rt.compareCandidates(context.Background(), nil, &j); err != nil {
		t.Fatal(err)
	}
	if j.StageProgress["comparison"] != 1 || j.Candidates[1].Error == "" {
		t.Fatal(j)
	}
	data, err := os.ReadFile(filepath.Join(rt.dir, "runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk censorDiskState
	if err = json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	if disk.Jobs[0].StageProgress["comparison"] != 1 {
		t.Fatal("not durable")
	}
	for _, summary := range []bool{false, true} {
		view := censorJobView(j, summary)
		data, err = json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		_ = json.Unmarshal(data, &out)
		if out["stage_progress"] == nil {
			t.Fatal("missing JSON field")
		}
	}
	snapshot := rt.snapshot()
	snapshot.Jobs[0].StageProgress["comparison"] = 0
	if rt.snapshot().Jobs[0].StageProgress["comparison"] != 1 {
		t.Fatal("snapshot aliases progress")
	}
}

func TestCensorProgressDebounceAndTerminalFlush(t *testing.T) {
	s, _, _ := integrationCensor(t)
	controlAPI(t, s, map[string]any{"action": "fetch"}, 200)
	rt := s.censorRuntime
	j := rt.snapshot().Jobs[0]
	j.Status = "running"
	j.StageProgress = newStageProgress()
	if err := rt.update(j); err != nil {
		t.Fatal(err)
	}
	before := rt.stateVersion
	if err := rt.stageProgress(&j, "search", 1, 3); err != nil {
		t.Fatal(err)
	}
	if err := rt.stageProgress(&j, "search", 2, 3); err != nil {
		t.Fatal(err)
	}
	if rt.stateVersion != before {
		t.Fatal("progress bypassed debounce")
	}
	j.Status = "failed"
	j.Error = "terminal"
	if err := rt.update(j); err != nil {
		t.Fatal(err)
	}
	if rt.progressDirty || rt.progressTimer != nil || rt.stateVersion == before {
		t.Fatal("terminal update did not synchronously flush progress")
	}
	data, err := os.ReadFile(filepath.Join(rt.dir, "runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk censorDiskState
	if err = json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	if disk.Jobs[0].Status != "failed" || disk.Jobs[0].StageProgress["search"] != float64(2)/3 {
		t.Fatal("terminal state or latest progress not durable", disk.Jobs[0])
	}
	// A stopped timer must not issue a later stale write.
	time.Sleep(censorProgressDebounce + 50*time.Millisecond)
	if rt.progressDirty || rt.progressTimer != nil {
		t.Fatal("debounce remained active after terminal flush")
	}
}

func TestCloneCensorJobDoesNotAliasMutableData(t *testing.T) {
	similarity := .75
	original := CensorJob{
		StageProgress: map[string]float64{"search": .5},
		Queries:       []string{"query"},
		Result: &censor.Result{
			Warnings:    []string{"warning"},
			Fingerprint: &censor.Fingerprint{Values: []uint32{1}},
			Comparison:  &censor.Comparison{FingerprintSimilarity: &similarity},
			SuspiciousIntervals: []censor.SuspiciousInterval{{
				Restoration: &censor.RestorationEvidence{Reason: "evidence"},
			}},
		},
		Candidates:  []CensorCandidate{{Metadata: map[string]string{"key": "value"}, Result: &censor.Result{Waveform: []censor.WaveformPoint{{Max: 1}}}}},
		Replacement: &CensorReplacement{URL: "https://example.com/replacement.mp3"},
	}
	cloned := cloneCensorJob(original)
	cloned.StageProgress["search"] = 1
	cloned.Queries[0] = "changed"
	cloned.Result.Warnings[0] = "changed"
	cloned.Result.Fingerprint.Values[0] = 2
	*cloned.Result.Comparison.FingerprintSimilarity = .25
	cloned.Result.SuspiciousIntervals[0].Restoration.Reason = "changed"
	cloned.Candidates[0].Metadata["key"] = "changed"
	cloned.Candidates[0].Result.Waveform[0].Max = 2
	cloned.Replacement.URL = "changed"
	if original.StageProgress["search"] != .5 || original.Queries[0] != "query" || original.Result.Warnings[0] != "warning" || original.Result.Fingerprint.Values[0] != 1 || *original.Result.Comparison.FingerprintSimilarity != .75 || original.Result.SuspiciousIntervals[0].Restoration.Reason != "evidence" || original.Candidates[0].Metadata["key"] != "value" || original.Candidates[0].Result.Waveform[0].Max != 1 || original.Replacement.URL != "https://example.com/replacement.mp3" {
		t.Fatal("clone aliases mutable job data")
	}
}

func TestCensorSearchProgressCancellation(t *testing.T) {
	s, _, _ := integrationCensor(t)
	controlAPI(t, s, map[string]any{"action": "fetch"}, 200)
	rt := s.censorRuntime
	j := rt.snapshot().Jobs[0]
	j.Status = "running"
	j.StageProgress = newStageProgress()
	if err := rt.update(j); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt.backend = &progressSearchBackend{CensorBackend: rt.backend, rt: rt, id: j.ID, cancel: cancel}
	if err := rt.search(ctx, CensorIdentity{}, &j, []string{"first", "second"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	saved, err := rt.job(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.StageProgress["search"] != .5 {
		t.Fatal(saved.StageProgress)
	}
}
