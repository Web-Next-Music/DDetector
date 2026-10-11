package bridge

import (
	"context"
	"ddetector-go/internal/censorsearch"
	"fmt"
	"math"

	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const censorMetadataLimit = 10
const censorMetadataQueryLimit = 6
const censorMetadataReview = "Manual review only: Yandex track metadata is not audio; comparison and approval are disabled"
const censorMetadataMissing = "Yandex metadata unavailable: connect a Yandex account. Audio search remains available"

var censorMBID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type censorYandexMetadata struct{ server *Server }

func (p *censorYandexMetadata) client() *yandexClient {
	p.server.mu.Lock()
	defer p.server.mu.Unlock()
	c := p.server.yandex
	if c == nil || c.token == "" {
		return nil
	}
	return c
}

func (p *censorYandexMetadata) Search(ctx context.Context, q censorsearch.Query) (censorsearch.Page, error) {
	c := p.client()
	if c == nil {
		return censorsearch.Page{}, ErrCensorUnavailable
	}
	var result struct {
		Tracks struct {
			Results []struct {
				nativeYandexTrack
				ISRC   string `json:"isrc"`
				Albums []struct {
					ID    yandexID `json:"id"`
					Title string   `json:"title"`
				} `json:"albums"`
			} `json:"results"`
		} `json:"tracks"`
	}
	text := strings.TrimSpace(q.Artist + " " + q.Title)
	if !validCensorQuery(text) {
		return censorsearch.Page{}, ErrCensorUnavailable
	}
	err := c.request(ctx, "GET", "/search?"+url.Values{"text": {text}, "type": {"track"}, "page": {"0"}, "nocorrect": {"true"}}.Encode(), "", &result)
	if err != nil {
		return censorsearch.Page{}, err
	}
	page := censorsearch.Page{Provider: "yandex"}
	limit := q.Limit
	if limit <= 0 || limit > censorMetadataLimit {
		limit = censorMetadataLimit
	}
	for _, t := range result.Tracks.Results {
		id, err := strconv.ParseInt(string(t.ID), 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		item := censorsearch.Candidate{ID: strconv.FormatInt(id, 10), Title: t.Title, Artist: t.artist(), DurationMS: t.DurationMS}
		if t.ISRC != "" {
			item.ISRCs = []string{t.ISRC}
		}
		for _, artist := range t.Artists {
			item.ArtistCredits = append(item.ArtistCredits, censorsearch.ArtistCredit{Name: artist.Name})
		}
		for _, album := range t.Albums {
			item.Releases = append(item.Releases, censorsearch.Release{ID: string(album.ID), Title: album.Title})
		}
		page.Candidates = append(page.Candidates, item)
		if len(page.Candidates) >= limit {
			break
		}
	}
	return page, nil
}

func (rt *censorRuntime) metadataStatus() (bool, string) {
	if p, ok := rt.metadataProvider.(*censorYandexMetadata); ok {
		if p.client() == nil {
			return false, censorMetadataMissing
		}
		return true, ""
	}
	return rt.metadataProvider != nil, rt.metadataConfigWarning
}

func censorYandexMetadataID(id string) bool {
	n, err := strconv.ParseInt(id, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == id
}

func censorMetadataQuery(j CensorJob) censorsearch.Query {
	q := censorsearch.Query{Artist: j.Request.Artist, Title: j.Request.Title, Limit: censorMetadataLimit}
	if j.Result != nil {
		seconds := j.Result.DurationSeconds
		if seconds > 0 && seconds <= 86400 && !math.IsNaN(seconds) && !math.IsInf(seconds, 0) {
			q.Duration = time.Duration(seconds * float64(time.Second))
		}
	}
	return q
}

func (rt *censorRuntime) searchMetadata(ctx context.Context, j *CensorJob) ([]CensorCandidate, []string) {
	configured, warning := rt.metadataStatus()
	j.MetadataWarning = warning
	if !configured {
		return nil, nil
	}
	j.MetadataWarning = ""
	query := censorMetadataQuery(*j)
	if strings.TrimSpace(query.Artist+query.Title) == "" {
		j.MetadataWarning = "Yandex metadata unavailable: request has no artist/title. Audio search continues"
		return nil, nil
	}
	metadataCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	page, err := rt.metadataProvider.Search(metadataCtx, query)
	if err != nil {
		j.MetadataWarning = "Yandex metadata unavailable; audio search continues"
		return nil, nil
	}
	candidates := []CensorCandidate{}
	variants := []string{}
	seenIDs, seenQueries := map[string]bool{}, map[string]bool{}
	addQuery := func(value string) {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if len(variants) < censorMetadataQueryLimit && validCensorQuery(value) && !seenQueries[key] {
			variants = append(variants, value)
			seenQueries[key] = true
		}
	}
	for _, recording := range page.Candidates {
		if len(candidates) >= censorMetadataLimit {
			break
		}
		if !censorYandexMetadataID(recording.ID) {
			continue
		}
		id := "yandex-metadata:" + recording.ID
		if seenIDs[id] {
			continue
		}
		candidate := CensorCandidate{ID: id, Title: recording.Title, Artist: recording.Artist, URL: "https://music.yandex.ru/track/" + recording.ID, Provider: "yandex", DurationSeconds: float64(recording.DurationMS) / 1000, Metadata: map[string]string{"kind": "page", "recording_id": recording.ID}, Error: censorMetadataReview}
		put := func(key, value string) {
			if value != "" && len(value) <= 2048 {
				candidate.Metadata[key] = value
			}
		}
		put("isrcs", strings.Join(recording.ISRCs, ", "))
		var alternate, albums []string
		for _, credit := range recording.ArtistCredits {
			if credit.Name != "" {
				alternate = append(alternate, credit.Name)
			}
			if credit.CreditedName != "" {
				alternate = append(alternate, credit.CreditedName)
			}
			alternate = append(alternate, credit.Aliases...)
		}
		for _, release := range recording.Releases {
			if release.Title != "" {
				albums = append(albums, release.Title)
			}
			if release.AlbumTitle != "" {
				albums = append(albums, release.AlbumTitle)
			}
		}
		put("artist_credits", strings.Join(alternate, "; "))
		put("albums", strings.Join(albums, "; "))
		if validateCensorCandidates([]CensorCandidate{candidate}) != nil {
			continue
		}
		candidates = append(candidates, candidate)
		seenIDs[id] = true
		// ISRCs are useful exact identifiers; aliases retain the recording title.
		for _, isrc := range recording.ISRCs {
			addQuery(isrc)
		}
		addQuery(recording.Artist + " " + recording.Title)
		for _, name := range alternate {
			if strings.TrimSpace(name) != "" {
				addQuery(name + " " + recording.Title)
			}
		}
	}
	return candidates, variants
}

func censorEnrichedQueries(base, variants []string) []string {
	out := append([]string(nil), base...)
	seen := map[string]bool{}
	for _, query := range base {
		seen[strings.ToLower(strings.TrimSpace(query))] = true
	}
	added := 0
	for _, query := range variants {
		key := strings.ToLower(strings.TrimSpace(query))
		if added >= censorMetadataQueryLimit || len(out) >= 4+censorMetadataQueryLimit {
			break
		}
		if !seen[key] && validCensorQuery(query) {
			out = append(out, query)
			seen[key] = true
			added++
		}
	}
	return out
}

// The remote validator keeps its original 100-result limit. Only locally created,
// inert metadata pages (including legacy MusicBrainz) receive the separate persisted metadata allowance.
func validateCensorJobCandidates(items []CensorCandidate) error {
	backend, metadata := []CensorCandidate{}, []CensorCandidate{}
	seen := map[string]bool{}
	for _, c := range items {
		if seen[c.ID] {
			return fmt.Errorf("duplicate censor candidate")
		}
		seen[c.ID] = true
		if strings.HasPrefix(c.ID, "musicbrainz:") {
			id := strings.TrimPrefix(c.ID, "musicbrainz:")
			if !censorMBID.MatchString(id) || c.Provider != "musicbrainz" || c.URL != "https://musicbrainz.org/recording/"+id || c.FullAudio || c.Downloadable || c.Result != nil || c.Metadata["kind"] != "page" {
				return fmt.Errorf("invalid MusicBrainz review candidate")
			}
			metadata = append(metadata, c)
		} else if strings.HasPrefix(c.ID, "yandex-metadata:") {
			id := strings.TrimPrefix(c.ID, "yandex-metadata:")
			if !censorYandexMetadataID(id) || c.Provider != "yandex" || c.URL != "https://music.yandex.ru/track/"+id || c.FullAudio || c.Downloadable || c.Result != nil || c.Metadata["kind"] != "page" {
				return fmt.Errorf("invalid Yandex review candidate")
			}
			metadata = append(metadata, c)
		} else {
			backend = append(backend, c)
		}
	}
	if len(metadata) > censorMetadataLimit {
		return fmt.Errorf("too many metadata candidates")
	}
	if err := validateCensorCandidates(backend); err != nil {
		return err
	}
	return validateCensorCandidates(metadata)
}
