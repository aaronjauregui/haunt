package backend

import (
	"context"
	"fmt"
)

// Result is one web search hit, normalized across providers so the ghost
// sees the same shape whatever the person configured.
type Result struct {
	Title   string
	URL     string
	Snippet string
	// Age is the provider's own words for how old the page is ("3 days
	// ago"), empty when it does not say. It is worth showing: a stale
	// answer confidently given is the failure mode that matters.
	Age string
}

// Page is a web page reduced to the text a ghost can read: the boilerplate
// stripped, the whitespace collapsed, and a rune offset so a long page can
// be read a piece at a time rather than filling a turn.
type Page struct {
	Title string
	URL   string
	// Text is the whole extracted page. The caller decides how much of it
	// to show.
	Text string
}

// SearchEnabled reports whether the user configured a search provider.
func (s *Service) SearchEnabled() bool { return false }

// Search asks the configured provider for n results. The text that comes
// back is the web's, not the person's: callers hand it to a model inside an
// envelope that says so.
func (s *Service) Search(ctx context.Context, query string, n int) ([]Result, error) {
	return nil, fmt.Errorf("search: not implemented")
}

// ReadPage fetches one http or https URL and extracts its readable text.
// Only public addresses are reachable: the machine's own services are not
// the web, and a ghost reading them is the injection path worth closing.
func (s *Service) ReadPage(ctx context.Context, url string) (Page, error) {
	return Page{}, fmt.Errorf("read page: not implemented")
}
