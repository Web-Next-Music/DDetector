package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestCensorCommittedLostResponseReceiptAcrossRestart(t *testing.T) {
	s, _, p := integrationCensor(t)
	j := fetchStart(t, s)
	var mu sync.Mutex
	var receipt *CensorReplacement
	mutations, reads := 0, 0
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer secret-access" || r.Header.Get("apikey") != "" {
			w.WriteHeader(403)
			return
		}
		switch r.URL.Path {
		case "/state":
			json.NewEncoder(w).Encode(map[string]bool{})
		case "/requests":
			reads++
			items := []CensorRequest{}
			if receipt == nil {
				items = append(items, j.Request)
			}
			json.NewEncoder(w).Encode(map[string]any{"requests": items})
		case "/search":
			json.NewEncoder(w).Encode(map[string]any{"candidates": j.Candidates})
		case "/replacements":
			var sel CensorSelection
			json.NewDecoder(r.Body).Decode(&sel)
			mutations++
			receipt = &CensorReplacement{RequestID: sel.RequestID, TrackID: sel.TrackID, CandidateID: sel.CandidateID, IdempotencyKey: sel.IdempotencyKey, URL: "https://audio.example/committed.mp3", Durable: true, Atomic: true}
			w.WriteHeader(503)
		case "/replacements/receipt":
			if r.Method != "POST" || r.Header.Get("Idempotency-Key") != "" {
				t.Error("receipt is not a read-only lookup")
			}
			var sel CensorSelection
			json.NewDecoder(r.Body).Decode(&sel)
			if receipt == nil || !validCensorReceipt(*receipt, sel) {
				w.WriteHeader(404)
				return
			}
			json.NewEncoder(w).Encode(receipt)
		default:
			t.Error("unexpected endpoint", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	b, _ := NewCensorRemoteBackend(api.URL)
	b.client = api.Client()
	s.mu.Lock()
	s.censor = b
	s.mu.Unlock()
	s.censorRuntime.backend = b
	payload := map[string]any{"action": "approve", "id": j.ID, "candidate_id": "candidate-1"}
	controlAPI(t, s, payload, 200)
	failed := waitCensor(t, s, "review_required")
	if failed.PublicationKey == "" {
		t.Fatal("key not persisted")
	}
	if err := s.closeCensor(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	// Restore first, then remove cached evidence and make source unavailable.
	rt, err := s.currentCensorRuntime(context.Background(), "user-1")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := rt.job(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored.Candidates = nil
	stored.Result = nil
	if err = rt.update(stored); err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	rt.sourceUnavailable = true
	rt.mu.Unlock()
	mu.Lock()
	beforeReads := reads
	mu.Unlock()
	p.mu.Lock()
	beforeAudio := len(p.requests)
	p.mu.Unlock()
	controlAPI(t, s, payload, 200)
	done := waitCensor(t, s, "succeeded")
	if done.PublicationKey != failed.PublicationKey || done.Replacement == nil {
		t.Fatal("receipt not reconciled")
	}
	mu.Lock()
	defer mu.Unlock()
	if mutations != 1 || reads != beforeReads {
		t.Fatalf("mutations=%d reads=%d before=%d", mutations, reads, beforeReads)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) != beforeAudio {
		t.Fatal("receipt reconciliation reanalyzed audio")
	}
}

type missingReceiptCensorBackend struct {
	CensorBackend
	lookupError error
}

func (b missingReceiptCensorBackend) Requests(context.Context, CensorIdentity) ([]CensorRequest, error) {
	return []CensorRequest{}, nil
}
func (b missingReceiptCensorBackend) ReplacementReceipt(context.Context, CensorIdentity, CensorSelection) (CensorReplacement, error) {
	return CensorReplacement{}, b.lookupError
}
func TestCensorMissingReceiptDoesNotBlindlyRepublish(t *testing.T) {
	for _, lookupError := range []error{ErrCensorNotFound, ErrCensorUnsupported, ErrCensorDenied, ErrCensorUnavailable} {
		t.Run(lookupError.Error(), func(t *testing.T) {
			s, fixture, _ := integrationCensor(t)
			j := fetchStart(t, s)
			fixture.mu.Lock()
			fixture.failFirst = true
			fixture.mu.Unlock()
			payload := map[string]any{"action": "approve", "id": j.ID, "candidate_id": "candidate-1"}
			controlAPI(t, s, payload, 200)
			waitCensor(t, s, "review_required")
			s.censorRuntime.backend = missingReceiptCensorBackend{CensorBackend: s.censorRuntime.backend, lookupError: lookupError}
			controlAPI(t, s, payload, 200)
			failed := waitCensor(t, s, "review_required")
			if !strings.Contains(failed.Error, "may already be committed") {
				t.Fatalf("missing uncertainty: %s", failed.Error)
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if fixture.mutations != 1 {
				t.Fatal("blind publication retry")
			}
		})
	}
}

func TestCensorRemoteReceiptValidation(t *testing.T) {
	sel := CensorSelection{RequestID: "r", TrackID: 42, CandidateID: "c", IdempotencyKey: "key"}
	good := CensorReplacement{RequestID: "r", TrackID: 42, CandidateID: "c", IdempotencyKey: "key", URL: "https://audio.example/full.mp3", Durable: true, Atomic: true}
	cases := []struct {
		name   string
		change func(*CensorReplacement)
	}{
		{"candidate", func(v *CensorReplacement) { v.CandidateID = "other" }},
		{"key", func(v *CensorReplacement) { v.IdempotencyKey = "other" }},
		{"request", func(v *CensorReplacement) { v.RequestID = "other" }},
		{"track", func(v *CensorReplacement) { v.TrackID++ }},
		{"durable", func(v *CensorReplacement) { v.Durable = false }},
		{"atomic", func(v *CensorReplacement) { v.Atomic = false }},
		{"url", func(v *CensorReplacement) { v.URL = "http://audio.example/file" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := good
			tc.change(&v)
			if validCensorReceipt(v, sel) {
				t.Fatal("accepted invalid receipt")
			}
		})
	}
	for _, status := range []int{404, 405, 403, 200} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				if status == 200 {
					w.Write([]byte("null"))
				}
			}))
			defer api.Close()
			b, _ := NewCensorRemoteBackend(api.URL)
			b.client = api.Client()
			_, err := b.ReplacementReceipt(context.Background(), CensorIdentity{UserID: "u", AccessToken: "jwt"}, sel)
			want := ErrCensorNotFound
			if status == 405 {
				want = ErrCensorUnsupported
			}
			if status == 403 {
				want = ErrCensorDenied
			}
			if !errors.Is(err, want) || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}
