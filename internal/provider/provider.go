// Package provider fetches content from external providers and normalises it
// into domain.Content.
//
// A provider is described by a Spec: where it lives, how it pages, and how to
// decode one page. Fetching, paging, retries, rate limiting and validation are
// shared, so adding a provider usually means writing one decode function.
package provider

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/url"
	"strconv"
	"strings"
	"time"

	"feedrank/internal/domain"
)

// Provider is a source of content.
type Provider interface {
	Name() string
	// Pages fetches the provider's content one page at a time, so a feed of
	// any size is processed in bounded memory. The sequence ends at the end of
	// the feed, when the caller stops ranging, or with a single error.
	Pages(ctx context.Context) iter.Seq2[Page, error]
}

// Page is one page of a provider's feed, normalised.
type Page struct {
	Number int
	Items  []domain.Content
	// Rejected lists items that failed normalisation. A bad item is left out
	// on its own; it never fails the page.
	Rejected []Rejection
}

// ErrPageLimit ends a walk that reached Paging.MaxPages while the provider
// still had more to give. The pages before it are valid; the rest of the feed
// was not fetched.
var ErrPageLimit = errors.New("stopped at the page limit; the rest of the feed was not fetched")

// Rejection explains why one item was left out.
type Rejection struct {
	ExternalID string
	Reason     string
}

// RawItem is a provider item under common field names, not yet validated.
// Decoders produce it; the shared normalisation turns it into domain.Content.
type RawItem struct {
	ID          string
	Title       string
	Type        string // the provider's own label, e.g. "article"
	PublishedAt string
	Tags        []string
	Views       int64
	Likes       int64
	ReadingTime int64
	Reactions   int64
}

// Paging sets how much of a feed one walk requests.
type Paging struct {
	Size int // items asked for per page
	// MaxPages caps one walk, in case a provider pages forever. Reaching it is
	// reported as ErrPageLimit, never passed over in silence.
	MaxPages int
}

// DefaultPaging allows a million items per provider in pages of 100.
func DefaultPaging() Paging { return Paging{Size: 100, MaxPages: 10_000} }

// Spec describes one provider.
type Spec struct {
	Name string
	URL  string

	// PageParam and SizeParam name the query parameters used for paging.
	PageParam string
	SizeParam string
	Paging    Paging

	// Decode parses one response body.
	Decode func(body []byte) ([]RawItem, error)
	// Types maps the provider's type labels onto canonical types. An unmapped
	// label is rejected rather than guessed, because each type is scored by a
	// different equation.
	Types map[string]domain.ContentType
	// DateLayouts are tried in order. A date without a zone is read as UTC.
	DateLayouts []string
}

// HTTPProvider fetches a Spec over HTTP.
type HTTPProvider struct {
	spec   Spec
	base   *url.URL
	client *Client
}

// New builds an HTTPProvider, rejecting an incomplete Spec.
func New(spec Spec, client *Client) (*HTTPProvider, error) {
	base, err := url.Parse(spec.URL)
	switch {
	case spec.Name == "":
		return nil, errors.New("provider spec has no name")
	case err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https"):
		return nil, fmt.Errorf("provider %s: %q is not an http(s) URL", spec.Name, spec.URL)
	case spec.Decode == nil || len(spec.Types) == 0 || len(spec.DateLayouts) == 0:
		return nil, fmt.Errorf("provider %s: spec needs Decode, Types and DateLayouts", spec.Name)
	case spec.PageParam == "" || spec.Paging.Size < 1 || spec.Paging.MaxPages < 1:
		return nil, fmt.Errorf("provider %s: spec needs PageParam and a positive page size and page limit", spec.Name)
	}
	return &HTTPProvider{spec: spec, base: base, client: client}, nil
}

// Name returns the provider's name, which is also its storage key.
func (p *HTTPProvider) Name() string { return p.spec.Name }

