package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSQLiteMigrationAndRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "cache.sqlite")
	legacy := strings.TrimSuffix(path, ".sqlite") + ".json"
	cache := tracksCache{UserID: "u", URL: "source", Fingerprint: detectorFingerprint(), LyricsSources: map[int64]string{1: lyricsSourceVersion}, Tracks: []Result{NewDetector().Process(Track{ID: 1, Title: "Song"}, []Lyric{{Text: "weed"}}, false)}}
	if err := saveTracksCache(legacy, cache); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := readTracksCache(path, "u", "source")
	if err != nil || len(migrated.Tracks) != 1 || needsLyricsAcquisition(migrated.Tracks[0], migrated) {
		t.Fatal(migrated, err)
	}
	after, _ := os.ReadFile(legacy)
	if string(after) != string(original) {
		t.Fatal("legacy copy changed")
	}
	for _, p := range []string{path, legacy} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("private permissions", err)
		}
	}
	db, err := openSQLiteCache(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE cache_tracks SET position=position+1`); err != nil {
		t.Fatal(err)
	}
	journal, err := os.Stat(path + "-journal")
	if err != nil || journal.Mode().Perm() != 0600 {
		t.Fatal("journal permissions", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TRIGGER reject_second BEFORE INSERT ON cache_tracks WHEN NEW.id=2 BEGIN SELECT RAISE(ABORT,'test rollback'); END`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	cache.Fingerprint = "changed"
	cache.Tracks[0].Title = "changed"
	cache.Tracks = append(cache.Tracks, NewDetector().Process(Track{ID: 2}, []Lyric{}, false))
	if err := saveTracksCache(path, cache); err == nil {
		t.Fatal("expected transaction failure")
	}
	saved, err := readTracksCache(path, "u", "source")
	if err != nil || len(saved.Tracks) != 1 || saved.Fingerprint != detectorFingerprint() || saved.Dirty || saved.Tracks[0].Title != "Song" {
		t.Fatal("rollback lost prior snapshot", saved, err)
	}
	other, err := readTracksCache(path, "other", "source")
	if err != nil || len(other.Tracks) != 0 {
		t.Fatal("cross-user cache", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := saveSQLiteCache(ctx, path, cache); err == nil {
		t.Fatal("cancelled save accepted")
	}
}

func TestSQLiteFailedMigrationAndPrivatePaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "cache.sqlite")
	legacy := strings.TrimSuffix(path, ".sqlite") + ".json"
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte(`{"user_id":"u","source_url":"source","tracks":[{"id":0}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readTracksCache(path, "u", "source"); err == nil {
		t.Fatal("invalid migration accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("failed migration published database")
	}
	if err := os.Symlink(legacy, path); err != nil {
		t.Fatal(err)
	}
	if _, err := readTracksCache(path, "u", "source"); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := saveTracksCache(path, tracksCache{}); err == nil {
		t.Fatal("symlink write accepted")
	}
}

func TestSQLiteRejectedCacheRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "cache.sqlite")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte("truncated SQLite cache")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	cache, err := readTracksCache(path, "u", "source")
	if err == nil || !cache.Rebuild || !cache.Dirty {
		t.Fatal("recovery not marked", err)
	}
	cache.Tracks = []Result{{Track: Track{ID: 0}, Status: "none"}}
	if err := saveTracksCache(path, cache); err == nil {
		t.Fatal("invalid recovery accepted")
	}
	bytes, _ := os.ReadFile(path)
	if string(bytes) != string(original) {
		t.Fatal("failed recovery changed original")
	}
	cache.Tracks = []Result{NewDetector().Process(Track{ID: 1}, []Lyric{}, false)}
	if err := saveTracksCache(path, cache); err != nil {
		t.Fatal(err)
	}
	loaded, err := readTracksCache(path, "u", "source")
	if err != nil || len(loaded.Tracks) != 1 || loaded.Rebuild {
		t.Fatal("recovery failed", err)
	}
	// A SQLite file can exceed the JSON payload budget due to pages/free space.
	// Sparse trailing space exercises admission without allocating 256 MiB RAM.
	if err := os.Truncate(path, maxTracksCacheBytes+4096); err != nil {
		t.Fatal(err)
	}
	loaded, err = readTracksCache(path, "u", "source")
	if err != nil || len(loaded.Tracks) != 1 {
		t.Fatal("physical size mistaken for payload", err)
	}
}

func TestSQLiteUnknownVersionIsPreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "cache.sqlite")
	cache := tracksCache{UserID: "u", URL: "source", Fingerprint: detectorFingerprint(), Tracks: []Result{}}
	if err := saveTracksCache(path, cache); err != nil {
		t.Fatal(err)
	}
	db, err := openSQLiteCache(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE cache_meta SET version=2`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := readTracksCache(path, "u", "source")
	if err == nil || loaded.Rebuild {
		t.Fatal("unknown schema marked for destructive recovery", err)
	}
	if err := saveTracksCache(path, loaded); err == nil {
		t.Fatal("unknown schema replaced")
	}
}

func TestSQLiteRejectsSymlinkSidecars(t *testing.T) {
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		t.Run(suffix, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "private", "cache.sqlite")
			cache := tracksCache{UserID: "u", URL: "source", Fingerprint: detectorFingerprint(), Tracks: []Result{}}
			if err := saveTracksCache(path, cache); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "unrelated")
			if err := os.WriteFile(target, []byte("preserve me"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path+suffix); err != nil {
				t.Fatal(err)
			}
			if _, err := readTracksCache(path, "u", "source"); err == nil {
				t.Fatal("symlink sidecar read accepted")
			}
			if err := saveTracksCache(path, cache); err == nil {
				t.Fatal("symlink sidecar write accepted")
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "preserve me" {
				t.Fatal("unrelated sidecar target changed", err)
			}
		})
	}
}

