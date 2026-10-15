package bridge

import (
	"context"
	"ddetector-go/internal/censor"
	"encoding/json"
	"errors"
	"math"
	"os/exec"
	"path/filepath"
	"strings"

	"time"
)

var ErrCensorSettingsBusy = errors.New("Censor settings are waiting for active processing to finish")

type CensorSettings struct {
	Workers    int    `json:"workers"`
	Mode       string `json:"mode"`
	CPUThreads int    `json:"cpu_threads"`

	CandidateLimit int     `json:"candidate_limit"`
	MinConfidence  float64 `json:"min_confidence"`
}
type censorSettingsPatch struct {
	Workers    *int    `json:"workers,omitempty"`
	Mode       *string `json:"mode,omitempty"`
	CPUThreads *int    `json:"cpu_threads,omitempty"`

	CandidateLimit *int     `json:"candidate_limit,omitempty"`
	MinConfidence  *float64 `json:"min_confidence,omitempty"`
}

func defaultCensorSettings() CensorSettings {
	return CensorSettings{Workers: 2, Mode: "balanced", CPUThreads: 1, CandidateLimit: 20, MinConfidence: .75}
}

// Fill absent fields in the earlier workers-only persisted schema. Explicit
// false/zero values remain explicit and must pass validation, not be defaulted.
func (s *CensorSettings) UnmarshalJSON(data []byte) error {
	type plain CensorSettings
	value := plain(defaultCensorSettings())
	if e := json.Unmarshal(data, &value); e != nil {
		return e
	}
	*s = CensorSettings(value)
	return nil
}
func (p censorSettingsPatch) empty() bool { return p == censorSettingsPatch{} }
func (p censorSettingsPatch) merge(s CensorSettings) CensorSettings {

	if p.Mode != nil {
		s.Mode = *p.Mode
		switch s.Mode {
		case "eco":
			s.Workers = 1
		case "balanced":
			s.Workers = 2
		case "performance":
			s.Workers = 4
		}
	}

	if p.Workers != nil {
		s.Workers = *p.Workers
	}
	if p.CPUThreads != nil {
		s.CPUThreads = *p.CPUThreads
	}

	if p.CandidateLimit != nil {
		s.CandidateLimit = *p.CandidateLimit
	}
	if p.MinConfidence != nil {
		s.MinConfidence = *p.MinConfidence
	}

	return s
}
func validateCensorSettings(s CensorSettings) error {

	if s.Workers < 1 || s.Workers > 32 || s.CPUThreads < 1 || s.CPUThreads > 64 || s.CandidateLimit < 1 || s.CandidateLimit > 100 || math.IsNaN(s.MinConfidence) || math.IsInf(s.MinConfidence, 0) || s.MinConfidence <= 0 || s.MinConfidence > 1 {
		return ErrCensorInvalid
	}
	switch s.Mode {
	case "eco", "balanced", "performance":
	default:
		return ErrCensorInvalid
	}

	return nil
}

type censorResourceError struct {
	message     string
	unavailable bool
}

func (e *censorResourceError) Error() string { return e.message }
func (e *censorResourceError) Unwrap() error {
	if e.unavailable {
		return ErrCensorUnavailable
	}
	return ErrCensorUnsupported
}
func censorResource(message string) error { return &censorResourceError{message: message} }
func censorMissing(message string) error {
	return &censorResourceError{message: message, unavailable: true}
}

func buildCensorConfig(ctx context.Context, s CensorSettings, dir string) (censor.Config, error) {
	var c censor.Config
	if e := ctx.Err(); e != nil {
		return c, e
	}
	if e := validateCensorSettings(s); e != nil {
		return c, e
	}

	c = censor.Config{StateDir: dir, Workers: s.Workers, CPUThreads: s.CPUThreads, MaxJobs: 512, MaxBytes: 256 << 20, MaxDuration: 30 * time.Minute, JobTimeout: 10 * time.Minute, HTTPTimeout: 90 * time.Second, SampleRate: 16000}
	switch s.Mode {
	case "eco":
		c.MaxWaveformPoints = 256

	case "balanced":
		c.MaxWaveformPoints = 1024

	case "performance":
		c.MaxWaveformPoints = 2048

	}
	// Fingerprints are optional: only enable the actual installed executable.
	if path, e := exec.LookPath("fpcalc"); e == nil {
		c.FPcalcCommand = path
	}
	return c, nil
}

func (rt *censorRuntime) settings() CensorSettings {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.disk.Settings
}
func (rt *censorRuntime) applySettings(ctx context.Context, p censorSettingsPatch) error {
	rt.mu.Lock()
	if rt.running || rt.commandActive {
		rt.mu.Unlock()
		return ErrCensorSettingsBusy
	}
	old := rt.disk.Settings
	rt.mu.Unlock()
	next := p.merge(old)
	if _, e := buildCensorConfig(ctx, next, filepath.Join(rt.dir, "engine")); e != nil {
		return e
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.running || rt.commandActive {
		return ErrCensorSettingsBusy
	}
	rt.disk.Settings = next
	if e := rt.persistLocked(); e != nil {
		rt.disk.Settings = old
		return e
	}
	return nil
}
func (rt *censorRuntime) configuredRequest(original, reference string) censor.Request {
	request := censor.Request{Source: censor.Source{URL: original}}
	if reference != "" {
		request.Reference = &censor.Source{URL: reference}
	}
	return request
}
func censorAudioCandidate(c CensorCandidate) bool {
	if strings.HasPrefix(c.ID, webCandidatePrefix) {
		return false
	}
	if !c.FullAudio || !c.Downloadable || !censorHTTPS(c.URL) {
		return false
	}
	kind := strings.ToLower(strings.TrimSpace(c.Metadata["kind"]))
	mediaType := strings.ToLower(strings.TrimSpace(c.Metadata["content_type"]))
	return kind != "page" && kind != "preview" && !strings.HasPrefix(mediaType, "text/")
}
func (rt *censorRuntime) suitableCandidate(c CensorCandidate) bool {
	return censorAudioCandidate(c) && strongCensorComparison(c.Result) && c.Result.Comparison.Confidence >= rt.settings().MinConfidence
}
func censorSettingsError(err error) (string, bool) {
	var resource *censorResourceError
	if errors.As(err, &resource) {
		return resource.Error(), true
	}
	return "", false
}
func isCensorResourceError(err error) bool {
	_, ok := censorSettingsError(err)
	return ok
}
