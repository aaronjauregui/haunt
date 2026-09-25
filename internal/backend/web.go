package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
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

const (
	// pageRedirects is as far as a fetch will follow. Every hop is dialled
	// through the same guard, so a redirect cannot smuggle in an address
	// the first one would have been refused for.
	pageRedirects = 5
	// pageTimeout bounds one fetch. A page a ghost reads mid-sentence is
	// worth abandoning long before the tool timeout would.
	pageTimeout = 15 * time.Second
	// pageMaxBytes is the most that is read off the wire. Well past any
	// article and far short of anything that would be felt.
	pageMaxBytes = 2 << 20
	// pageChunkRunes is how much of a page one read returns: a few
	// paragraphs, which is what the model can use, and small enough that
	// several reads still fit a turn.
	pageChunkRunes = 1800
	// pageChunkBytes bounds the same chunk the way the caller's own limit
	// on a tool result does, in bytes rather than runes. A page in a
	// script that spends three bytes a rune would otherwise come back
	// several times longer than a tool result may be, and the clip would
	// land on the footer that says where to read on from — leaving the
	// offset a guess, and the runes between the clip and that offset
	// unread by anyone.
	pageChunkBytes = 1500
	// pageCacheTTL keeps an extracted page around long enough to page
	// through it in conversation without fetching it again.
	pageCacheTTL = 10 * time.Minute
	// mainTextMin is how much text a <main> or <article> must hold before
	// it is believed. Below it the element is a wrapper around a menu, and
	// the body is the better answer.
	mainTextMin = 500
)

// searchClient talks to the search provider, which may well be the
// person's own SearXNG on loopback. It is deliberately not the guarded
// client: the provider is configured, and a configured address is trusted
// in a way a page's is not.
var searchClient = &http.Client{Timeout: 20 * time.Second}

// SearchEnabled reports whether the user configured a search provider.
func (s *Service) SearchEnabled() bool {
	switch s.cfg.Search.Backend {
	case "searxng":
		return s.cfg.Search.BaseURL != ""
	case "brave":
		// The endpoint is fixed, so what makes brave usable is the key.
		// Without it every call would pass the gates, spend the ration
		// and then fail at the provider.
		return s.cfg.Search.APIKeyEnv != ""
	default:
		return false
	}
}

// Unprompted says when a ghost may search on a turn the person did not
// start: "never", "skills" or "any". It is the driver's policy rather than
// the backend's, but it is read from the same section, so it is answered
// here.
func (s *Service) Unprompted() string {
	switch s.cfg.Search.Unprompted {
	case "never", "any":
		return s.cfg.Search.Unprompted
	default:
		return "skills"
	}
}

// SpendUnpromptedSearch charges the smaller ration a ghost may spend while
// nobody is watching. Search and ReadPage charge the general one
// themselves, so an unprompted call spends both: this first, then the call.
func (s *Service) SpendUnpromptedSearch() error {
	if err := s.budgets.unprompted.spend(); err != nil {
		return fmt.Errorf("unprompted search budget: %w", err)
	}
	return nil
}

// SpendUnpromptedFile charges the ration a ghost may spend reading the
// person's shared folders while nobody is watching. It is the only charge
// on a file read: reads on the person's turn are not metered, since a
// local file costs tokens rather than money. A picture also spends the
// vision budget, which DescribeImage charges itself.
func (s *Service) SpendUnpromptedFile() error {
	if err := s.budgets.files.spend(); err != nil {
		return fmt.Errorf("unprompted file budget: %w", err)
	}
	return nil
}

