package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const lrclibGetURL = "https://lrclib.net/api/get"
const lrclibUserAgent = "DDetector-Go/1 (https://github.com/Web-Next-Music/DDetector)"

// The endpoint is fixed; tests inject a transport rather than relaxing HTTPS.
// Each client serializes requests and waits 250 ms after completion, as requested
// by https://lrclib.net/docs. The gate also protects the Retry-After deadline.
type lrclibClient struct {
	transport http.RoundTripper
	gate      chan struct{}
	next      time.Time
}

func newLRCLIBClient() *lrclibClient {
	return &lrclibClient{gate: make(chan struct{}, 1)}
}

func hasTimedLyrics(lines []Lyric) bool {
	for _, line := range lines {
		if line.TS != nil && line.Text != "" {
			return true
		}
	}
	return false
}

// get returns lyrics, whether an instrumental record was found, and an error.
// 404/empty records are absence; other failures must not become cached absence.
func (c *lrclibClient) get(ctx context.Context, track Track) ([]Lyric, bool, error) {
	title, artist := strings.TrimSpace(track.Title), strings.TrimSpace(track.Artist)
	if title == "" || artist == "" {
		return []Lyric{}, false, nil
	}
	select {
	case c.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
	defer func() { <-c.gate }()
	if delay := time.Until(c.next); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	retryAt := time.Time{}
	defer func() {
		c.next = time.Now().Add(250 * time.Millisecond)
		if retryAt.After(c.next) {
			c.next = retryAt
		}
	}()
	params := url.Values{"track_name": {title}, "artist_name": {artist}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, lrclibGetURL+"?"+params.Encode(), nil)
	if err != nil {
		return nil, false, fmt.Errorf("invalid LRCLIB request")
	}
	req.Header.Set("User-Agent", lrclibUserAgent)
	req.Header.Set("Accept", "application/json")
	// Do not reuse the authenticated Yandex client, its cookie jar, or redirects.
	client := &http.Client{Transport: c.transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		return nil, false, fmt.Errorf("LRCLIB network request failed")
	}
	defer res.Body.Close()
	if res.StatusCode == 429 || res.StatusCode == 503 {
		value := res.Header.Get("Retry-After")
		if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds >= 0 && seconds <= int64((1<<63-1)/time.Second) {
			retryAt = time.Now().Add(time.Duration(seconds) * time.Second)
		} else if date, err := http.ParseTime(value); err == nil {
			retryAt = date
		} else {
			retryAt = time.Now().Add(time.Minute)
		}
	}
	if res.StatusCode == http.StatusNotFound {
		return []Lyric{}, false, nil
	}
	if res.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("LRCLIB request failed (HTTP %d)", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		return nil, false, fmt.Errorf("LRCLIB response read failed")
	}
	if len(body) > 2<<20 {
		return nil, false, fmt.Errorf("LRCLIB response too large")
	}
	var record struct {
		ID           int64  `json:"id"`
		TrackName    string `json:"trackName"`
		ArtistName   string `json:"artistName"`
		Instrumental bool   `json:"instrumental"`
		SyncedLyrics string `json:"syncedLyrics"`
		PlainLyrics  string `json:"plainLyrics"`
	}
	if !utf8.Valid(body) || json.Unmarshal(body, &record) != nil || record.ID <= 0 {
		return nil, false, fmt.Errorf("invalid LRCLIB response")
	}
	normalize := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
	if record.Instrumental || strings.TrimSpace(record.SyncedLyrics) != "" || strings.TrimSpace(record.PlainLyrics) != "" || record.TrackName != "" || record.ArtistName != "" {
		if normalize(record.TrackName) != normalize(title) || normalize(record.ArtistName) != normalize(artist) {
			return nil, false, fmt.Errorf("LRCLIB metadata mismatch")
		}
	}
	if record.Instrumental {
		return []Lyric{}, true, nil
	}
	synced, err := parseLRCChecked(record.SyncedLyrics)
	if err != nil {
		return nil, false, err
	}
	if hasTimedLyrics(synced) {
		return synced, false, nil
	}
	plain, err := parseLyricsTextChecked(record.PlainLyrics)
	return plain, false, err
}

func (c *yandexClient) lyricsForTrack(ctx context.Context, track Track) ([]Lyric, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var primaryErr error
	if c.lrclib != nil {
		lines, instrumental, err := c.lrclib.get(ctx, track)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err == nil && (instrumental || len(lines) > 0) {
			return lines, nil
		}
		primaryErr = err
	}
	lines, err := c.lyrics(ctx, track.ID)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err == nil && len(lines) > 0 {
		return lines, nil
	}
	if primaryErr != nil {
		return nil, primaryErr
	}
	return lines, err
}
