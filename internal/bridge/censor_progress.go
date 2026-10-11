package bridge

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
	return rt.update(*j)
}

// Approval revalidation must not replace the stored job with temporary search data.
func (rt *censorRuntime) searchProgress(j *CensorJob, completed, total int) error {
	if j.Status != "running" || j.ID == "" {
		return nil
	}
	return rt.stageProgress(j, "search", completed, total)
}