// Search asks the configured provider for n results. The text that comes
// back is the web's, not the person's: callers hand it to a model inside an
// envelope that says so.
func (s *Service) Search(ctx context.Context, query string, n int) ([]Result, error) {
	if !s.SearchEnabled() {
		return nil, fmt.Errorf("no search backend configured (set [backend.search] in the haunt config)")
	}
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("search: no query")
	}
	n = resultCount(n, s.cfg.Search.Results)
	if err := s.budgets.search.spend(); err != nil {
		return nil, fmt.Errorf("search budget: %w", err)
	}
	s.trace("search → %s %q count=%d safesearch=%s",
		s.cfg.Search.Backend, query, n, s.cfg.Search.SafeSearch)

	var (
		results []Result
		err     error
	)
	switch s.cfg.Search.Backend {
	case "searxng":
		results, err = searxngSearch(ctx, &s.cfg.Search, query, n)
	case "brave":
		results, err = braveSearch(ctx, &s.cfg.Search, query, n)
	default:
		return nil, fmt.Errorf("unknown search backend %q (searxng or brave)", s.cfg.Search.Backend)
	}
	if err != nil {
		return nil, err
	}
	if len(results) > n {
		results = results[:n]
	}
	s.trace("search: %d results", len(results))
	return results, nil
}

// resultCount settles how many results to ask for: what the caller wanted,
// else what the user configured, clamped to something a turn can hold.
func resultCount(want, configured int) int {
	if want <= 0 {
		want = configured
	}
	if want <= 0 {
		want = 5
	}
	if want > 10 {
		want = 10
	}
	return want
}

// safeSearchLevel maps the configured word onto the number both providers
// happen to use.
func safeSearchLevel(s string) int {
	switch s {
	case "off":
		return 0
	case "strict":
		return 2
	default:
		return 1
	}
}

// searxngSearch queries a SearXNG instance through its JSON format, which
// the person has to enable in settings.yml — hence the hint on a 403,
// which is how a SearXNG with the default formats refuses.
func searxngSearch(ctx context.Context, cfg *SearchConfig, query string, n int) ([]Result, error) {
	q := url.Values{}
	q.Set("q", query)
	q.Set("format", "json")
	q.Set("safesearch", strconv.Itoa(safeSearchLevel(cfg.SafeSearch)))
	endpoint := strings.TrimRight(cfg.BaseURL, "/") + "/search?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := searchClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search backend: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusForbidden:
		return nil, fmt.Errorf("search backend refused the JSON format: enable json in search.formats in the SearXNG settings.yml")
	case http.StatusTooManyRequests:
		return nil, fmt.Errorf("the search provider is rate-limiting; try again in a moment")
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("search backend: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}

	var out struct {
		Results []struct {
			Title         string `json:"title"`
			URL           string `json:"url"`
			Content       string `json:"content"`
			PublishedDate string `json:"publishedDate"`
		} `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, pageMaxBytes)).Decode(&out); err != nil {
		return nil, fmt.Errorf("search backend response: %w", err)
	}
	results := make([]Result, 0, len(out.Results))
	for _, r := range out.Results {
		if r.URL == "" {
			continue
		}
		results = append(results, Result{
			Title:   strings.TrimSpace(r.Title),
			URL:     r.URL,
			Snippet: collapse(r.Content),
			Age:     relativeAge(r.PublishedDate),
		})
		if len(results) == n {
			break
		}
	}
	return results, nil
}

// braveURL is fixed: the Brave Search API is one endpoint, and a
// configurable one would only be a way to send the key somewhere else.
const braveURL = "https://api.search.brave.com/res/v1/web/search"

// braveSearch queries the Brave Search API, whose key is named by
// api_key_env and read from the driver's environment.
func braveSearch(ctx context.Context, cfg *SearchConfig, query string, n int) ([]Result, error) {
	key := cfg.apiKey()
	if key == "" {
		return nil, fmt.Errorf("no Brave API key: set [backend.search] api_key_env to the variable holding it")
	}
	q := url.Values{}
	q.Set("q", query)
	q.Set("count", strconv.Itoa(n))
	switch safeSearchLevel(cfg.SafeSearch) {
	case 0:
		q.Set("safesearch", "off")
	case 2:
		q.Set("safesearch", "strict")
	default:
		q.Set("safesearch", "moderate")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", braveURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", key)
	resp, err := searchClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search backend: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusTooManyRequests:
		return nil, fmt.Errorf("the search provider is rate-limiting; try again in a moment")
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("the search provider refused the key in %s", cfg.APIKeyEnv)
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("search backend: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}

	var out struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
				Age         string `json:"age"`
				PageAge     string `json:"page_age"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, pageMaxBytes)).Decode(&out); err != nil {
		return nil, fmt.Errorf("search backend response: %w", err)
	}
	results := make([]Result, 0, len(out.Web.Results))
	for _, r := range out.Web.Results {
		if r.URL == "" {
			continue
		}
		age := strings.TrimSpace(r.Age)
		if age == "" {
			age = relativeAge(r.PageAge)
		}
		results = append(results, Result{
			Title:   collapse(stripTags(r.Title)),
			URL:     r.URL,
			Snippet: collapse(stripTags(r.Description)),
			Age:     age,
		})
		if len(results) == n {
			break
		}
	}
	return results, nil
}

