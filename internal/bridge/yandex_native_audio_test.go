package bridge

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type nativeAudioTransport func(*http.Request) (*http.Response, error)

func (f nativeAudioTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNativeCandidateRefreshesSignedAudio(t *testing.T) {
	descriptors := 0
	c := newYandexClient("provider-secret")
	c.http.Transport = nativeAudioTransport(func(r *http.Request) (*http.Response, error) {
		body := ""
		if r.URL.Host == "api.music.yandex.net" {
			if r.Header.Get("Authorization") != "OAuth provider-secret" {
				t.Error("provider token missing")
			}
			switch r.URL.Path {
			case "/tracks/42":
				body = `{"result":[{"id":42,"title":"track","explicit":true,"available":true,"durationMs":120000}]}`
			case "/tracks/42/download-info":
				body = `{"result":[{"codec":"mp3","preview":false,"bitrateInKbps":320,"downloadInfoUrl":"https://storage.mds.yandex.net/descriptor"}]}`
			default:
				t.Error("unexpected API path")
			}
		} else if r.URL.Host == "storage.mds.yandex.net" {
			if r.Header.Get("Authorization") != "" || r.Header.Get("apikey") != "" {
				t.Error("credentials leaked to descriptor")
			}
			descriptors++
			body = fmt.Sprintf(`<download-info><host>storage.mds.yandex.net</host><path>/full.mp3</path><ts>%d</ts><s>salt</s></download-info>`, descriptors)
		} else {
			t.Error("unexpected host")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	n := &nativeSource{server: &Server{yandex: c}}
	first, err := n.Candidate(context.Background(), CensorIdentity{AccessToken: "foreign-secret"}, "candidate:yandex:42")
	if err != nil {
		t.Fatal(err)
	}
	second, err := n.Candidate(context.Background(), CensorIdentity{}, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.URL == second.URL || !second.FullAudio || !second.Downloadable || descriptors != 2 {
		t.Fatal("lookup did not refresh full audio")
	}
}

func TestNativeDescriptorRedirectRejected(t *testing.T) {
	c := newYandexClient("provider-secret")
	c.http.Transport = nativeAudioTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "api.music.yandex.net" {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"result":[{"codec":"mp3","downloadInfoUrl":"https://storage.mds.yandex.net/descriptor"}]}`)), Request: r}, nil
		}
		if r.URL.Host != "storage.mds.yandex.net" {
			t.Fatal("followed descriptor redirect")
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"https://evil.test/credential-sink"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	if _, err := c.nativeFullMP3(context.Background(), 42); err == nil {
		t.Fatal("accepted descriptor redirect")
	}
}
