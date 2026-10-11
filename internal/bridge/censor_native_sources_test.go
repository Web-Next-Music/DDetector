package bridge

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNativeSourceAnonCredentials(t *testing.T) {
	jwt := func(role string) string {
		return "e30." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"role":%q}`, role))) + ".signature"
	}
	for _, key := range []string{"", "sb_secret_sensitive", jwt("service_role"), jwt("authenticated")} {
		if nativeAnonKey(key) {
			t.Fatal("accepted non-anon credential")
		}
	}
	if !nativeAnonKey(jwt("anon")) || !nativeAnonKey("sb_publishable_test") {
		t.Fatal("rejected public credential")
	}
}
func TestNativeSourceReportedOnlyAndNoForeignJWT(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/rest/v1/reported_tracks" || r.Header.Get("Authorization") != "" || r.Header.Get("apikey") != "sb_publishable_test" {
			t.Error("unexpected source request or credential forwarding")
		}
		if r.URL.Query().Get("limit") != "1000" || r.URL.Query().Get("or") != "(type.eq.track,type.is.null)" || r.URL.Query().Get("select") != "track_id,type" {
			t.Error("missing report type filtering or count bound")
		}
		fmt.Fprint(w, `[{"track_id":42,"type":"track"},{"track_id":"42","type":null},{"track_id":123},{"track_id":"not-a-track","type":"album"},{"track_id":999,"type":"artist"}]`)
	}))
	defer srv.Close()
	c, _ := NewClient(srv.URL, "sb_publishable_test")
	n := &nativeSource{source: c}
	ids, err := n.reported(context.Background())
	if err != nil || len(ids) != 2 || ids[0] != 42 || calls != 1 {
		t.Fatal(ids, err, calls)
	}
	_, err = n.Request(context.Background(), CensorIdentity{AccessToken: "foreign-secret"}, "request:yandex:999")
	if err != ErrCensorNotFound {
		t.Fatal(err)
	}
	for _, opaque := range []string{"candidate:yandex:42", "request:yandex:042", "request:yandex:-1", "request:yandex:42?secret"} {
		if _, err := nativeTrackID("request", opaque); err == nil {
			t.Fatal("accepted", opaque)
		}
	}
}
func TestNativeSourcePermissionObstacle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		fmt.Fprint(w, `{"message":"sensitive response"}`)
	}))
	defer srv.Close()
	c, _ := NewClient(srv.URL, "sb_publishable_test")
	n := &nativeSource{source: c}
	_, err := n.Requests(context.Background(), CensorIdentity{AccessToken: "foreign-secret"})
	if err == nil || !strings.Contains(err.Error(), "reported_tracks") || !strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "sensitive") {
		t.Fatal(err)
	}
}
func TestNativeYandexDescriptor(t *testing.T) {
	path := "/data/136146_d158926e.14534319.6.10994777/320?track-id=10994777&from=service&token=a%2Fb%3D"
	descriptor := fmt.Sprintf(`{"host":"storage.mds.yandex.net","path":%q,"ts":"1730000000","s":"sample-salt"}`, path)
	got, err := nativeYandexMediaURL([]byte(descriptor))
	want := "https://storage.mds.yandex.net/get-mp3/99b451638fd216edd9fd4b2a2373b981/1730000000" + path
	if err != nil || got != want {
		t.Fatal("descriptor signing mismatch", err)
	}
	for _, bad := range []string{`{"host":"evil.test","path":"/a","ts":"1","s":"s"}`, `{"host":"storage.mds.yandex.net/a","path":"/a","ts":"1","s":"s"}`, `{"host":"storage.mds.yandex.net","path":"/a#b","ts":"1","s":"s"}`, `{"host":"storage.mds.yandex.net","path":"/a","ts":"..","s":"s"}`} {
		if _, err := nativeYandexMediaURL([]byte(bad)); err == nil {
			t.Fatal("accepted invalid descriptor")
		}
	}
	xml := `<download-info><host>storage.mds.yandex.net</host><path>/file.mp3</path><ts>65f1aBc0</ts><s>salt</s></download-info>`
	if got, err := nativeYandexMediaURL([]byte(xml)); err != nil || !strings.Contains(got, "/65f1aBc0/file.mp3") {
		t.Fatal(err)
	}
}
func TestNativeLegacyYandexCandidatePreviewRejected(t *testing.T) {
	downloads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "OAuth private-token" {
			t.Error("missing provider authorization")
		}
		switch r.URL.Path {
		case "/tracks/42":
			fmt.Fprint(w, `{"result":[{"id":42,"title":"explicit","explicit":true,"available":true,"durationMs":120000}]}`)
		case "/tracks/42/download-info":
			downloads++
			fmt.Fprint(w, `{"result":[{"codec":"mp3","preview":true,"downloadInfoUrl":"https://storage.mds.yandex.net/preview"},{"codec":"mp3","drm":true,"downloadInfoUrl":"https://storage.mds.yandex.net/drm"}]}`)
		default:
			t.Error("unexpected provider path")
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := newYandexClient("private-token")
	c.base = srv.URL
	n := &nativeSource{server: &Server{yandex: c}}
	out, err := n.Candidate(context.Background(), CensorIdentity{AccessToken: "foreign-secret"}, "candidate:yandex:42")
	if err == nil || out.FullAudio || out.Downloadable || out.URL != "https://music.yandex.ru/track/42" || !strings.Contains(out.Error, "preview") || downloads != 1 {
		t.Fatal(out, err, downloads)
	}
}
