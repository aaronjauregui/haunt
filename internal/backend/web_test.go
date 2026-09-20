package backend

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
)

// allowLoopback relaxes the dialer guard for one test, so the rest of the
// fetch path can be exercised against an httptest server. The guard itself
// is tested against the real thing in TestGuardAddress.
func allowLoopback(t *testing.T) {
	t.Helper()
	dialGuard = func(string, string, syscall.RawConn) error { return nil }
	t.Cleanup(func() { dialGuard = guardAddress })
}

// freshPageCache empties the page cache, which is package-wide and would
// otherwise carry one test's page into the next.
func freshPageCache(t *testing.T) {
	t.Helper()
	clear := func() {
		pageCache.Lock()
		defer pageCache.Unlock()
		pageCache.entries = map[string]pageEntry{}
	}
	clear()
	t.Cleanup(clear)
}

func searchService(t *testing.T, cfg SearchConfig) *Service {
	t.Helper()
	return NewService(&Config{
		Search: cfg,
		Budget: BudgetConfig{SearchRequestsPerHour: 10, UnpromptedSearchPerHour: 2},
	}, nil)
}

func TestSearchEnabled(t *testing.T) {
	cases := []struct {
		cfg  SearchConfig
		want bool
	}{
		{SearchConfig{}, false},
		{SearchConfig{Backend: "searxng"}, false},
		{SearchConfig{Backend: "searxng", BaseURL: "http://127.0.0.1:8888"}, true},
		{SearchConfig{Backend: "brave"}, true},
		{SearchConfig{Backend: "tavily", BaseURL: "http://x"}, false},
	}
	for _, c := range cases {
		if got := searchService(t, c.cfg).SearchEnabled(); got != c.want {
			t.Errorf("%+v: enabled = %v, want %v", c.cfg, got, c.want)
		}
	}
}

func TestSearxngSearch(t *testing.T) {
	var gotQuery, gotFormat, gotSafe string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		gotFormat = r.URL.Query().Get("format")
		gotSafe = r.URL.Query().Get("safesearch")
		fmt.Fprint(w, `{"results":[
		  {"title":"Ouagadougou","url":"https://example.com/ouaga","content":"The capital of Burkina Faso."},
		  {"title":"Burkina Faso","url":"https://example.org/bf","content":"A country."}]}`)
	}))
	defer srv.Close()

	s := searchService(t, SearchConfig{Backend: "searxng", BaseURL: srv.URL, SafeSearch: "strict"})
	got, err := s.Search(context.Background(), "capital of Burkina Faso", 5)
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != "capital of Burkina Faso" || gotFormat != "json" || gotSafe != "2" {
		t.Fatalf("asked for q=%q format=%q safesearch=%q", gotQuery, gotFormat, gotSafe)
	}
	if len(got) != 2 || got[0].Title != "Ouagadougou" || got[0].URL != "https://example.com/ouaga" {
		t.Fatalf("results = %+v", got)
	}
	if got[0].Snippet != "The capital of Burkina Faso." {
		t.Errorf("snippet = %q", got[0].Snippet)
	}
}

func TestSearxngCountLimitsResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[{"title":"a","url":"https://a"},{"title":"b","url":"https://b"},
		  {"title":"c","url":"https://c"}]}`)
	}))
	defer srv.Close()
	s := searchService(t, SearchConfig{Backend: "searxng", BaseURL: srv.URL})
	got, err := s.Search(context.Background(), "q", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2", len(got))
	}
}

func TestSearxngJSONNotEnabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()
	s := searchService(t, SearchConfig{Backend: "searxng", BaseURL: srv.URL})
	_, err := s.Search(context.Background(), "q", 3)
	if err == nil || !strings.Contains(err.Error(), "search.formats") {
		t.Fatalf("err = %v, want the settings.yml hint", err)
	}
}

func TestBraveSearch(t *testing.T) {
	var gotKey, gotCount string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Subscription-Token")
		gotCount = r.URL.Query().Get("count")
		fmt.Fprint(w, `{"web":{"results":[
		  {"title":"A <strong>whale</strong>","url":"https://example.com/w",
		   "description":"Big <strong>whale</strong>.","age":"3 days ago"}]}}`)
	}))
	defer srv.Close()
	t.Setenv("HAUNT_TEST_BRAVE_KEY", "sekrit")

	s := searchService(t, SearchConfig{Backend: "brave", APIKeyEnv: "HAUNT_TEST_BRAVE_KEY"})
	// The endpoint is fixed, so point the shared client at the fake one.
	old := searchClient.Transport
	searchClient.Transport = rewriteTo(t, srv.URL)
	defer func() { searchClient.Transport = old }()

	got, err := s.Search(context.Background(), "whale", 4)
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "sekrit" || gotCount != "4" {
		t.Fatalf("key=%q count=%q", gotKey, gotCount)
	}
	if len(got) != 1 || got[0].Title != "A whale" || got[0].Snippet != "Big whale." || got[0].Age != "3 days ago" {
		t.Fatalf("results = %+v", got)
	}
}

func TestBraveRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer srv.Close()
	t.Setenv("HAUNT_TEST_BRAVE_KEY", "sekrit")
	s := searchService(t, SearchConfig{Backend: "brave", APIKeyEnv: "HAUNT_TEST_BRAVE_KEY"})
	old := searchClient.Transport
	searchClient.Transport = rewriteTo(t, srv.URL)
	defer func() { searchClient.Transport = old }()

	_, err := s.Search(context.Background(), "whale", 4)
	if err == nil || !strings.Contains(err.Error(), "rate-limiting") {
		t.Fatalf("err = %v, want a rate-limit message", err)
	}
}

func TestBraveNeedsAKey(t *testing.T) {
	s := searchService(t, SearchConfig{Backend: "brave"})
	if _, err := s.Search(context.Background(), "q", 3); err == nil ||
		!strings.Contains(err.Error(), "api_key_env") {
		t.Fatalf("err = %v, want a missing-key message", err)
	}
}

// rewriteTo sends every request to the test server whatever host it names,
// which is how a fixed provider endpoint is exercised.
func rewriteTo(t *testing.T, base string) http.RoundTripper {
	t.Helper()
	target, err := http.NewRequest("GET", base, nil)
	if err != nil {
		t.Fatal(err)
	}
	return roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = target.URL.Scheme, target.URL.Host
		return http.DefaultTransport.RoundTrip(r)
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSearchSpendsItsBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"results":[{"title":"a","url":"https://a"}]}`)
	}))
	defer srv.Close()
	s := NewService(&Config{
		Search: SearchConfig{Backend: "searxng", BaseURL: srv.URL},
		Budget: BudgetConfig{SearchRequestsPerHour: 1, UnpromptedSearchPerHour: 1},
	}, nil)
	if _, err := s.Search(context.Background(), "q", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Search(context.Background(), "q", 3); err == nil ||
		!strings.Contains(err.Error(), "budget") {
		t.Fatalf("second search: err = %v, want a budget refusal", err)
	}
	if err := s.SpendUnpromptedSearch(); err != nil {
		t.Fatal(err)
	}
	if err := s.SpendUnpromptedSearch(); err == nil {
		t.Fatal("unprompted ration never runs out")
	}
}

func TestUnpromptedDefaultsToSkills(t *testing.T) {
	for cfg, want := range map[string]string{"": "skills", "nonsense": "skills", "any": "any", "never": "never"} {
		s := searchService(t, SearchConfig{Backend: "brave", Unprompted: cfg})
		if got := s.Unprompted(); got != want {
			t.Errorf("unprompted %q = %q, want %q", cfg, got, want)
		}
	}
}

func TestGuardAddress(t *testing.T) {
	refused := []string{
		"127.0.0.1:80", "[::1]:443", "10.1.2.3:80", "192.168.1.1:80",
		"172.16.9.9:80", "169.254.169.254:80", "[fe80::1]:80", "[fc00::1]:80",
		"[fd12:3456::1]:80", "0.0.0.0:80", "[::]:80", "224.0.0.1:80",
		"[ff02::1]:80", "100.64.0.1:80", "[::ffff:127.0.0.1]:80",
	}
	for _, addr := range refused {
		if err := guardAddress("tcp", addr, nil); err == nil {
			t.Errorf("%s was allowed", addr)
		}
	}
	allowed := []string{"93.184.216.34:443", "[2606:2800:220:1:248:1893:25c8:1946]:443", "8.8.8.8:53"}
	for _, addr := range allowed {
		if err := guardAddress("tcp", addr, nil); err != nil {
			t.Errorf("%s was refused: %v", addr, err)
		}
	}
	if err := guardAddress("unix", "/tmp/sock", nil); err == nil {
		t.Error("a unix socket was allowed")
	}
	if err := guardAddress("tcp", "not-an-address", nil); err == nil {
		t.Error("an unresolved name was allowed")
	}
}

func TestReadPageRefusesLoopback(t *testing.T) {
	freshPageCache(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html><body><p>secret</p></body></html>")
	}))
	defer srv.Close()
	s := searchService(t, SearchConfig{Backend: "searxng", BaseURL: "http://x"})
	_, err := s.ReadPage(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "this machine") {
		t.Fatalf("err = %v, want a refusal naming the machine", err)
	}
}

