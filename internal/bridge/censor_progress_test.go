package bridge

import (
	"context"
	"ddetector-go/internal/censor"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
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
