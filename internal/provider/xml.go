package provider

import (
	"encoding/xml"
	"time"

	"feedrank/internal/domain"
)

// NewXMLProvider returns provider 2, which serves XML:
//
//	<feed>
//	  <items>
//	    <item>
//	      <id>a1</id><headline>...</headline><type>article</type>
//	      <stats><reading_time>8</reading_time><reactions>450</reactions></stats>
//	      <publication_date>2024-03-14</publication_date>
//	      <categories><category>programming</category></categories>
//	    </item>
//	  </items>
//	  <meta><total_count>75</total_count><current_page>1</current_page>...</meta>
//	</feed>
//
// It calls text "article", and its dates have no time or zone, so they are
// read as midnight UTC.
func NewXMLProvider(name, url string, paging Paging, client *Client) (*HTTPProvider, error) {
	return New(Spec{
		Name:      name,
		URL:       url,
		PageParam: "page",
		SizeParam: "items_per_page",
		Paging:    paging,
		Decode:    decodeXML,
		Types: map[string]domain.ContentType{
			"video":   domain.Video,
			"article": domain.Text,
			"text":    domain.Text,
		},
		DateLayouts: []string{time.DateOnly, time.RFC3339},
	}, client)
}

type xmlFeed struct {
	Items []struct {
		ID          string   `xml:"id"`
		Headline    string   `xml:"headline"`
		Type        string   `xml:"type"`
		PublishedAt string   `xml:"publication_date"`
		Categories  []string `xml:"categories>category"`
		Stats       struct {
			Views       int64 `xml:"views"`
			Likes       int64 `xml:"likes"`
			ReadingTime int64 `xml:"reading_time"`
			Reactions   int64 `xml:"reactions"`
		} `xml:"stats"`
	} `xml:"items>item"`
}

func decodeXML(body []byte) ([]RawItem, error) {
	var feed xmlFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, err
	}

	items := make([]RawItem, 0, len(feed.Items))
	for _, it := range feed.Items {
		items = append(items, RawItem{
			ID:          it.ID,
			Title:       it.Headline,
			Type:        it.Type,
			PublishedAt: it.PublishedAt,
			Tags:        it.Categories,
			Views:       it.Stats.Views,
			Likes:       it.Stats.Likes,
			ReadingTime: it.Stats.ReadingTime,
			Reactions:   it.Stats.Reactions,
		})
	}
	return items, nil
}
