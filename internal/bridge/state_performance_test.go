package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func conditionalState(s *Server, tag, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", s.base+"/api/state", nil)
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("If-None-Match", tag)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestConditionalState(t *testing.T) {
	s := testServer(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected upstream") })
	first := conditionalState(s, "", "bridge-key")
	tag := first.Header().Get("ETag")
	if first.Code != 200 || tag == "" {
		t.Fatal(first.Code, tag)
	}
	encoded := &s.stateJSON[0]
	for _, match := range []string{tag, "W/" + tag, `"other", ` + tag, "*"} {
		w := conditionalState(s, match, "bridge-key")
		if w.Code != 304 || w.Body.Len() != 0 || w.Header().Get("ETag") != tag {
			t.Fatal(w.Code, w.Body, w.Header())
		}
		if encoded != &s.stateJSON[0] {
			t.Fatal("unchanged state re-encoded")
		}
	}
	if w := conditionalState(s, tag, "wrong-key"); w.Code != 401 {
		t.Fatal("conditional request bypassed authentication", w.Code)
	}
	for _, field := range []string{"Authenticated", "AccessChecked", "HasAccess", "IsBanned", "LoginPending", "Loading", "Fetching", "Processing", "Processed", "FoundCount", "Error"} {
		t.Run(field, func(t *testing.T) {
			s.mu.Lock()
			v := reflect.ValueOf(&s.state).Elem().FieldByName(field)
			switch v.Kind() {
			case reflect.Bool:
				v.SetBool(!v.Bool())
			case reflect.Int:
				v.SetInt(v.Int() + 1)
			case reflect.String:
				v.SetString("changed")
			}
			s.mu.Unlock()
			w := conditionalState(s, tag, "bridge-key")
			if w.Code != 200 || w.Header().Get("ETag") == tag {
				t.Fatal("missed direct scalar change", field)
			}
			tag = w.Header().Get("ETag")
		})
	}
	changes := []struct {
		name   string
		change func()
	}{
		{"user", func() { s.state.User = &User{ID: "new-user"} }},
		{"fetch", func() { s.state.LastFetch = &FetchResult{Total: 10} }},
		{"fetch direct write", func() { s.state.LastFetch.Total++ }},
		{"tracks replaced", func() { s.state.Tracks = []Result{{Track: Track{ID: 1, Title: "track"}}} }},
		{"processing", func() { s.state.Tracks[0].Status = "found"; s.tracksRevision++ }},
		{"ignore", func() { s.state.Tracks[0].Ignored = true; s.tracksRevision++ }},
		{"access clears", func() { s.state.Tracks = []Result{} }},
		{"logout reset", func() { s.state = State{Tracks: []Result{}} }},
	}
	for _, change := range changes {
		s.mu.Lock()
		change.change()
		s.mu.Unlock()
		w := conditionalState(s, tag, "bridge-key")
		if w.Code != 200 || w.Header().Get("ETag") == tag {
			t.Fatal("missed change", change.name)
		}
		tag = w.Header().Get("ETag")
	}
	s.publishResults([]Result{{Track: Track{ID: 2}}})
	w := conditionalState(s, tag, "bridge-key")
	if w.Code != 200 {
		t.Fatal("missed publishResults")
	}
	tag = w.Header().Get("ETag")
	s.publishResults([]Result{{Track: Track{ID: 3}}})
	w = conditionalState(s, tag, "bridge-key")
	if w.Code != 200 {
		t.Fatal("missed same-length publishResults")
	}
	tag = w.Header().Get("ETag")
	s.login = &pendingLogin{Expires: time.Now().Add(-time.Second)}
	s.state.LoginPending = true
	w = conditionalState(s, tag, "bridge-key")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Login expired") {
		t.Fatal("missed login expiry")
	}
}

func TestConditionalStateOperations(t *testing.T) {
	denied := false
	s := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/v1/user":
			fmt.Fprint(w, `{"id":"user-1"}`)
		case "/rest/v1/bans":
			fmt.Fprint(w, `[]`)
		case "/rest/v1/ddetector_users":
			if denied {
				fmt.Fprint(w, `[]`)
			} else {
				fmt.Fprint(w, `[{"user_id":"user-1"}]`)
			}
		case "/rest/v1/ddetector_tracks":
			if r.Header.Get("Range") == "0-999" {
				fmt.Fprint(w, `[{"id":1}]`)
			} else {
				fmt.Fprint(w, `[]`)
			}
		case "/tracks":
			fmt.Fprint(w, `{"result":[{"id":"1","title":"track"}]}`)
		case "/tracks/1/lyrics":
			w.WriteHeader(404)
		case "/tracks/1/supplement":
			fmt.Fprint(w, `{"result":{"lyrics":{"fullLyrics":"weed"}}}`)
		case "/rest/v1/ddetector_ignored_tracks":
			if r.Method == "GET" {
				fmt.Fprint(w, `[]`)
			} else {
				w.WriteHeader(204)
			}
		case "/functions/v1/sync-ddetector-tracks":
			fmt.Fprint(w, `{"added":0}`)
		case "/auth/v1/logout":
			w.WriteHeader(204)
		default:
			t.Error("unexpected upstream", r.URL.Path)
		}
	})
	install(s)
	s.yandex = newYandexClient("test-token")
	s.yandex.base = s.client.URL
	s.yandex.uid = "123"
	tag := conditionalState(s, "", "bridge-key").Header().Get("ETag")
	fresh := func() {
		t.Helper()
		w := conditionalState(s, tag, "bridge-key")
		if w.Code != 200 || w.Header().Get("ETag") == tag {
			t.Fatal("operation did not invalidate state")
		}
		tag = w.Header().Get("ETag")
		if conditionalState(s, tag, "bridge-key").Code != 304 {
			t.Fatal("operation never settled")
		}
	}
	if err := s.load(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if len(s.state.Tracks) != 1 {
		t.Fatal("processing did not publish tracks")
	}
	fresh()
	if w := api(s, "POST", "/api/ignore", `{"track_id":1,"ignored":true}`, "bridge-key"); w.Code != 200 {
		t.Fatal(w.Body)
	}
	fresh()
	if w := api(s, "POST", "/api/ignore", `{"track_id":1,"ignored":true}`, "bridge-key"); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if conditionalState(s, tag, "bridge-key").Code != 304 {
		t.Fatal("no-op ignore changed state")
	}
	denied = true
	if w := api(s, "POST", "/api/ignore", `{"track_id":1,"ignored":false}`, "bridge-key"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	fresh()
	if len(s.state.Tracks) != 0 {
		t.Fatal("denial retained tracks")
	}
	if w := api(s, "POST", "/api/logout", "", "bridge-key"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	fresh()
}

func TestDeltaStateTracksAndConcurrentSnapshots(t *testing.T) {
	s := &Server{state: State{Tracks: []Result{{Track: Track{ID: 1}}}}}
	request := func(revision string) map[string]json.RawMessage {
		r := httptest.NewRequest("GET", "/api/state", nil)
		r.Header.Set("X-State-Delta", "1")
		r.Header.Set("X-Tracks-Revision", revision)
		w := httptest.NewRecorder()
		s.sendState(w, r)
		var out map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Error(err)
		}
		return out
	}
	first := request("")
	var revision string
	_ = json.Unmarshal(first["tracksRevision"], &revision)
	encoded := &s.tracksJSON[0]
	s.state.Processed++
	if next := request(revision); next["tracks"] != nil || string(next["processed"]) != "1" {
		t.Fatal(next)
	}
	if encoded != &s.tracksJSON[0] {
		t.Fatal("scalar re-encoded tracks")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); request(revision) }()
	}
	wg.Wait()
	s.state.Tracks[0].Ignored = true
	s.tracksRevision++
	if request(revision)["tracks"] == nil {
		t.Fatal("mutation omitted")
	}
	s.state = State{Tracks: []Result{}}
	if string(request(revision)["tracks"]) != "[]" {
		t.Fatal("logout did not clear")
	}
}

