package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeDefaultUnsignedQueueFetch(t *testing.T) {
	t.Setenv("FCKCENSOR_API_URL", "")
	s := censorServer(t)
	rt, e := s.currentCensorRuntime(context.Background(), s.session.User.ID)
	if e != nil {
		t.Fatal(e)
	}
	b, ok := rt.backend.(*nativeBackend)
	if !ok {
		t.Fatal("native default missing")
	}
	queue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("apikey") != "sb_publishable_test" {
			t.Error("unexpected caller authorization")
		}
		if r.URL.Path != "/rest/v1/reported_tracks" {
			t.Error("queue used catalog")
		}
		w.Write([]byte(`[{"track_id":42,"type":"track"}]`))
	}))
	defer queue.Close()
	b.source, _ = NewClient(queue.URL, "sb_publishable_test")
	w := api(s, "GET", "/api/censor/requests", "", "bridge-key")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if e = rt.fetch(context.Background(), CensorIdentity{UserID: rt.uid}); e != nil {
		t.Fatal(e)
	}
	state := rt.snapshot()
	if len(state.Jobs) != 1 || state.Jobs[0].Request.OriginalURL != "" || state.Jobs[0].Request.Origin != "reported_tracks" {
		t.Fatal("lazy request resolved audio")
	}
	if validateCensorRequests([]CensorRequest{state.Jobs[0].Request}) == nil {
		t.Fatal("remote accepted absent audio")
	}
	if _, e = openCensorRuntime(s, rt.uid, b); e != nil {
		t.Fatal("lazy persisted state rejected", e)
	}
}

func TestNativeBackendPublicationReplayRollback(t *testing.T) {
	p, i, selection, request, candidate, _ := nativePublisherFixture(t)
	rt := &censorRuntime{disk: censorDiskState{Jobs: []CensorJob{{Request: request.CensorRequest, Candidates: []CensorCandidate{candidate}, ExpectedMapping: request.ExpectedMapping, RepositoryPath: p.repo, PublicationKey: selection.IdempotencyKey, SelectedCandidate: selection.CandidateID}}}}
	b := &nativeBackend{runtime: rt, publisher: p, repo: p.repo}
	ctx := context.Background()
	receipt, e := b.Replace(ctx, i, selection)
	if e != nil {
		t.Fatal(e)
	}
	if !receipt.LocalOnly || !receipt.Durable || !receipt.Atomic || receipt.GitHubPublished || receipt.RequestCompleted || receipt.Scope != "local" || !validCensorReceipt(receipt, selection) {
		t.Fatal("incorrect local semantics", receipt)
	}
	replay, e := b.ReplacementReceipt(ctx, i, selection)
	if e != nil || replay != receipt {
		t.Fatal("receipt replay failed", e)
	}
	replay, e = b.Replace(ctx, i, selection)
	if e != nil || replay != receipt {
		t.Fatal("publication replay failed", e)
	}
	rt.disk.Jobs[0].RollbackKey = "rollback"
	rollback := selection
	rollback.IdempotencyKey = "rollback"
	for n := 0; n < 2; n++ {
		r, e := b.Rollback(ctx, i, rollback)
		if e != nil || !validCensorReceipt(r, rollback) || !r.LocalOnly {
			t.Fatal("rollback failed", e)
		}
	}
	if string(nativeReadTest(t, p.repo+"/tracks/42")) != "original" {
		t.Fatal("rollback did not restore original")
	}
	data, _ := json.Marshal(receipt)
	if !json.Valid(data) {
		t.Fatal("receipt encoding")
	}
}

func TestNativeFailurePreservesOperationError(t *testing.T) {
	t.Setenv("FCKCENSOR_API_URL", "")
	s := censorServer(t)
	defer s.Close(context.Background())
	rt, err := s.currentCensorRuntime(context.Background(), s.session.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"analysis audio download failed", "candidate search failed", "full recording comparison failed"} {
		if err := rt.startCommand("", func(context.Context, CensorIdentity) error { return fmt.Errorf("%s", message) }); err != nil {
			t.Fatal(err)
		}
		rt.mu.Lock()
		done := rt.commandDone
		rt.mu.Unlock()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("command did not finish")
		}
		state := rt.snapshot()
		if state.Error != message || state.NativeRepositoryReady || state.RepositoryError == "" {
			t.Fatal("operation error masked by readiness", state)
		}
	}
}

