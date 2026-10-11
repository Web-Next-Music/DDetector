package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func nativePublisherFixture(t *testing.T) (*nativePublisher, CensorIdentity, CensorSelection, NativePublicationRequest, CensorCandidate, *httptest.Server) {
	t.Helper()
	repo := t.TempDir()
	runtime := t.TempDir()
	if e := os.Chmod(runtime, 0700); e != nil {
		t.Fatal(e)
	}
	for _, d := range []string{".git", "tracks"} {
		if e := os.Mkdir(filepath.Join(repo, d), 0700); e != nil {
			t.Fatal(e)
		}
	}
	files := map[string]string{"list.json": `{"unknown":{"large":9007199254740993},"tracks":{"42":"https://old.example/42"}}`, "list_v2.json": `{"unknown":true,"sources":[{"supportedVersions":">=2.0.0","tracks":{"42":{"title":"keep"}},"tracksStorages":[{"unknown":"storage","tracks":{"42":{"url":"https://old.example/42","unknown":"entry","durationMs":1000},"99":"https://old.example/99"}}]}]}`, "tracks/42": "original"}
	for path, b := range files {
		if e := os.WriteFile(filepath.Join(repo, path), []byte(b), 0644); e != nil {
			t.Fatal(e)
		}
	}
	p, e := NewNativePublisher(repo, runtime)
	if e != nil {
		t.Fatal(e)
	}
	p.validate = func(context.Context, string) error { return nil }
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("apikey") != "" {
			t.Error("secret sent")
		}
		w.Write([]byte("full-mp3"))
	}))
	t.Cleanup(api.Close)
	i := CensorIdentity{UserID: "user", AccessToken: "never-persist-this"}
	s := CensorSelection{RequestID: "request:yandex:42", TrackID: 42, CandidateID: "candidate:yandex:99", IdempotencyKey: "key"}
	r := NativePublicationRequest{CensorRequest: CensorRequest{ID: s.RequestID, TrackID: 42, OriginalURL: "https://analysis.example/signed?secret=analysis-secret"}, ExpectedMapping: "https://old.example/42"}
	c := CensorCandidate{ID: s.CandidateID, URL: api.URL + "/signed?token=never-persist-url", Downloadable: true, FullAudio: true}
	return p, i, s, r, c, api
}
func nativeReadTest(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestNativePublicationLocalReceiptAndRollback(t *testing.T) {
	p, i, s, r, c, api := nativePublisherFixture(t)
	ctx := context.Background()
	original := map[string][]byte{}
	for _, path := range []string{"list.json", "list_v2.json", "tracks/42"} {
		original[path] = nativeReadTest(t, filepath.Join(p.repo, path))
	}
	receipt, e := p.Replace(ctx, i, s, r, c)
	if e != nil {
		t.Fatal(e)
	}
	if receipt.Status != "local_only" || receipt.GitHubPublished || receipt.RequestCompleted || !receipt.Durable {
		t.Fatalf("misleading receipt %+v", receipt)
	}
	if string(nativeReadTest(t, filepath.Join(p.repo, "tracks/42"))) != "full-mp3" {
		t.Fatal("audio not persisted")
	}
	l, e := nativeObject(nativeReadTest(t, filepath.Join(p.repo, "list.json")))
	if e != nil {
		t.Fatal(e)
	}
	if l["tracks"].(map[string]any)["42"] != receipt.PublicationURL || l["unknown"].(map[string]any)["large"] != json.Number("9007199254740993") {
		t.Fatal("legacy mapping/precision lost")
	}
	v, _ := nativeObject(nativeReadTest(t, filepath.Join(p.repo, "list_v2.json")))
	sources := v["sources"].([]any)
	if len(sources) != 1 {
		t.Fatal("version gate changed")
	}
	source := sources[0].(map[string]any)
	if source["tracks"].(map[string]any)["42"].(map[string]any)["title"] != "keep" {
		t.Fatal("metadata changed")
	}
	stores := source["tracksStorages"].([]any)
	replacement := stores[1].(map[string]any)["tracks"].(map[string]any)["42"].(map[string]any)
	if replacement["url"] != receipt.PublicationURL || replacement["unknown"] != "entry" {
		t.Fatal("replacement metadata lost")
	}
	if _, ok := replacement["durationMs"]; ok {
		t.Fatal("stale duration retained")
	}
	api.Close()
	again, e := p.Replace(ctx, i, s, r, c)
	if e != nil || again != receipt {
		t.Fatalf("retry: %+v %v", again, e)
	}
	restarted, e := NewNativePublisher(p.repo, filepath.Dir(p.history))
	if e != nil {
		t.Fatal(e)
	}
	got, e := restarted.ReplacementReceipt(ctx, i, s)
	if e != nil || got != receipt {
		t.Fatalf("restart receipt %v", e)
	}
	changed := s
	changed.CandidateID = "other"
	if _, e = p.ReplacementReceipt(ctx, i, changed); !errors.Is(e, ErrCensorNotFound) {
		t.Fatal("candidate scope", e)
	}
	other := i
	other.UserID = "other"
	if _, e = p.ReplacementReceipt(ctx, other, s); !errors.Is(e, ErrCensorNotFound) {
		t.Fatal("identity scope", e)
	}
	entries, _ := os.ReadDir(p.history)
	for _, entry := range entries {
		b := nativeReadTest(t, filepath.Join(p.history, entry.Name()))
		if bytes.Contains(b, []byte(i.AccessToken)) || bytes.Contains(b, []byte("never-persist-url")) {
			t.Fatal("secret persisted")
		}
		st, _ := entry.Info()
		if st.Mode().Perm()&0077 != 0 {
			t.Fatal("nonprivate history")
		}
	}
	rolled, e := p.Rollback(ctx, i, s)
	if e != nil || rolled.Status != "local_rolled_back" {
		t.Fatal("rollback", e)
	}
	for path, b := range original {
		if !bytes.Equal(b, nativeReadTest(t, filepath.Join(p.repo, path))) {
			t.Fatal("preimage not restored", path)
		}
	}
	if _, e = p.Rollback(ctx, i, s); e != nil {
		t.Fatal(e)
	}
}
func TestNativePublicationFailurePreservesFiles(t *testing.T) {
	for _, kind := range []string{"cas", "validator", "symlink", "lock", "cancel", "external-edit", "download"} {
		t.Run(kind, func(t *testing.T) {
			p, i, s, r, c, api := nativePublisherFixture(t)
			ctx := context.Background()
			before := nativeReadTest(t, filepath.Join(p.repo, "list.json"))
			switch kind {
			case "cas":
				r.ExpectedMapping = "https://wrong.example/42"
			case "validator":
				p.validate = func(context.Context, string) error { return errors.New("invalid mp3") }
			case "symlink":
				os.Remove(filepath.Join(p.repo, "tracks/42"))
				os.Symlink(filepath.Join(p.repo, "list.json"), filepath.Join(p.repo, "tracks/42"))
			case "lock":
				os.WriteFile(filepath.Join(p.repo, ".ddetector-upload.lock"), []byte("Qt busy"), 0600)
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "external-edit":
				p.validate = func(context.Context, string) error {
					return os.WriteFile(filepath.Join(p.repo, "list.json"), []byte("external"), 0644)
				}
			case "download":
				api.Close()
			}
			if _, e := p.Replace(ctx, i, s, r, c); e == nil {
				t.Fatal("accepted failure")
			}
			now := nativeReadTest(t, filepath.Join(p.repo, "list.json"))
			if kind == "external-edit" {
				if string(now) != "external" {
					t.Fatal("external edit clobbered")
				}
			} else if !bytes.Equal(now, before) {
				t.Fatal("failed operation mutated list")
			}
		})
	}
}
func TestNativePublicationCrashRecovery(t *testing.T) {
	p, i, s, r, c, _ := nativePublisherFixture(t)
	if _, e := p.Replace(context.Background(), i, s, r, c); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(p.history, nativeScope(i, s)+".json")
	var j nativeJournal
	json.Unmarshal(nativeReadTest(t, path), &j)
	j.Done = false
	for _, change := range j.Changes[1:] {
		b := nativeReadTest(t, filepath.Join(p.history, change.Before))
		if e := os.WriteFile(filepath.Join(p.repo, change.Path), b, 0644); e != nil {
			t.Fatal(e)
		}
	}
	if e := p.saveJournal(path, j); e != nil {
		t.Fatal(e)
	}
	if e := p.Recover(context.Background()); e != nil {
		t.Fatal(e)
	}
	got, e := p.ReplacementReceipt(context.Background(), i, s)
	if e != nil || got.Status != "local_only" {
		t.Fatal(e)
	}
	// Recovery never overwrites a third-party edit, even after a partial write.
	j.Done = false
	p.saveJournal(path, j)
	os.WriteFile(filepath.Join(p.repo, "list.json"), []byte("external"), 0644)
	if e := p.Recover(context.Background()); !errors.Is(e, ErrCensorConflict) {
		t.Fatal("recovery overwrote edit", e)
	}
	if string(nativeReadTest(t, filepath.Join(p.repo, "list.json"))) != "external" {
		t.Fatal("lost edit")
	}
}
func TestNativePublicationNewTrackRollback(t *testing.T) {
	p, i, s, r, c, _ := nativePublisherFixture(t)
	os.Remove(filepath.Join(p.repo, "tracks/42"))
	os.WriteFile(filepath.Join(p.repo, "list.json"), []byte(`{"tracks":{}}`), 0644)
	os.WriteFile(filepath.Join(p.repo, "list_v2.json"), []byte(`{"sources":[]}`), 0644)
	r.ExpectedMapping = ""
	if _, e := p.Replace(context.Background(), i, s, r, c); e != nil {
		t.Fatal(e)
	}
	if _, e := p.Rollback(context.Background(), i, s); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(p.repo, "tracks/42")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("new audio not removed")
	}
}
func TestNativeListsTemplatedAndDirectMappings(t *testing.T) {
	for _, storage := range []string{`{"urlTemplate":"old.example/%%","trackIds":[{"id":42,"durationMs":1000,"custom":true},99]}`, `{"urlTemplate":"old.example/%%","tracks":{"42":{"url":"42","custom":true},"99":99}}`, `{"tracks":{"42":{"url":"https://old.example/42","custom":true},"99":"https://old.example/99"}}`} {
		legacy := []byte(`{"tracks":{"42":"https://old.example/42"}}`)
		v2 := []byte(`{"sources":[{"supportedVersions":[">=2.0.0"],"tracksStorages":[` + storage + `]}]}`)
		_, out, e := nativeLists(legacy, v2, "42", "https://old.example/42", "https://published.example/42")
		if e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(string(out), `"custom": true`) {
			t.Fatal("unknown field lost")
		}
		if _, _, e = nativeLists(legacy, v2, "42", "https://wrong.example/42", "https://published.example/42"); !errors.Is(e, ErrCensorConflict) {
			t.Fatal("CAS missing")
		}
	}
}
func TestNativeMappingAndMalformedLists(t *testing.T) {
	p, _, _, r, _, _ := nativePublisherFixture(t)
	mapping, err := p.Mapping(context.Background(), 42)
	if err != nil || mapping != r.ExpectedMapping {
		t.Fatalf("mapping=%q err=%v", mapping, err)
	}
	for _, invalid := range []string{`{"tracks":{"42":"a","42":"b"}}`, `{"tracks":{}} {}`, `null`, `{"tracks":[]}`} {
		os.WriteFile(filepath.Join(p.repo, "list.json"), []byte(invalid), 0644)
		if _, err := p.Mapping(context.Background(), 42); err == nil {
			t.Fatalf("accepted %s", invalid)
		}
	}
}

