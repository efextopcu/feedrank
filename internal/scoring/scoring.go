// Package scoring implements the ranking formula from the brief:
//
//	final = base × type_coefficient + recency + engagement
//
//	        base                          engagement                      coefficient
//	video   views/1000 + likes/100        (likes/views) × 10              1.5
//	text    reading_time + reactions/50   (reactions/reading_time) × 5    1.0
//
// Recency is the only term that changes with time, so it is never stored.
// Static computes the other terms at ingestion; the search query adds recency
// for the moment it runs, using the tiers from RecencyTiers.
package scoring

import (
	"slices"
	"time"

	"feedrank/internal/domain"
)

// Type coefficients from the brief.
const (
	VideoCoefficient = 1.5
	TextCoefficient  = 1.0
)

const day = 24 * time.Hour

// Tier awards Points to content published at most MaxAge ago.
type Tier struct {
	MaxAge time.Duration
	Points float64
}

// A month is read as 30 days and three months as 90.
var recencyTiers = []Tier{
	{MaxAge: 7 * day, Points: 5},
	{MaxAge: 30 * day, Points: 3},
	{MaxAge: 90 * day, Points: 1},
}

// RecencyTiers returns the recency tiers, youngest first. Anything older than
// the last tier scores zero.
func RecencyTiers() []Tier { return slices.Clone(recencyTiers) }

// MaxRecency is the most recency can add to a score.
func MaxRecency() float64 {
	best := 0.0
	for _, t := range recencyTiers {
		best = max(best, t.Points)
	}
	return best
}

// Static returns every term of the score except recency, which is left at
// zero. Final is therefore the score of an item too old to earn recency points.
func Static(c domain.Content) domain.Score {
	m := c.Metrics
	var s domain.Score

	switch c.Type {
	case domain.Video:
		s.Base = float64(m.Views)/1000 + float64(m.Likes)/100
		s.TypeCoefficient = VideoCoefficient
		s.Engagement = ratio(m.Likes, m.Views) * 10
	case domain.Text:
		s.Base = float64(m.ReadingTime) + float64(m.Reactions)/50
		s.TypeCoefficient = TextCoefficient
		s.Engagement = ratio(m.Reactions, m.ReadingTime) * 5
	}

	s.Final = s.Base*s.TypeCoefficient + s.Engagement
	return s
}

// Recency awards points for how recently content was published. Tier edges
// are inclusive, so exactly seven days old still counts as "within a week",
// and a future date counts as brand new.
func Recency(publishedAt, now time.Time) float64 {
	age := max(now.Sub(publishedAt), 0)
	for _, t := range recencyTiers {
		if age <= t.MaxAge {
			return t.Points
		}
	}
	return 0
}

// ratio is n/d, or zero when d is zero: a video with no views or an article
// with no reading time has no measurable engagement, not infinite engagement.
func ratio(n, d int64) float64 {
	if d <= 0 {
		return 0
	}
	return float64(n) / float64(d)
}
