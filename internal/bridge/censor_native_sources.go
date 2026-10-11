package bridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"os"
	"strconv"
	"strings"
)

// nativeSource supplies inputs only. Publication and runtime wiring are separate.
// Source reads deliberately use anon credentials, never the bridge user's JWT:
// the request project may not be the project that issued that JWT.
type nativeSource struct {
	server      *Server
	source      *Client
	configErr   error
	archiveHTTP *http.Client

	webHTTP *http.Client
}

func NativeSources(server *Server) *nativeSource {
	n := &nativeSource{server: server}
	base, key := os.Getenv("FCKCENSOR_SUPABASE_URL"), os.Getenv("FCKCENSOR_SUPABASE_ANON_KEY")
	if base == "" && key == "" {
		base, key = nativeReportProjectURL, nativeReportAnonKey
	}
	if !nativeAnonKey(key) {
		n.configErr = errors.New("reported_tracks requires a public anon or publishable project key; privileged keys are not accepted")
		return n
	}
	n.source, n.configErr = NewClient(base, key)
	if n.configErr == nil && (n.source == nil || !nativeAnonKey(n.source.Key)) {
		n.configErr = errors.New("reported_tracks requires a public anon or publishable project key; privileged keys are not accepted")
	}
	return n
}

func nativeAnonKey(key string) bool {
	if strings.HasPrefix(key, "sb_publishable_") {
		return len(key) > len("sb_publishable_") && !strings.ContainsAny(key, " \r\n\t")
	}
	parts := strings.Split(key, ".")
	if len(parts) != 3 {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Role string `json:"role"`
	}
	return json.Unmarshal(b, &claims) == nil && claims.Role == "anon"
}

func (n *nativeSource) yandex() (*yandexClient, error) {
	if n.server == nil {
		return nil, errors.New("connect a Yandex account to obtain authorized full audio")
	}
	n.server.mu.Lock()
	c := n.server.yandex
	n.server.mu.Unlock()
	if c == nil || c.token == "" {
		return nil, errors.New("connect a Yandex account to obtain authorized full audio")
	}
	return c, nil
}

func nativeOpaqueID(kind string, id int64) string {
	return kind + ":yandex:" + strconv.FormatInt(id, 10)
}
func nativeTrackID(kind, opaque string) (int64, error) {
	prefix := kind + ":yandex:"
	if !strings.HasPrefix(opaque, prefix) {
		return 0, ErrCensorNotFound
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(opaque, prefix), 10, 64)
	if err != nil || id <= 0 || nativeOpaqueID(kind, id) != opaque {
		return 0, ErrCensorNotFound
	}
	return id, nil
}

// LazyOriginalAudio tells coordinating code that Requests returns queue identities,
// not playable audio. Call Request immediately before analysis; never send an empty
// OriginalURL to the existing processor or treat a provider page as original audio.
func (n *nativeSource) LazyOriginalAudio() bool { return true }

