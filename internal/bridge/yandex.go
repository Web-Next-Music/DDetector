package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Yandex credentials are deliberately memory-only and never included in State.
type yandexClient struct {
	base, token, uid string
	http             *http.Client
	lrclib           *lrclibClient
}

func newYandexClient(token string) *yandexClient {
	return &yandexClient{base: "https://api.music.yandex.net", token: token, lrclib: newLRCLIBClient(), http: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func (c *yandexClient) request(ctx context.Context, method, path, body string, out any) error {
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.base+path, strings.NewReader(body))
		if err != nil {
			return fmt.Errorf("invalid Yandex request")
		}
		req.Header.Set("Authorization", "OAuth "+c.token)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("Yandex network request failed")
		}
		b, readErr := io.ReadAll(io.LimitReader(res.Body, (16<<20)+1))
		res.Body.Close()
		if readErr != nil {
			return fmt.Errorf("Yandex response read failed")
		}
		if len(b) > 16<<20 {
			return fmt.Errorf("Yandex response too large")
		}
		if res.StatusCode == 429 || res.StatusCode >= 500 {
			if attempt == 2 {
				return yandexHTTPError(res.StatusCode)
			}
			delay := time.Duration(attempt+1) * time.Second
			if n, e := strconv.Atoi(res.Header.Get("Retry-After")); e == nil && n > 0 {
				if n > 10 {
					n = 10
				}
				delay = time.Duration(n) * time.Second
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			continue
		}
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			return yandexHTTPError(res.StatusCode)
		}
		var envelope struct {
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(b, &envelope) != nil || len(envelope.Result) == 0 || string(envelope.Result) == "null" || (len(envelope.Error) > 0 && string(envelope.Error) != "null") {
			return fmt.Errorf("invalid Yandex response")
		}
		if json.Unmarshal(envelope.Result, out) != nil {
			return fmt.Errorf("invalid Yandex result")
		}
		return nil
	}
	return fmt.Errorf("Yandex request failed")
}
func (c *yandexClient) identify(ctx context.Context) error {
	var v struct {
		Account struct {
			UID json.Number `json:"uid"`
		} `json:"account"`
	}
	if err := c.request(ctx, "GET", "/account/status", "", &v); err != nil {
		return err
	}
	id, err := strconv.ParseInt(string(v.Account.UID), 10, 64)
	if err != nil || id <= 0 {
		return fmt.Errorf("invalid Yandex account")
	}
	c.uid = strconv.FormatInt(id, 10)
	return nil
}

type yandexID string

func (id *yandexID) UnmarshalJSON(b []byte) error {
	var s string
	if len(b) > 0 && b[0] == '"' {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
	} else {
		s = string(b)
	}
	n, e := strconv.ParseInt(s, 10, 64)
	if e != nil || n <= 0 {
		return fmt.Errorf("invalid Yandex track ID")
	}
	*id = yandexID(s)
	return nil
}

func (c *yandexClient) metadata(ctx context.Context, ids []string) (map[int64]Track, error) {
	out := map[int64]Track{}
	for start := 0; start < len(ids); start += 100 {
		end := start + 100
		if end > len(ids) {
			end = len(ids)
		}
		var batch []struct {
			ID      yandexID `json:"id"`
			Title   string   `json:"title"`
			ISRC    string   `json:"isrc"`
			Cover   string   `json:"coverUri"`
			OGImage string   `json:"ogImage"`
			Albums  []struct {
				Cover   string `json:"coverUri"`
				OGImage string `json:"ogImage"`
			} `json:"albums"`
			Available *bool           `json:"available"`
			Error     json.RawMessage `json:"error"`
			Artists   []TrackArtist   `json:"artists"`
		}
		if err := c.request(ctx, "POST", "/tracks", url.Values{"track-ids": {strings.Join(ids[start:end], ",")}}.Encode(), &batch); err != nil {
			return nil, err
		}
		for _, v := range batch {
			id, _ := strconv.ParseInt(string(v.ID), 10, 64)
			names := []string{}
			for _, a := range v.Artists {
				names = append(names, a.Name)
			}
			t := Track{Artists: v.Artists, ID: id, Title: v.Title, Artist: strings.Join(names, ", "), ISRC: normalizeISRC(v.ISRC), Available: v.Available}
			// These per-track markers explicitly report missing catalog rights or deletion.
			// Request failures and unrecognized errors are not availability signals.
			var trackError string
			if t.Available == nil && json.Unmarshal(v.Error, &trackError) == nil && (trackError == "not-found" || trackError == "no-rights") {
				available := false
				t.Available = &available
			}
			covers := []string{v.Cover, v.OGImage}
			for _, album := range v.Albums {
				covers = append(covers, album.Cover, album.OGImage)
			}
			t.Cover = yandexCoverURL(covers...)
			out[id] = t
		}
	}
	return out, nil
}

