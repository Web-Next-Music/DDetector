package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const censorRemoteLimit = 8 << 20

var ErrCensorUnavailable = errors.New("censor backend unavailable")
var ErrCensorUnsupported = errors.New("censor operation unsupported")

type CensorRemoteBackend struct {
	base         string
	client       *http.Client
	mu           sync.Mutex
	capabilities map[string]CensorRemoteState
}
type CensorRemoteState struct {
	Configured         *bool `json:"configured,omitempty"`
	Rollback           bool  `json:"rollback"`
	ResultsPersistence bool  `json:"results_persistence"`
	Known              bool  `json:"-"`
}

// NewCensorRemoteBackend uses only the current user's JWT. No Supabase apikey or
// privileged credential is sent. Redirects are disabled to keep the JWT on origin.
func NewCensorRemoteBackend(base string) (*CensorRemoteBackend, error) {
	if !censorHTTPS(base) {
		return nil, errors.New("FCKCENSOR_API_URL must be an HTTPS URL without credentials or query")
	}
	u, _ := url.Parse(base)
	if u.RawQuery != "" {
		return nil, errors.New("FCKCENSOR_API_URL must not contain a query")
	}
	return &CensorRemoteBackend{base: strings.TrimRight(base, "/"), client: &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, capabilities: map[string]CensorRemoteState{}}, nil
}
func censorHTTPS(raw string) bool {
	if len(raw) == 0 || len(raw) > 8192 {
		return false
	}
	u, e := url.Parse(raw)
	return e == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == "" && u.Opaque == ""
}
func (b *CensorRemoteBackend) callOnce(ctx context.Context, i CensorIdentity, method, path string, in, out any, key string) error {
	if i.AccessToken == "" || i.UserID == "" {
		return ErrCensorDenied
	}
	data, e := json.Marshal(in)
	if e != nil {
		return e
	}
	if len(data) > censorRemoteLimit {
		return errors.New("censor request too large")
	}
	var body io.Reader
	if in != nil {
		body = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, method, b.base+path, body)
	if e != nil {
		return ErrCensorUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+i.AccessToken)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res, e := b.client.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrCensorUnavailable
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case 401, 403:
		return ErrCensorDenied
	case 404:
		return ErrCensorNotFound
	case 405, 501:
		return ErrCensorUnsupported
	case 409:
		return ErrCensorConflict
	case 408, 429, 500, 502, 503, 504:
		return &censorRemoteTransient{status: res.StatusCode, retryAfter: censorRetryAfter(res.Header.Get("Retry-After"))}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("censor API failed (HTTP %d)", res.StatusCode)
	}
	data, e = io.ReadAll(io.LimitReader(res.Body, censorRemoteLimit+1))
	if e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrCensorUnavailable
	}
	if len(data) > censorRemoteLimit {
		return errors.New("invalid bounded censor response")
	}
	if out != nil {
		if json.Unmarshal(data, out) != nil {
			return errors.New("invalid censor JSON")
		}
	}
	return nil
}
func (b *CensorRemoteBackend) State(ctx context.Context, i CensorIdentity) (CensorRemoteState, error) {
	var state CensorRemoteState
	e := b.call(ctx, i, "GET", "/state", nil, &state, "")
	state.Known = e == nil
	if errors.Is(e, ErrCensorNotFound) || errors.Is(e, ErrCensorUnsupported) {
		e = nil
	}
	if e == nil {
		b.mu.Lock()
		b.capabilities[i.UserID] = state
		b.mu.Unlock()
	}
	return state, e
}
func (b *CensorRemoteBackend) Requests(ctx context.Context, i CensorIdentity) ([]CensorRequest, error) {
	state, e := b.State(ctx, i)
	if e != nil {
		return nil, e
	}
	if state.Configured != nil && !*state.Configured {
		return nil, ErrCensorUnavailable
	}
	var out struct {
		Requests []CensorRequest `json:"requests"`
	}
	e = b.call(ctx, i, "GET", "/requests", nil, &out, "")
	if errors.Is(e, ErrCensorNotFound) || errors.Is(e, ErrCensorUnsupported) {
		return nil, ErrCensorUnavailable
	}
	if e == nil {
		e = validateCensorRequests(out.Requests)
	}
	return out.Requests, e
}
func (b *CensorRemoteBackend) Search(ctx context.Context, i CensorIdentity, q string) ([]CensorCandidate, error) {
	var out struct {
		Candidates []CensorCandidate `json:"candidates"`
	}
	if !validCensorQuery(q) {
		return nil, errors.New("invalid search query")
	}
	e := b.call(ctx, i, "POST", "/search", map[string]string{"query": q}, &out, "")
	if e == nil {
		e = validateCensorCandidates(out.Candidates)
	}
	return out.Candidates, e
}