func TestSQLiteIncrementalRowsAndSidecarRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "cache.sqlite")
	cache := tracksCache{UserID: "u", URL: "source", Fingerprint: detectorFingerprint(), Tracks: []Result{
		NewDetector().Process(Track{ID: 1, Title: "first"}, []Lyric{}, false),
		NewDetector().Process(Track{ID: 2, Title: "second"}, []Lyric{}, false),
	}}
	if err := saveTracksCache(path, cache); err != nil {
		t.Fatal(err)
	}
	db, err := openSQLiteCache(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE writes(kind TEXT); CREATE TRIGGER track_writes AFTER UPDATE ON cache_tracks BEGIN INSERT INTO writes VALUES('track'); END; CREATE TRIGGER meta_writes AFTER UPDATE ON cache_meta BEGIN INSERT INTO writes VALUES('meta'); END;`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := saveTracksCache(path, cache); err != nil {
		t.Fatal(err)
	}
	cache.Tracks[1].Title = "changed"
	if err := saveTracksCache(path, cache); err != nil {
		t.Fatal(err)
	}
	db, err = openSQLiteCache(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM writes`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if count != 1 {
		t.Fatalf("expected only the changed track to be rewritten, got %d", count)
	}
	if err := os.WriteFile(path+"-journal", []byte("unresolved journal"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cache.Rebuild = true
	if err := saveTracksCache(path, cache); err == nil {
		t.Fatal("replacement with unresolved journal accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatal("rejected recovery changed original", err)
	}
}

func TestPostgRESTEmptyPageContract(t *testing.T) {
	for _, tc := range []struct {
		body, rangeValue string
		wantError        bool
	}{
		{"", "", true}, {"[]", "", false}, {"[]", "*/0", false},
		{"[]", "*/*", false}, {"[]", "*/2", true}, {"[]", "0-1/2", true},
	} {
		t.Run(tc.body+tc.rangeValue, func(t *testing.T) {
			s := testServer(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.rangeValue != "" {
					w.Header().Set("Content-Range", tc.rangeValue)
				}
				fmt.Fprint(w, tc.body)
			})
			_, err := rows[Track](context.Background(), s.client, "ddetector_tracks", "id", "id.asc", "access")
			if (err != nil) != tc.wantError {
				t.Fatal("empty-page contract", err)
			}
		})
	}
}

func BenchmarkSQLiteCheckpoint(b *testing.B) {
	for _, size := range []int{100, 1000} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "private", "cache.sqlite")
			cache := tracksCache{UserID: "u", URL: "source", Fingerprint: detectorFingerprint()}
			for i := 0; i < size; i++ {
				cache.Tracks = append(cache.Tracks, NewDetector().Process(Track{ID: int64(i + 1)}, []Lyric{{Text: "sample lyrics"}}, false))
			}
			if err := saveTracksCache(path, cache); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				cache.Tracks[0].Title = strconv.Itoa(i)
				if err := saveTracksCache(path, cache); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestPostgRESTRangeContract(t *testing.T) {
	for _, tc := range []struct {
		name       string
		rangeValue string
		status     int
		second     string
		wantError  bool
	}{
		{"known end", "0-1/2", 200, "", false},
		{"416 end", "0-1/*", 416, "*/2", false},
		{"416 unknown", "0-1/*", 416, "", true},
		{"wrong start", "1-2/3", 200, "", true},
		{"wrong size", "0-2/3", 200, "", true},
		{"repeated page", "", 200, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			s := testServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					if tc.rangeValue != "" {
						w.Header().Set("Content-Range", tc.rangeValue)
					}
					fmt.Fprint(w, `[{"id":1},{"id":2}]`)
					return
				}
				if tc.status == 416 {
					if tc.second != "" {
						w.Header().Set("Content-Range", tc.second)
					}
					w.WriteHeader(416)
					fmt.Fprint(w, `{"code":"PGRST103"}`)
					return
				}
				fmt.Fprint(w, `[{"id":1},{"id":2}]`)
			})
			got, err := rows[Track](context.Background(), s.client, "ddetector_tracks", "id", "id.asc", "access")
			if (err != nil) != tc.wantError {
				t.Fatal(got, err)
			}
			if calls > 2 {
				t.Fatal("pagination loop")
			}
		})
	}
}