func TestNativeConstructorBoundaries(t *testing.T) {
	p, _, _, _, _, _ := nativePublisherFixture(t)
	for _, pair := range [][2]string{{"", filepath.Dir(p.history)}, {p.repo, p.repo}, {p.repo, "relative"}} {
		if _, err := NewNativePublisher(pair[0], pair[1]); err == nil {
			t.Fatal("accepted invalid boundary", pair)
		}
	}
	public := t.TempDir()
	os.Chmod(public, 0755)
	if _, err := NewNativePublisher(p.repo, public); err == nil {
		t.Fatal("accepted public runtime")
	}
	linked := filepath.Join(t.TempDir(), "repo")
	os.Symlink(p.repo, linked)
	if _, err := NewNativePublisher(linked, filepath.Dir(p.history)); err == nil {
		t.Fatal("accepted symlink repo")
	}
}

func TestNativeRollbackCrashRecovery(t *testing.T) {
	p, i, s, r, c, _ := nativePublisherFixture(t)
	if _, err := p.Replace(context.Background(), i, s, r, c); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(p.history, nativeScope(i, s)+".json")
	var j nativeJournal
	json.Unmarshal(nativeReadTest(t, path), &j)
	for n, change := range j.Changes {
		j.Changes[n] = nativeFileChange{Path: change.Path, Before: change.After, After: change.Before, Existed: true}
	}
	j.Done = false
	j.RolledBack = true
	j.Receipt.Status = "local_rolled_back"
	p.saveJournal(path, j)
	audio := j.Changes[0]
	os.WriteFile(filepath.Join(p.repo, audio.Path), nativeReadTest(t, filepath.Join(p.history, audio.After)), 0644)
	if err := p.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := p.Rollback(context.Background(), i, s)
	if err != nil || got.Status != "local_rolled_back" {
		t.Fatal(got, err)
	}
	if string(nativeReadTest(t, filepath.Join(p.repo, "tracks/42"))) != "original" {
		t.Fatal("rollback recovery failed")
	}
}

func TestNativeMP3Validator(t *testing.T) {
	if _, e := exec.LookPath("ffmpeg"); e != nil {
		t.Skip("ffmpeg unavailable")
	}
	p, _, _, _, _, _ := nativePublisherFixture(t)
	real, e := NewNativePublisher(p.repo, filepath.Dir(p.history))
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "audio.mp3")
	cmd := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-codec:a", "libmp3lame", path)
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("generate mp3: %v %s", e, out)
	}
	if e = real.validate(context.Background(), path); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(path, []byte("not audio"), 0600)
	if e = real.validate(context.Background(), path); e == nil {
		t.Fatal("invalid mp3 accepted")
	}
}
