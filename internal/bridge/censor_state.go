package bridge

import (
	"ddetector-go/internal/censor"
	"encoding/json"
	"math"
	"net/http"
)

const censorUIPageSize = 100
const censorUISummaryBytes = 64 << 10

type censorUIInterval struct {
	censor.SuspiciousInterval
	Label string `json:"label"`
}
type censorUIWaveform struct {
	Samples []float64              `json:"samples"`
	Points  []censor.WaveformPoint `json:"points"`
}
type censorUIResult struct {
	*censor.Result
	Waveform            any                `json:"waveform"`
	SuspiciousIntervals []censorUIInterval `json:"suspicious_intervals"`
	OmittedFields       []string           `json:"omitted_fields,omitempty"`
}
type censorUIJob struct {
	CensorJob
	Result         *censorUIResult `json:"result,omitempty"`
	DetailsOmitted bool            `json:"details_omitted"`
}
type censorUIState struct {
	CensorState
	Jobs       []censorUIJob `json:"jobs"`
	Offset     int           `json:"offset"`
	NextOffset int           `json:"next_offset"`
	JobsTotal  int           `json:"jobs_total"`
}

// Projection never alters stored evidence or approval inputs. Large optional
// media/text fields are omitted explicitly; comparisons and intervals survive.
func censorResultView(r *censor.Result, compact bool) *censorUIResult {
	if r == nil {
		return nil
	}
	copy := *r
	omitted := []string{}
	if compact {
		if len(copy.Transcript) > 0 {
			copy.Transcript = ""
			omitted = append(omitted, "transcript")
		}
		if len(copy.ExcerptTranscripts) > 0 {
			copy.ExcerptTranscripts = nil
			omitted = append(omitted, "excerpt_transcripts")
		}
		if copy.Fingerprint != nil {
			copy.Fingerprint = nil
			omitted = append(omitted, "fingerprint")
		}
		if len(copy.Waveform) > 0 {
			copy.Waveform = nil
			omitted = append(omitted, "waveform")
		}
	}
	intervals := []censorUIInterval{}
	for _, interval := range copy.SuspiciousIntervals {
		intervals = append(intervals, censorUIInterval{interval, interval.Reason})
	}
	var waveform any
	if len(copy.Waveform) > 0 {
		samples := make([]float64, len(copy.Waveform))
		for n, point := range copy.Waveform {
			samples[n] = math.Min(1, math.Max(math.Abs(point.Min), math.Abs(point.Max)))
		}
		waveform = censorUIWaveform{samples, copy.Waveform}
	}
	return &censorUIResult{Result: &copy, Waveform: waveform, SuspiciousIntervals: intervals, OmittedFields: omitted}
}
func censorJobView(job CensorJob, summary bool) censorUIJob {
	// Skip optional heavy projections before allocating/serializing them.
	hint := censorResultSizeHint(job.Result)
	for _, candidate := range job.Candidates {
		hint += censorResultSizeHint(candidate.Result)
	}
	if summary && hint > censorUISummaryBytes {
		job.Result = nil
		job.Candidates = []CensorCandidate{}
		job.Queries = nil
		return censorUIJob{CensorJob: job, DetailsOmitted: true}
	}
	compact := !summary && hint > 14<<20
	item := censorUIJob{CensorJob: job, Result: censorResultView(job.Result, compact), DetailsOmitted: compact}
	if compact {
		item.Candidates = append([]CensorCandidate{}, job.Candidates...)
		for n := range item.Candidates {
			view := censorResultView(item.Candidates[n].Result, true)
			if view != nil {
				item.Candidates[n].Result = view.Result
			}
		}
	}
	// Small jobs retain the legacy inline view; large summaries contain no audio
	// results. Qt always fetches selected detail, regardless of inline availability.
	data, err := json.Marshal(item)
	if summary && (err != nil || len(data) > censorUISummaryBytes) {
		item.Result = nil
		item.CensorJob.Result = nil
		item.Candidates = []CensorCandidate{}
		item.Queries = nil
		item.DetailsOmitted = true
	} else if !summary && len(data) > 15<<20 {
		item.Result = censorResultView(job.Result, true)
		item.Candidates = append([]CensorCandidate{}, job.Candidates...)
		for n := range item.Candidates {
			view := censorResultView(item.Candidates[n].Result, true)
			if view != nil {
				item.Candidates[n].Result = view.Result
			}
		}
		item.DetailsOmitted = true
	}
	return item
}
func censorResultSizeHint(r *censor.Result) int {
	if r == nil {
		return 0
	}
	size := len(r.Transcript)*6 + len(r.Waveform)*256 + len(r.SuspiciousIntervals)*1024
	for _, excerpt := range r.ExcerptTranscripts {
		size += len(excerpt.Text)*6 + 256
	}
	for _, warning := range r.Warnings {
		size += len(warning) * 6
	}
	if r.Fingerprint != nil {
		size += len(r.Fingerprint.Values) * 12
	}
	return size
}

func sendCensorState(w http.ResponseWriter, status int, state CensorState) {
	sendCensorPage(w, status, state, 0)
}
func sendCensorPage(w http.ResponseWriter, status int, state CensorState, offset int) {
	total := len(state.Jobs)
	out := censorUIState{CensorState: state, Jobs: []censorUIJob{}, Offset: offset, NextOffset: -1, JobsTotal: total}
	out.CensorState.Jobs = nil
	end := min(total, offset+censorUIPageSize)
	for n := offset; n < end; n++ {
		out.Jobs = append(out.Jobs, censorJobView(state.Jobs[n], true))
	}
	if end < total {
		out.NextOffset = end
	}
	sendCensorJSON(w, status, out)
}
func sendCensorJSON(w http.ResponseWriter, status int, out any) {
	data, err := json.Marshal(out)
	if err != nil || len(data)+1 > 16<<20 {
		failure(w, 502, "censor view exceeds response capacity or contains invalid analysis data")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(data, '\n'))
}