func TestNativeConfigureClearsOnlyLegacyRepositoryErrors(t *testing.T) {
	t.Setenv("FCKCENSOR_API_URL", "")
	fixture, _, _, _, _, _ := nativePublisherFixture(t)
	s := censorServer(t)
	defer s.Close(context.Background())
	rt, err := s.currentCensorRuntime(context.Background(), s.session.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	b := rt.backend.(*nativeBackend)
	rt.mu.Lock()
	rt.lastError = legacyMissingRepositoryMessage
	rt.disk.Jobs = []CensorJob{
		{ID: "legacy", Status: "failed", Error: legacyMissingRepositoryMessage},
		{ID: "genuine", Status: "failed", Error: "analysis audio download failed"},
		{ID: "similar", Status: "failed", Error: legacyMissingRepositoryMessage + " Additional failure."},
	}
	rt.mu.Unlock()
	if err := b.configure(context.Background(), fixture.repo); err != nil {
		t.Fatal(err)
	}
	state := rt.snapshot()
	if !state.NativeRepositoryReady || state.RepositoryError != "" || state.Error != "" || state.Jobs[0].Error != "" || state.Jobs[1].Error != "analysis audio download failed" || state.Jobs[2].Error == "" {
		t.Fatal("incorrect legacy cleanup", state)
	}
	data, err := os.ReadFile(filepath.Join(rt.dir, "runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk censorDiskState
	if err := json.Unmarshal(data, &disk); err != nil {
		t.Fatal(err)
	}
	if disk.Jobs[0].Error != "" || disk.Jobs[1].Error != state.Jobs[1].Error {
		t.Fatal("cleanup not persisted")
	}
	rt.mu.Lock()
	rt.lastError = "genuine provider failure"
	rt.mu.Unlock()
	if err := b.configure(context.Background(), fixture.repo); err != nil {
		t.Fatal(err)
	}
	if rt.snapshot().Error != "genuine provider failure" {
		t.Fatal("configuration erased genuine runtime error")
	}
}

func TestNativeConfigureControlValidation(t *testing.T) {
	for _, in := range []censorControl{{Action: "native_configure"}, {Action: "native_configure", RepositoryPath: "relative"}, {Action: "fetch", RepositoryPath: "/repo"}, {Action: "native_configure", RepositoryPath: "/repo", ID: "job"}} {
		if validCensorControl(in) {
			t.Fatal("invalid configuration accepted", in)
		}
	}
	if !validCensorControl(censorControl{Action: "native_configure", RepositoryPath: "/repo"}) {
		t.Fatal("valid configuration rejected")
	}
}

func nativeDirectSiteFixture(t *testing.T, n *nativeSource) {
	t.Helper()
	n.webHTTP = &http.Client{Transport: nativeAudioTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("apikey") != "" {
			t.Error("credentials forwarded to public music site")
		}
		if r.URL.Hostname() != "hitmos.fm" {
			return siteTestResponse(r, "text/html", "no results"), nil
		}
		switch {
		case r.Method == http.MethodGet && !strings.HasSuffix(r.URL.Path, ".mp3"):
			name := strings.ReplaceAll(strings.ToLower(r.URL.Query().Get("q")), " ", "-")
			return siteTestResponse(r, "text/html", `<audio src="/`+name+`.mp3"></audio>`), nil
		case (r.Method == http.MethodHead || r.Method == http.MethodGet) && strings.HasSuffix(r.URL.Path, ".mp3"):
			return siteTestResponse(r, "audio/mpeg", "full-mp3"), nil
		default:
			t.Errorf("unexpected discovery request %s %s", r.Method, r.URL)
			return archiveTestResponse(r, 404, ""), nil
		}
	})}
}

// Publication/recovery tests retain legacy Archive evidence independently of
// today's discovery default. Never promote unverified public-site media to full audio.
func nativeLegacyArchiveReviewFixture(t *testing.T, b *nativeBackend, job CensorJob) CensorJob {
	t.Helper()
	candidates, err := b.archiveSearch(context.Background(), "Fixture Artist Track")
	if err != nil || len(candidates) != 1 {
		t.Fatalf("legacy Archive fixture: %v %v", candidates, err)
	}
	job.Candidates = candidates
	if err := b.runtime.update(job); err != nil {
		t.Fatal(err)
	}
	if err := b.runtime.compareCandidates(context.Background(), nil, &job); err != nil {
		t.Fatal(err)
	}
	if !b.runtime.suitableCandidate(job.Candidates[0]) {
		t.Fatal("legacy Archive fixture lacks comparison evidence")
	}
	job.Stage = "review"
	if err := b.runtime.update(job); err != nil {
		t.Fatal(err)
	}
	return job
}

func TestNativePipelineFreshURLsLocalSaveAndConfigurePersistence(t *testing.T) {
	t.Setenv("FCKCENSOR_API_URL", "")
	fixture, _, _, _, _, _ := nativePublisherFixture(t)
	s := censorServer(t)
	defer s.Close(context.Background())
	processor := &orchestrationProcessor{}
	s.censorProcessor = processor
	var descriptors atomic.Int64
	provider := newYandexClient("provider-secret")
	provider.http.Transport = nativeAudioTransport(func(r *http.Request) (*http.Response, error) {
		body := ""
		switch {
		case r.URL.Path == "/tracks/42" || r.URL.Path == "/tracks/99":
			id := strings.TrimPrefix(r.URL.Path, "/tracks/")
			body = fmt.Sprintf(`{"result":[{"id":%s,"title":"track","artists":[{"name":"artist"}],"explicit":true,"available":true,"durationMs":30000}]}`, id)
		case r.URL.Path == "/search":
			t.Error("native candidate discovery must not search Yandex")
		case strings.HasSuffix(r.URL.Path, "/download-info"):
			body = `{"result":[{"codec":"mp3","preview":false,"bitrateInKbps":320,"downloadInfoUrl":"https://storage.mds.yandex.net/descriptor"}]}`
		case r.URL.Path == "/descriptor":
			body = fmt.Sprintf(`<download-info><host>storage.mds.yandex.net</host><path>/full.mp3</path><ts>%d</ts><s>salt</s></download-info>`, descriptors.Add(1))
		default:
			t.Errorf("unexpected provider path %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	s.yandex = provider
	rt, e := s.currentCensorRuntime(context.Background(), s.session.User.ID)
	if e != nil {
		t.Fatal(e)
	}
	b := rt.backend.(*nativeBackend)
	archiveClient, archiveCalls := archiveFixtureClient(t)
	b.archiveHTTP = archiveClient
	nativeDirectSiteFixture(t, b.nativeSource)
	queue := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[{"track_id":42,"type":"track"}]`)) }))
	defer queue.Close()
	b.source, _ = NewClient(queue.URL, "sb_publishable_test")
	controlAPI(t, s, map[string]any{"action": "native_configure", "repository_path": fixture.repo}, 200)
	publisher := b.publisher.(*nativePublisher)
	publisher.validate = func(context.Context, string) error { return nil }
	publisher.client.Transport = nativeAudioTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" {
			t.Error("credential leaked to media")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("full-mp3")), Request: r}, nil
	})
	originalRuntime := s.censorRuntime
	controlAPI(t, s, map[string]any{"action": "native_configure", "repository_path": fixture.repo}, 200)
	if s.censorRuntime != originalRuntime || b.publisher != publisher {
		t.Fatal("same repository reset runtime")
	}
	job := fetchStart(t, s)
	if len(job.Candidates) != 1 || job.Candidates[0].Provider != "direct_web" || job.Candidates[0].FullAudio || job.Candidates[0].Result != nil || archiveCalls.Load() != 0 {
		t.Fatal("native discovery did not preserve unverified direct-site evidence", job)
	}
	controlAPI(t, s, map[string]any{"action": "approve", "id": job.ID, "candidate_id": job.Candidates[0].ID}, 409)
	job = nativeLegacyArchiveReviewFixture(t, b, job)
	originalURL := job.Request.OriginalURL
	metadataCalls := archiveCalls.Load()
	// Simulate a crash after the publication key was saved but before a journal
	// existed, followed by a user editing both catalogs.
	job.PublicationKey = "crash-key"
	job.SelectedCandidate = job.Candidates[0].ID
	job.ExpectedMapping = "https://old.example/42"
	job.RepositoryPath = fixture.repo
	if err := rt.update(job); err != nil {
		t.Fatal(err)
	}
	preimages := map[string][]byte{}
	for _, name := range []string{"list.json", "list_v2.json"} {
		path := filepath.Join(fixture.repo, name)
		preimages[path] = nativeReadTest(t, path)
		edited := bytes.ReplaceAll(preimages[path], []byte("https://old.example/42"), []byte("https://user-edit.example/42"))
		if err := os.WriteFile(path, edited, 0644); err != nil {
			t.Fatal(err)
		}
	}
	controlAPI(t, s, map[string]any{"action": "approve", "id": job.ID, "candidate_id": job.SelectedCandidate}, 200)
	retried := waitCensor(t, s, "review_required")
	if retried.ExpectedMapping != job.ExpectedMapping || retried.PublicationKey != job.PublicationKey {
		t.Fatal("retry replaced original CAS evidence", retried)
	}
	for path, before := range preimages {
		expected := bytes.ReplaceAll(before, []byte("https://old.example/42"), []byte("https://user-edit.example/42"))
		if !bytes.Equal(nativeReadTest(t, path), expected) {
			t.Fatal("retry overwrote user catalog edit")
		}
		if err := os.WriteFile(path, before, 0644); err != nil {
			t.Fatal(err)
		}
	}
	controlAPI(t, s, map[string]any{"action": "approve", "id": job.ID, "candidate_id": job.Candidates[0].ID}, 200)
	saved := waitCensor(t, s, "local_saved")
	if saved.Request.OriginalURL == originalURL || archiveCalls.Load() <= metadataCalls || saved.Candidates[0].Provider != "internet_archive" {
		t.Fatal("approval did not refresh original audio and Archive evidence")
	}
	if saved.ExpectedMapping != "https://old.example/42" || saved.RepositoryPath != fixture.repo || saved.Replacement == nil || !saved.Replacement.LocalOnly || saved.Replacement.RequestCompleted {
		t.Fatal("incorrect local result", saved)
	}
	controlAPI(t, s, map[string]any{"action": "approve", "id": saved.ID, "candidate_id": saved.SelectedCandidate}, 200)
	controlAPI(t, s, map[string]any{"action": "fetch"}, 200)
	if rt.snapshot().Jobs[0].Status != "local_saved" {
		t.Fatal("fetch reset completed local state")
	}
	controlAPI(t, s, map[string]any{"action": "native_configure", "repository_path": "/different/repo"}, 409)
	reopened, e := openCensorRuntime(s, rt.uid, &nativeBackend{nativeSource: NativeSources(s)})
	if e != nil {
		t.Fatal(e)
	}
	if reopened.disk.RepositoryPath != fixture.repo || reopened.disk.Jobs[0].Status != "local_saved" {
		t.Fatal("repository/local state not durable")
	}
	controlAPI(t, s, map[string]any{"action": "rollback", "id": saved.ID}, 200)
	waitCensor(t, s, "rolled_back")
	if string(nativeReadTest(t, fixture.repo+"/tracks/42")) != "original" {
		t.Fatal("runtime rollback failed")
	}
	processor.mu.Lock()
	defer processor.mu.Unlock()
	if len(processor.requests) < 3 {
		t.Fatal("missing analysis/recheck")
	}
}

type nativeDiskSnapshotEntry struct {
	Mode     os.FileMode
	Modified time.Time
	Data     string
}

func nativeDiskSnapshot(t *testing.T, root string) map[string]nativeDiskSnapshotEntry {
	t.Helper()
	out := map[string]nativeDiskSnapshotEntry{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		entry := nativeDiskSnapshotEntry{Mode: info.Mode(), Modified: info.ModTime()}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry.Data = string(data)
		}
		out[path] = entry
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestNativeStartupPollingDefersRepositoryRecovery(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing=%v", missing), func(t *testing.T) {
			t.Setenv("FCKCENSOR_API_URL", "")
			fixture, _, selection, request, candidate, _ := nativePublisherFixture(t)
			s := censorServer(t)
			defer s.Close(context.Background())
			rt, err := s.currentCensorRuntime(context.Background(), s.session.User.ID)
			if err != nil {
				t.Fatal(err)
			}
			backend := rt.backend.(*nativeBackend)
			controlAPI(t, s, map[string]any{"action": "native_configure", "repository_path": fixture.repo}, 200)
			publisher := backend.publisher.(*nativePublisher)
			publisher.validate = func(context.Context, string) error { return nil }
			identity := CensorIdentity{UserID: rt.uid}
			receipt, err := publisher.Replace(context.Background(), identity, selection, request, candidate)
			if err != nil {
				t.Fatal(err)
			}
			journalPath := filepath.Join(publisher.history, nativeScope(identity, selection)+".json")
			var journal nativeJournal
			if err = json.Unmarshal(nativeReadTest(t, journalPath), &journal); err != nil {
				t.Fatal(err)
			}
			journal.Done = false
			for _, change := range journal.Changes[1:] {
				if err = os.WriteFile(filepath.Join(publisher.repo, change.Path), nativeReadTest(t, filepath.Join(publisher.history, change.Before)), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err = publisher.saveJournal(journalPath, journal); err != nil {
				t.Fatal(err)
			}
			candidate.URL = "https://audio.example/full.mp3"
			rt.mu.Lock()
			rt.disk.Jobs = []CensorJob{{ID: "pending-publication", Request: request.CensorRequest, Candidates: []CensorCandidate{candidate}, Status: "publishing", PublicationKey: selection.IdempotencyKey, SelectedCandidate: selection.CandidateID, ExpectedMapping: request.ExpectedMapping, RepositoryPath: fixture.repo}}
			err = rt.persistLocked()
			rt.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if err = rt.releaseStorage(); err != nil {
				t.Fatal(err)
			}
			s.mu.Lock()
			s.censorRuntime = nil
			s.mu.Unlock()
			actualRepo := fixture.repo
			if missing {
				actualRepo = filepath.Join(t.TempDir(), "unavailable-repository")
				if err = os.Rename(fixture.repo, actualRepo); err != nil {
					t.Fatal(err)
				}
			}
			repoBefore := nativeDiskSnapshot(t, actualRepo)
			runtimeBefore := nativeDiskSnapshot(t, rt.dir)
			for n := 0; n < 2; n++ {
				w := api(s, "GET", "/api/censor/state", "", "bridge-key")
				var state CensorState
				if err = json.Unmarshal(w.Body.Bytes(), &state); err != nil {
					t.Fatal(err)
				}
				if w.Code != 200 || !state.Native || !state.Configured || state.NativeRepositoryReady || state.RepositoryPath != fixture.repo || len(state.Jobs) != 1 || state.RepositoryError == "" {
					t.Fatal("saved state unavailable", w.Code, w.Body)
				}
				if state.Jobs[0].Status != "review_required" {
					t.Fatal("interrupted publication not restored for review")
				}
				if s.censorRuntime.backend.(*nativeBackend).publisher != nil {
					t.Fatal("polling opened publisher")
				}
			}
			if !reflect.DeepEqual(repoBefore, nativeDiskSnapshot(t, actualRepo)) || !reflect.DeepEqual(runtimeBefore, nativeDiskSnapshot(t, rt.dir)) {
				t.Fatal("GET polling wrote disk or recovered pending journal")
			}
			controlAPI(t, s, map[string]any{"action": "native_configure", "repository_path": "/different/repository"}, 409)
			if missing {
				controlAPI(t, s, map[string]any{"action": "native_configure", "repository_path": fixture.repo}, 502)
				state := s.censorRuntime.snapshot()
				if !state.Configured || !state.Native || state.NativeRepositoryReady || len(state.Jobs) != 1 || state.RepositoryError == "" {
					t.Fatal("failed configuration hid jobs", state)
				}
				if err = os.Rename(actualRepo, fixture.repo); err != nil {
					t.Fatal(err)
				}
			}
			state := controlAPI(t, s, map[string]any{"action": "native_configure", "repository_path": fixture.repo}, 200)
			if !state.NativeRepositoryReady || state.Error != "" || len(state.Jobs) != 1 {
				t.Fatal("explicit recovery not ready", state)
			}
			restored := s.censorRuntime.backend.(*nativeBackend)
			recovered, err := restored.ReplacementReceipt(context.Background(), identity, selection)
			if err != nil || recovered != nativeReplacement(receipt) {
				t.Fatal("explicit configuration did not recover pending journal", err)
			}
			controlAPI(t, s, map[string]any{"action": "approve", "id": "pending-publication", "candidate_id": selection.CandidateID}, 200)
			saved := waitCensor(t, s, "local_saved")
			if saved.ExpectedMapping != request.ExpectedMapping {
				t.Fatal("receipt reconciliation replaced CAS evidence")
			}
			controlAPI(t, s, map[string]any{"action": "rollback", "id": saved.ID}, 200)
			waitCensor(t, s, "rolled_back")
			if string(nativeReadTest(t, fixture.repo+"/tracks/42")) != "original" {
				t.Fatal("reconfigured rollback did not restore preimage")
			}
		})
	}
}
