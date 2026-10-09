package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const exclusionListURL = "https://raw.githubusercontent.com/Hazzz895/FckCensorData/refs/heads/main/list.json"
const maxExclusionListBytes = 8 << 20

func downloadExclusions(ctx context.Context, transport http.RoundTripper) (map[int64]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// This client has no cookie jar or provider credentials and never follows redirects.
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, exclusionListURL, nil)
	if err != nil {
		return nil, err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("exclusion list download failed; check connectivity to raw.githubusercontent.com and retry Fetch")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("exclusion list download returned HTTP %d; retry Fetch when raw.githubusercontent.com is available", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxExclusionListBytes+1))
	if err != nil {
		return nil, fmt.Errorf("exclusion list read failed; check connectivity and retry Fetch")
	}
	if len(data) > maxExclusionListBytes {
		return nil, fmt.Errorf("exclusion list exceeds 8 MiB; contact the list maintainer before retrying Fetch")
	}
	var list struct {
		Tracks map[string]json.RawMessage `json:"tracks"`
	}
	if json.Unmarshal(data, &list) != nil || list.Tracks == nil {
		return nil, fmt.Errorf("invalid exclusion list: expected a tracks object; contact the list maintainer and retry Fetch")
	}
	excluded := make(map[int64]bool, len(list.Tracks))
	for key, value := range list.Tracks {
		id, err := strconv.ParseInt(key, 10, 64)
		var url string
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != key || json.Unmarshal(value, &url) != nil || string(value) == "null" {
			return nil, fmt.Errorf("invalid exclusion list: expected positive track IDs mapped to URL strings; contact the list maintainer and retry Fetch")
		}
		excluded[id] = true
	}
	return excluded, nil
}

func filterExcluded(cache *tracksCache) {
	tracks := make([]Result, 0, len(cache.Tracks))
	for _, r := range cache.Tracks {
		if cache.Excluded[r.ID] {
			delete(cache.LyricsSources, r.ID)
			cache.Dirty = true
			continue
		}
		tracks = append(tracks, r)
	}
	cache.Tracks = tracks
}