func TestPostgRESTDeduplicatesTrackIdentity(t *testing.T) {
	calls := 0
	s := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			w.Header().Set("Content-Range", "0-1/4")
			fmt.Fprint(w, `[{"id":1},{"id":2,"added_at":"old"}]`)
		case 2:
			w.Header().Set("Content-Range", "2-3/4")
			fmt.Fprint(w, `[{"id":2,"added_at":"new"},{"id":3}]`)
		default:
			t.Error("unexpected request")
			fmt.Fprint(w, `[]`)
		}
	})
	got, err := rows[Track](context.Background(), s.client, "ddetector_tracks", "id,added_at", "added_at.desc,id.asc", "access")
	if err != nil || len(got) != 3 || calls != 2 {
		t.Fatal(got, err, calls)
	}
}

func TestLRCLIBMetadataContract(t *testing.T) {
	for _, body := range []string{
		`{"id":1,"plainLyrics":"weed"}`,
		`{"id":1,"trackName":"Other","artistName":"Artist","plainLyrics":"weed"}`,
		`{"id":1,"trackName":"Song","artistName":"Other","instrumental":true}`,
	} {
		c := newLRCLIBClient()
		c.transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return response(200, body), nil })
		if _, _, err := c.get(context.Background(), Track{Title: "Song", Artist: "Artist"}); err == nil {
			t.Fatal("unmatched record accepted")
		}
	}
	c := newLRCLIBClient()
	c.transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(200, `{"id":1,"trackName":"  SONG ","artistName":"artist","plainLyrics":"weed"}`), nil
	})
	if lines, _, err := c.get(context.Background(), Track{Title: "Song", Artist: "Artist"}); err != nil || len(lines) != 1 {
		t.Fatal(lines, err)
	}
}

