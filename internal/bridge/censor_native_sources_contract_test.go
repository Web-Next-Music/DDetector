package bridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNativeDefaultUsesReportProjectNotWebsite(t *testing.T) {
	t.Setenv("FCKCENSOR_SUPABASE_URL", "")
	t.Setenv("FCKCENSOR_SUPABASE_ANON_KEY", "")
	n := NativeSources(&Server{client: &Client{URL: "https://website.example", Key: "sb_publishable_website"}})
	if n.configErr != nil || n.source == nil {
		t.Fatal("default source configuration unavailable")
	}
	if n.source.URL != "https://pzomqvgckpgkshxhpite.supabase.co" || !nativeAnonKey(n.source.Key) {
		t.Fatal("incorrect default report project or public credential role")
	}
	parts := splitNativeTestJWT(n.source.Key)
	var claims struct {
		Ref  string `json:"ref"`
		Role string `json:"role"`
	}
	if json.Unmarshal(parts, &claims) != nil || claims.Ref != "pzomqvgckpgkshxhpite" || claims.Role != "anon" {
		t.Fatal("default public credential does not belong to report project")
	}
}
func splitNativeTestJWT(key string) []byte {
	// Never include the credential in a test failure.
	start, end := -1, -1
	for i, c := range key {
		if c == '.' {
			if start == -1 {
				start = i + 1
			} else {
				end = i
				break
			}
		}
	}
	if start < 0 || end < start {
		return nil
	}
	b, _ := base64.RawURLEncoding.DecodeString(key[start:end])
	return b
}

func TestNativeRequestsLazyAndBounded(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Get("limit") != "1000" {
			t.Error("missing bounded fetch")
		}
		fmt.Fprint(w, "[")
		for id := 1; id <= 1000; id++ {
			if id > 1 {
				fmt.Fprint(w, ",")
			}
			fmt.Fprintf(w, `{"track_id":%d,"type":"track"}`, id)
		}
		fmt.Fprint(w, "]")
	}))
	defer srv.Close()
	c, _ := NewClient(srv.URL, "sb_publishable_test")
	provider := newYandexClient("private-provider-token")
	provider.http.Transport = nativeAudioTransport(func(r *http.Request) (*http.Response, error) {
		t.Error("queue fetch must not resolve Yandex metadata or signed streams")
		return nil, fmt.Errorf("unexpected provider request")
	})
	n := &nativeSource{source: c, server: &Server{yandex: provider}}
	out, err := n.Requests(context.Background(), CensorIdentity{AccessToken: "foreign-user-jwt"})
	if err != nil || len(out) != 1000 || requests != 1 || !n.LazyOriginalAudio() {
		t.Fatal("unexpected lazy queue result", len(out), err, requests)
	}
	for _, item := range out {
		if item.OriginalURL != "" || item.ID != nativeOpaqueID("request", item.TrackID) {
			t.Fatal("fetch resolved audio or lost deterministic identity")
		}
	}
}

func TestNativePublicAnonAuthorizationIsNotCallerJWT(t *testing.T) {
	publicKey := "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"role":"anon"}`)) + ".signature"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+publicKey || r.Header.Get("apikey") != publicKey {
			t.Error("source did not use anon authorization")
		}
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()
	c, _ := NewClient(srv.URL, publicKey)
	n := &nativeSource{source: c}
	if _, err := n.Requests(context.Background(), CensorIdentity{AccessToken: "foreign-user-jwt"}); err != nil {
		t.Fatal(err)
	}
}