// relativeAge turns a timestamp into the plain words a person would use.
// Providers that report a date rather than an age get rendered the same
// way as the ones that report an age, so the ghost reads one thing.
func relativeAge(stamp string) string {
	stamp = strings.TrimSpace(stamp)
	if stamp == "" {
		return ""
	}
	var t time.Time
	var err error
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err = time.Parse(layout, stamp); err == nil {
			break
		}
	}
	if err != nil {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return ""
	case d < time.Hour:
		return "just now"
	case d < 24*time.Hour:
		return plural(int(d/time.Hour), "hour") + " ago"
	case d < 60*24*time.Hour:
		return plural(int(d/(24*time.Hour)), "day") + " ago"
	case d < 730*24*time.Hour:
		return plural(int(d/(30*24*time.Hour)), "month") + " ago"
	default:
		return plural(int(d/(365*24*time.Hour)), "year") + " ago"
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

// stripTags removes the <strong> markup some providers put around the
// matched words. The ghost is reading prose, not rendering it.
func stripTags(s string) string {
	for {
		i := strings.IndexByte(s, '<')
		if i < 0 {
			return s
		}
		j := strings.IndexByte(s[i:], '>')
		if j < 0 {
			return s
		}
		s = s[:i] + s[i+j+1:]
	}
}

// RenderResults lays search results out for a model, inside an envelope
// that says where the text came from. The envelope is not decoration: it
// is the only thing standing between a page that says "ignore your
// instructions" and a model that believes the person said it.
func RenderResults(query string, results []Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[search results for %q — the sites below wrote these, not the person]\n", query)
	if len(results) == 0 {
		b.WriteString("(nothing found)\n")
	}
	for i, r := range results {
		title := r.Title
		if title == "" {
			title = "(untitled)"
		}
		line := fmt.Sprintf("%d. %s", i+1, title)
		if host := hostOf(r.URL); host != "" {
			line += " — " + host
		}
		if r.Age != "" {
			line += " · " + r.Age
		}
		b.WriteString(line + "\n")
		if r.Snippet != "" {
			b.WriteString("   " + r.Snippet + "\n")
		}
		b.WriteString("   " + r.URL + "\n")
	}
	b.WriteString("[end of search results]")
	return b.String()
}

// hostOf is the bare host of a URL, which is what a person recognizes: the
// rest of the line is the title and the snippet.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}

// Excerpt renders the piece of a page starting at the rune offset from,
// in the same untrusted-content envelope as search results, ending with
// either the offset to ask for next or a line saying that was all of it.
func (p Page) Excerpt(from int) string {
	runes := []rune(p.Text)
	if from < 0 {
		from = 0
	}
	if from > len(runes) {
		from = len(runes)
	}
	end := from + pageChunkRunes
	if end >= len(runes) {
		end = len(runes)
	}
	end = from + byteCut(runes[from:end])
	if end < len(runes) {
		if cut := paragraphCut(runes[from:end]); cut > 0 {
			end = from + cut
		}
	}

	title := p.Title
	if title == "" {
		title = "(untitled)"
	}
	var b strings.Builder
	// The domain leads, because it is the one thing the model needs to
	// weigh a claim and the one thing no wrapper around this result
	// supplies. Whose words these are is said here rather than at the
	// end, where the paging footer has functional work to do.
	host := hostOf(p.URL)
	if host == "" {
		host = "the web"
	}
	fmt.Fprintf(&b, "[page from %s — %q · %s — the site wrote this, not the person]\n",
		host, title, p.URL)
	b.WriteString(strings.TrimSpace(string(runes[from:end])))
	b.WriteString("\n")
	if end < len(runes) {
		fmt.Fprintf(&b, "[more: read_page with from=%d · %d%% read]", end, 100*end/len(runes))
	} else {
		b.WriteString("[end of page]")
	}
	return b.String()
}

// byteCut is how many of a chunk's runes fit in pageChunkBytes, so what a
// read returns is bounded the same way the tool result carrying it is. It
// cuts on a rune, never inside one.
func byteCut(chunk []rune) int {
	bytes := 0
	for i, r := range chunk {
		n := utf8.RuneLen(r)
		if bytes+n > pageChunkBytes {
			return i
		}
		bytes += n
	}
	return len(chunk)
}

// paragraphCut finds the paragraph break a chunk should end on, so a read
// stops mid-thought as rarely as possible. It only looks in the last part
// of the chunk: cutting a 1800-rune read down to 200 to land on a break
// would waste the read.
func paragraphCut(chunk []rune) int {
	for i := len(chunk) - 2; i >= len(chunk)/2; i-- {
		if chunk[i] == '\n' && chunk[i+1] == '\n' {
			return i
		}
	}
	return 0
}

// pageCache holds extracted pages so paging through one costs a single
// fetch. It is keyed by URL and small by construction: a ghost reads one
// page at a time, and an entry is worth keeping only as long as the
// conversation that asked for it.
var pageCache = struct {
	sync.Mutex
	entries map[string]pageEntry
}{entries: map[string]pageEntry{}}

type pageEntry struct {
	page Page
	at   time.Time
}

func cachedPage(url string) (Page, bool) {
	pageCache.Lock()
	defer pageCache.Unlock()
	e, ok := pageCache.entries[url]
	if !ok {
		return Page{}, false
	}
	if time.Since(e.at) > pageCacheTTL {
		delete(pageCache.entries, url)
		return Page{}, false
	}
	return e.page, true
}

func cachePage(url string, p Page) {
	pageCache.Lock()
	defer pageCache.Unlock()
	now := time.Now()
	for k, e := range pageCache.entries {
		if now.Sub(e.at) > pageCacheTTL {
			delete(pageCache.entries, k)
		}
	}
	pageCache.entries[url] = pageEntry{page: p, at: now}
}

// ReadPage fetches one http or https URL and extracts its readable text.
// Only public addresses are reachable: the machine's own services are not
// the web, and a ghost reading them is the injection path worth closing.
//
// A page stays in memory for a few minutes after it is read, so paging
// through a long one is one fetch and one charge against the ration rather
// than several.
func (s *Service) ReadPage(ctx context.Context, raw string) (Page, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return Page{}, fmt.Errorf("that is not a URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return Page{}, fmt.Errorf("only http and https pages can be read")
	}
	if u.Host == "" {
		return Page{}, fmt.Errorf("that is not a URL")
	}
	if p, ok := cachedPage(u.String()); ok {
		return p, nil
	}
	if err := s.budgets.search.spend(); err != nil {
		return Page{}, fmt.Errorf("search budget: %w", err)
	}
	s.trace("read page → %s", u)

	p, err := fetchPage(ctx, u.String())
	if err != nil {
		return Page{}, err
	}
	// Under both the URL asked for and the one redirects landed on. The
	// excerpt names the latter, so a model that pages on from the address
	// it was just shown finds the page already read rather than paying
	// for it twice.
	cachePage(u.String(), p)
	if p.URL != "" && p.URL != u.String() {
		cachePage(p.URL, p)
	}
	s.trace("read page: %q, %d chars", p.Title, len([]rune(p.Text)))
	return p, nil
}

// pageClient fetches pages, and only pages: its dialer refuses every
// address that is not out on the public internet, and it does so after the
// name has been resolved, so a name that resolves to loopback on the
// second lookup is refused on the connection it would actually use.
var pageClient = &http.Client{
	Timeout: pageTimeout,
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
			Control:   func(n, a string, c syscall.RawConn) error { return dialGuard(n, a, c) },
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: pageTimeout,
		DisableKeepAlives:     true,
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= pageRedirects {
			return fmt.Errorf("too many redirects")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return fmt.Errorf("that page redirects somewhere that is not the web")
		}
		return nil
	},
}