// Read-only and opt-in. Fixtures must be provisioned out of band in an isolated
// test project; neither service-role keys nor production writes belong here.
func TestIntegrationRLSReadContract(t *testing.T) {
	if os.Getenv("DDETECTOR_RLS_TEST") != "1" {
		t.Skip("opt-in isolated PostgREST contract")
	}
	required := func(name string) string {
		t.Helper()
		value := os.Getenv(name)
		if value == "" {
			t.Fatalf("required environment variable: %s", name)
		}
		return value
	}
	c, err := NewClient(required("DDETECTOR_TEST_SUPABASE_URL"), required("DDETECTOR_TEST_ANON_KEY"))
	if err != nil {
		t.Fatal("invalid test client configuration")
	}
	allowed := required("DDETECTOR_TEST_ALLOWED_TOKEN")
	denied := required("DDETECTOR_TEST_DENIED_TOKEN")
	uid := required("DDETECTOR_TEST_ALLOWED_UID")
	fixtureID := required("DDETECTOR_TEST_TRACK_ID")
	id, err := strconv.ParseInt(fixtureID, 10, 64)
	if err != nil || id <= 0 {
		t.Fatal("invalid test fixture track ID")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var allowedUser, deniedUser User
	if err := c.request(ctx, "GET", "/auth/v1/user", allowed, nil, nil, &allowedUser); err != nil || allowedUser.ID != uid {
		t.Fatal("allowed fixture identity mismatch")
	}
	if err := c.request(ctx, "GET", "/auth/v1/user", denied, nil, nil, &deniedUser); err != nil || deniedUser.ID == "" || deniedUser.ID == uid {
		t.Fatal("denied fixture must be a separate authenticated user")
	}
	permitted, banned, err := c.permitted(ctx, allowed, uid)
	if err != nil || !permitted || banned {
		t.Fatal("allowed fixture lacks access")
	}
	var tracks []Track
	if err := c.request(ctx, "GET", "/rest/v1/ddetector_tracks?select=id&id=eq."+url.QueryEscape(fixtureID), allowed, nil, nil, &tracks); err != nil || len(tracks) != 1 || tracks[0].ID != id {
		t.Fatal("allowed fixture track not visible")
	}
	permitted, banned, err = c.permitted(ctx, denied, deniedUser.ID)
	if err != nil || permitted || banned {
		t.Fatal("denied fixture must be an authenticated non-banned user without access")
	}
	assertHidden := func(token, table, filter string) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, "GET", c.URL+"/rest/v1/"+table+"?select=*&limit=1"+filter, nil)
		if err != nil {
			t.Fatal("invalid contract request")
		}
		req.Header.Set("apikey", c.Key)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := c.HTTP.Do(req)
		if err != nil {
			t.Fatal("contract network request failed")
		}
		if res.StatusCode == 401 || res.StatusCode == 403 {
			res.Body.Close()
			return
		}
		var rows []json.RawMessage
		decodeErr := json.NewDecoder(http.MaxBytesReader(nil, res.Body, 1<<20)).Decode(&rows)
		res.Body.Close()
		if res.StatusCode != 200 || decodeErr != nil || rows == nil || len(rows) != 0 {
			t.Fatalf("RLS read isolation failed for %s", table)
		}
	}
	for _, token := range []string{"", denied} {
		for _, table := range []string{"ddetector_tracks", "ddetector_ignored_tracks", "ddetector_users"} {
			assertHidden(token, table, "")
		}
	}
	assertHidden("", "bans", "")
	for _, table := range []string{"ddetector_users", "bans"} {
		assertHidden(allowed, table, "&user_id=neq."+url.QueryEscape(uid))
	}
}
