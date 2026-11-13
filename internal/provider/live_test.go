//go:build live

package provider

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveEndpoints checks the real provider endpoints still match the
// decoders. Fixtures cannot catch a provider changing its format, but this
// depends on the internet, so it is kept out of the normal test run. It reads
// the same PROVIDER1_URL and PROVIDER2_URL as the server (`make test-live`
// takes them from .env).
func TestLiveEndpoints(t *testing.T) {
	url1, url2 := os.Getenv("PROVIDER1_URL"), os.Getenv("PROVIDER2_URL")
	if url1 == "" || url2 == "" {
		t.Skip("PROVIDER1_URL and PROVIDER2_URL are not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	p1, _ := NewJSONProvider("provider1", url1, DefaultPaging(), NewClient(DefaultClientConfig()))
	p2, _ := NewXMLProvider("provider2", url2, DefaultPaging(), NewClient(DefaultClientConfig()))

	for _, p := range []Provider{p1, p2} {
		t.Run(p.Name(), func(t *testing.T) {
			var items int
			for page, err := range p.Pages(ctx) {
				if err != nil {
					t.Fatal(err)
				}
				items += len(page.Items)
				if len(page.Rejected) > 0 {
					t.Errorf("page %d rejected %+v; the provider's format may have changed", page.Number, page.Rejected)
				}
			}
			if items == 0 {
				t.Error("no items; the provider's format may have changed")
			}
		})
	}
}
