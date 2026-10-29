package bridge

import (
	"bytes"
	"context"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Only the artwork CDN is trusted, never arbitrary URLs from cached metadata.
func safeYandexCover(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return u.Host == "avatars.yandex.net" || u.Host == "avatars.mds.yandex.net"
}

type coverOutcome struct {
	broken  bool
	expires time.Time
}
type coverBudgetKey struct{}
type coverBudget struct {
	remaining int
	searches  int
	deadline  time.Time
}

func withCoverBudget(ctx context.Context) context.Context {
	return context.WithValue(ctx, coverBudgetKey{}, &coverBudget{remaining: 32, deadline: time.Now().Add(5 * time.Second)})
}

// Called under p.mu: probes are serialized, bounded, and share results by URL.
// Network failures expire quickly; a failed probe never removes existing artwork.
func (p *spotifyService) brokenYandexCover(ctx context.Context, raw string) bool {
	if !safeYandexCover(raw) {
		return false
	}
	now := time.Now()
	if hit, ok := p.coverCache[raw]; ok && now.Before(hit.expires) {
		return hit.broken
	}
	deadline := now.Add(750 * time.Millisecond)
	if budget, ok := ctx.Value(coverBudgetKey{}).(*coverBudget); ok {
		if budget.remaining <= 0 || !now.Before(budget.deadline) {
			return false
		}
		budget.remaining--
		if budget.deadline.Before(deadline) {
			deadline = budget.deadline
		}
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "image/jpeg, image/png, image/gif")
	// Reuse the injected transport, but never follow redirects or forward credentials.
	client := &http.Client{Transport: p.http.Transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	broken := true
	ttl := 30 * time.Second
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			const limit = 2 << 20
			data, readErr := io.ReadAll(io.LimitReader(resp.Body, limit+1))
			if readErr == nil && len(data) <= limit {
				cfg, _, decodeErr := image.DecodeConfig(bytes.NewReader(data))
				broken = decodeErr != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 16384 || cfg.Height > 16384
			}
			if !broken {
				ttl = 10 * time.Minute
			}
		}
	}
	if ctx.Err() != nil && ctx.Err() == context.Canceled {
		return false
	}
	if p.coverCache == nil {
		p.coverCache = make(map[string]coverOutcome)
	}
	if len(p.coverCache) >= 2048 {
		for key, hit := range p.coverCache {
			if !now.Before(hit.expires) {
				delete(p.coverCache, key)
			}
		}
		if len(p.coverCache) >= 2048 {
			clear(p.coverCache)
		}
	}
	p.coverCache[raw] = coverOutcome{broken: broken, expires: now.Add(ttl)}
	return broken
}
