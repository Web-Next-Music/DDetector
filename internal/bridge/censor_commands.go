package bridge

import (
	"context"
	"errors"
	"time"
)

// Heavy commands outlive the HTTP request, but never the authenticated runtime.
// This keeps full-audio comparison inside the existing Qt/server request deadlines.
func (rt *censorRuntime) startCommand(id string, work func(context.Context, CensorIdentity) error) error {
	rt.mu.Lock()
	if rt.commandActive || rt.failure != "" {
		rt.mu.Unlock()
		return ErrCensorConflict
	}
	ctx, cancel := context.WithTimeout(rt.server.lifecycle, 30*time.Minute)
	rt.commandActive = true
	rt.commandCancel = cancel
	rt.commandDone = make(chan struct{})
	rt.lastError = ""
	done := rt.commandDone
	rt.mu.Unlock()
	go func() {
		identity, e := rt.identity(ctx)
		if e == nil {
			e = work(ctx, identity)
		}
		var optionalPersistenceError error
		if e == nil {
			if backend, ok := rt.backend.(CensorResultsBackend); ok {
				if job, jobErr := rt.job(id); jobErr == nil {
					if identity, authErr := rt.identity(ctx); authErr == nil {
						optionalPersistenceError = backend.SaveResults(ctx, identity, job)
					}
				}
			}
		}
		cancel()
		rt.mu.Lock()
		if e != nil {
			message := "Censor operation failed; verify backend/audio availability and retry"
			switch {
			case isCensorResourceError(e):
				message, _ = censorSettingsError(e)
			case errors.Is(e, ErrCensorConflict):
				message = "Censor selection or audio evidence is stale/insufficient; review and analyze again"
			case errors.Is(e, ErrCensorDenied):
				message = "Censor authorization was denied"
			case errors.Is(e, ErrCensorUnavailable):
				message = "Censor backend is unavailable"
			case errors.Is(e, context.Canceled):
				message = ""
			}
			if native := rt.nativeFailureMessage(e); native != "" && !errors.Is(e, context.Canceled) {
				message = native
			}
			rt.lastError = message
			if j, err := rt.jobLocked(id); err == nil {
				if j.Error == "" {
					j.Error = message
				}
				if j.Status == "running" {
					j.Status = "failed"
					if errors.Is(e, context.Canceled) {
						j.Status = "pending"
					}
				}
				_ = rt.persistLocked()
			}
		}
		if optionalPersistenceError != nil {
			rt.lastError = "Local results saved; optional remote persistence failed"
		}
		rt.commandActive = false
		rt.commandCancel = nil
		close(done)
		rt.mu.Unlock()
	}()
	return nil
}
func (rt *censorRuntime) validateApproval(j CensorJob, candidateID string) error {
	if j.Status == "succeeded" || j.Status == "local_saved" {
		if j.SelectedCandidate == candidateID && j.Replacement != nil {
			return nil
		}
		return ErrCensorConflict
	}
	if j.Status != "review_required" || j.PublicationKey != "" && j.SelectedCandidate != candidateID {
		return ErrCensorConflict
	}
	for _, c := range j.Candidates {
		if c.ID == candidateID && rt.suitableCandidate(c) {
			return nil
		}
	}
	return ErrCensorConflict
}
func (rt *censorRuntime) manualSearch(ctx context.Context, i CensorIdentity, j CensorJob, query string) error {
	if err := rt.resolveRequest(ctx, i, &j); err != nil {
		return err
	}
	if e := rt.search(ctx, i, &j, []string{query}); e != nil {
		return e
	}
	// Preserve original DSP evidence for page-only searches, but discard the
	// previous candidate's alignment, restoration and reference transcripts.
	if j.Result != nil && j.Result.Comparison != nil {
		j.Result.Comparison = nil
		j.Result.Transcript = ""
		j.Result.ExcerptTranscripts = nil
		for n := range j.Result.SuspiciousIntervals {
			if j.Result.SuspiciousIntervals[n].Restoration != nil {
				j.Result.SuspiciousIntervals[n].Restoration = nil
				j.Result.SuspiciousIntervals[n].Status = "unverified"
			}
		}
	}
	if e := rt.update(j); e != nil {
		return e
	}
	if e := rt.compareCandidates(ctx, nil, &j); e != nil {
		return e
	}
	j.Status = "review_required"
	j.Stage = "review"
	j.Error = ""
	return rt.update(j)
}
