package bridge

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

type siteTestTransport func(*http.Request) (*http.Response, error)

func (f siteTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func siteTestResponse(r *http.Request, kind, body string) *http.Response {
	return &http.Response{
		StatusCode:    http.StatusOK,
		Header:        http.Header{"Content-Type": {kind}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       r,
	}
}

func TestDirectSiteDefinitions(t *testing.T) {
	if len(directSites) != 29 {
		t.Fatalf("site count = %d", len(directSites))
	}
	seen := map[string]bool{}
	for _, site := range directSites {
		if seen[site.host] {
			t.Fatalf("duplicate site %q", site.host)
		}
		seen[site.host] = true
		raw := site.searchURL("Artist Song")
		if !strings.HasPrefix(raw, "https://"+site.host+"/") || !strings.Contains(raw, "Artist") || strings.Contains(strings.ToLower(raw), "sefon") {
			t.Fatalf("unsafe search definition for %q: %q", site.host, raw)
		}
	}
	if seen["sefon.pro"] {
		t.Fatal("blocked domain present")
	}
}

func TestSiteSearchFailSoftAggregationAndMetadata(t *testing.T) {
	var mu sync.Mutex
	requested := map[string]int{}
	n := &nativeSource{webHTTP: &http.Client{Transport: siteTestTransport(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		requested[r.URL.Hostname()]++
		mu.Unlock()
		switch r.URL.Hostname() {
		case "hitmos.fm":
			if strings.HasSuffix(r.URL.Path, ".mp3") {
				return siteTestResponse(r, "audio/mpeg", "audio-one"), nil
			}
			return siteTestResponse(r, "text/html", `<a href="/Artist-Song.mp3">Artist Song</a>`), nil
		case "hitmos.me":
			return nil, errors.New("temporary failure")
		case "hitmo.net":
			if r.URL.Path == "/track/artist-song" {
				return siteTestResponse(r, "text/html", `<audio src="/Artist-Song-Two.mp3"></audio>`), nil
			}
			if strings.HasSuffix(r.URL.Path, ".mp3") {
				return siteTestResponse(r, "audio/mpeg", "audio-two"), nil
			}
			return siteTestResponse(r, "text/html", `<a href="/track/artist-song">Artist Song</a><a href="https://other.example/bad">Artist Song</a>`), nil
		default:
			return siteTestResponse(r, "text/html", "no results"), nil
		}
	})}}

	got, err := n.siteSearch(context.Background(), "Artist Song")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("candidates = %+v", got)
	}
	for _, candidate := range got {
		if candidate.Provider != "direct_web" || candidate.Metadata["discovery_provider"] != "direct_web" || candidate.Metadata["site"] == "" || candidate.Metadata["content_type"] != "audio/mpeg" {
			t.Fatalf("invalid candidate metadata: %+v", candidate)
		}
	}
	if requested["sefon.pro"] != 0 || requested["other.example"] != 0 {
		t.Fatalf("blocked or cross-site host requested: %+v", requested)
	}
	for _, site := range directSites {
		if requested[site.host] == 0 {
			t.Errorf("site not searched: %s", site.host)
		}
	}
}

func TestTrackMatchRequiresEveryTokenInOneEvidenceField(t *testing.T) {
	query := "Strict Artist Exact Song"
	if trackMatch(query, CensorCandidate{Metadata: map[string]string{
		"audio_url":  "https://cdn.example/strict-artist.mp3",
		"page_title": "Exact Song",
		"source_url": "https://music.example/track/7",
	}}) {
		t.Fatal("accepted tokens combined from unrelated evidence fields")
	}
	if !trackMatch(query, CensorCandidate{Metadata: map[string]string{
		"audio_url": "https://cdn.example/strict-artist-exact-song.mp3",
	}}) {
		t.Fatal("rejected exact artist/title evidence")
	}
	if trackMatch(query, CensorCandidate{Metadata: map[string]string{
		"page_title": "Strict Artist Different Song",
	}}) {
		t.Fatal("accepted a different track by the same artist")
	}
}

func TestSiteSearchStartsAllSitesTogetherAndReportsRealProgress(t *testing.T) {
	var mu sync.Mutex
	started := map[string]bool{}
	inFlight, peak := 0, 0
	gate := make(chan struct{})
	n := &nativeSource{webHTTP: &http.Client{Transport: siteTestTransport(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		first := !started[r.URL.Hostname()]
		if first {
			started[r.URL.Hostname()] = true
			inFlight++
			if inFlight > peak {
				peak = inFlight
			}
			if len(started) == len(directSites) {
				close(gate)
			}
		}
		mu.Unlock()
		if first {
			<-gate
		}
		return siteTestResponse(r, "text/html", "no results"), nil
	})}}
	progress := []int{}
	sites := map[string]bool{}
	ctx := withSiteProgress(context.Background(), func(site string, completed, total int) error {
		if total != len(directSites) {
			t.Fatalf("progress total = %d", total)
		}
		sites[site] = true
		progress = append(progress, completed)
		return nil
	})
	if _, err := n.siteSearch(ctx, "Strict Artist Exact Song"); err != nil {
		t.Fatal(err)
	}
	if peak != len(directSites) || len(started) != len(directSites) {
		t.Fatalf("sites were not started together: peak=%d started=%d", peak, len(started))
	}
	if len(progress) != len(directSites) || len(sites) != len(directSites) || progress[0] != 1 || progress[len(progress)-1] != len(directSites) {
		t.Fatalf("real progress not reported per site: %v sites=%v", progress, sites)
	}
}

func TestSiteSearchPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&nativeSource{}).siteSearch(ctx, "Artist Song"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
