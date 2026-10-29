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

func TestNativeListsReuseGitHubList(t *testing.T) {
	const id = "126984723"
	const template = "raw.githubusercontent.com/Hazzz895/FckCensorData/refs/heads/main/tracks/%%"
	published := "https://" + strings.ReplaceAll(template, "%%", id)
	storage := `{"urlTemplate":"` + template + `","trackIds":[{"id":119689460,"durationMs":110838},152396542],"tracks":{"149360077":149665384,"155807913":151855578},"unknown":9007199254740993}`
	before := []byte(`{"sources":[{"supportedVersions":">=2.0.0","tracks":{"1":{"title":"keep"}}},{"supportedVersions":">0.0.0","tracksStorages":[` + storage + `]}]}`)
	_, out, err := nativeLists(nil, before, id, "", published, 123456)
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Replace(before, []byte(`"tracks":{"1":{"title":"keep"}}`), []byte(`"tracks":{"1":{"title":"keep"}},"tracksStorages":[{"trackIds":[{"durationMs":123456,"id":126984723}],"urlTemplate":"`+template+`"}]`), 1)
	if !bytes.Equal(out, want) {
		t.Fatalf("unexpected list edit: %s", out)
	}
	for track, target := range map[string]string{id: id, "149360077": "149665384", "155807913": "151855578"} {
		got, err := nativeMapping(out, track)
		if err != nil || got != "https://"+strings.ReplaceAll(template, "%%", target) {
			t.Fatalf("mapping %s: %s %v", track, got, err)
		}
	}
	_, again, err := nativeLists(nil, out, id, published, published, 123456)
	if err != nil || !bytes.Equal(out, again) {
		t.Fatal("not idempotent", err)
	}
	ambiguous := []byte(`{"sources":[{"tracksStorages":[` + storage + `,` + storage + `]}]}`)
	if _, _, err := nativeLists(nil, ambiguous, id, "", published, 123456); err == nil {
		t.Fatal("accepted ambiguous storage")
	}
}

