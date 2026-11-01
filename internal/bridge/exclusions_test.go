package bridge

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeLocalExclusions(t *testing.T, repo, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, "list_v2.json"), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLocalExclusions(t *testing.T) {
	repo := t.TempDir()
	writeLocalExclusions(t, repo, `{
		"sources": [
			{"tracks": {"999": {"title": "metadata only"}}, "tracksStorages": [
				{"urlTemplate": "https://example.com/%%", "trackIds": ["101", 102, {"id": 103, "durationMs": 1234}]},
				{"tracks": {"104": 204, "105": {"url": "105", "durationMs": 1234}}}
			]},
			{"tracksStorages": [{"trackIds": ["106"]}]}
		]
	}`)

	got, err := localExclusions(repo)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]bool{101: true, 102: true, 103: true, 104: true, 105: true, 106: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("localExclusions() = %v, want %v", got, want)
	}
}

func TestLocalExclusionsInvalidFile(t *testing.T) {
	repo := t.TempDir()
	writeLocalExclusions(t, repo, `{"sources": [`)
	if _, err := localExclusions(repo); err == nil {
		t.Fatal("expected error for invalid list_v2.json")
	}
}

func TestLocalExclusionsMissingFile(t *testing.T) {
	if _, err := localExclusions(t.TempDir()); err == nil {
		t.Fatal("expected error for missing list_v2.json")
	}
}

func TestLocalExclusionsReadsChangedFile(t *testing.T) {
	repo := t.TempDir()
	writeLocalExclusions(t, repo, `{"sources":[{"tracksStorages":[{"trackIds":["101"]}]}]}`)
	got, err := localExclusions(repo)
	if err != nil || !reflect.DeepEqual(got, map[int64]bool{101: true}) {
		t.Fatalf("first read = %v, %v; want track 101", got, err)
	}

	writeLocalExclusions(t, repo, `{"sources":[{"tracksStorages":[{"tracks":{"102":"url"}}]}]}`)
	got, err = localExclusions(repo)
	if err != nil || !reflect.DeepEqual(got, map[int64]bool{102: true}) {
		t.Fatalf("second read = %v, %v; want track 102", got, err)
	}
}
