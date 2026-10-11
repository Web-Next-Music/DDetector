package bridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

const archiveFixtureMetadata = `{"metadata":{"identifier":"archive-fixture","mediatype":"audio","title":"Fixture Track","creator":["Fixture Artist"],"licenseurl":"https://creativecommons.org/licenses/by/4.0/","rights":"Fixture attribution"},"files":[{"name":"track.mp3","source":"original","format":"VBR MP3","size":"8","length":"30","title":"Fixture Track"}]}`

func archiveTestResponse(r *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r, ContentLength: int64(len(body))}
}
func archiveFixtureClient(t *testing.T) (*http.Client, *atomic.Int64) {
	t.Helper()
	calls := &atomic.Int64{}
	client := &http.Client{Transport: nativeAudioTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "archive.org" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("apikey") != "" {
			t.Error("unexpected Archive host or forwarded credentials")
		}
		switch r.URL.Path {
		case "/advancedsearch.php":
			if r.Method != "GET" || r.URL.Query().Get("rows") != "12" || r.URL.Query().Get("output") != "json" || r.URL.Query().Get("fl[]") != "identifier" || !strings.HasPrefix(r.URL.Query().Get("q"), "mediatype:audio AND (") {
				t.Error("invalid official search request")
			}
			return archiveTestResponse(r, 200, `{"response":{"docs":[{"identifier":"archive-fixture"},{"identifier":"archive-fixture"}]}}`), nil
		case "/metadata/archive-fixture":
			calls.Add(1)
			return archiveTestResponse(r, 200, archiveFixtureMetadata), nil
		case "/download/archive-fixture/track.mp3":
			if r.Method != "HEAD" {
				t.Error("discovery must not download audio")
			}
			res := archiveTestResponse(r, 200, "")
			res.Header.Set("Content-Type", "audio/mpeg")
			res.ContentLength = 8
			return res, nil
		default:
			t.Errorf("unexpected Archive request %s", r.URL)
			return archiveTestResponse(r, 404, ""), nil
		}
	})}
	return client, calls
}
func TestArchiveSearchAndFreshResolversWithoutYandex(t *testing.T) {
	client, calls := archiveFixtureClient(t)
	provider := newYandexClient("secret")
	provider.http.Transport = nativeAudioTransport(func(r *http.Request) (*http.Response, error) {
		t.Error("candidate discovery contacted Yandex")
		return nil, errors.New("unexpected Yandex request")
	})
	n := &nativeSource{server: &Server{yandex: provider}, archiveHTTP: client}
	candidates, err := n.archiveSearch(context.Background(), "Fixture Artist Track")
	if err != nil || len(candidates) != 1 {
		t.Fatal(candidates, err)
	}
	c := candidates[0]
	if c.ID != archiveCandidateID("archive-fixture", "track.mp3") || c.Provider != "internet_archive" || !c.FullAudio || !c.Downloadable || c.Metadata["license_url"] != "https://creativecommons.org/licenses/by/4.0/" || c.Metadata["source_url"] != "https://archive.org/details/archive-fixture" || c.Metadata["uncensored"] != "unverified" {
		t.Fatal(c)
	}
	// Self-contained IDs survive JSON persistence and a new source instance.
	data, _ := json.Marshal(c)
	var restored CensorCandidate
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	reopened := &nativeSource{archiveHTTP: client}
	for _, resolve := range []func(context.Context, CensorIdentity, string) (CensorCandidate, error){reopened.Candidate, reopened.PlaybackCandidate} {
		fresh, err := resolve(context.Background(), CensorIdentity{}, restored.ID)
		if err != nil || fresh.ID != c.ID || fresh.URL != c.URL || fresh.Metadata["rights"] != "Fixture attribution" {
			t.Fatal(fresh, err)
		}
	}
	if calls.Load() != 3 {
		t.Fatal("resolvers did not refresh metadata", calls.Load())
	}
}
func TestArchiveIDsAndTrustedURLs(t *testing.T) {
	id := archiveCandidateID("valid-item", "album/01 song.mp3")
	item, file, err := archiveDecodeID(id)
	if err != nil || item != "valid-item" || file != "album/01 song.mp3" || archiveDownloadURL(item, file) != "https://archive.org/download/valid-item/album/01%20song.mp3" {
		t.Fatal(id, item, file, err)
	}
	for _, file := range []string{"../track.mp3", "/track.mp3", "a/../track.mp3", "a//track.mp3", "a%2ftrack.mp3", "a?token.mp3", "a\\track.mp3", "a\n.mp3", "track.wav", strings.Repeat("x", 180) + ".mp3"} {
		if archiveCandidateID("valid", file) != "" {
			t.Fatal("accepted unsafe file", file)
		}
	}
	for _, item := range []string{"..", "../bad", "@private", "bad?query", "", strings.Repeat("x", 101)} {
		if archiveCandidateID(item, "track.mp3") != "" {
			t.Fatal("accepted unsafe item", item)
		}
	}
	for _, bad := range []string{archiveCandidatePrefix + "bad", archiveCandidatePrefix + "!!!!:abc", id + "=", archiveCandidatePrefix + base64.RawURLEncoding.EncodeToString([]byte("valid")) + ":" + base64.RawURLEncoding.EncodeToString([]byte("../track.mp3"))} {
		if _, _, err := archiveDecodeID(bad); err == nil {
			t.Fatal("accepted unsafe ID", bad)
		}
	}
	for _, raw := range []string{"http://archive.org/download/a/b.mp3", "https://evilarchive.org/items/a/b.mp3", "https://archive.org.evil.test/items/a/b.mp3", "https://user:secret@archive.org/download/a/b.mp3", "https://archive.org:443/download/a/b.mp3", "https://archive.org/account/login", "https://127.0.0.1/items/a/b.mp3"} {
		u, _ := url.Parse(raw)
		if archiveMediaURL(u) {
			t.Fatal("accepted unsafe URL", raw)
		}
	}
	for _, raw := range []string{"https://archive.org/download/a/b.mp3", "https://ia800001.us.archive.org/1/items/a/b.mp3"} {
		// Data-node URLs include a numeric shard before /items/.
		u, _ := url.Parse(raw)
		if !archiveMediaURL(u) {
			t.Fatal("rejected Archive URL")
		}
	}
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.1.1", "100.64.0.1", "192.0.2.1", "::1", "2001:db8::1"} {
		if archivePublicIP(net.ParseIP(ip)) {
			t.Fatal("accepted private/reserved IP", ip)
		}
	}
}
func archiveFixtureItem(t *testing.T) archiveItem {
	t.Helper()
	var item archiveItem
	if err := json.Unmarshal([]byte(archiveFixtureMetadata), &item); err != nil {
		t.Fatal(err)
	}
	return item
}
func TestArchiveEligibilityFailsClosed(t *testing.T) {
	for _, license := range []string{"https://creativecommons.org/publicdomain/zero/1.0/", "http://creativecommons.org/publicdomain/mark/1.0/", "https://creativecommons.org/licenses/by/3.0/"} {
		item := archiveFixtureItem(t)
		item.Metadata["licenseurl"], _ = json.Marshal(license)
		if _, ok := archiveCandidate(item, "archive-fixture", item.Files[0]); !ok {
			t.Fatal("rejected permissive license", license)
		}
	}
	for _, license := range []string{"", "Public domain", "https://creativecommons.org/licenses/by-nc/4.0/", "https://creativecommons.org/licenses/by-nd/4.0/", "https://creativecommons.org/licenses/by-sa/4.0/", "https://creativecommons.org.evil/licenses/by/4.0/", "https://creativecommons.org/licenses/by/4.0/?x=1"} {
		item := archiveFixtureItem(t)
		item.Metadata["licenseurl"], _ = json.Marshal(license)
		if _, ok := archiveCandidate(item, "archive-fixture", item.Files[0]); ok {
			t.Fatal("accepted unsupported license", license)
		}
	}
	cases := []struct {
		name   string
		mutate func(*archiveItem)
	}{
		{"restricted", func(i *archiveItem) { i.Metadata["access-restricted-item"] = json.RawMessage(`"true"`) }},
		{"nodownload", func(i *archiveItem) { i.Metadata["no-download"] = json.RawMessage(`true`) }},
		{"dark", func(i *archiveItem) { i.Restricted = json.RawMessage(`true`) }},
		{"borrow", func(i *archiveItem) { i.Metadata["lending___is_borrowable"] = json.RawMessage(`true`) }},
		{"collection", func(i *archiveItem) { i.Metadata["collection"] = json.RawMessage(`["restricted_collection"]`) }},
		{"wrong item", func(i *archiveItem) { i.Metadata["identifier"] = json.RawMessage(`"other"`) }},
		{"derivative", func(i *archiveItem) { i.Files[0]["source"] = json.RawMessage(`"derivative"`) }},
		{"preview name", func(i *archiveItem) { i.Files[0]["name"] = json.RawMessage(`"preview.mp3"`) }},
		{"preview flag", func(i *archiveItem) { i.Files[0]["preview"] = json.RawMessage(`true`) }},
		{"private file", func(i *archiveItem) { i.Files[0]["private"] = json.RawMessage(`"true"`) }},
		{"drm", func(i *archiveItem) { i.Files[0]["drm"] = json.RawMessage(`true`) }},
		{"unknown length", func(i *archiveItem) { delete(i.Files[0], "length") }},
		{"oversize", func(i *archiveItem) { i.Files[0]["size"] = json.RawMessage(`"999999999"`) }},
		{"conflicting file license", func(i *archiveItem) {
			i.Files[0]["licenseurl"] = json.RawMessage(`"https://creativecommons.org/licenses/by-nc/4.0/"`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item := archiveFixtureItem(t)
			tc.mutate(&item)
			if _, ok := archiveCandidate(item, "archive-fixture", item.Files[0]); ok {
				t.Fatal("unsafe candidate accepted")
			}
		})
	}
}
func TestArchiveFreshResolverRejectsWithdrawnRights(t *testing.T) {
	for _, resolvePlayback := range []bool{false, true} {
		n := &nativeSource{archiveHTTP: &http.Client{Transport: nativeAudioTransport(func(r *http.Request) (*http.Response, error) {
			if r.Method == "HEAD" {
				t.Error("restricted file probed")
			}
			return archiveTestResponse(r, 200, strings.Replace(archiveFixtureMetadata, "licenses/by/4.0", "licenses/by-nc/4.0", 1)), nil
		})}}
		resolve := n.Candidate
		if resolvePlayback {
			resolve = n.PlaybackCandidate
		}
		if _, err := resolve(context.Background(), CensorIdentity{}, archiveCandidateID("archive-fixture", "track.mp3")); !errors.Is(err, ErrCensorConflict) {
			t.Fatal("withdrawn license accepted", err)
		}
	}
}
func TestArchiveBoundedAPIsCancellationAndNoFallback(t *testing.T) {
	for _, body := range []string{strings.Repeat("x", archiveJSONLimit+1), `not-json`, `{"response":{"docs":[` + strings.Repeat(`{"identifier":"archive-fixture"},`, archiveItemLimit) + `{"identifier":"last"}]}}`, `{"error":"search unavailable"}`} {
		n := &nativeSource{archiveHTTP: &http.Client{Transport: nativeAudioTransport(func(r *http.Request) (*http.Response, error) { return archiveTestResponse(r, 200, body), nil })}}
		if _, err := n.archiveSearch(context.Background(), "track"); err == nil {
			t.Fatal("invalid search response accepted")
		}
	}
	calls := 0
	n := &nativeSource{archiveHTTP: &http.Client{Transport: nativeAudioTransport(func(r *http.Request) (*http.Response, error) { calls++; return archiveTestResponse(r, 302, ""), nil })}}
	if _, err := n.archiveSearch(context.Background(), "track"); err == nil || calls != 1 {
		t.Fatal("API redirect accepted", err, calls)
	}
	for _, query := range []string{"", strings.Repeat("x", 1025)} {
		if _, err := n.archiveSearch(context.Background(), query); err == nil {
			t.Fatal("invalid query accepted")
		}
	}
	n.archiveHTTP.Transport = nativeAudioTransport(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := n.archiveSearch(ctx, "track"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost", err)
	}
}
func TestArchiveQueryIsLiteral(t *testing.T) {
	query := `artist " OR mediatype:texts *:*`
	n := &nativeSource{archiveHTTP: &http.Client{Transport: nativeAudioTransport(func(r *http.Request) (*http.Response, error) {
		expected := `mediatype:audio AND ("artist" AND "\"" AND "OR" AND "mediatype:texts" AND "*:*")`
		if r.URL.Query().Get("q") != expected {
			t.Fatalf("unescaped Lucene query: %q", r.URL.Query().Get("q"))
		}
		return archiveTestResponse(r, 200, `{"response":{"docs":[]}}`), nil
	})}}
	if _, err := n.archiveSearch(context.Background(), query); err != nil {
		t.Fatal(err)
	}
}
func TestArchiveProbeRejectsUnavailableAndPreviewResponses(t *testing.T) {
	for _, tc := range []struct {
		status int
		ctype  string
		size   int64
	}{{403, "audio/mpeg", 8}, {200, "text/html", 8}, {206, "audio/mpeg", 8}, {200, "audio/mpeg", 4}, {200, "audio/mpeg", -1}} {
		n := &nativeSource{archiveHTTP: &http.Client{Transport: nativeAudioTransport(func(r *http.Request) (*http.Response, error) {
			res := archiveTestResponse(r, tc.status, "")
			res.Header.Set("Content-Type", tc.ctype)
			res.ContentLength = tc.size
			return res, nil
		})}}
		item := archiveFixtureItem(t)
		c, _ := archiveCandidate(item, "archive-fixture", item.Files[0])
		if err := n.archiveDownloadable(context.Background(), c); err == nil {
			t.Fatal("non-full public response accepted", tc)
		}
	}
}
func TestArchiveSearchProbeBudget(t *testing.T) {
	probes := 0
	n := &nativeSource{archiveHTTP: &http.Client{Transport: nativeAudioTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == "HEAD" {
			probes++
			return archiveTestResponse(r, 403, ""), nil
		}
		if r.URL.Path == "/advancedsearch.php" {
			return archiveTestResponse(r, 200, `{"response":{"docs":[{"identifier":"archive-fixture"}]}}`), nil
		}
		item := archiveFixtureItem(t)
		item.Files = nil
		for j := 0; j < 100; j++ {
			f := archiveFixtureItem(t).Files[0]
			f["name"] = json.RawMessage(fmt.Sprintf(`"track%d.mp3"`, j))
			item.Files = append(item.Files, f)
		}
		data, _ := json.Marshal(item)
		return archiveTestResponse(r, 200, string(data)), nil
	})}}
	results, err := n.archiveSearch(context.Background(), "track")
	if err != nil || len(results) != 0 || probes != archiveCandidateLimit {
		t.Fatal("unbounded probe requests", err, probes)
	}
}

