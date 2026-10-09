package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	URL, Key string
	HTTP     *http.Client
}

func NewClient(base, key string) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("invalid Supabase URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")) {
		return nil, fmt.Errorf("Supabase URL must use HTTPS")
	}
	if key == "" {
		return nil, fmt.Errorf("Supabase anon key is required")
	}
	return &Client{strings.TrimRight(base, "/"), key, &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) request(ctx context.Context, method, path, token string, body any, headers map[string]string, out any) error {
	return c.requestResponse(ctx, method, path, token, body, headers, out, nil)
}

func (c *Client) requestResponse(ctx context.Context, method, path, token string, body any, headers map[string]string, out any, responseHeaders http.Header) error {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("apikey", c.Key)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("Supabase network request failed")
	}
	defer res.Body.Close()
	if responseHeaders != nil {
		for k, v := range res.Header {
			responseHeaders[k] = append([]string(nil), v...)
		}
		if res.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			total, err := strconv.Atoi(strings.TrimPrefix(res.Header.Get("Content-Range"), "*/"))
			start, _, ok := strings.Cut(headers["Range"], "-")
			offset, parseErr := strconv.Atoi(start)
			if strings.HasPrefix(res.Header.Get("Content-Range"), "*/") && err == nil && total >= 0 && ok && parseErr == nil && offset >= total {
				return nil
			}
		}
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 32<<20+1))
	if err != nil {
		return fmt.Errorf("Supabase response read failed")
	}
	if len(b) > 32<<20 {
		return fmt.Errorf("Supabase response too large")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("Supabase request failed (HTTP %d)", res.StatusCode)
	}
	if out != nil {
		if len(b) == 0 {
			return fmt.Errorf("missing Supabase JSON")
		}
		if err = json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("invalid Supabase JSON")
		}
	}
	return nil
}
func rows[T any](ctx context.Context, c *Client, table, columns, order, token string) ([]T, error) {
	return filteredRows[T](ctx, c, table, columns, order, token, "")
}
func filteredRows[T any](ctx context.Context, c *Client, table, columns, order, token, filter string) ([]T, error) {
	all := []T{}
	seen := map[string]bool{}
	for offset := 0; ; {
		var batch []T
		path := "/rest/v1/" + table + "?select=" + url.QueryEscape(columns) + "&order=" + url.QueryEscape(order) + filter
		responseHeaders := http.Header{}
		err := c.requestResponse(ctx, "GET", path, token, nil, map[string]string{"Range-Unit": "items", "Range": fmt.Sprintf("%d-%d", offset, offset+999)}, &batch, responseHeaders)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			value := responseHeaders.Get("Content-Range")
			if value != "" {
				totalPart, ok := strings.CutPrefix(value, "*/")
				if !ok {
					return nil, fmt.Errorf("invalid empty PostgREST page range")
				}
				if totalPart != "*" {
					total, err := strconv.Atoi(totalPart)
					if err != nil || total < 0 || offset < total {
						return nil, fmt.Errorf("incomplete PostgREST pagination")
					}
				}
			}
			return all, nil
		}
		next, terminal, err := pageRange(responseHeaders.Get("Content-Range"), offset, len(batch))
		if err != nil {
			return nil, err
		}
		added := 0
		for _, row := range batch {
			b, err := json.Marshal(row)
			if err != nil {
				return nil, err
			}
			var identity map[string]json.RawMessage
			_ = json.Unmarshal(b, &identity)
			key := string(b)
			// These projections expose the track identity, not a mutable sort key.
			if id, ok := identity["id"]; ok {
				key = "id:" + string(id)
			} else if id, ok := identity["track_id"]; ok {
				key = "track_id:" + string(id)
			}
			if !seen[key] {
				seen[key] = true
				all = append(all, row)
				added++
			}
		}
		if added == 0 {
			return nil, fmt.Errorf("pagination made no progress")
		}
		if terminal {
			return all, nil
		}
		// Server caps can be smaller than 1000; never infer EOF from page size.
		offset = next
		if offset >= 10000000 {
			return nil, fmt.Errorf("pagination limit exceeded")
		}
	}
}

func pageRange(value string, offset, count int) (int, bool, error) {
	if value == "" {
		return offset + count, false, nil
	}
	rangePart, totalPart, ok := strings.Cut(value, "/")
	startPart, endPart, rangeOK := strings.Cut(rangePart, "-")
	start, e1 := strconv.Atoi(startPart)
	end, e2 := strconv.Atoi(endPart)
	if !ok || !rangeOK || e1 != nil || e2 != nil || start != offset || end < start || end-start+1 != count || count > 1000 {
		return 0, false, fmt.Errorf("invalid PostgREST page range")
	}
	if totalPart == "*" {
		return end + 1, false, nil
	}
	total, err := strconv.Atoi(totalPart)
	if err != nil || total <= end {
		return 0, false, fmt.Errorf("invalid PostgREST page total")
	}
	return end + 1, end+1 == total, nil
}

func (c *Client) permitted(ctx context.Context, token, uid string) (bool, bool, error) {
	var bans, access []struct {
		UserID string `json:"user_id"`
	}
	q := "?select=user_id&user_id=eq." + url.QueryEscape(uid) + "&limit=1"
	if err := c.request(ctx, "GET", "/rest/v1/bans"+q, token, nil, nil, &bans); err != nil {
		return false, false, err
	}
	if len(bans) > 0 {
		return false, true, nil
	}
	err := c.request(ctx, "GET", "/rest/v1/ddetector_users"+q, token, nil, nil, &access)
	return len(access) > 0, false, err
}