var isrcPattern = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{3}[0-9]{7}$`)

func normalizeISRC(raw string) string {
	isrc := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(raw), "-", ""))
	if !isrcPattern.MatchString(isrc) {
		return ""
	}
	return isrc
}

// Only use artwork attached to this track or its albums, never a fuzzy search match.
func yandexCoverURL(candidates ...string) *string {
	for _, raw := range candidates {
		cover := strings.TrimSpace(raw)
		if cover == "" {
			continue
		}
		if strings.HasPrefix(cover, "//") {
			cover = "https:" + cover
		} else if !strings.Contains(cover, "://") {
			cover = "https://" + cover
		}
		cover = strings.ReplaceAll(cover, "%%", "200x200")
		u, err := url.Parse(cover)
		if err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" {
			return &cover
		}
	}
	return nil
}

func (c *yandexClient) supplement(ctx context.Context, id int64) ([]Lyric, error) {
	var v struct {
		Lyrics *struct {
			Full string `json:"fullLyrics"`
		} `json:"lyrics"`
	}
	if err := c.request(ctx, "GET", "/tracks/"+strconv.FormatInt(id, 10)+"/supplement", "", &v); err != nil {
		return nil, err
	}
	if v.Lyrics == nil || strings.TrimSpace(v.Lyrics.Full) == "" {
		return []Lyric{}, nil
	}
	if len(v.Lyrics.Full) > maxLyricsBytes {
		return nil, fmt.Errorf("lyrics input too large")
	}
	out := []Lyric{}
	for _, line := range strings.Split(strings.ReplaceAll(v.Lyrics.Full, "\r\n", "\n"), "\n") {
		if len(line) > maxLyricLineBytes || len(out) >= maxLyricsLines {
			return nil, fmt.Errorf("lyrics line budget exceeded")
		}
		out = append(out, Lyric{Text: line})
	}
	return out, validateLyrics(out)
}

// HTTP status errors never include response bodies or signed URLs.
type yandexHTTPError int

func (e yandexHTTPError) Error() string {
	return fmt.Sprintf("Yandex request failed (HTTP %d)", int(e))
}
func (s *Server) setYandexToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token *string `json:"access_token"`
	}
	if decode(w, r, &in) != nil || in.Token == nil || len(*in.Token) > 4096 || strings.ContainsAny(*in.Token, " \t\r\n") {
		failure(w, 400, "expected access_token without whitespace")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	s.mu.Lock()
	done := s.jobDone
	if s.jobCancel != nil {
		s.jobCancel()
	}
	s.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			failure(w, 504, "load cancellation timed out")
			return
		}
	}
	if !s.tryOperation() {
		failure(w, 409, "operation in progress")
		return
	}
	defer s.op.Unlock()
	work, finish := s.operationContext(ctx, 30*time.Second)
	defer finish()
	ctx = work
	// Discard the previous credential even when replacement validation fails.
	s.mu.Lock()
	s.yandex = nil
	s.state.YandexConnected = false
	s.state.Tracks = []Result{}
	s.state.Processed = 0
	s.state.FoundCount = 0
	s.state.LastFetch = nil
	s.state.Error = ""
	s.state.Loading = false
	s.state.Fetching = false
	s.state.Processing = false
	s.mu.Unlock()
	if *in.Token == "" {
		send(w, 200, map[string]any{"ok": true, "yandexConnected": false})
		return
	}
	if _, err := s.authorize(ctx); err != nil {
		failure(w, 403, err.Error())
		return
	}
	factory := s.yandexFactory
	if factory == nil {
		factory = newYandexClient
	}
	c := factory(*in.Token)
	if err := c.identify(ctx); err != nil {
		failure(w, 502, err.Error())
		return
	}
	s.mu.Lock()
	s.yandex = c
	s.state.YandexConnected = true
	uid := s.session.User.ID
	s.mu.Unlock()
	if err := s.restoreTracksCache(ctx, c, uid); err != nil {
		s.mu.Lock()
		s.state.Error = "Unable to restore local track cache: " + err.Error()
		s.mu.Unlock()
	}
	send(w, 200, map[string]any{"ok": true, "yandexConnected": true, "yandex_user_id": c.uid})
}
func lyricsUnavailable(err error) bool {
	var status yandexHTTPError
	return errors.As(err, &status) && (status == 404 || status == 403)
}
