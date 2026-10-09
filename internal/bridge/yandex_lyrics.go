package bridge

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Public Android protocol key and algorithm from MarshalX/yandex-music-api:
// yandex_music/utils/sign_request.py get_sign_request (not a user credential).
const yandexLyricsSignKey = "p93jhgh689SBReK6ghtw62"

func lyricsSignature(id, timestamp string) string {
	mac := hmac.New(sha256.New, []byte(yandexLyricsSignKey))
	mac.Write([]byte(id + timestamp))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
func trustedLyricsURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "storage.yandex.net" || strings.HasSuffix(host, ".storage.yandex.net") || host == "storage.mds.yandex.net" || strings.HasSuffix(host, ".storage.mds.yandex.net")
}
func (c *yandexClient) downloadLyrics(ctx context.Context, raw string) (string, error) {
	if !trustedLyricsURL(raw) {
		return "", fmt.Errorf("untrusted Yandex lyrics download URL")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		return "", fmt.Errorf("invalid lyrics download request")
	}
	// Never reuse API headers, cookies, or redirect behavior for signed downloads.
	client := &http.Client{Transport: c.http.Transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("lyrics download failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("lyrics download failed (HTTP %d)", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if err != nil {
		return "", fmt.Errorf("lyrics download read failed")
	}
	if len(b) > 2<<20 {
		return "", fmt.Errorf("lyrics download too large")
	}
	if !utf8.Valid(b) {
		return "", fmt.Errorf("invalid lyrics encoding")
	}
	return string(b), nil
}

// Signed format contract: MarshalX/yandex-music-api, _client/tracks.py
// tracks_lyrics: LRC (timed) or TEXT (plain), both returning downloadUrl.
func (c *yandexClient) lyricsFormat(ctx context.Context, id int64, format string) ([]Lyric, error) {
	track := strconv.FormatInt(id, 10)
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	params := url.Values{"format": {format}, "timeStamp": {timestamp}, "sign": {lyricsSignature(track, timestamp)}}
	var v struct {
		DownloadURL string `json:"downloadUrl"`
	}
	if err := c.request(ctx, "GET", "/tracks/"+track+"/lyrics?"+params.Encode(), "", &v); err != nil {
		return nil, err
	}
	if v.DownloadURL == "" {
		return []Lyric{}, nil
	}
	text, err := c.downloadLyrics(ctx, v.DownloadURL)
	if err != nil {
		return nil, err
	}
	if format == "TEXT" {
		return parseLyricsTextChecked(text)
	}
	return parseLRCChecked(text)
}

func parseLyricsText(text string) []Lyric {
	lines, _ := parseLyricsTextChecked(text)
	return lines
}
func parseLyricsTextChecked(text string) ([]Lyric, error) {
	if len(text) > maxLyricsBytes || strings.Count(text, "\n") >= maxLyricsLines {
		return nil, fmt.Errorf("lyrics input too large")
	}
	lines := []Lyric{}
	text = strings.TrimPrefix(strings.ReplaceAll(text, "\r\n", "\n"), "\ufeff")
	for _, raw := range strings.Split(text, "\n") {
		if line := strings.TrimSpace(raw); line != "" {
			if len(line) > maxLyricLineBytes || len(lines) >= maxLyricsLines {
				return nil, fmt.Errorf("lyrics line budget exceeded")
			}
			lines = append(lines, Lyric{Text: line})
		}
	}
	return lines, validateLyrics(lines)
}

func (c *yandexClient) lyrics(ctx context.Context, id int64) ([]Lyric, error) {
	var modernErr error
	var untimed []Lyric
	for _, format := range []string{"LRC", "TEXT"} {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		lines, err := c.lyricsFormat(ctx, id, format)
		if err != nil {
			if modernErr == nil && !lyricsUnavailable(err) {
				modernErr = err
			}
			continue
		}
		if format == "TEXT" && len(lines) > 0 {
			return lines, nil
		}
		for _, line := range lines {
			if line.TS != nil && line.Text != "" {
				return lines, nil
			}
		}
		if format == "LRC" {
			untimed = lines
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if len(untimed) > 0 {
		return untimed, nil
	}
	fallback, err := c.supplement(ctx, id)
	if err == nil && len(fallback) > 0 {
		return fallback, nil
	}
	if modernErr != nil && !lyricsUnavailable(modernErr) {
		return nil, modernErr
	}
	if err != nil && !lyricsUnavailable(err) {
		return nil, err
	}
	return []Lyric{}, nil
}

var lrcStamp = regexp.MustCompile(`^\[(\d+):([0-5]\d)(?:[.:](\d{1,3}))?\]`)
var lrcMetadata = regexp.MustCompile(`^\[(?:ar|al|ti|au|by|re|ve|length|offset):[^\]]*\]$`)
var lrcOffset = regexp.MustCompile(`(?m)^\[offset:([+-]?\d+)\]\s*$`)

// Expand repeated timestamps, apply LRC's millisecond offset, and normalize to
// mm:ss.mmm (the bridge/Qt seek contract). Metadata is not lyric content.
func parseLRC(text string) []Lyric {
	lines, _ := parseLRCChecked(text)
	return lines
}
func parseLRCChecked(text string) ([]Lyric, error) {
	if len(text) > maxLyricsBytes || strings.Count(text, "\n") >= maxLyricsLines {
		return nil, fmt.Errorf("LRC input too large")
	}
	text = strings.TrimPrefix(strings.ReplaceAll(text, "\r\n", "\n"), "\ufeff")
	offset := int64(0)
	if m := lrcOffset.FindStringSubmatch(text); m != nil {
		offset, _ = strconv.ParseInt(m[1], 10, 64)
		if offset > 86400000 || offset < -86400000 {
			offset = 0
		}
	}
	type entry struct {
		line Lyric
		ms   int64
	}
	entries := []entry{}
	expandedBytes := 0
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		times := []int64{}
		for {
			m := lrcStamp.FindStringSubmatch(line)
			if m == nil {
				break
			}
			minute, e := strconv.ParseInt(m[1], 10, 64)
			if e != nil || minute > 100000 {
				break
			}
			second, _ := strconv.ParseInt(m[2], 10, 64)
			fraction := m[3]
			for len(fraction) < 3 {
				fraction += "0"
			}
			millis, _ := strconv.ParseInt(fraction, 10, 64)
			ms := minute*60000 + second*1000 + millis + offset
			if ms < 0 {
				ms = 0
			}
			if len(times) >= maxLRCStampsPerLine {
				return nil, fmt.Errorf("LRC timestamp budget exceeded")
			}
			times = append(times, ms)
			line = line[len(m[0]):]
		}
		line = strings.TrimSpace(line)
		count := len(times)
		if count == 0 && line != "" && !lrcMetadata.MatchString(line) {
			count = 1
		}
		if count > 0 {
			if len(line) > maxLyricLineBytes || len(entries)+count > maxLyricsLines {
				return nil, fmt.Errorf("LRC line budget exceeded")
			}
			// Include normalized timestamp bytes in the expanded budget.
			expandedBytes += count * (len(line) + 16)
			if expandedBytes > maxLyricsBytes {
				return nil, fmt.Errorf("expanded LRC budget exceeded")
			}
		}
		if len(times) == 0 {
			if line != "" && !lrcMetadata.MatchString(line) {
				entries = append(entries, entry{line: Lyric{Text: line}, ms: -1})
			}
			continue
		}
		for _, ms := range times {
			stamp := fmt.Sprintf("%02d:%02d.%03d", ms/60000, (ms/1000)%60, ms%1000)
			entries = append(entries, entry{line: Lyric{TS: &stamp, Text: strings.TrimSpace(line)}, ms: ms})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].ms < entries[j].ms })
	out := []Lyric{}
	for _, e := range entries {
		out = append(out, e.line)
	}
	return out, nil
}