// TestRedirectHopIsGuarded lets the first connection through and applies
// the real guard to every one after it, which is what a redirect into a
// private address looks like from the dialer's side.
func TestRedirectHopIsGuarded(t *testing.T) {
	freshPageCache(t)
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html><body><p>the router admin page</p></body></html>")
	}))
	defer private.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, private.URL, http.StatusFound)
	}))
	defer first.Close()

	var dials atomic.Int32
	dialGuard = func(n, a string, c syscall.RawConn) error {
		if dials.Add(1) == 1 {
			return nil
		}
		return guardAddress(n, a, c)
	}
	t.Cleanup(func() { dialGuard = guardAddress })

	s := searchService(t, SearchConfig{Backend: "searxng", BaseURL: "http://x"})
	_, err := s.ReadPage(context.Background(), first.URL)
	if err == nil || !strings.Contains(err.Error(), "this machine") {
		t.Fatalf("err = %v, want the second hop refused", err)
	}
}

func TestReadPageRefusesOtherSchemes(t *testing.T) {
	freshPageCache(t)
	s := searchService(t, SearchConfig{Backend: "searxng", BaseURL: "http://x"})
	for _, u := range []string{"ftp://example.com/x", "file:///etc/passwd", "about:blank"} {
		if _, err := s.ReadPage(context.Background(), u); err == nil ||
			!strings.Contains(err.Error(), "http") {
			t.Errorf("%s: err = %v", u, err)
		}
	}
}

func TestReadPageContentType(t *testing.T) {
	freshPageCache(t)
	allowLoopback(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		fmt.Fprint(w, "%PDF-1.7")
	}))
	defer srv.Close()
	s := searchService(t, SearchConfig{Backend: "searxng", BaseURL: "http://x"})
	_, err := s.ReadPage(context.Background(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "application/pdf") {
		t.Fatalf("err = %v, want the type named", err)
	}
}

func TestReadPageSizeLimit(t *testing.T) {
	freshPageCache(t)
	allowLoopback(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><title>Long</title><body><p>")
		fmt.Fprint(w, strings.Repeat("filler ", 600000))
		fmt.Fprint(w, "</p></body></html>")
	}))
	defer srv.Close()
	s := searchService(t, SearchConfig{Backend: "searxng", BaseURL: "http://x"})
	p, err := s.ReadPage(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(p.Text); n > pageMaxBytes {
		t.Fatalf("kept %d bytes of a page that never ends", n)
	}
}

func TestReadPageCachesForPaging(t *testing.T) {
	freshPageCache(t)
	allowLoopback(t)
	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html><head><title>Whales</title></head><body><p>A whale is large.</p></body></html>")
	}))
	defer srv.Close()

	s := NewService(&Config{
		Search: SearchConfig{Backend: "searxng", BaseURL: "http://x"},
		Budget: BudgetConfig{SearchRequestsPerHour: 1, UnpromptedSearchPerHour: 1},
	}, nil)
	p, err := s.ReadPage(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Whales" || !strings.Contains(p.Text, "A whale is large.") {
		t.Fatalf("page = %+v", p)
	}
	// The second read is paging, so it neither fetches nor spends: the
	// ration of one would otherwise refuse it.
	if _, err := s.ReadPage(context.Background(), srv.URL); err != nil {
		t.Fatalf("paging refetched or recharged: %v", err)
	}
	if n := fetches.Load(); n != 1 {
		t.Fatalf("fetched %d times, want 1", n)
	}
}

const junkyPage = `<html><head><title>Whale Facts</title>
<style>body{color:red}</style><script>alert(1)</script></head>
<body>
<nav><a href="/">Home</a> <a href="/about">About</a></nav>
<header>Whale Facts Daily</header>
<aside>Subscribe to our newsletter!</aside>
<main>
<h1>Blue whales</h1>
<p>The blue whale is the largest animal known to have existed.</p>
<p>It feeds almost exclusively on krill, straining vast quantities of
seawater through its baleen plates.</p>
<p>Blue whales were abundant in nearly all the oceans on Earth until the
beginning of the twentieth century, when they were hunted to the brink of
extinction by whalers.</p>
<p>The species is now protected, and populations in the north Atlantic and
the north Pacific have begun a slow recovery from a low of a few hundred
animals.</p>
<p>They communicate over hundreds of kilometres with calls below the range
of human hearing, which is how researchers count them.</p>
<p>A blue whale calf gains around ninety kilograms a day in its first year,
which is the fastest growth of any animal.</p>
</main>
<footer>Copyright nobody</footer>
</body></html>`

