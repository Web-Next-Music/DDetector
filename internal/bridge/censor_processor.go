package bridge

import (
	"context"
	"ddetector-go/internal/censor"
	"errors"
)

type censorQueueProcessor struct {
	runtime *censorRuntime
	parent  context.Context
	engine  censor.Processor
}

func (rt *censorRuntime) queueProcessor(ctx context.Context, cfg censor.Config) (censor.Processor, error) {
	engine := rt.processor
	if engine == nil {
		var e error
		engine, e = censor.NewEngine(cfg)
		if e != nil {
			return nil, e
		}
	}
	return &censorQueueProcessor{runtime: rt, parent: ctx, engine: engine}, nil
}
func (p *censorQueueProcessor) Analyze(queueCtx context.Context, r censor.Request) (censor.Result, error) {
	// Even a recovered engine queue must not introduce local files, arbitrary
	// URLs or a candidate that is no longer present in this account's runtime.
	if r.Source.Path != "" || !censorHTTPS(r.Source.URL) || r.Reference != nil && (r.Reference.Path != "" || !censorHTTPS(r.Reference.URL)) {
		return censor.Result{}, errors.New("invalid bridge audio source")
	}
	p.runtime.mu.Lock()
	permitted := false
	for _, j := range p.runtime.disk.Jobs {
		if (j.Status != "pending" && j.Status != "running") || j.Request.OriginalURL != r.Source.URL {
			continue
		}
		if r.Reference == nil {
			permitted = true
			break
		}
		for _, c := range j.Candidates {
			if c.URL == r.Reference.URL && censorAudioCandidate(c) {
				permitted = true
				break
			}
		}
		if permitted {
			break
		}
	}
	p.runtime.mu.Unlock()
	if !permitted {
		return censor.Result{}, errors.New("audio source is not associated with this account")
	}
	ctx, cancel := context.WithCancel(p.parent)
	defer cancel()
	stop := context.AfterFunc(queueCtx, cancel)
	defer stop()
	return p.engine.Analyze(ctx, r)
}
