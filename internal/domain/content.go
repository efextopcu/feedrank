// Package domain holds the types the rest of the service shares: content and
// its score, search queries, and classified errors. It imports nothing else
// from this module.
package domain

import "time"

// ContentType is the canonical content type. The scoring formula differs per
// type, so every provider label has to map onto one of these.
type ContentType string

// The canonical content types.
const (
	Video ContentType = "video"
	Text  ContentType = "text"
)

// Valid reports whether t is a canonical type.
func (t ContentType) Valid() bool { return t == Video || t == Text }

// Metrics are the engagement counters a provider reports. Videos carry views
// and likes; text carries reading time in minutes and reactions.
type Metrics struct {
	Views       int64
	Likes       int64
	ReadingTime int64
	Reactions   int64
}

// Score is an item's score split into the terms of the formula:
//
//	Final = Base × TypeCoefficient + Engagement + Recency
type Score struct {
	Base            float64
	TypeCoefficient float64
	Engagement      float64
	Recency         float64
	Final           float64
}

// Content is one normalised item from a provider.
type Content struct {
	ID          int64 // assigned by storage
	Provider    string
	ExternalID  string // the provider's own id, unique only within that provider
	Title       string
	Type        ContentType
	Tags        []string
	PublishedAt time.Time
	Metrics     Metrics
	Score       Score

	// Relevance is the full-text rank of a keyword search; zero otherwise.
	Relevance float64
}
