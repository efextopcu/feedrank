package provider

import (
	"encoding/json"
	"time"

	"feedrank/internal/domain"
)

// NewJSONProvider returns provider 1, which serves JSON:
//
//	{"contents": [{"id": "v1", "title": "...", "type": "video",
//	   "metrics": {"views": 15000, "likes": 1200, "duration": "15:30"},
//	   "published_at": "2024-03-15T10:00:00Z", "tags": ["programming"]}],
//	 "pagination": {"total": 150, "page": 1, "per_page": 10}}
func NewJSONProvider(name, url string, paging Paging, client *Client) (*HTTPProvider, error) {
	return New(Spec{
		Name:      name,
		URL:       url,
		PageParam: "page",
		SizeParam: "per_page",
		Paging:    paging,
		Decode:    decodeJSON,
		Types: map[string]domain.ContentType{
			"video": domain.Video,
			"text":  domain.Text,
		},
		DateLayouts: []string{time.RFC3339},
	}, client)
}

type jsonPage struct {
	Contents []struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		Type        string   `json:"type"`
		PublishedAt string   `json:"published_at"`
		Tags        []string `json:"tags"`
		Metrics     struct {
			Views       int64 `json:"views"`
			Likes       int64 `json:"likes"`
			ReadingTime int64 `json:"reading_time"`
			Reactions   int64 `json:"reactions"`
		} `json:"metrics"`
	} `json:"contents"`
}

func decodeJSON(body []byte) ([]RawItem, error) {
	var page jsonPage
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, err
	}

	items := make([]RawItem, 0, len(page.Contents))
	for _, c := range page.Contents {
		items = append(items, RawItem{
			ID:          c.ID,
			Title:       c.Title,
			Type:        c.Type,
			PublishedAt: c.PublishedAt,
			Tags:        c.Tags,
			Views:       c.Metrics.Views,
			Likes:       c.Metrics.Likes,
			ReadingTime: c.Metrics.ReadingTime,
			Reactions:   c.Metrics.Reactions,
		})
	}
	return items, nil
}