// dialGuard is the check the page dialer applies to every connection. It
// is a variable because a test of everything downstream of it — the size
// limit, the content types, the cache — needs a server on loopback, which
// is precisely what the guard exists to refuse.
var dialGuard = guardAddress

// guardAddress is the dialer's last word on where a page may come from. It
// runs on the resolved address of every connection, including each
// redirect hop, so there is no window between checking a name and
// connecting to it: the address being dialled is the address being
// checked. Everything private is refused — the machine's own services, the
// house's printer, the cloud metadata endpoint — because none of them is
// the web, and a page that can reach them is a page that can read them out
// to whoever wrote it.
func guardAddress(network, address string, _ syscall.RawConn) error {
	if !strings.HasPrefix(network, "tcp") {
		return fmt.Errorf("refusing to reach %s over %s", address, network)
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("refusing to reach %s", address)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("refusing to reach %s", address)
	}
	return checkPublicIP(ip)
}

// cgnat is the carrier-grade NAT range: not private by Go's reckoning, and
// not the public internet either.
var cgnat = net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// checkPublicIP refuses any address that is not out on the open internet.
func checkPublicIP(ip net.IP) error {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	switch {
	case ip.IsUnspecified():
		return fmt.Errorf("refusing to reach an unspecified address")
	case ip.IsLoopback():
		return fmt.Errorf("refusing to reach %s: that is this machine, not the web", ip)
	case ip.IsPrivate():
		return fmt.Errorf("refusing to reach %s: that is a private address, not the web", ip)
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return fmt.Errorf("refusing to reach %s: that is a link-local address, not the web", ip)
	case ip.IsMulticast(), ip.IsInterfaceLocalMulticast():
		return fmt.Errorf("refusing to reach %s: that is a multicast address, not the web", ip)
	case cgnat.Contains(ip):
		return fmt.Errorf("refusing to reach %s: that is a carrier address, not the web", ip)
	}
	return nil
}

