package robots

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(handler http.Handler) (*Client, *httptest.Server) {
	server := httptest.NewServer(handler)
	client := New(Config{
		UserAgent:  "TTVPersonalArchiver/1.0 (+personal offline reading)",
		HTTPClient: server.Client(),
	})
	return client, server
}

func serveRobots(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(body))
	})
}

func TestCheckAllowsAndDisallows(t *testing.T) {
	client, server := newTestClient(serveRobots(strings.Join([]string{
		"User-agent: *",
		"Disallow: /admin",
		"Disallow: /private/",
		"Allow: /private/public",
		"Crawl-delay: 2",
	}, "\n")))
	defer server.Close()

	cases := []struct {
		path        string
		wantAllowed bool
	}{
		{"/truyen", true},
		{"/admin", false},
		{"/admin/users", false},
		{"/private/secret", false},
		{"/private/public/page", true},
	}
	for _, tc := range cases {
		decision, err := client.Check(context.Background(), server.URL+tc.path)
		if err != nil {
			t.Fatalf("Check(%q) returned error: %v", tc.path, err)
		}
		if decision.Allowed != tc.wantAllowed {
			t.Errorf("Check(%q) allowed = %v, want %v (reason: %s)", tc.path, decision.Allowed, tc.wantAllowed, decision.Reason)
		}
	}

	decision, err := client.Check(context.Background(), server.URL+"/truyen")
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if decision.CrawlDelay != 2*time.Second {
		t.Errorf("CrawlDelay = %v, want 2s", decision.CrawlDelay)
	}
}

func TestCheckPrefersMostSpecificUserAgent(t *testing.T) {
	client, server := newTestClient(serveRobots(strings.Join([]string{
		"User-agent: *",
		"Disallow: /",
		"",
		"User-agent: TTVPersonalArchiver",
		"Disallow: /admin",
		"Crawl-delay: 5",
	}, "\n")))
	defer server.Close()

	decision, err := client.Check(context.Background(), server.URL+"/truyen")
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if !decision.Allowed {
		t.Fatalf("expected the named group to win over the wildcard, got denial: %s", decision.Reason)
	}
	if decision.CrawlDelay != 5*time.Second {
		t.Errorf("CrawlDelay = %v, want 5s", decision.CrawlDelay)
	}
}

func TestCheckFailsClosedOnServerError(t *testing.T) {
	client, server := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	decision, err := client.Check(context.Background(), server.URL+"/truyen")
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if decision.Allowed {
		t.Fatal("expected fail-closed denial when robots.txt returns 500")
	}
	if !strings.Contains(decision.Reason, "could not be read") {
		t.Errorf("Reason = %q, want it to explain the fetch failure", decision.Reason)
	}
}

func TestCheckFailsClosedWhenHostUnreachable(t *testing.T) {
	client, server := newTestClient(serveRobots(""))
	url := server.URL
	server.Close() // Nothing is listening any more.

	decision, err := client.Check(context.Background(), url+"/truyen")
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if decision.Allowed {
		t.Fatal("expected fail-closed denial when the host is unreachable")
	}
}

func TestCheckAllowsWhenRobotsMissing(t *testing.T) {
	client, server := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	decision, err := client.Check(context.Background(), server.URL+"/truyen")
	if err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if !decision.Allowed {
		t.Fatalf("a 404 robots.txt means no restrictions, got denial: %s", decision.Reason)
	}
}

func TestCachePerHostIssuesOneRequest(t *testing.T) {
	var hits int64
	client, server := newTestClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			atomic.AddInt64(&hits, 1)
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /admin\n"))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	for i := 0; i < 5; i++ {
		if _, err := client.Check(context.Background(), server.URL+"/truyen"); err != nil {
			t.Fatalf("Check returned error: %v", err)
		}
	}
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Errorf("robots.txt fetched %d times, want 1 (cache miss)", got)
	}
}

func TestCacheExpiryRefetches(t *testing.T) {
	var hits int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			atomic.AddInt64(&hits, 1)
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /admin\n"))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	client := New(Config{
		UserAgent:  "TTVPersonalArchiver/1.0",
		HTTPClient: server.Client(),
		CacheTTL:   10 * time.Millisecond,
	})

	if _, err := client.Check(context.Background(), server.URL+"/truyen"); err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	time.Sleep(25 * time.Millisecond)
	if _, err := client.Check(context.Background(), server.URL+"/truyen"); err != nil {
		t.Fatalf("Check returned error: %v", err)
	}
	if got := atomic.LoadInt64(&hits); got != 2 {
		t.Errorf("robots.txt fetched %d times, want 2 after TTL expiry", got)
	}
}

func TestMatchPattern(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"/admin", "/admin", true},
		{"/admin", "/admin/users", true},
		{"/admin", "/administrator", true},
		{"/admin", "/user", false},
		{"/*.pdf$", "/files/report.pdf", true},
		{"/*.pdf$", "/files/report.pdf.html", false},
		{"/search?", "/search?q=1", true},
		{"/a/*/b", "/a/x/b", true},
		{"/a/*/b", "/a/b", false},
		{"/$", "/", true},
		{"/$", "/truyen", false},
	}
	for _, tc := range cases {
		if got := matchPattern(tc.pattern, tc.path); got != tc.want {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

func TestProductToken(t *testing.T) {
	cases := map[string]string{
		"TTVPersonalArchiver/1.0 (+personal offline reading)": "ttvpersonalarchiver",
		"SomeBot":       "somebot",
		"Other/2.1":     "other",
		"  Spaced/1.0 ": "spaced",
	}
	for input, want := range cases {
		if got := productToken(input); got != want {
			t.Errorf("productToken(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestParseIgnoresUnknownFieldsAndComments(t *testing.T) {
	set := parse(strings.NewReader(strings.Join([]string{
		"# a comment",
		"Sitemap: https://example.com/sitemap.xml",
		"User-agent: *   # trailing comment",
		"Disallow: /admin",
		"Unknown-field: whatever",
		"Crawl-delay: 1.5",
	}, "\n")))

	group, _ := set.group("somebot")
	if len(group.rules) != 1 || group.rules[0].pattern != "/admin" {
		t.Fatalf("rules = %+v, want a single /admin disallow", group.rules)
	}
	if group.crawlDelay != 1500*time.Millisecond {
		t.Errorf("crawlDelay = %v, want 1.5s", group.crawlDelay)
	}
}

func TestEmptyDisallowAllowsEverything(t *testing.T) {
	set := parse(strings.NewReader("User-agent: *\nDisallow:\n"))
	group, _ := set.group("somebot")
	if allowed, _ := group.allows("/anything"); !allowed {
		t.Error("an empty Disallow must not restrict anything")
	}
}