func TestCensorViewInvalidatesWithoutTimestamp(t *testing.T) {
	job := CensorJob{ID: "cache-test", Status: "stopped"}
	first := censorJobView(job, true)
	repeated := censorJobView(job, true)
	if first.DetailRevision == "" || &first.encoded[0] != &repeated.encoded[0] {
		t.Fatal("cache missed")
	}
	job.Status = "pending"
	next := censorJobView(job, true)
	if next.DetailRevision == first.DetailRevision {
		t.Fatal("status mutation missed")
	}
	detail := censorJobView(job, false)
	if detail.DetailRevision != next.DetailRevision {
		t.Fatal("detail revision mismatch")
	}
	job.Error = "changed without timestamp"
	if censorJobView(job, false).DetailRevision == detail.DetailRevision {
		t.Fatal("detail mutation missed")
	}
}

func BenchmarkStateScalarChangeLarge(b *testing.B) {
	for _, delta := range []bool{false, true} {
		name := "full"
		if delta {
			name = "delta"
		}
		b.Run(name, func(b *testing.B) {
			s := &Server{state: State{Tracks: make([]Result, 10000)}}
			for i := range s.state.Tracks {
				s.state.Tracks[i] = Result{Track: Track{ID: int64(i + 1), Title: "large track"}, Lyrics: []Lyric{{Text: strings.Repeat("lyrics ", 1000)}}}
			}
			r := httptest.NewRequest("GET", "/api/state", nil)
			if _, _, err := s.stateResponse(r); err != nil {
				b.Fatal(err)
			}
			if delta {
				r.Header.Set("X-State-Delta", "1")
				r.Header.Set("X-Tracks-Revision", s.tracksTag)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.state.Processed++
				data, _, err := s.stateResponse(r)
				if err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(float64(len(data)), "response-B")
			}
		})
	}
}

func BenchmarkConditionalStateLarge(b *testing.B) {
	s := &Server{key: "bridge-key", base: "http://127.0.0.1:32100", state: State{Tracks: make([]Result, 10000)}}
	for i := range s.state.Tracks {
		s.state.Tracks[i] = Result{Track: Track{ID: int64(i + 1), Title: "large track"}, Lyrics: []Lyric{{Text: strings.Repeat("lyrics ", 1000)}}}
	}
	tag := conditionalState(s, "", "bridge-key").Header().Get("ETag")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if conditionalState(s, tag, "bridge-key").Code != 304 {
			b.Fatal("expected 304")
		}
	}
}
