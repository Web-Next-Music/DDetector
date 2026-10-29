package bridge

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestCoverProbeBudgetAndExpiry(t *testing.T) {
	s := spotifyTestServer(t)
	p := s.spotifyService()
	calls := 0
	p.http.Transport = spotifyTransport(func(*http.Request) (*http.Response, error) { calls++; return response(404, "missing"), nil })
	ctx := withCoverBudget(context.Background())
	for i := 0; i < 1000; i++ {
		p.brokenYandexCover(ctx, fmt.Sprintf("https://avatars.yandex.net/%d", i))
	}
	if calls != 32 {
		t.Fatalf("unbounded probes: %d", calls)
	}
	raw := "https://avatars.yandex.net/0"
	hit := p.coverCache[raw]
	hit.expires = time.Now().Add(-time.Second)
	p.coverCache[raw] = hit
	p.brokenYandexCover(context.Background(), raw)
	if calls != 33 {
		t.Fatal("expired negative result not refreshed")
	}
	p.brokenYandexCover(context.Background(), "https://example.com/private")
	p.brokenYandexCover(context.Background(), "https://i.scdn.co/image/cached")
	if calls != 33 {
		t.Fatal("probed non-Yandex cover")
	}
}
