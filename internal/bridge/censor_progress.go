package bridge

import (
	"encoding/json"
	"time"
)

func newStageProgress() map[string]float64 {
	return map[string]float64{"analysis": 0, "search": 0, "comparison": 0}
}

func cloneStageProgress(progress map[string]float64) map[string]float64 {
	if progress == nil {
		return nil
	}
	copy := make(map[string]float64, len(progress))
	for stage, value := range progress {
		copy[stage] = value
	}
	return copy
}

func (rt *censorRuntime) persistProgressAfterDelay(generation uint64) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if generation != rt.progressGeneration || !rt.progressDirty {
		return
	}
	// persistLocked records/cancels failures exactly like synchronous updates.
	_ = rt.persistLocked()
}

func (rt *censorRuntime) updateProgress(j CensorJob) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	current, err := rt.jobLocked(j.ID)
	if err != nil {
		return err
	}
	j.UpdatedAt = time.Now().UTC()
	cloned := cloneCensorJob(j)
	// Preserve the per-job bound without encoding or admitting the full runtime.
	// Aggregate admission is performed by the eventual durable write and by every
	// crash-critical synchronous update.
	data, err := json.Marshal(cloned)
	if err != nil {
		return err
	}
	limit := censorJobLimit
	if j.PublicationKey == "" && (j.Status == "running" || j.Status == "review_required") {
		limit -= 16 << 10
	}
	if len(data) > limit {
		return ErrCensorCapacity
	}
	*current = cloned
	rt.progressDirty = true
	rt.progressGeneration++
	generation := rt.progressGeneration
	if rt.progressTimer != nil {
		rt.progressTimer.Stop()
	}
	rt.progressTimer = time.AfterFunc(censorProgressDebounce, func() {
		rt.persistProgressAfterDelay(generation)
	})
	return nil
}

func (rt *censorRuntime) flushProgress() error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if !rt.progressDirty {
		return nil
	}
	return rt.persistLocked()
}

// A completed unit is an attempted operation, not necessarily a successful one.
// An empty stage is complete because it has no work remaining.
func (rt *censorRuntime) stageProgress(j *CensorJob, stage string, completed, total int) error {
	if j.StageProgress == nil {
		j.StageProgress = newStageProgress()
	}
	value := 1.0
	if total > 0 {
		value = float64(completed) / float64(total)
	}
	j.StageProgress[stage] = min(1, max(0, value))
	return rt.updateProgress(*j)
}

// Approval revalidation must not replace the stored job with temporary search data.
func (rt *censorRuntime) searchProgress(j *CensorJob, completed, total int) error {
	if j.Status != "running" || j.ID == "" {
		return nil
	}
	return rt.stageProgress(j, "search", completed, total)
}
