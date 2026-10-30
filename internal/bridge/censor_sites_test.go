package bridge

import (
	"context"
	"encoding/json"
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

	completedSites := map[string]bool{}
	streamed := 0
	ctx := withSiteProgress(context.Background(), func(site string, completed, total int) error {
		completedSites[site] = true
		return nil
	})
	ctx = context.WithValue(ctx, siteCandidatesContextKey{}, siteCandidatesCallback(func(candidates []CensorCandidate) error {
		for _, candidate := range candidates {
			if completedSites[candidate.Metadata["site"]] {
				t.Fatal("candidate was delayed until after site completion")
			}
			streamed++
		}
		return nil
	}))
	got, err := n.siteSearch(ctx, "Artist Song")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || streamed != len(got) {
		t.Fatalf("candidates = %+v, streamed = %d", got, streamed)
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
	batches := 0
	sites := map[string]bool{}
	ctx := withSiteProgress(context.Background(), func(site string, completed, total int) error {
		if total != len(directSites) {
			t.Fatalf("progress total = %d", total)
		}
		if batches != 0 {
			t.Fatalf("empty search must not publish candidate batches: %d", batches)
		}
		sites[site] = true
		progress = append(progress, completed)
		return nil
	})
	ctx = context.WithValue(ctx, siteCandidatesContextKey{}, siteCandidatesCallback(func(candidates []CensorCandidate) error {
		batches++
		return nil
	}))
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

func TestHitmosPlaybackFallback(t *testing.T) {
	for _, download := range []string{"", "https://dwc1.hitmos.fm/unavailable.mp3"} {
		t.Run(download, func(t *testing.T) {
			payload := `0:{"tracks":[{"id":999,"title":"Other","artist":"Other","play":"https://plc1.hitmos.fm/wrong.mp3"},{"id":14377458,"title":"Виновата скорость (2025)","artist":"Расстройство","download":"` + download + `","play":"https://plc1.hitmos.fm/track.mp3"}]}`
			encoded, _ := json.Marshal(payload)
			body := `<script>self.__next_f.push([1,` + string(encoded) + `])</script>`
			n := &nativeSource{webHTTP: &http.Client{Transport: siteTestTransport(func(r *http.Request) (*http.Response, error) {
				switch r.URL.Hostname() {
				case "rus.hitmos.fm":
					return siteTestResponse(r, "text/html", body), nil
				case "dwc1.hitmos.fm":
					return nil, errors.New("download unavailable")
				case "plc1.hitmos.fm":
					if r.URL.Path != "/track.mp3" {
						t.Errorf("requested recommendation: %s", r.URL)
					}
					return siteTestResponse(r, "audio/mpeg", "audio"), nil
				default:
					t.Fatalf("unexpected request: %s", r.URL)
					return nil, errors.New("unexpected host")
				}
			})}}
			got, err := n.webDiscover(context.Background(), "https://rus.hitmos.fm/song/14377458")
			if err != nil || len(got) != 1 {
				t.Fatalf("candidates = %+v, error = %v", got, err)
			}
			if got[0].URL != "https://plc1.hitmos.fm/track.mp3" || !got[0].Downloadable || !trackMatch("Расстройство Виновата скорость", got[0]) {
				t.Fatalf("invalid playback candidate: %+v", got[0])
			}
			if _, err := n.webResolve(context.Background(), got[0].ID); err != nil {
				t.Fatalf("cannot resolve playback candidate: %v", err)
			}
		})
	}
}

func TestHitmosSearchParameter(t *testing.T) {
	for _, site := range directSites {
		if site.host == "hitmos.fm" && site.searchURL("Artist Song") != "https://hitmos.fm/search?q=Artist+Song" {
			t.Fatalf("incorrect Hitmos search: %s", site.searchURL("Artist Song"))
		}
	}
}

func TestSiteSearchPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&nativeSource{}).siteSearch(ctx, "Artist Song"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