// Pages walks the provider's pages, yielding each as soon as it is fetched.
//
// The walk ends at a short page, or at a page that brings nothing new since
// the one before: both live endpoints ignore the page parameter and serve the
// same items for every page. Only the previous page's ids are remembered, so
// memory stays bounded however long the feed is. A failed page ends the walk
// with its error; the pages already yielded stand.
func (p *HTTPProvider) Pages(ctx context.Context) iter.Seq2[Page, error] {
	return func(yield func(Page, error) bool) {
		var previous map[string]bool

		for n := 1; ; n++ {
			if n > p.spec.Paging.MaxPages {
				yield(Page{}, fmt.Errorf("%s: %w (%d pages)", p.spec.Name, ErrPageLimit, p.spec.Paging.MaxPages))
				return
			}

			body, err := p.client.Get(ctx, p.pageURL(n))
			if err != nil {
				yield(Page{}, fmt.Errorf("%s page %d: %w", p.spec.Name, n, err))
				return
			}
			raw, err := p.spec.Decode(body)
			if err != nil {
				yield(Page{}, fmt.Errorf("%s page %d: decoding: %w", p.spec.Name, n, err))
				return
			}

			page := Page{Number: n}
			current := make(map[string]bool, len(raw))
			for _, r := range raw {
				id := strings.TrimSpace(r.ID)
				if id != "" {
					if current[id] || previous[id] {
						continue
					}
					current[id] = true
				}

				item, err := p.normalize(r)
				if err != nil {
					page.Rejected = append(page.Rejected, Rejection{ExternalID: id, Reason: err.Error()})
					continue
				}
				page.Items = append(page.Items, item)
			}

			if len(current) == 0 {
				return // nothing new: the provider is repeating itself
			}
			if !yield(page, nil) || len(raw) < p.spec.Paging.Size {
				return
			}
			previous = current
		}
	}
}

func (p *HTTPProvider) pageURL(page int) string {
	u := *p.base
	q := u.Query()
	q.Set(p.spec.PageParam, strconv.Itoa(page))
	if p.spec.SizeParam != "" {
		q.Set(p.spec.SizeParam, strconv.Itoa(p.spec.Paging.Size))
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// normalize validates a raw item and maps it onto domain.Content.
func (p *HTTPProvider) normalize(r RawItem) (domain.Content, error) {
	id, title := strings.TrimSpace(r.ID), strings.TrimSpace(r.Title)
	if id == "" {
		return domain.Content{}, errors.New("missing id")
	}
	if title == "" {
		return domain.Content{}, errors.New("missing title")
	}

	contentType, ok := p.spec.Types[strings.ToLower(strings.TrimSpace(r.Type))]
	if !ok {
		return domain.Content{}, fmt.Errorf("unknown content type %q", r.Type)
	}

	publishedAt, err := parseDate(r.PublishedAt, p.spec.DateLayouts)
	if err != nil {
		return domain.Content{}, err
	}

	if r.Views < 0 || r.Likes < 0 || r.ReadingTime < 0 || r.Reactions < 0 {
		return domain.Content{}, errors.New("negative metric")
	}

	return domain.Content{
		Provider:    p.spec.Name,
		ExternalID:  id,
		Title:       title,
		Type:        contentType,
		Tags:        normalizeTags(r.Tags),
		PublishedAt: publishedAt,
		Metrics: domain.Metrics{
			Views:       r.Views,
			Likes:       r.Likes,
			ReadingTime: r.ReadingTime,
			Reactions:   r.Reactions,
		},
	}, nil
}

func parseDate(raw string, layouts []string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, raw, time.UTC); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable publish date %q", raw)
}

// normalizeTags lowercases, trims and de-duplicates tags, keeping their order.
// It never returns nil, because the tags column is NOT NULL.
func normalizeTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	seen := make(map[string]bool, len(tags))
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag != "" && !seen[tag] {
			seen[tag] = true
			out = append(out, tag)
		}
	}
	return out
}
