package censor

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var (
	ErrClosed    = errors.New("censor: queue closed")
	ErrQueueFull = errors.New("censor: queue full")
	ErrNotFound  = errors.New("censor: job not found")
)

type Source struct {
	Path string `json:"path,omitempty"`
	URL  string `json:"url,omitempty"`
}
type Request struct {
	Source     Source  `json:"source"`
	Reference  *Source `json:"reference,omitempty"`
	Transcribe bool    `json:"transcribe,omitempty"` // Deprecated and ignored; retained for persisted request compatibility.
}
type Config struct {
	StateDir string
	FFmpeg   string
	Workers  int
	// Per external command, not a process-wide Go concurrency limit. Zero defaults to one.
	CPUThreads  int
	MaxJobs     int
	MaxBytes    int64
	MaxDuration time.Duration
	SampleRate  int
	HTTPTimeout time.Duration
	JobTimeout  time.Duration
	// Private/loopback HTTP addresses are rejected unless explicitly enabled.
	AllowPrivateHTTP       bool
	MaxWaveformPoints      int
	MaxSuspiciousIntervals int
	MaxTranscriptBytes     int // Legacy persisted-result size budget.
	// Empty disables Chromaprint. Usually "fpcalc"; executable and model assets are caller-owned.
	FPcalcCommand       string
	MaxFingerprintBytes int
}

type Comparison struct {
	// Positive means the source content occurs later than the reference content.
	OffsetSeconds         float64  `json:"offset_seconds"`
	Correlation           float64  `json:"correlation"`
	Confidence            float64  `json:"confidence"`
	OverlapSeconds        float64  `json:"overlap_seconds"`
	Comparable            bool     `json:"comparable"`
	SpectralSimilarity    float64  `json:"spectral_similarity"`
	SpectralMatchFraction float64  `json:"spectral_match_fraction"`
	ConfidenceClass       string   `json:"confidence_class"`
	FingerprintSimilarity *float64 `json:"fingerprint_similarity,omitempty"`
}
type WaveformPoint struct {
	StartSeconds float64 `json:"start_seconds"`
	EndSeconds   float64 `json:"end_seconds"`
	Min          float64 `json:"min"`
	Max          float64 `json:"max"`
	RMS          float64 `json:"rms"`
}
type SuspiciousInterval struct {
	StartSeconds float64              `json:"start_seconds"`
	EndSeconds   float64              `json:"end_seconds"`
	Reason       string               `json:"reason"`
	Confidence   float64              `json:"confidence"`
	Status       string               `json:"status"`
	Restoration  *RestorationEvidence `json:"restoration,omitempty"`
}
type RestorationEvidence struct {
	ReferenceStartSeconds float64 `json:"reference_start_seconds"`
	ReferenceEndSeconds   float64 `json:"reference_end_seconds"`
	Confidence            float64 `json:"confidence"`
	Reason                string  `json:"reason"`
}
type Fingerprint struct {
	Algorithm       string   `json:"algorithm"`
	DurationSeconds float64  `json:"duration_seconds"`
	AnalyzedSeconds float64  `json:"analyzed_seconds"`
	Values          []uint32 `json:"values"`
}

type ExcerptTranscript struct {
	StartSeconds float64 `json:"start_seconds"`
	EndSeconds   float64 `json:"end_seconds"`
	Source       string  `json:"source"`
	Text         string  `json:"text"`
	Device       string  `json:"device"`
	GPUBackend   string  `json:"gpu_backend,omitempty"`
	GPUVerified  bool    `json:"gpu_verified"`
}

type Result struct {
	DurationSeconds     float64              `json:"duration_seconds"`
	Waveform            []WaveformPoint      `json:"waveform"`
	SuspiciousIntervals []SuspiciousInterval `json:"suspicious_intervals"`
	IntervalsTruncated  bool                 `json:"intervals_truncated"`
	Fingerprint         *Fingerprint         `json:"fingerprint,omitempty"`
	ExcerptTranscripts  []ExcerptTranscript  `json:"excerpt_transcripts,omitempty"` // Legacy persisted evidence.

	Comparison *Comparison `json:"comparison,omitempty"`
	Transcript string      `json:"transcript,omitempty"` // Legacy persisted evidence.

	Warnings []string `json:"warnings,omitempty"`
}
type Status string

