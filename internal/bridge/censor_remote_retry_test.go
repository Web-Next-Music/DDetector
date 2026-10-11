package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestCensorRemoteReadRetryExponentialAndReplayableBody(t *testing.T) {
	for _, operation := range []string{"requests", "search", "state"} {
		t.Run(operation, func(t *testing.T) {
			var mu sync.Mutex
			attempts := []time.Time{}
			remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer user-jwt" || r.Header.Get("apikey") != "" {
					t.Error("retry changed credentials")
				}
				if operation == "requests" && r.URL.Path == "/state" {
					w.WriteHeader(404)
					return
				}
				if r.URL.Path == "/search" {
					var q map[string]string
					if json.NewDecoder(r.Body).Decode(&q) != nil || q["query"] != "original title" {
						t.Error("search retry lost body")
					}
				}
				mu.Lock()
				attempts = append(attempts, time.Now())
				count := len(attempts)
				mu.Unlock()
				if count < 3 {
					w.WriteHeader(503)
					return
				}
				switch operation {
				case "requests":
					fmt.Fprint(w, `{"requests":[{"id":"r","track_id":42,"original_url":"https://audio.example/full.mp3"}]}`)
				case "search":
					fmt.Fprint(w, `{"candidates":[{"id":"page","url":"https://provider.example/page"}]}`)
				case "state":
					fmt.Fprint(w, `{"rollback":true}`)
				}
			}))
			defer remote.Close()
			b, e := NewCensorRemoteBackend(remote.URL)
			if e != nil {
				t.Fatal(e)
			}
			b.client = remote.Client()
			identity := CensorIdentity{UserID: "u", AccessToken: "user-jwt"}
			switch operation {
			case "requests":
				_, e = b.Requests(context.Background(), identity)
			case "search":
				_, e = b.Search(context.Background(), identity, "original title")
			case "state":
				_, e = b.State(context.Background(), identity)
			}
			if e != nil {
				t.Fatal(e)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(attempts) != 3 || attempts[1].Sub(attempts[0]) < 200*time.Millisecond || attempts[2].Sub(attempts[1]) < 400*time.Millisecond {
				t.Fatal("unbounded or non-exponential retry", attempts)
			}
		})
	}
}
func TestCensorRemoteRetriesBoundedAndNonTransientNotRetried(t *testing.T) {
	for _, tc := range []struct {
		status      int
		body, after string
		attempts    int
	}{{503, "", "", 3}, {429, "", "", 3}, {429, "", "60", 1}, {403, "", "", 1}, {404, "", "", 1}, {409, "", "", 1}, {200, "invalid JSON", "", 1}} {
		t.Run(fmt.Sprintf("%d-%s", tc.status, tc.after), func(t *testing.T) {
			var mu sync.Mutex
			calls := 0
			remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				calls++
				mu.Unlock()
				w.Header().Set("Retry-After", tc.after)
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer remote.Close()
			b, _ := NewCensorRemoteBackend(remote.URL)
			b.client = remote.Client()
			if _, e := b.Search(context.Background(), CensorIdentity{UserID: "u", AccessToken: "jwt"}, "query"); e == nil {
				t.Fatal("failure accepted")
			}
			mu.Lock()
			defer mu.Unlock()
			if calls != tc.attempts {
				t.Fatal("retry count", calls, tc.attempts)
			}
		})
	}
}
func TestCensorRemoteCancellationInterruptsBackoff(t *testing.T) {
	first := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	calls := 0
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.WriteHeader(503)
		once.Do(func() { close(first) })
	}))
	defer remote.Close()
	b, _ := NewCensorRemoteBackend(remote.URL)
	b.client = remote.Client()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := b.Search(ctx, CensorIdentity{UserID: "u", AccessToken: "jwt"}, "query"); done <- e }()
	<-first
	cancel()
	select {
	case e := <-done:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("backoff ignored cancellation")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatal("retried cancelled read", calls)
	}
}
func TestCensorRemoteMutationsNeverAutomaticallyRetried(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { mu.Lock(); calls++; mu.Unlock(); w.WriteHeader(503) }))
	defer remote.Close()
	b, _ := NewCensorRemoteBackend(remote.URL)
	b.client = remote.Client()
	identity := CensorIdentity{UserID: "u", AccessToken: "jwt"}
	selection := CensorSelection{RequestID: "r", TrackID: 42, CandidateID: "c", IdempotencyKey: "stable-key"}
	if _, e := b.Replace(context.Background(), identity, selection); e == nil {
		t.Fatal("publication failure ignored")
	}
	if _, e := b.Rollback(context.Background(), identity, selection); e == nil {
		t.Fatal("rollback failure ignored")
	}
	if e := b.call(context.Background(), identity, "POST", "/state", map[string]string{"state": "review"}, nil, ""); e == nil {
		t.Fatal("state failure ignored")
	}
	mu.Lock()
	count := calls
	mu.Unlock()
	if count != 3 {
		t.Fatal("mutation automatically retried", count)
	}
	selection.IdempotencyKey = ""
	if _, e := b.Replace(context.Background(), identity, selection); e == nil {
		t.Fatal("unkeyed mutation accepted")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != count {
		t.Fatal("unkeyed mutation reached remote")
	}
}
