package bridge

import (
	"crypto/sha256"
	"ddetector-go/internal/censor"
	"encoding"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"sync"
	"time"
)

const censorUIPageSize = 100
const censorUISummaryBytes = 64 << 10

type censorUIWaveform struct {
	Samples []float64              `json:"samples"`
	Points  []censor.WaveformPoint `json:"points"`
}
type censorUIResult struct {
	*censor.Result
	Waveform      any      `json:"waveform"`
	OmittedFields []string `json:"omitted_fields,omitempty"`
}
type censorUIJob struct {
	CensorJob
	Result         *censorUIResult `json:"result,omitempty"`
	DetailsOmitted bool            `json:"details_omitted"`
	DetailRevision string          `json:"detail_revision,omitempty"`
	encoded        []byte
}
type censorUIState struct {
	CensorState
	Jobs       []censorUIJob `json:"jobs"`
	Offset     int           `json:"offset"`
	NextOffset int           `json:"next_offset"`
	JobsTotal  int           `json:"jobs_total"`
}

// Projection never alters stored evidence or approval inputs. Large optional
// media/text fields are omitted explicitly; recording comparisons survive.
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
	var waveform any
	if len(copy.Waveform) > 0 {
		samples := make([]float64, len(copy.Waveform))
		for n, point := range copy.Waveform {
			samples[n] = math.Min(1, math.Max(math.Abs(point.Min), math.Abs(point.Max)))
		}
		waveform = censorUIWaveform{samples, copy.Waveform}
	}
	return &censorUIResult{Result: &copy, Waveform: waveform, OmittedFields: omitted}
}

// Keep a whole page plus selected/off-page jobs, rather than churning a tiny
// cache on every page poll. Each key is admitted by a bounded structural walk;
// neither evidence size nor cache miss count can cause an unbounded marshal.
const censorViewCacheEntries = 2 * censorUIPageSize
const censorViewJobBudget = censorUISummaryBytes

var censorViews struct {
	sync.Mutex
	entries map[censorViewKey]*censorViewEntry
	order   []censorViewKey
}

type censorViewKey struct {
	id   string
	full bool
}
type censorViewEntry struct {
	job             CensorJob
	summary, detail censorUIJob
}

