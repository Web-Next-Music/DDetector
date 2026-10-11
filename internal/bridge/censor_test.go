package bridge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

type fakeCensor struct {
	calls  int
	err    error
	result CensorReplacement
}

func (f *fakeCensor) check(i CensorIdentity) error {
	f.calls++
	if i.UserID != "user-1" || i.AccessToken != "secret-access" {
		return errors.New("incorrect identity")
	}
	return f.err
}
func (f *fakeCensor) Requests(ctx context.Context, i CensorIdentity) ([]CensorRequest, error) {
	return []CensorRequest{{ID: "request-1", TrackID: 42, OriginalURL: "https://cdn.example/original.mp3"}}, f.check(i)
}
func (f *fakeCensor) Search(ctx context.Context, i CensorIdentity, q string) ([]CensorCandidate, error) {
	if q != "artist title" {
		return nil, errors.New("incorrect query")
	}
	return []CensorCandidate{{ID: "candidate-1", URL: "https://cdn.example/candidate.mp3", FullAudio: true, Downloadable: true}}, f.check(i)
}
func (f *fakeCensor) Replace(ctx context.Context, i CensorIdentity, s CensorSelection) (CensorReplacement, error) {
	return f.result, f.check(i)
}
func censorServer(t *testing.T) *Server {
	s := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/v1/user":
			fmt.Fprint(w, `{"id":"user-1"}`)
		case "/rest/v1/bans":
			fmt.Fprint(w, `[]`)
		case "/rest/v1/ddetector_users":
			fmt.Fprint(w, `[{"user_id":"user-1"}]`)
		default:
			t.Error("unexpected upstream request", r.URL.Path)
			w.WriteHeader(500)
		}
	})
	install(s)
	return s
}
func TestCensorEndpoints(t *testing.T) {
	s := censorServer(t)
	f := &fakeCensor{result: CensorReplacement{RequestID: "request-1", TrackID: 42, URL: "https://cdn.example/music.mp3"}}
	s.SetCensorBackend(f)
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/censor/requests", ""},
		{"POST", "/api/censor/search", `{"query":" artist title "}`},
	} {
		w := api(s, tc.method, tc.path, tc.body, "bridge-key")
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "secret-access") {
			t.Fatal("token leaked")
		}
	}
	if f.calls != 2 {
		t.Fatal(f.calls)
	}
}
func TestCensorRejectsUnsafeInputs(t *testing.T) {
	s := censorServer(t)
	f := &fakeCensor{}
	s.SetCensorBackend(f)
	for _, tc := range []struct {
		method, path, body, key string
		status                  int
	}{
		{"GET", "/api/censor/requests", "", "", 401},
		{"POST", "/api/censor/requests", "{}", "bridge-key", 405},
		{"GET", "/api/censor/missing", "", "bridge-key", 404},
		{"GET", "/api/censor/requests?table=catalog", "", "bridge-key", 400},
		{"POST", "/api/censor/search", `{"query":" "}`, "bridge-key", 400},
		{"POST", "/api/censor/search", `{"query":"artist title"} {}`, "bridge-key", 400},
		{"POST", "/api/censor/search", `{"query":"artist title","rpc":"evil"}`, "bridge-key", 400},
		{"POST", "/api/censor/replacement", `{"request_id":"r","track_id":42,"candidate_id":"c","url":"http://127.0.0.1"}`, "bridge-key", 400},
		{"POST", "/api/censor/replacement", `{"request_id":"r","track_id":0,"candidate_id":"c"}`, "bridge-key", 400},
	} {
		if w := api(s, tc.method, tc.path, tc.body, tc.key); w.Code != tc.status {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body)
		}
	}
	if f.calls != 0 {
		t.Fatal("invalid input reached backend")
	}
}
func TestCensorUnavailableAndErrors(t *testing.T) {
	t.Setenv("FCKCENSOR_API_URL", "")
	s := censorServer(t)
	f := &fakeCensor{}
	s.SetCensorBackend(f)
	for _, tc := range []struct {
		err    error
		status int
	}{{ErrCensorDenied, 403}, {ErrCensorConflict, 409}, {ErrCensorNotFound, 404}, {errors.New("secret credential"), 502}} {
		f.err = tc.err
		w := api(s, "GET", "/api/censor/requests", "", "bridge-key")
		if w.Code != tc.status || strings.Contains(w.Body.String(), "secret credential") {
			t.Fatal(w.Code, w.Body)
		}
	}
}
func TestCensorReplacementResponseValidation(t *testing.T) {
	s := censorServer(t)
	f := &fakeCensor{}
	s.SetCensorBackend(f)
	for _, result := range []CensorReplacement{
		{RequestID: "r", TrackID: 42, URL: "file:///tmp/music.mp3"},
		{RequestID: "r", TrackID: 42, URL: "https://user:password@cdn.example/a.mp3"},
		{RequestID: "other", TrackID: 42, URL: "https://cdn.example/a.mp3"},
	} {
		f.result = result
		w := api(s, "POST", "/api/censor/replacement", `{"request_id":"r","track_id":42,"candidate_id":"c"}`, "bridge-key")
		if w.Code != 409 {
			t.Fatal(w.Code, w.Body)
		}
		if f.calls != 0 {
			t.Fatal("legacy endpoint bypassed review gate")
		}
	}
}
func TestCensorRequiresSessionAndSerializes(t *testing.T) {
	s := censorServer(t)
	f := &fakeCensor{}
	s.SetCensorBackend(f)
	s.op.Lock()
	w := api(s, "GET", "/api/censor/requests", "", "bridge-key")
	s.op.Unlock()
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
	s.session = nil
	w = api(s, "GET", "/api/censor/requests", "", "bridge-key")
	if w.Code != 403 || f.calls != 0 {
		t.Fatal(w.Code, f.calls)
	}
}