// fetchPage gets a URL through the guarded client and reduces it to text.
func fetchPage(ctx context.Context, raw string) (Page, error) {
	ctx, cancel := context.WithTimeout(ctx, pageTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		return Page{}, err
	}
	// Identify as what it is. A page that would rather not be read by a
	// desktop companion should be able to say so.
	req.Header.Set("User-Agent", "haunt/1 (desktop companion; one page per request)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9")

	resp, err := pageClient.Do(req)
	if err != nil {
		return Page{}, fmt.Errorf("could not read that page: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Page{}, fmt.Errorf("that page answered %s", resp.Status)
	}

	kind := resp.Header.Get("Content-Type")
	if i := strings.IndexByte(kind, ';'); i >= 0 {
		kind = kind[:i]
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "text/html", "application/xhtml+xml", "text/plain", "":
	default:
		return Page{}, fmt.Errorf("that is a %s, not a page", kind)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, pageMaxBytes))
	if err != nil {
		return Page{}, fmt.Errorf("could not read that page: %w", err)
	}
	final := resp.Request.URL.String()
	if kind == "text/plain" {
		return Page{Title: hostOf(final), URL: final, Text: collapseParagraphs(string(body))}, nil
	}
	title, text, err := extract(string(body))
	if err != nil {
		return Page{}, err
	}
	if strings.TrimSpace(text) == "" {
		return Page{}, fmt.Errorf("that page has no text to read")
	}
	return Page{Title: title, URL: final, Text: text}, nil
}