// This deliberately overestimates the JSON and retained data size without
// reading string contents or walking oversized containers. Unknown/custom
// encoders fail closed; time.Time is the only custom encoder in CensorJob.
// Every visited node consumes budget, bounding traversal as well as memory.
func censorViewFits(job CensorJob) bool {
	budget := censorViewJobBudget
	return censorViewFitsValue(reflect.ValueOf(job), &budget)
}
func censorViewFitsValue(v reflect.Value, budget *int) bool {
	const nodeCost = 64
	if *budget < nodeCost {
		return false
	}
	*budget -= nodeCost
	if v.Type() == reflect.TypeOf(time.Time{}) {
		return true
	}
	if v.Type().Implements(reflect.TypeOf((*json.Marshaler)(nil)).Elem()) ||
		v.Type().Implements(reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()) {
		return false
	}
	switch v.Kind() {
	case reflect.Pointer:
		return v.IsNil() || censorViewFitsValue(v.Elem(), budget)
	case reflect.String:
		if v.Len() > *budget/6 {
			return false
		}
		*budget -= v.Len() * 6
	case reflect.Struct:
		for n := 0; n < v.NumField(); n++ {
			field := v.Type().Field(n)
			if !field.IsExported() {
				continue
			}
			if len(field.Tag) > *budget/6 {
				return false
			}
			*budget -= len(field.Tag) * 6
			if !censorViewFitsValue(v.Field(n), budget) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		if v.Len() > *budget/nodeCost {
			return false
		}
		for n := 0; n < v.Len(); n++ {
			if !censorViewFitsValue(v.Index(n), budget) {
				return false
			}
		}
	case reflect.Map:
		if v.Len() > *budget/(2*nodeCost) {
			return false
		}
		it := v.MapRange()
		for it.Next() {
			if !censorViewFitsValue(it.Key(), budget) || !censorViewFitsValue(it.Value(), budget) {
				return false
			}
		}
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
	default:
		return false
	}
	return true
}

func censorSummaryJob(job CensorJob) CensorJob {
	job.Result = nil
	job.Candidates = []CensorCandidate{}
	job.Queries = nil
	return job
}

func (v censorUIJob) MarshalJSON() ([]byte, error) {
	if v.encoded != nil {
		return v.encoded, nil
	}
	type plain censorUIJob
	return json.Marshal(plain(v))
}

func censorJobView(job CensorJob, summary bool) censorUIJob {
	full := censorViewFits(job)
	if !full {
		if !summary {
			// No authoritative revision exists for legacy mutable jobs. Never
			// suppress detail polling using an incomplete evidence signature.
			return buildCensorJobView(job, false, "")
		}
		job = censorSummaryJob(job)
		if !censorViewFits(job) {
			// Even metadata is oversized: retain the existing response-capacity
			// failure behavior, but never serialize the omitted evidence.
			return censorUIJob{CensorJob: job, DetailsOmitted: true}
		}
	}
	key := censorViewKey{job.ID, full}
	censorViews.Lock()
	cached := censorViews.entries[key]
	censorViews.Unlock()
	// Entries are immutable. Equality, cloning, projection, serialization and
	// SHA all run outside the global map lock. Concurrent misses may duplicate
	// bounded work, but cannot publish a revision for a different snapshot.
	if cached != nil && reflect.DeepEqual(cached.job, job) {
		if summary {
			return cached.summary
		}
		return cached.detail
	}
	snapshot := cloneCensorJob(job)
	entry := &censorViewEntry{job: snapshot}
	if full {
		raw, err := json.Marshal(snapshot)
		if err != nil {
			return buildCensorJobView(job, summary, "")
		}
		revision := fmt.Sprintf("%x", sha256.Sum256(raw))
		entry.summary = buildCensorJobView(snapshot, true, revision)
		if entry.summary.DetailsOmitted {
			entry.detail = buildCensorJobView(snapshot, false, revision)
		} else {
			entry.detail = entry.summary
		}
	} else {
		entry.summary = censorUIJob{CensorJob: snapshot, DetailsOmitted: true}
	}
	if entry.summary.encoded == nil {
		data, err := json.Marshal(entry.summary)
		if err != nil {
			return entry.summary
		}
		entry.summary.encoded = data
	}
	if full && entry.detail.encoded == nil {
		data, err := json.Marshal(entry.detail)
		if err != nil {
			return entry.detail
		}
		entry.detail.encoded = data
	}
	censorViews.Lock()
	if censorViews.entries == nil {
		censorViews.entries = make(map[censorViewKey]*censorViewEntry)
	}
	if _, exists := censorViews.entries[key]; !exists {
		if len(censorViews.order) == censorViewCacheEntries {
			delete(censorViews.entries, censorViews.order[0])
			censorViews.order = censorViews.order[1:]
		}
		censorViews.order = append(censorViews.order, key)
	}
	censorViews.entries[key] = entry
	censorViews.Unlock()
	if summary {
		return entry.summary
	}
	return entry.detail
}

func buildCensorJobView(job CensorJob, summary bool, revision string) censorUIJob {
	// Skip optional heavy projections before allocating/serializing them.
	hint := censorResultSizeHint(job.Result)
	for _, candidate := range job.Candidates {
		hint += censorResultSizeHint(candidate.Result)
	}
	if summary && hint > censorUISummaryBytes {
		job.Result = nil
		job.Candidates = []CensorCandidate{}
		job.Queries = nil
		return censorUIJob{CensorJob: job, DetailsOmitted: true, DetailRevision: revision}
	}
	compact := !summary && hint > 14<<20
	item := censorUIJob{CensorJob: job, Result: censorResultView(job.Result, compact), DetailsOmitted: compact, DetailRevision: revision}
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
	} else if err == nil {
		item.encoded = data
	}
	return item
}
func censorResultSizeHint(r *censor.Result) int {
	if r == nil {
		return 0
	}
	size := len(r.Transcript)*6 + len(r.Waveform)*256
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
