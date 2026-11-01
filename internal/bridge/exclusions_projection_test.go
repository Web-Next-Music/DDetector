package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestExclusionProjectionUpdatesState(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "list_v2.json")
	write := func(data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"sources":[]}`)
	rt := &censorRuntime{uid: "user", backend: &nativeBackend{}, disk: censorDiskState{RepositoryPath: repo}}
	s := &Server{censorRuntime: rt}
	s.publishResults([]Result{{Track: Track{ID: 1}, Status: "found"}, {Track: Track{ID: 2}, Status: "none"}})
	request := httptest.NewRequest("GET", "/api/state", nil)
	read := func() (State, string) {
		t.Helper()
		data, tag, err := s.stateResponse(request)
		if err != nil {
			t.Fatal(err)
		}
		var state State
		if err := json.Unmarshal(data, &state); err != nil {
			t.Fatal(err)
		}
		return state, tag
	}
	before, tag := read()
	if len(before.Tracks) != 2 || before.FoundCount != 1 {
		t.Fatal(before)
	}
	write(`{"sources":[{"tracksStorages":[{"trackIds":[1]}]}]}`)
	hidden, changed := read()
	if len(hidden.Tracks) != 1 || hidden.Tracks[0].ID != 2 || hidden.FoundCount != 0 || tag == changed {
		t.Fatal(hidden, tag, changed)
	}
	write(`{"sources":[]}`)
	restored, restoredTag := read()
	if len(restored.Tracks) != 2 || restored.Tracks[0].ID != 1 || restored.FoundCount != 1 || tag != restoredTag {
		t.Fatal(restored)
	}
	write(`{`)
	afterError, errorTag := read()
	if len(afterError.Tracks) != 2 || errorTag != restoredTag {
		t.Fatal("invalid policy changed last valid projection", afterError)
	}
	if _, err := s.localExclusions(context.Background(), "user"); err == nil {
		t.Fatal("load accepted invalid policy")
	}
	write(`{"sources":[{"tracksStorages":[{"trackIds":[2]}]}]}`)
	recovered, _ := read()
	if len(recovered.Tracks) != 1 || recovered.Tracks[0].ID != 1 {
		t.Fatal("policy did not recover", recovered)
	}
}

func TestExclusionProjectionSameTimestampReplacement(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "list_v2.json")
	write := func(id string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(`{"sources":[{"tracksStorages":[{"trackIds":[`+id+`]}]}]}`), 0600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Unix(1234567890, 0)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	write("1")
	s := &Server{censorRuntime: &censorRuntime{backend: &nativeBackend{}, disk: censorDiskState{RepositoryPath: repo}}}
	s.publishResults([]Result{{Track: Track{ID: 1}}, {Track: Track{ID: 2}}})
	r := httptest.NewRequest("GET", "/api/state", nil)
	if _, _, err := s.stateResponse(r); err != nil {
		t.Fatal(err)
	}
	write("2")
	data, _, err := s.stateResponse(r)
	if err != nil {
		t.Fatal(err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Tracks) != 1 || state.Tracks[0].ID != 1 {
		t.Fatal(state.Tracks)
	}
}

func TestExclusionProjectionTokenRevocationClearsHiddenTracks(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, "list_v2.json")
	if err := os.WriteFile(path, []byte(`{"sources":[{"tracksStorages":[{"trackIds":[1]}]}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	s := testServer(t, func(http.ResponseWriter, *http.Request) { t.Error("unexpected upstream") })
	s.censorRuntime = &censorRuntime{backend: &nativeBackend{}, disk: censorDiskState{RepositoryPath: repo}}
	s.publishResults([]Result{{Track: Track{ID: 1}}})
	if _, _, err := s.stateResponse(httptest.NewRequest("GET", "/api/state", nil)); err != nil {
		t.Fatal(err)
	}
	if w := api(s, "POST", "/api/yandex/token", `{"access_token":""}`, "bridge-key"); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if err := os.WriteFile(path, []byte(`{"sources":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	data, _, err := s.stateResponse(httptest.NewRequest("GET", "/api/state", nil))
	if err != nil {
		t.Fatal(err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Tracks) != 0 || s.allTracks != nil {
		t.Fatal("revoked track reappeared", state.Tracks)
	}
}

func TestExclusionProjectionConcurrentPublish(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "list_v2.json"), []byte(`{"sources":[{"tracksStorages":[{"trackIds":[1]}]}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	s := &Server{censorRuntime: &censorRuntime{backend: &nativeBackend{}, disk: censorDiskState{RepositoryPath: repo}}}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				s.publishResults([]Result{{Track: Track{ID: 1}}, {Track: Track{ID: 2}}})
				if _, _, err := s.stateResponse(httptest.NewRequest("GET", "/api/state", nil)); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	data, _, err := s.stateResponse(httptest.NewRequest("GET", "/api/state", nil))
	if err != nil {
		t.Fatal(err)
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Tracks) != 1 || state.Tracks[0].ID != 2 {
		t.Fatal(state.Tracks)
	}
}
