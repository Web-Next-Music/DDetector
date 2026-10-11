package censorsearch

import (
	"context"
	"time"
)

// Provider searches metadata only. It neither retrieves nor promises audio.
type Provider interface {
	Search(context.Context, Query) (Page, error)
}

type Query struct {
	Artist   string
	Title    string
	ISRC     string
	Duration time.Duration
	Limit    int
	Offset   int
}

type Page struct {
	Provider   string      `json:"provider"`
	Total      int         `json:"total"`
	Offset     int         `json:"offset"`
	Candidates []Candidate `json:"candidates"`
}

type ArtistCredit struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	CreditedName string   `json:"credited_name"`
	JoinPhrase   string   `json:"join_phrase"`
	Aliases      []string `json:"aliases,omitempty"`
}

type Release struct {
	ID            string         `json:"id"`
	Title         string         `json:"title"`
	Date          string         `json:"date,omitempty"`
	Country       string         `json:"country,omitempty"`
	Status        string         `json:"status,omitempty"`
	AlbumID       string         `json:"album_id,omitempty"`
	AlbumTitle    string         `json:"album_title,omitempty"`
	AlbumType     string         `json:"album_type,omitempty"`
	URL           string         `json:"url"`
	ArtistCredits []ArtistCredit `json:"artist_credits,omitempty"`
}

type Candidate struct {
	ID            string         `json:"id"`
	Title         string         `json:"title"`
	Artist        string         `json:"artist"`
	ArtistCredits []ArtistCredit `json:"artist_credits"`
	DurationMS    int64          `json:"duration_ms"`
	ISRCs         []string       `json:"isrcs"`
	Releases      []Release      `json:"releases"`
	Score         int            `json:"score"`
	URL           string         `json:"url"`
	FullAudio     bool           `json:"full_audio"`
	Downloadable  bool           `json:"downloadable"`
}