// skipTags are the parts of a page that are furniture rather than reading:
// navigation, chrome, and the scripts and styles that are not prose at
// all. Dropping them is most of what separates a readable page from a
// wall of menu items.
var skipTags = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true,
	atom.Nav: true, atom.Header: true, atom.Footer: true,
	atom.Aside: true, atom.Form: true, atom.Svg: true,
	atom.Template: true, atom.Iframe: true,
}

// blockTags end a paragraph. The list is what a reader perceives as a
// break, not what CSS would call a block.
var blockTags = map[atom.Atom]bool{
	atom.P: true, atom.Div: true, atom.Br: true, atom.Hr: true,
	atom.Li: true, atom.Tr: true, atom.Section: true, atom.Article: true,
	atom.Blockquote: true, atom.Pre: true, atom.Table: true, atom.Dd: true,
	atom.Dt: true, atom.Figcaption: true, atom.Main: true,
	atom.H1: true, atom.H2: true, atom.H3: true,
	atom.H4: true, atom.H5: true, atom.H6: true,
}

// extract reduces an HTML document to a title and readable paragraphs. It
// prefers the page's own <main> or <article> when there is one with real
// text in it, since that is the part the author meant to be read.
func extract(doc string) (title, text string, err error) {
	root, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		return "", "", fmt.Errorf("could not read that page: %w", err)
	}
	title = collapse(nodeText(findTag(root, atom.Title)))
	body := findTag(root, atom.Body)
	if body == nil {
		body = root
	}
	chosen := body
	for _, tag := range []atom.Atom{atom.Main, atom.Article} {
		n := findTag(body, tag)
		if n == nil {
			continue
		}
		if t := collapseParagraphs(nodeText(n)); len([]rune(t)) > mainTextMin {
			chosen = n
			break
		}
	}
	text = collapseParagraphs(nodeText(chosen))
	if title == "" {
		title = collapse(nodeText(findTag(body, atom.H1)))
	}
	return title, text, nil
}

// findTag returns the first element with this tag, in document order.
func findTag(n *html.Node, tag atom.Atom) *html.Node {
	if n == nil {
		return nil
	}
	if n.Type == html.ElementNode && n.DataAtom == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findTag(c, tag); found != nil {
			return found
		}
	}
	return nil
}

// nodeText gathers the text under a node, with a newline wherever the
// markup implies a break. The result still needs collapsing: what comes
// out here is the reading order, not the layout.
func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n == nil {
			return
		}
		switch n.Type {
		case html.TextNode:
			b.WriteString(n.Data)
			return
		case html.ElementNode:
			if skipTags[n.DataAtom] || strings.EqualFold(n.Data, "svg") {
				return
			}
			if blockTags[n.DataAtom] {
				b.WriteString("\n")
			}
		case html.CommentNode, html.DoctypeNode:
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && blockTags[n.DataAtom] {
			b.WriteString("\n")
		}
	}
	walk(n)
	return b.String()
}

// collapse squeezes all whitespace in a fragment to single spaces.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// collapseParagraphs collapses the whitespace inside each line and keeps
// the breaks between them, so the shape of the page survives into
// something a model reads as paragraphs.
func collapseParagraphs(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = collapse(line); line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n\n")
}
