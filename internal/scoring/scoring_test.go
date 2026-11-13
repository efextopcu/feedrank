package scoring_test

import (
	"math"
	"testing"
	"time"

	"feedrank/internal/domain"
	"feedrank/internal/scoring"
)

func TestStatic(t *testing.T) {
	tests := []struct {
		name string
		in   domain.Content
		want domain.Score
	}{
		{
			// Provider 1's "Sourdough Starter Basics".
			name: "video",
			in:   domain.Content{Type: domain.Video, Metrics: domain.Metrics{Views: 15000, Likes: 1200}},
			want: domain.Score{
				Base:            15000.0/1000 + 1200.0/100, // 27
				TypeCoefficient: 1.5,
				Engagement:      1200.0 / 15000 * 10, // 0.8
				Final:           27*1.5 + 0.8,
			},
		},
		{
			// Provider 2's "Cast Iron Care Guide".
			name: "text",
			in:   domain.Content{Type: domain.Text, Metrics: domain.Metrics{ReadingTime: 8, Reactions: 450}},
			want: domain.Score{
				Base:            8 + 450.0/50, // 17
				TypeCoefficient: 1.0,
				Engagement:      450.0 / 8 * 5, // 281.25
				Final:           17 + 281.25,
			},
		},
		{
			name: "video without views has no engagement",
			in:   domain.Content{Type: domain.Video, Metrics: domain.Metrics{Likes: 10}},
			want: domain.Score{Base: 0.1, TypeCoefficient: 1.5, Final: 0.1 * 1.5},
		},
		{
			name: "text without reading time has no engagement",
			in:   domain.Content{Type: domain.Text, Metrics: domain.Metrics{Reactions: 100}},
			want: domain.Score{Base: 2, TypeCoefficient: 1.0, Final: 2},
		},
		{
			name: "metrics of the other type are ignored",
			in:   domain.Content{Type: domain.Text, Metrics: domain.Metrics{Views: 1_000_000, Likes: 50_000, ReadingTime: 5}},
			want: domain.Score{Base: 5, TypeCoefficient: 1.0, Final: 5},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scoring.Static(tt.in)
			if !closeScore(got, tt.want) {
				t.Errorf("Static() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestRecency(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour

	tests := []struct {
		age  time.Duration
		want float64
	}{
		{-day, 5}, // future dates count as new
		{0, 5},
		{7 * day, 5}, // inclusive edge: still within a week
		{7*day + time.Second, 3},
		{30 * day, 3},
		{30*day + time.Second, 1},
		{90 * day, 1},
		{90*day + time.Second, 0},
		{3 * 365 * day, 0},
	}
	for _, tt := range tests {
		if got := scoring.Recency(now.Add(-tt.age), now); got != tt.want {
			t.Errorf("Recency(age %v) = %v, want %v", tt.age, got, tt.want)
		}
	}
}

func TestRecencyTiersAreACopy(t *testing.T) {
	tiers := scoring.RecencyTiers()
	tiers[0].Points = 100
	if scoring.RecencyTiers()[0].Points == 100 {
		t.Fatal("callers must not be able to change the shared tiers")
	}
}

func closeScore(a, b domain.Score) bool {
	near := func(x, y float64) bool { return math.Abs(x-y) < 1e-9 }
	return near(a.Base, b.Base) && near(a.TypeCoefficient, b.TypeCoefficient) &&
		near(a.Engagement, b.Engagement) && near(a.Recency, b.Recency) && near(a.Final, b.Final)
}
