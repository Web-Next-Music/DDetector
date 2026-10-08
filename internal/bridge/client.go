package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	if out != nil && len(b) > 0 {
		if err = json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("invalid Supabase JSON")
		}
	}
	return nil
}
func rows[T any](ctx context.Context, c *Client, table, columns, order, token string) ([]T, error) {
	all := []T{}
	for offset := 0; ; offset += 1000 {
		var batch []T
		path := "/rest/v1/" + table + "?select=" + url.QueryEscape(columns) + "&order=" + url.QueryEscape(order)
		err := c.request(ctx, "GET", path, token, nil, map[string]string{"Range-Unit": "items", "Range": fmt.Sprintf("%d-%d", offset, offset+999)}, &batch)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < 1000 {
			return all, nil
		}
		if offset >= 10000000 {
			return nil, fmt.Errorf("pagination limit exceeded")
		}
	}
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
