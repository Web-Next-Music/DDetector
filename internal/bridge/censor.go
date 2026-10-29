package bridge

import (
	"context"
	"ddetector-go/internal/censor"
	"errors"
	"strings"
	"unicode"
)

// CensorIdentity is server-derived. AccessToken must never be returned to HTTP clients.
// Backends must apply their own censor authorization/RLS, not assume DDetector access
// grants permission to publish replacements.
type CensorIdentity struct{ UserID, AccessToken string }
type CensorRequest struct {
	OriginalUnavailable bool   `json:"original_unavailable,omitempty"`
	Origin              string `json:"origin,omitempty"`
	ID                  string `json:"id"`
	TrackID             int64  `json:"track_id"`
	Title               string `json:"title"`
	Artist              string `json:"artist"`
	OriginalURL         string `json:"original_url"`
}
type CensorCandidate struct {
	ID              string            `json:"id"`
	Title           string            `json:"title"`
	Artist          string            `json:"artist"`
	URL             string            `json:"url"`
	Provider        string            `json:"provider,omitempty"`
	DurationSeconds float64           `json:"duration_seconds,omitempty"`
	FullAudio       bool              `json:"full_audio"`
	Downloadable    bool              `json:"downloadable"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	Result          *censor.Result    `json:"result,omitempty"`
	Error           string            `json:"error,omitempty"`
}
type CensorSelection struct {
	RequestID      string `json:"request_id"`
	TrackID        int64  `json:"track_id"`
	CandidateID    string `json:"candidate_id"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}
type CensorReplacement struct {
	LocalOnly        bool   `json:"local_only"`
	LocalPath        string `json:"local_path,omitempty"`
	GitHubPublished  bool   `json:"github_published"`
	RequestCompleted bool   `json:"request_completed"`
	Scope            string `json:"scope,omitempty"`
	CandidateID      string `json:"candidate_id,omitempty"`
	RequestID        string `json:"request_id"`
	TrackID          int64  `json:"track_id"`
	URL              string `json:"url"`
	Durable          bool   `json:"durable"`
	Atomic           bool   `json:"atomic"`
	IdempotencyKey   string `json:"idempotency_key,omitempty"`
}

// CensorBackend supplies real requests, full-audio search and durable publication;
// internal/censor owns analysis, not the request source.
// Requests reads a real request source, never the published replacement catalog.
// Candidate IDs are opaque, scoped to the caller, and must be revalidated by Replace.
// Replace must verify request/track association, candidate provenance and permissions,
// publish atomically/idempotently, and return only after durable publication. It owns
// MP3 validation and storage; the bridge accepts only backend-provenanced audio URLs.
type CensorBackend interface {
	Requests(context.Context, CensorIdentity) ([]CensorRequest, error)
	Search(context.Context, CensorIdentity, string) ([]CensorCandidate, error)
	Replace(context.Context, CensorIdentity, CensorSelection) (CensorReplacement, error)
}

var (
	ErrCensorDenied   = errors.New("censor access denied")
	ErrCensorConflict = errors.New("censor selection is stale")
	ErrCensorNotFound = errors.New("censor item not found")
)

// SetCensorBackend configures the source before the first censor operation.
// Once a runtime owns jobs, swapping backend provenance is not safe.
func (s *Server) SetCensorBackend(b CensorBackend) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.censorRuntime != nil {
		return ErrCensorConflict
	}
	s.censor = b
	return nil
}

// CensorReceiptBackend optionally reads an authoritative publication receipt under
// the current user's authorization. Lookup must not mutate. ErrCensorNotFound means
// no receipt is available, not proof of no commit; ErrCensorUnsupported means the
// backend cannot provide this lookup. Receipts must bind the complete selection.
type CensorReceiptBackend interface {
	ReplacementReceipt(context.Context, CensorIdentity, CensorSelection) (CensorReplacement, error)
}

// Rollback is optional; an unsupported rollback is reported, never simulated.
type CensorRollbackBackend interface {
	Rollback(context.Context, CensorIdentity, CensorSelection) (CensorReplacement, error)
}

// Optional remote persistence of reviewed analysis; local persistence is always used.
type CensorResultsBackend interface {
	SaveResults(context.Context, CensorIdentity, CensorJob) error
}

func censorID(v string) bool {
	if v == "" || len(v) > 256 || strings.TrimSpace(v) != v {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