const (
	Pending   Status = "pending"
	Running   Status = "running"
	Succeeded Status = "succeeded"
	Failed    Status = "failed"
	Cancelled Status = "cancelled"
)

type Job struct {
	ID        string    `json:"id"`
	Request   Request   `json:"request"`
	Status    Status    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Result    *Result   `json:"result,omitempty"`
	Error     string    `json:"error,omitempty"`
}

type Processor interface {
	Analyze(context.Context, Request) (Result, error)
}

func normalize(c Config) (Config, error) {
	if c.FFmpeg == "" {
		c.FFmpeg = "ffmpeg"
		if runtime.GOOS == "windows" {
			if executable, err := os.Executable(); err == nil {
				bundled := filepath.Join(filepath.Dir(executable), "ffmpeg.exe")
				if info, err := os.Stat(bundled); err == nil && !info.IsDir() {
					c.FFmpeg = bundled
				}
			}
		}
	}
	if c.CPUThreads == 0 {
		c.CPUThreads = 1
	}
	if c.CPUThreads < 1 || c.CPUThreads > 64 {
		return c, errors.New("censor: CPUThreads must be between 1 and 64")
	}

	if c.Workers == 0 {
		c.Workers = 2
	}
	if c.MaxJobs == 0 {
		c.MaxJobs = 256
	}
	if c.MaxBytes == 0 {
		c.MaxBytes = 256 << 20
	}
	if c.MaxDuration == 0 {
		c.MaxDuration = 30 * time.Minute
	}
	if c.SampleRate == 0 {
		c.SampleRate = 16000
	}
	if c.HTTPTimeout == 0 {
		c.HTTPTimeout = 2 * time.Minute
	}
	if c.JobTimeout == 0 {
		c.JobTimeout = 10 * time.Minute
	}
	if c.MaxTranscriptBytes == 0 {
		c.MaxTranscriptBytes = 1 << 20
	}
	if c.Workers < 1 || c.Workers > 32 || c.MaxJobs < 1 || c.MaxJobs > 10000 || c.MaxBytes < 1024 || c.MaxBytes > 16<<30 || c.MaxDuration < time.Second || c.MaxDuration > 24*time.Hour || c.SampleRate < 8000 || c.SampleRate > 48000 || c.HTTPTimeout <= 0 || c.JobTimeout <= 0 || c.MaxTranscriptBytes < 1 || c.MaxTranscriptBytes > 16<<20 {
		return c, errors.New("censor: invalid resource limits")
	}
	if c.MaxWaveformPoints == 0 {
		c.MaxWaveformPoints = 1024
	}
	if c.MaxSuspiciousIntervals == 0 {
		c.MaxSuspiciousIntervals = 128
	}
	if c.MaxFingerprintBytes == 0 {
		c.MaxFingerprintBytes = 65536
	}
	if c.MaxWaveformPoints < 1 || c.MaxWaveformPoints > 2048 || c.MaxSuspiciousIntervals < 1 || c.MaxSuspiciousIntervals > 128 || c.MaxFingerprintBytes < 1024 || c.MaxFingerprintBytes > 131072 {
		return c, errors.New("censor: invalid analysis limits")
	}

	return c, nil
}
func validateSource(s Source) error {
	if (s.Path == "") == (s.URL == "") {
		return errors.New("censor: exactly one of path or URL required")
	}
	if len(s.Path) > 4096 || len(s.URL) > 8192 || strings.ContainsRune(s.Path, 0) {
		return errors.New("censor: source identifier exceeds limit or contains NUL")
	}
	if s.URL != "" {
		u, err := url.Parse(s.URL)
		if err != nil {
			return err
		}
		return validateURL(u)
	}
	return nil
}
func validateRequest(r Request) error {
	if err := validateSource(r.Source); err != nil {
		return err
	}
	if r.Reference != nil {
		return validateSource(*r.Reference)
	}
	return nil
}