// ReplacementReceipt probes an optional read-only endpoint, never the mutation
// endpoint. A flat receipt or JSON null is accepted; unsupported is explicit.
func (b *CensorRemoteBackend) ReplacementReceipt(ctx context.Context, i CensorIdentity, s CensorSelection) (CensorReplacement, error) {
	if !validCensorReceiptSelection(s) {
		return CensorReplacement{}, ErrCensorConflict
	}
	var out *CensorReplacement
	if err := b.call(ctx, i, "POST", "/replacements/receipt", s, &out, ""); err != nil {
		return CensorReplacement{}, err
	}
	if out == nil {
		return CensorReplacement{}, ErrCensorNotFound
	}
	if !validCensorReceipt(*out, s) {
		return CensorReplacement{}, ErrCensorConflict
	}
	return *out, nil
}

func validCensorReceiptSelection(s CensorSelection) bool {
	return censorID(s.RequestID) && s.TrackID > 0 && censorID(s.CandidateID) && censorID(s.IdempotencyKey)
}
func validCensorReceipt(v CensorReplacement, s CensorSelection) bool {
	return validCensorReceiptSelection(s) && validCensorReplacement(v, s) && v.CandidateID == s.CandidateID && v.IdempotencyKey == s.IdempotencyKey && v.Durable && v.Atomic
}

func (b *CensorRemoteBackend) Replace(ctx context.Context, i CensorIdentity, s CensorSelection) (CensorReplacement, error) {
	return b.mutate(ctx, i, "/replacements", s)
}
func (b *CensorRemoteBackend) Rollback(ctx context.Context, i CensorIdentity, s CensorSelection) (CensorReplacement, error) {
	b.mu.Lock()
	capabilities := b.capabilities[i.UserID]
	b.mu.Unlock()
	if capabilities.Known && !capabilities.Rollback {
		return CensorReplacement{}, ErrCensorUnsupported
	}
	out, err := b.mutate(ctx, i, "/rollback", s)
	if errors.Is(err, ErrCensorNotFound) {
		err = ErrCensorUnsupported
	}
	return out, err
}
func (b *CensorRemoteBackend) mutate(ctx context.Context, i CensorIdentity, path string, s CensorSelection) (CensorReplacement, error) {
	var out CensorReplacement
	if !censorID(s.IdempotencyKey) || !censorID(s.RequestID) || !censorID(s.CandidateID) || s.TrackID <= 0 {
		return out, errors.New("valid selection and idempotency key required")
	}
	e := b.call(ctx, i, "POST", path, s, &out, s.IdempotencyKey)
	if e == nil && (!validCensorReplacement(out, s) || !out.Durable || !out.Atomic || out.IdempotencyKey != s.IdempotencyKey) {
		e = errors.New("censor publication is not durably acknowledged")
	}
	return out, e
}
func (b *CensorRemoteBackend) SaveResults(ctx context.Context, i CensorIdentity, j CensorJob) error {
	b.mu.Lock()
	supported := b.capabilities[i.UserID].ResultsPersistence
	b.mu.Unlock()
	if !supported {
		return nil
	}
	return b.call(ctx, i, "POST", "/state", map[string]any{"job": j}, nil, "")
}
func validateCensorRequests(items []CensorRequest) error {
	if len(items) > 1000 {
		return errors.New("too many censor requests")
	}
	seen := map[string]bool{}
	for _, v := range items {
		if !censorID(v.ID) || v.TrackID <= 0 || !censorHTTPS(v.OriginalURL) || len(v.Title) > 1024 || len(v.Artist) > 1024 || seen[v.ID] {
			return errors.New("invalid censor request source")
		}
		seen[v.ID] = true
	}
	return nil
}
func validateCensorCandidates(items []CensorCandidate) error {
	if len(items) > 100 {
		return errors.New("too many censor candidates")
	}
	seen := map[string]bool{}
	for _, v := range items {
		if !censorID(v.ID) || !censorHTTPS(v.URL) || len(v.Title) > 1024 || len(v.Artist) > 1024 || len(v.Provider) > 256 || v.DurationSeconds < 0 || v.DurationSeconds > 86400 || math.IsNaN(v.DurationSeconds) || math.IsInf(v.DurationSeconds, 0) || seen[v.ID] || len(v.Metadata) > 32 {
			return errors.New("invalid censor candidate URL or metadata")
		}
		for k, value := range v.Metadata {
			if len(k) > 128 || len(value) > 2048 {
				return errors.New("candidate metadata exceeds limit")
			}
		}
		seen[v.ID] = true
	}
	return nil
}
func validCensorReplacement(v CensorReplacement, s CensorSelection) bool {
	return censorHTTPS(v.URL) && v.RequestID == s.RequestID && v.TrackID == s.TrackID
}
