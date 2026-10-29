package bridge

import (
	"context"

	"net/url"
	"sort"
	"strings"
	"time"
)

type spotifyTrack struct {
	Name    string `json:"name"`
	Artists []struct {
		Name string `json:"name"`
	} `json:"artists"`
	ExternalIDs struct {
		ISRC string `json:"isrc"`
	} `json:"external_ids"`
	Album struct {
		Images []struct {
			URL string `json:"url"`
		} `json:"images"`
	} `json:"album"`
}

func spotifyNormalize(v string) string { return strings.ToLower(strings.Join(strings.Fields(v), " ")) }
func spotifyArtistSet(names []string) string {
	set := map[string]bool{}
	for _, n := range names {
		n = spotifyNormalize(n)
		if n != "" {
			set[n] = true
		}
	}
	values := make([]string, 0, len(set))
	for n := range set {
		values = append(values, n)
	}
	sort.Strings(values)
	return strings.Join(values, "\x00")
}
func spotifyISRC(t Track) string {
	return normalizeISRC(t.ISRC)
}
func spotifyMatch(t Track, candidates []spotifyTrack) *spotifyTrack {
	isrc := spotifyISRC(t)
	var match *spotifyTrack
	for i := range candidates {
		c := &candidates[i]
		valid := false
		if isrc != "" {
			valid = strings.EqualFold(strings.TrimSpace(c.ExternalIDs.ISRC), isrc)
		} else if strings.TrimSpace(t.Title) != "" && strings.TrimSpace(t.Artist) != "" {
			names := []string{}
			for _, a := range c.Artists {
				names = append(names, a.Name)
			}
			valid = spotifyNormalize(t.Title) == spotifyNormalize(c.Name) && spotifyArtistSet(strings.Split(t.Artist, ", ")) == spotifyArtistSet(names)
		}
		if valid {
			if match != nil {
				return nil
			}
			match = c
		}
	}
	return match
}
func spotifyCover(c *spotifyTrack) *string {
	for _, image := range c.Album.Images {
		u, err := url.Parse(image.URL)
		if err == nil && u.Scheme == "https" && u.Host == "i.scdn.co" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && strings.HasPrefix(u.Path, "/image/") && len(u.Path) > len("/image/") {
			v := u.String()
			return &v
		}
	}
	return nil
}

// Called only after a successful Yandex metadata request and cached-data merge.
// Spotify never changes Yandex IDs, availability, lyrics, or existing display data.
func (s *Server) spotifyFallback(ctx context.Context, t Track) Track {
	p := s.spotifyService()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.credentials.Refresh == "" {
		return t
	}
	missingCover := t.Cover == nil || strings.TrimSpace(*t.Cover) == ""
	if !missingCover && safeYandexCover(*t.Cover) {
		missingCover = p.brokenYandexCover(ctx, *t.Cover)
	}
	if !missingCover && strings.TrimSpace(t.Title) != "" && strings.TrimSpace(t.Artist) != "" {
		return t
	}
	if budget, ok := ctx.Value(coverBudgetKey{}).(*coverBudget); ok {
		if budget.searches >= 32 || !time.Now().Before(budget.deadline) {
			return t
		}
		budget.searches++
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, budget.deadline)
		defer cancel()
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if !p.credentials.Expires.After(time.Now().Add(30 * time.Second)) {
		if err := p.token(ctx, s, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {p.credentials.Refresh}}); err != nil {
			p.error = err.Error()
			return t
		}
	}
	q := ""
	if isrc := spotifyISRC(t); isrc != "" {
		q = "isrc:" + isrc
	} else {
		if strings.TrimSpace(t.Title) == "" || strings.TrimSpace(t.Artist) == "" {
			return t
		}
		q = "track:" + t.Title + " artist:" + t.Artist
	}
	var v struct {
		Tracks struct {
			Items []spotifyTrack `json:"items"`
			Next  *string        `json:"next"`
		} `json:"tracks"`
	}
	endpoint := "https://api.spotify.com/v1/search?" + url.Values{"q": {q}, "type": {"track"}, "limit": {"10"}}.Encode()
	if err := p.request(ctx, endpoint, nil, p.credentials.Access, &v); err != nil {
		p.error = err.Error()
		return t
	}
	// A truncated result set cannot establish uniqueness.
	if v.Tracks.Next != nil && *v.Tracks.Next != "" {
		return t
	}
	c := spotifyMatch(t, v.Tracks.Items)
	if c == nil {
		return t
	}
	if strings.TrimSpace(t.Title) == "" {
		t.Title = c.Name
	}
	if strings.TrimSpace(t.Artist) == "" {
		names := []string{}
		for _, a := range c.Artists {
			names = append(names, a.Name)
		}
		t.Artist = strings.Join(names, ", ")
	}
	if missingCover {
		if cover := spotifyCover(c); cover != nil {
			t.Cover = cover
		}
	}
	p.error = ""
	return t
}