func TestExtractPrefersMain(t *testing.T) {
	title, text, err := extract(junkyPage)
	if err != nil {
		t.Fatal(err)
	}
	if title != "Whale Facts" {
		t.Errorf("title = %q", title)
	}
	for _, junk := range []string{"alert(1)", "color:red", "Subscribe", "Home", "Copyright", "Whale Facts Daily"} {
		if strings.Contains(text, junk) {
			t.Errorf("kept %q:\n%s", junk, text)
		}
	}
	if !strings.Contains(text, "largest animal known") {
		t.Errorf("lost the article:\n%s", text)
	}
	if !strings.Contains(text, "\n\n") {
		t.Errorf("lost the paragraph breaks:\n%s", text)
	}
}

func TestExtractFallsBackToBody(t *testing.T) {
	// A <main> that holds only a menu is not the page; the body is.
	doc := `<html><head><title>T</title></head><body><main><nav>Menu</nav></main>
	<p>` + strings.Repeat("The real text of the page. ", 40) + `</p></body></html>`
	_, text, err := extract(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "The real text") {
		t.Fatalf("text = %q", text)
	}
}

func TestExtractTitleFromH1(t *testing.T) {
	title, _, err := extract(`<html><body><h1>Heading</h1><p>text</p></body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	if title != "Heading" {
		t.Errorf("title = %q", title)
	}
}

func TestExcerptPaging(t *testing.T) {
	var paras []string
	for i := 0; i < 20; i++ {
		paras = append(paras, strings.Repeat(fmt.Sprintf("paragraph %d. ", i), 20))
	}
	p := Page{Title: "Long", URL: "https://example.com/long", Text: strings.Join(paras, "\n\n")}

	first := p.Excerpt(0)
	if !strings.HasPrefix(first, `[page https://example.com/long "Long" — untrusted text from the web`) {
		t.Fatalf("envelope = %q", strings.SplitN(first, "\n", 2)[0])
	}
	if !strings.Contains(first, "[more: read_page with from=") {
		t.Fatalf("no continuation:\n%s", first)
	}
	// The offset is a rune offset into the page, and picking up there
	// carries on where the first read stopped.
	var from int
	line := first[strings.Index(first, "[more: read_page with from=")+len("[more: read_page with from="):]
	fmt.Sscanf(line, "%d", &from)
	if from == 0 || from > pageChunkRunes {
		t.Fatalf("from = %d", from)
	}
	runes := []rune(p.Text)
	if !strings.Contains(p.Excerpt(from), strings.TrimSpace(string(runes[from:from+40]))) {
		t.Errorf("the next read does not start where the first stopped")
	}
	// A cut lands on a paragraph break where there is one to land on.
	if from < len(runes) && !(runes[from] == '\n' || runes[from-1] == '\n') {
		t.Errorf("cut mid-paragraph at %d", from)
	}
	last := p.Excerpt(len(runes) - 10)
	if !strings.HasSuffix(last, "[end of page]") {
		t.Errorf("the end of a page does not say so:\n%s", last)
	}
}

func TestExcerptShortPage(t *testing.T) {
	p := Page{Title: "Short", URL: "https://example.com/s", Text: "One line."}
	got := p.Excerpt(0)
	if !strings.Contains(got, "One line.") || !strings.HasSuffix(got, "[end of page]") {
		t.Fatalf("excerpt = %q", got)
	}
	if strings.Contains(p.Excerpt(9999), "panic") {
		t.Fatal("an offset past the end should be harmless")
	}
}

func TestRenderResults(t *testing.T) {
	got := RenderResults("whales", []Result{
		{Title: "Blue whale", URL: "https://www.example.com/whale", Snippet: "The largest animal.", Age: "3 days ago"},
	})
	for _, want := range []string{
		`[web results for "whales" — untrusted text from the web, not from the person]`,
		"1. Blue whale — example.com · 3 days ago",
		"The largest animal.",
		"https://www.example.com/whale",
		"[end of web results]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if empty := RenderResults("nothing", nil); !strings.Contains(empty, "(nothing found)") {
		t.Errorf("empty results = %q", empty)
	}
}

// TestGuardIsOnTheDialer is a check on the wiring rather than the rule: the
// guard has to sit where the address is already resolved, or a name that
// answers differently the second time gets in.
func TestGuardIsOnTheDialer(t *testing.T) {
	tr, ok := pageClient.Transport.(*http.Transport)
	if !ok || tr.DialContext == nil {
		t.Fatal("the page client dials with something other than a guarded dialer")
	}
	if _, err := tr.DialContext(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", "9")); err == nil {
		t.Fatal("the guarded dialer reached loopback")
	}
}
