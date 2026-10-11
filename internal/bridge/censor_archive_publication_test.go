package bridge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveDownloadRedirectSafety(t *testing.T) {
	item := archiveFixtureItem(t)
	candidate, _ := archiveCandidate(item, "archive-fixture", item.Files[0])
	for _, target := range []string{"https://ia800001.us.archive.org/1/items/archive-fixture/track.mp3", "https://evil.example/track.mp3", "http://archive.org/download/archive-fixture/track.mp3", "https://archive.org/account/login", "https://user:secret@archive.org/download/archive-fixture/track.mp3"} {
		t.Run(target, func(t *testing.T) {
			calls := 0
			n := &nativeSource{archiveHTTP: &http.Client{Transport: nativeAudioTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					res := archiveTestResponse(r, 302, "")
					res.Header.Set("Location", target)
					return res, nil
				}
				if r.URL.Host != "ia800001.us.archive.org" || r.Header.Get("Authorization") != "" {
					t.Fatal("unsafe redirect followed")
				}
				res := archiveTestResponse(r, 200, "")
				res.Header.Set("Content-Type", "audio/mpeg")
				res.ContentLength = 8
				return res, nil
			})}}
			err := n.archiveDownloadable(context.Background(), candidate)
			allowed := strings.HasPrefix(target, "https://ia800001.us.archive.org/")
			if allowed && (err != nil || calls != 2) || !allowed && (err == nil || calls != 1) {
				t.Fatal("incorrect redirect decision", err, calls)
			}
		})
	}
}
func TestArchivePublicationPreservesLicenseAndProvenance(t *testing.T) {
	p, i, s, r, _, _ := nativePublisherFixture(t)
	item := archiveFixtureItem(t)
	c, _ := archiveCandidate(item, "archive-fixture", item.Files[0])
	s.CandidateID = c.ID
	calls := 0
	p.client.Transport = nativeAudioTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Header.Get("Authorization") != "" {
			t.Fatal("publication leaked credentials")
		}
		if calls == 1 {
			res := archiveTestResponse(req, 302, "")
			res.Header.Set("Location", "https://ia800001.us.archive.org/1/items/archive-fixture/track.mp3")
			return res, nil
		}
		return archiveTestResponse(req, 200, "full-mp3"), nil
	})
	receipt, err := p.Replace(context.Background(), i, s, r, c)
	if err != nil || !receipt.Durable || calls != 2 {
		t.Fatal(receipt, err, calls)
	}
	data, err := os.ReadFile(filepath.Join(p.history, nativeScope(i, s)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var journal nativeJournal
	if err := json.Unmarshal(data, &journal); err != nil {
		t.Fatal(err)
	}
	if journal.Provenance["license_url"] != c.Metadata["license_url"] || journal.Provenance["source_url"] != c.Metadata["source_url"] || journal.Provenance["attribution_creator"] != "Fixture Artist" {
		t.Fatal("license/provenance lost", journal.Provenance)
	}
}
func TestArchiveLegacyYandexResolversStillWork(t *testing.T) {
	c := newYandexClient("legacy-secret")
	c.http.Transport = nativeAudioTransport(func(r *http.Request) (*http.Response, error) {
		body := ""
		switch r.URL.Path {
		case "/tracks/99":
			body = `{"result":[{"id":99,"title":"Legacy Fixture","available":true,"explicit":true,"durationMs":30000}]}`
		case "/tracks/99/download-info":
			body = `{"result":[{"codec":"mp3","downloadInfoUrl":"https://storage.mds.yandex.net/descriptor"}]}`
		case "/descriptor":
			body = `<download-info><host>storage.mds.yandex.net</host><path>/full.mp3</path><ts>1234</ts><s>salt</s></download-info>`
		default:
			t.Fatalf("unexpected legacy request %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	n := &nativeSource{server: &Server{yandex: c}}
	for _, resolve := range []func(context.Context, CensorIdentity, string) (CensorCandidate, error){n.Candidate, n.PlaybackCandidate} {
		result, err := resolve(context.Background(), CensorIdentity{}, "candidate:yandex:99")
		if err != nil || !result.FullAudio || !result.Downloadable || result.Provider != "yandex" {
			t.Fatal(result, err)
		}
	}
}
func TestArchiveRuntimeDoesNotInstallYandexMetadataDiscovery(t *testing.T) {
	s, b, _, _ := nativeManualFixture(t)
	if b.runtime.metadataProvider != nil {
		t.Fatal("Yandex metadata candidate discovery installed")
	}
	if s.yandex == nil {
		t.Fatal("original Yandex access removed")
	}
}