func TestNativeNewTrackSourceGate(t *testing.T) {
	const published = "https://new.example/42"
	for _, gate := range []string{`">0.0.0"`, `"<2.0.0"`, `[">=2.0.0"]`} {
		input := []byte(`{"sources":[{"supportedVersions":` + gate + `,"tracks":{"1":{"title":"Keep"}}}]}`)
		_, out, err := nativeLists(nil, input, "42", "", published, 123456)
		if err != nil {
			t.Fatal(err)
		}
		doc, _ := nativeObject(out)
		sources := doc["sources"].([]any)
		if len(sources) != 2 || sources[1].(map[string]any)["supportedVersions"] != ">=2.0.0" {
			t.Fatalf("wrong source: %s", out)
		}
		if !bytes.Contains(out, input[len(`{"sources":[`):len(input)-2]) {
			t.Fatalf("existing source changed: %s", out)
		}
		if _, has := sources[1].(map[string]any)["tracks"]; has {
			t.Fatal("plain publication created metadata")
		}
	}
	ambiguous := []byte(`{"sources":[{"supportedVersions":">=2.0.0"},{"supportedVersions":">=2.0.0"}]}`)
	if _, _, err := nativeLists(nil, ambiguous, "42", "", published, 123456); !errors.Is(err, ErrCensorConflict) {
		t.Fatal("ambiguous gate accepted", err)
	}
	input := []byte(`{"sources":[{"supportedVersions":">=2.0.0","tracksStorages":[{"urlTemplate":"new.example/%%","tracks":{"1":1}}]}]}`)
	_, out, err := nativeLists(nil, input, "42", "", published, 123456)
	if err != nil || !bytes.Contains(out, []byte(`"42":{"durationMs":123456,"url":"42"}`)) {
		t.Fatalf("templated mapping duration: %s %v", out, err)
	}
}

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
	p.duration = func(_ context.Context, path string) (int64, error) {
		if string(nativeReadTest(t, path)) != "full-mp3" {
			t.Fatal("duration must probe downloaded audio")
		}
		return 123456, nil
	}
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
	if !bytes.Equal(original["list.json"], nativeReadTest(t, filepath.Join(p.repo, "list.json"))) {
		t.Fatal("legacy list changed")
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
			before := nativeReadTest(t, filepath.Join(p.repo, "list_v2.json"))
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
					return os.WriteFile(filepath.Join(p.repo, "list_v2.json"), []byte("external"), 0644)
				}
			case "download":
				api.Close()
			}
			if _, e := p.Replace(ctx, i, s, r, c); e == nil {
				t.Fatal("accepted failure")
			}
			now := nativeReadTest(t, filepath.Join(p.repo, "list_v2.json"))
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
	os.WriteFile(filepath.Join(p.repo, "list_v2.json"), []byte("external"), 0644)
	if e := p.Recover(context.Background()); !errors.Is(e, ErrCensorConflict) {
		t.Fatal("recovery overwrote edit", e)
	}
	if string(nativeReadTest(t, filepath.Join(p.repo, "list_v2.json"))) != "external" {
		t.Fatal("lost edit")
	}
}
func TestNativePublicationNewTrackRollback(t *testing.T) {
	p, i, s, r, c, _ := nativePublisherFixture(t)
	p.validate = func(context.Context, string) error {
		t.Fatal("new tracks must not decode again via validate")
		return nil
	}
	probe := p.duration
	calls := 0
	p.duration = func(ctx context.Context, path string) (int64, error) {
		calls++
		return probe(ctx, path)
	}
	os.Remove(filepath.Join(p.repo, "tracks/42"))
	os.WriteFile(filepath.Join(p.repo, "list.json"), []byte(`{"tracks":{}}`), 0644)
	os.WriteFile(filepath.Join(p.repo, "list_v2.json"), []byte(`{"sources":[{"supportedVersions":">=2.0.0","tracksStorages":[{"urlTemplate":"raw.githubusercontent.com/Hazzz895/FckCensorData/refs/heads/main/tracks/%%","trackIds":[99]}]}]}`), 0644)
	r.ExpectedMapping = ""
	if _, e := p.Replace(context.Background(), i, s, r, c); e != nil {
		t.Fatal(e)
	}
	if calls != 1 {
		t.Fatalf("duration probe calls = %d, want 1", calls)
	}
	out := nativeReadTest(t, filepath.Join(p.repo, "list_v2.json"))
	if !bytes.Contains(out, []byte(`"trackIds":[99,{"durationMs":123456,"id":42}]`)) {
		t.Fatalf("missing numeric ID and downloaded duration: %s", out)
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
		_, out, e := nativeLists(legacy, v2, "42", "https://old.example/42", "https://published.example/42", 123456)
		if e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(string(out), `"custom":true`) {
			t.Fatal("unknown field lost")
		}
		if _, _, e = nativeLists(legacy, v2, "42", "https://wrong.example/42", "https://published.example/42", 123456); !errors.Is(e, ErrCensorConflict) {
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
	for _, invalid := range []string{`{"sources":[],"sources":[]}`, `{"sources":[]} {}`, `null`, `{"sources":{}}`} {
		os.WriteFile(filepath.Join(p.repo, "list_v2.json"), []byte(invalid), 0644)
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
	cmd := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=1.0007", "-codec:a", "libmp3lame", path)
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("generate mp3: %v %s", e, out)
	}
	if e = real.validate(context.Background(), path); e != nil {
		t.Fatal(e)
	}
	durationMs, err := real.duration(context.Background(), path)
	if err != nil || durationMs != 1000 {
		t.Fatalf("decoded duration = %d ms, want 1000: %v", durationMs, err)
	}
	os.WriteFile(path, []byte("not audio"), 0600)
	if e = real.validate(context.Background(), path); e == nil {
		t.Fatal("invalid mp3 accepted")
	}
}

func TestNativeV2OnlyPublication(t *testing.T) {
	for _, legacy := range []string{"absent", "not JSON", `{"tracks":{"42":"https://unrelated.example/42"}}`} {
		t.Run(legacy, func(t *testing.T) {
			p, i, s, r, c, _ := nativePublisherFixture(t)
			path := filepath.Join(p.repo, "list.json")
			if legacy == "absent" {
				os.Remove(path)
			} else {
				os.WriteFile(path, []byte(legacy), 0644)
			}
			mapping, err := p.Mapping(context.Background(), 42)
			if err != nil || mapping != r.ExpectedMapping {
				t.Fatalf("mapping %q: %v", mapping, err)
			}
			if _, err := p.Replace(context.Background(), i, s, r, c); err != nil {
				t.Fatal(err)
			}
			if legacy == "absent" {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("created legacy list", err)
				}
			} else if string(nativeReadTest(t, path)) != legacy {
				t.Fatal("legacy bytes changed")
			}
			var journal nativeJournal
			if err := json.Unmarshal(nativeReadTest(t, filepath.Join(p.history, nativeScope(i, s)+".json")), &journal); err != nil {
				t.Fatal(err)
			}
			for _, change := range journal.Changes {
				if change.Path == "list.json" {
					t.Fatal("legacy list journaled")
				}
			}
		})
	}
}

func TestNativeV2MappingStorages(t *testing.T) {
	for _, storage := range []string{
		`{"urlTemplate":"old.example/%%","trackIds":[{"id":42,"custom":true}]}`,
		`{"urlTemplate":"old.example/%%","tracks":{"42":42}}`,
		`{"tracks":{"42":"https://old.example/42"}}`,
	} {
		data := []byte(`{"sources":[{"tracks":{"99":{"url":"https://metadata.example/99"}},"tracksStorages":[` + storage + `]}]}`)
		mapping, err := nativeMapping(data, "42")
		if err != nil || mapping != "https://old.example/42" {
			t.Fatalf("%s: %q %v", storage, mapping, err)
		}
		if mapping, err := nativeMapping(data, "99"); err != nil || mapping != "" {
			t.Fatalf("metadata used as audio: %q %v", mapping, err)
		}
	}
	if _, err := nativeMapping([]byte(`{"sources":[{"tracksStorages":[{"tracks":{"42":"https://a.example/42"}},{"tracks":{"42":"https://b.example/42"}}]}]}`), "42"); !errors.Is(err, ErrCensorConflict) {
		t.Fatal("ambiguous mapping accepted", err)
	}
}

func TestNativePatchPrettyInsertions(t *testing.T) {
	for _, tc := range []struct {
		name, input, expected string
		value                 any
	}{
		{"source", "{\n  \"sources\": [\n    { \"unknown\" : 1e+03 }\n  ]\n}\n", "{\n  \"sources\": [\n    { \"unknown\" : 1e+03 },\n    {\n      \"tracksStorages\": [\n        {\n          \"tracks\": {\n            \"42\": \"https://new.example/42\"\n          }\n        }\n      ]\n    }\n  ]\n}\n", map[string]any{"sources": []any{map[string]any{"unknown": json.Number("1e+03")}, map[string]any{"tracksStorages": []any{map[string]any{"tracks": map[string]any{"42": "https://new.example/42"}}}}}}},
		{"storage", "{\n  \"tracksStorages\": [\n    { \"unknown\" : \"\\u0061\" }\n  ]\n}\n", "{\n  \"tracksStorages\": [\n    { \"unknown\" : \"\\u0061\" },\n    {\n      \"tracks\": {\n        \"42\": \"https://new.example/42\"\n      }\n    }\n  ]\n}\n", map[string]any{"tracksStorages": []any{map[string]any{"unknown": "a"}, map[string]any{"tracks": map[string]any{"42": "https://new.example/42"}}}}},
		{"map", "{\n  \"tracks\": {\n    \"1\" : \"https://old.example/1\"\n  }\n}\n", "{\n  \"tracks\": {\n    \"1\" : \"https://old.example/1\",\n    \"42\": {\n      \"url\": \"https://new.example/42\"\n    }\n  }\n}\n", map[string]any{"tracks": map[string]any{"1": "https://old.example/1", "42": map[string]any{"url": "https://new.example/42"}}}},
		{"empty", "{\n  \"sources\": [\n  ]\n}\n", "{\n  \"sources\": [\n    {\n      \"tracks\": {}\n    }\n  ]\n}\n", map[string]any{"sources": []any{map[string]any{"tracks": map[string]any{}}}}},
		{"mixed-separators", "{\n  \"tracks\": {\n    \"1\" : \"one\", \n    \"2\":\"two\",\n\n    \"3\" : \"three\"\n  }\n}\n", "{\n  \"tracks\": {\n    \"1\" : \"one\", \n    \"2\":\"two\",\n\n    \"3\" : \"three\", \n    \"42\": \"new\"\n  }\n}\n", map[string]any{"tracks": map[string]any{"1": "one", "2": "two", "3": "three", "42": "new"}}},
		{"compact", `{"sources":[{"unknown":true}]}`, `{"sources":[{"unknown":true},{"tracks":{"42":"https://new.example/42"}}]}`, map[string]any{"sources": []any{map[string]any{"unknown": true}, map[string]any{"tracks": map[string]any{"42": "https://new.example/42"}}}}},
	} {
		for _, layout := range []struct{ name, indent, eol, bom string }{
			{"spaces", "  ", "\n", ""},
			{"tabs-crlf-bom", "\t", "\r\n", "\xef\xbb\xbf"},
		} {
			t.Run(tc.name+"/"+layout.name, func(t *testing.T) {
				format := func(s string) string {
					lines := strings.Split(s, "\n")
					for n, line := range lines {
						spaces := len(line) - len(strings.TrimLeft(line, " "))
						lines[n] = strings.Repeat(layout.indent, spaces/2) + line[spaces:]
					}
					return layout.bom + strings.Join(lines, layout.eol)
				}
				out, err := nativePatchJSON([]byte(format(tc.input)), tc.value)
				if err != nil || string(out) != format(tc.expected) {
					t.Fatalf("patch: %v\ngot: %s\nwant: %s", err, out, format(tc.expected))
				}
			})
		}
	}
}

func TestNativeV2FormattingPreserved(t *testing.T) {
	// An existing direct mapping changes only its entry and the appended storage;
	// unrelated metadata, escaping, key order, BOM and CRLF stay byte-identical.
	input := "\xef\xbb\xbf{\r\n\t\"z\" : 9007199254740993,\r\n\t\"sources\" : [ { \"supportedVersions\" : \">=2.0.0\", \"tracks\":{\"42\":{\"title\":\"\\u006bеep\"}}, \"tracksStorages\" : [ { \"unknown\":true, \"tracks\":{\"42\":\"https://old.example/42\",\"99\":\"https://old.example/99\"} } ] } ],\r\n\t\"a\" : false\r\n}\r\n"
	_, out, err := nativeLists(nil, []byte(input), "42", "https://old.example/42", "https://published.example/42", 123456)
	if err != nil {
		t.Fatal(err)
	}
	expected := strings.Replace(input, `"42":"https://old.example/42",`, "", 1)
	expected = strings.Replace(expected, ` } ] } ]`, ` }, {"tracks":{"42":{"durationMs":123456,"url":"https://published.example/42"}}} ] } ]`, 1)
	if string(out) != expected {
		t.Fatalf("format changed\ngot: %s\nwant: %s", out, expected)
	}
	mapping, err := nativeMapping(out, "42")
	if err != nil || mapping != "https://published.example/42" {
		t.Fatalf("patched mapping %q %v", mapping, err)
	}
	// A no-op patch must preserve even noncanonical number and string spellings.
	raw := []byte("{\"n\":1e+03, \"s\":\"\\u0061\"}\n")
	doc, _ := nativeObject(raw)
	patched, err := nativePatchJSON(raw, doc)
	if err != nil || !bytes.Equal(raw, patched) {
		t.Fatalf("no-op patch: %s %v", patched, err)
	}
}