func (n *nativeSource) reported(ctx context.Context) ([]int64, error) {
	if n.configErr != nil {
		return nil, n.configErr
	}
	if n.source == nil {
		return nil, errors.New("reported_tracks project configuration is unavailable")
	}
	if !nativeAnonKey(n.source.Key) {
		return nil, errors.New("reported_tracks requires public anon credentials; privileged keys are not accepted")
	}
	ids := []int64{}
	seen := map[int64]bool{}
	var rows []struct {
		TrackID json.RawMessage `json:"track_id"`
		Type    *string         `json:"type"`
	}
	// The original upstream reporting client writes tracks without a type.
	// Accept those legacy null rows, but never album/artist report identifiers.
	path := "/rest/v1/reported_tracks?select=track_id,type&or=(type.eq.track,type.is.null)&order=track_id.asc&limit=1000"
	token := ""
	if !strings.HasPrefix(n.source.Key, "sb_publishable_") {
		token = n.source.Key
	}
	if err := n.source.request(ctx, http.MethodGet, path, token, nil, nil, &rows); err != nil {
		return nil, fmt.Errorf("cannot read reported_tracks with public anon permission (verify source project, exposed table and SELECT RLS): %w", err)
	}
	if len(rows) > 1000 {
		return nil, errors.New("reported_tracks exceeded the 1000-row fetch bound")
	}
	for _, row := range rows {
		if row.Type != nil && *row.Type != "track" {
			continue
		}
		var trackID yandexID
		if json.Unmarshal(row.TrackID, &trackID) != nil {
			return nil, errors.New("reported_tracks contains an invalid track_id")
		}
		id, err := strconv.ParseInt(string(trackID), 10, 64)
		if err != nil || id <= 0 {
			return nil, errors.New("reported_tracks contains an invalid track_id")
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (n *nativeSource) Requests(ctx context.Context, _ CensorIdentity) ([]CensorRequest, error) {
	ids, err := n.reported(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]CensorRequest, 0, len(ids))
	for _, id := range ids {
		out = append(out, CensorRequest{ID: nativeOpaqueID("request", id), TrackID: id})
	}
	return out, nil
}
func (n *nativeSource) requestAudio(ctx context.Context, c *yandexClient, id int64) (CensorRequest, error) {
	t, err := c.nativeTrack(ctx, id)
	if err != nil {
		return CensorRequest{}, err
	}
	audio, err := c.nativeFullMP3(ctx, id)
	if err != nil {
		return CensorRequest{}, err
	}
	return CensorRequest{ID: nativeOpaqueID("request", id), TrackID: id, Title: t.Title, Artist: t.artist(), OriginalURL: audio}, nil
}

// Request rechecks source membership and refreshes expiring audio URLs.
func (n *nativeSource) Request(ctx context.Context, _ CensorIdentity, opaque string) (CensorRequest, error) {
	id, err := nativeTrackID("request", opaque)
	if err != nil {
		return CensorRequest{}, err
	}
	ids, err := n.reported(ctx)
	if err != nil {
		return CensorRequest{}, err
	}
	for _, found := range ids {
		if found == id {
			c, err := n.yandex()
			if err != nil {
				return CensorRequest{}, err
			}
			return n.requestAudio(ctx, c, id)
		}
	}
	return CensorRequest{}, ErrCensorNotFound
}

func (n *nativeSource) Search(ctx context.Context, _ CensorIdentity, query string) ([]CensorCandidate, error) {
	return n.siteSearch(ctx, query)
}

func nativeCandidate(t nativeYandexTrack, id int64) CensorCandidate {
	return CensorCandidate{ID: nativeOpaqueID("candidate", id), Title: t.Title, Artist: t.artist(), URL: "https://music.yandex.ru/track/" + strconv.FormatInt(id, 10), Provider: "yandex", DurationSeconds: float64(t.DurationMS) / 1000, Metadata: map[string]string{"track_id": strconv.FormatInt(id, 10), "explicit": strconv.FormatBool(t.Explicit), "kind": "page", "content_type": "text/html"}}
}

// Candidate revalidates provider metadata and resolves a fresh non-DRM stream.
func (n *nativeSource) Candidate(ctx context.Context, _ CensorIdentity, opaque string) (CensorCandidate, error) {
	return n.resolveCandidate(ctx, opaque, true)
}

// PlaybackCandidate refreshes full audio without treating listening as approval.
func (n *nativeSource) PlaybackCandidate(ctx context.Context, _ CensorIdentity, opaque string) (CensorCandidate, error) {
	return n.resolveCandidate(ctx, opaque, false)
}

func (n *nativeSource) resolveCandidate(ctx context.Context, opaque string, requireExplicit bool) (CensorCandidate, error) {
	if strings.HasPrefix(opaque, webCandidatePrefix) {
		return n.webResolve(ctx, opaque)
	}
	if strings.HasPrefix(opaque, archiveCandidatePrefix) {
		return n.archiveResolve(ctx, opaque)
	}
	id, err := nativeTrackID("candidate", opaque)
	if err != nil {
		return CensorCandidate{}, err
	}
	c, err := n.yandex()
	if err != nil {
		return CensorCandidate{}, err
	}
	t, err := c.nativeTrack(ctx, id)
	if err != nil {
		return CensorCandidate{}, err
	}
	if requireExplicit && !t.Explicit {
		return CensorCandidate{}, ErrCensorConflict
	}
	candidate := nativeCandidate(t, id)
	audio, err := c.nativeFullMP3(ctx, id)
	if err != nil {
		candidate.Error = err.Error()
		return candidate, err
	}
	candidate.URL = audio
	candidate.FullAudio = true
	candidate.Downloadable = true
	candidate.Metadata["kind"] = "audio"
	candidate.Metadata["content_type"] = "audio/mpeg"
	return candidate, nil
}

// Public anon configuration from https://github.com/Hazzz895/FckCensor/blob/main/script.js.
// Verified anon role/project and read-only reported_tracks GET; no service key.
const nativeReportProjectURL = "https://pzomqvgckpgkshxhpite.supabase.co"
const nativeReportAnonKey = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJzdXBhYmFzZSIsInJlZiI6InB6b21xdmdja3Bna3NoeGhwaXRlIiwicm9sZSI6ImFub24iLCJpYXQiOjE3NzUwNTgzNDEsImV4cCI6MjA5MDYzNDM0MX0.ggCxM-ver3gDWUBWyhSBfy3n7rpdW8jtlxRQVCXkhNg"
