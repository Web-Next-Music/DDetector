package bridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestNativeRestorationDuration(t *testing.T) {
	const old = "https://old.example/42"
	const published = "https://new.example/42"
	storages := []string{
		`{"urlTemplate":"old.example/%%","trackIds":[42,99]}`,
		`{"urlTemplate":"old.example/%%","trackIds":[{"id":42,"durationMs":1000,"custom":true},99]}`,
		`{"urlTemplate":"old.example/%%","tracks":{"42":42,"99":99}}`,
		`{"urlTemplate":"old.example/%%","tracks":{"42":{"url":"42","durationMs":1000,"custom":true},"99":99}}`,
		`{"urlTemplate":"ignored.example/%%","tracks":{"42":{"url":"https://old.example/42","custom":true}}}`,
		`{"tracks":{"42":"https://old.example/42","99":"https://old.example/99"}}`,
		`{"tracks":{"42":{"url":"https://old.example/42","durationMs":1000,"custom":true}}}`,
	}
	for _, storage := range storages {
		for _, destination := range []string{old, published} {
			t.Run(storage+destination, func(t *testing.T) {
				before := []byte(`{"sources":[{"tracks":{"42":{"title":"keep"}},"tracksStorages":[` + storage + `]}]}`)
				legacy := []byte("untouched legacy")
				gotLegacy, out, err := nativeLists(legacy, before, "42", old, destination, 123456)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(gotLegacy, legacy) {
					t.Fatal("legacy changed")
				}
				mapping, err := nativeMapping(out, "42")
				if err != nil || mapping != destination {
					t.Fatalf("mapping = %q: %v", mapping, err)
				}
				doc, err := nativeObject(out)
				if err != nil {
					t.Fatal(err)
				}
				count := 0
				for _, raw := range doc["sources"].([]any)[0].(map[string]any)["tracksStorages"].([]any) {
					store := raw.(map[string]any)
					check := func(entry any) {
						t.Helper()
						metadata, ok := entry.(map[string]any)
						if !ok || metadata["durationMs"] != json.Number("123456") {
							t.Fatalf("missing measured duration: %s", out)
						}
						if bytes.Contains([]byte(storage), []byte(`"custom":true`)) && metadata["custom"] != true {
							t.Fatal("metadata lost")
						}
						count++
					}
					if tracks, ok := store["tracks"].(map[string]any); ok {
						if entry, has := tracks["42"]; has {
							check(entry)
						}
					}
					if ids, ok := store["trackIds"].([]any); ok {
						for _, entry := range ids {
							if nativeID(entry) == "42" {
								check(entry)
							}
						}
					}
				}
				if count != 1 {
					t.Fatalf("entries = %d", count)
				}
				_, again, err := nativeLists(legacy, out, "42", destination, destination, 123456)
				if err != nil || !bytes.Equal(out, again) {
					t.Fatalf("not byte-idempotent: %s: %v", again, err)
				}
				_, unchanged, err := nativeLists(legacy, before, "42", old, old, 0)
				if err != nil || !bytes.Equal(before, unchanged) {
					t.Fatal("nonpositive duration changed same-URL list", err)
				}
				if _, _, err := nativeLists(legacy, before, "42", published, destination, 123456); !errors.Is(err, ErrCensorConflict) {
					t.Fatal("CAS bypassed", err)
				}
			})
		}
	}
}

func TestNativeRestorationNewMapDuration(t *testing.T) {
	for _, storage := range []string{``, `,"tracksStorages":[{"urlTemplate":"new.example/%%","tracks":{"99":99}}]`, `,"tracksStorages":[{"urlTemplate":"new.example/%%","trackIds":[99]}]`} {
		before := []byte(`{"sources":[{"supportedVersions":">=2.0.0"` + storage + `}]}`)
		_, out, err := nativeLists(nil, before, "42", "", "https://new.example/42", 123456)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(out, []byte(`"durationMs":123456`)) {
			t.Fatalf("missing duration: %s", out)
		}
		mapping, err := nativeMapping(out, "42")
		if err != nil || mapping != "https://new.example/42" {
			t.Fatalf("mapping = %q: %v", mapping, err)
		}
	}
}

func TestNativeRestorationExplicitURL(t *testing.T) {
	for _, value := range []any{"https://explicit.example/42", map[string]any{"url": "https://explicit.example/42"}, "http://explicit.example/42"} {
		want := value
		if metadata, ok := value.(map[string]any); ok {
			want = metadata["url"]
		}
		got, err := nativeURL("template.example/%%", value)
		if err != nil || got != want {
			t.Fatalf("URL = %q, want %q: %v", got, want, err)
		}
	}
	for _, value := range []any{"ftp://explicit.example/42", "https://user@explicit.example/42"} {
		if _, err := nativeURL("template.example/%%", value); !errors.Is(err, ErrCensorConflict) {
			t.Fatal("invalid explicit URL accepted", err)
		}
	}
}
