package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
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

// Called under p.mu: probes are serialized, bounded, and share results by URL.
// Network failures expire quickly; a failed probe never removes existing artwork.
func (p *coverService) brokenYandexCover(ctx context.Context, raw string) bool {
	if !safeYandexCover(raw) {
		return false
	}
	now := time.Now()
	if hit, ok := p.coverCache[raw]; ok && now.Before(hit.expires) {
		return hit.broken
	}
	deadline := now.Add(750 * time.Millisecond)

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

type coverSearchResult struct {
	url     string
	expires time.Time
}

type coverService struct {
	mu          sync.Mutex
	http        *http.Client
	coverCache  map[string]coverOutcome
	searchCache map[string]coverSearchResult
}

func (s *Server) coverService() *coverService {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.covers == nil {
		s.covers = &coverService{http: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	}
	return s.covers
}

func safeGeniusCover(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Path != "" && u.Path != "/" && (u.Host == "images.genius.com" || u.Host == "images.rapgenius.com")
}

// Artwork lookup never changes the track's metadata or removes existing artwork.
func (s *Server) coverFallback(ctx context.Context, t Track) Track {
	if strings.TrimSpace(t.Title) == "" || strings.TrimSpace(t.Artist) == "" || ctx.Err() != nil {
		return t
	}
	p := s.coverService()
	p.mu.Lock()
	defer p.mu.Unlock()
	if t.Cover != nil && (safeGeniusCover(*t.Cover) || (safeYandexCover(*t.Cover) && !p.brokenYandexCover(ctx, *t.Cover))) {
		return t
	}
	query := url.Values{"artist": {strings.TrimSpace(t.Artist)}, "song": {strings.TrimSpace(t.Title)}}.Encode()
	if hit, ok := p.searchCache[query]; ok && time.Now().Before(hit.expires) {
		if hit.url != "" {
			cover := hit.url
			t.Cover = &cover
		}
		return t
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.sv443.net/geniurl/search/top?"+query, nil)
	if err != nil {
		return t
	}
	req.Header.Set("Accept", "application/json")
	resp, err := p.http.Do(req)
	cover := ""
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var result struct {
				Error     bool `json:"error"`
				Matches   int  `json:"matches"`
				Resources struct {
					Image     string `json:"image"`
					Thumbnail string `json:"thumbnail"`
				} `json:"resources"`
			}
			const limit = 1 << 20
			data, readErr := io.ReadAll(io.LimitReader(resp.Body, limit+1))
			if readErr == nil && len(data) <= limit && json.Unmarshal(data, &result) == nil && !result.Error && result.Matches > 0 {
				for _, raw := range []string{result.Resources.Image, result.Resources.Thumbnail} {
					if safeGeniusCover(raw) {
						cover = raw
						break
					}
				}
			}
		}
	}
	if ctx.Err() != nil {
		return t
	}
	if p.searchCache == nil || len(p.searchCache) >= 2048 {
		p.searchCache = make(map[string]coverSearchResult)
	}
	ttl := 30 * time.Second
	if cover != "" {
		ttl = 24 * time.Hour
		t.Cover = &cover
	}
	p.searchCache[query] = coverSearchResult{url: cover, expires: time.Now().Add(ttl)}
	return t
}