func TestArchiveDurationFormats(t *testing.T) {
	for _, tc := range []struct {
		value   string
		seconds float64
	}{
		{"30.5", 30.5}, {"01:30.5", 90.5}, {"01:02:03", 3723},
		{"01:99", 0}, {"NaN", 0}, {"-1:30", 0}, {"1:2:3:4", 0},
	} {
		file := archiveRecord{}
		file["length"], _ = json.Marshal(tc.value)
		if got := archiveDuration(file); got != tc.seconds {
			t.Fatal(tc, got)
		}
	}
}

func TestArchiveMetadataAndReturnedCandidateBounds(t *testing.T) {
	for _, count := range []int{100, 1001} {
		probes := 0
		n := &nativeSource{archiveHTTP: &http.Client{Transport: nativeAudioTransport(func(r *http.Request) (*http.Response, error) {
			if r.Method == "HEAD" {
				probes++
				res := archiveTestResponse(r, 200, "")
				res.Header.Set("Content-Type", "audio/mpeg")
				res.ContentLength = 8
				return res, nil
			}
			if r.URL.Path == "/advancedsearch.php" {
				return archiveTestResponse(r, 200, `{"response":{"docs":[{"identifier":"archive-fixture"}]}}`), nil
			}
			item := archiveFixtureItem(t)
			item.Files = nil
			for j := 0; j < count; j++ {
				f := archiveFixtureItem(t).Files[0]
				f["name"] = json.RawMessage(fmt.Sprintf(`"track%d.mp3"`, j))
				item.Files = append(item.Files, f)
			}
			data, _ := json.Marshal(item)
			return archiveTestResponse(r, 200, string(data)), nil
		})}}
		results, err := n.archiveSearch(context.Background(), "track")
		want := archiveCandidateLimit
		if count > 1000 {
			want = 0
		}
		if err != nil || len(results) != want || probes != want {
			t.Fatal(count, len(results), probes, err)
		}
	}
	for _, body := range []string{strings.Repeat("x", archiveJSONLimit+1), `{"error":"unavailable"}`, `[]`} {
		n := &nativeSource{archiveHTTP: &http.Client{Transport: nativeAudioTransport(func(r *http.Request) (*http.Response, error) { return archiveTestResponse(r, 200, body), nil })}}
		if _, err := n.Candidate(context.Background(), CensorIdentity{}, archiveCandidateID("archive-fixture", "track.mp3")); err == nil {
			t.Fatal("invalid metadata accepted")
		}
	}
}
