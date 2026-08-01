// Package robots implements a RFC 9309 robots.txt client with a per-host
// cache.
//
// The crawler consults this package before every document request. Two
// behaviours are deliberate and worth stating up front:
//
//   - Fail closed. If robots.txt cannot be retrieved (network error, timeout,
//     or 5xx), the host is treated as fully disallowed until the negative cache
//     entry expires. A crawler that cannot read the rules must not guess.
//   - A 404/410 is not a failure. Per RFC 9309 an "unavailable" status means the
//     origin has published no restrictions, so everything is allowed.
package robots

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultCacheTTL is how long a successfully parsed robots.txt is reused.
const DefaultCacheTTL = 12 * time.Hour

// DefaultFailureTTL is how long a fetch failure keeps a host disallowed before
// the crawler tries robots.txt again. Kept short so a transient outage does not
// stall a run for hours.
const DefaultFailureTTL = 5 * time.Minute

// DefaultMaxBytes caps the robots.txt body. RFC 9309 requires parsers to honour
// at least 500 KiB.
const DefaultMaxBytes int64 = 512 * 1024

// Decision is the result of evaluating a URL against a host's robots.txt.
type Decision struct {
	// Allowed reports whether the URL may be requested.
	Allowed bool
	// CrawlDelay is the Crawl-delay advertised for the matching group, or zero
	// when the host advertises none.
	CrawlDelay time.Duration
	// Reason explains a denial in a form suitable for logs and job errors.
	Reason string
}

// Config configures a Client. The zero value of each field falls back to a
// documented default.
type Config struct {
	// UserAgent is the full User-Agent header sent when fetching robots.txt.
	// Its leading product token is also the name matched against groups.
	UserAgent string
	// Timeout bounds a single robots.txt request.
	Timeout time.Duration
	// CacheTTL overrides DefaultCacheTTL.
	CacheTTL time.Duration
	// FailureTTL overrides DefaultFailureTTL.
	FailureTTL time.Duration
	// MaxBytes overrides DefaultMaxBytes.
	MaxBytes int64
	// HTTPClient allows tests to inject a transport.
	HTTPClient *http.Client
}

// Client fetches, caches and evaluates robots.txt per host.
type Client struct {
	httpClient *http.Client
	userAgent  string
	token      string
	cacheTTL   time.Duration
	failureTTL time.Duration
	maxBytes   int64

	mu    sync.Mutex
	hosts map[string]*cacheEntry
}

type cacheEntry struct {
	// once guards a single in-flight fetch per host so a burst of workers
	// produces one robots.txt request rather than N.
	once      sync.Once
	ready     chan struct{}
	rules     *ruleSet
	fetchErr  error
	expiresAt time.Time
}

// New builds a Client. It never fails; misconfiguration falls back to defaults.
func New(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	cacheTTL := cfg.CacheTTL
	if cacheTTL <= 0 {
		cacheTTL = DefaultCacheTTL
	}
	failureTTL := cfg.FailureTTL
	if failureTTL <= 0 {
		failureTTL = DefaultFailureTTL
	}
	maxBytes := cfg.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	userAgent := strings.TrimSpace(cfg.UserAgent)
	if userAgent == "" {
		userAgent = "Go-http-client/1.1"
	}
	return &Client{
		httpClient: httpClient,
		userAgent:  userAgent,
		token:      productToken(userAgent),
		cacheTTL:   cacheTTL,
		failureTTL: failureTTL,
		maxBytes:   maxBytes,
		hosts:      make(map[string]*cacheEntry),
	}
}

// Check evaluates target against the robots.txt of its host, fetching and
// caching that file on first use.
func (c *Client) Check(ctx context.Context, target string) (Decision, error) {
	parsed, err := url.Parse(target)
	if err != nil {
		return Decision{}, fmt.Errorf("invalid URL %q: %w", target, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return Decision{}, fmt.Errorf("unsupported scheme %q", parsed.Scheme)
	}

	rules, err := c.rulesFor(ctx, parsed)
	if err != nil {
		// A cancelled caller is a shutdown, not a robots verdict. Report it as
		// an error so the crawler releases the job instead of recording it as
		// disallowed.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Decision{}, ctxErr
		}
		// Fail closed: an unreadable robots.txt disallows the whole host.
		return Decision{
			Allowed: false,
			Reason:  fmt.Sprintf("robots.txt for %s could not be read: %v", parsed.Host, err),
		}, nil
	}

	group, agent := rules.group(c.token)
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	if parsed.RawQuery != "" {
		path += "?" + parsed.RawQuery
	}

	allowed, pattern := group.allows(path)
	decision := Decision{Allowed: allowed, CrawlDelay: group.crawlDelay}
	if !allowed {
		decision.Reason = fmt.Sprintf("disallowed by robots.txt rule %q for user-agent %q", pattern, agent)
	}
	return decision, nil
}

// rulesFor returns the cached rule set for a host, fetching it when the cache
// is cold or stale.
func (c *Client) rulesFor(ctx context.Context, u *url.URL) (*ruleSet, error) {
	key := u.Scheme + "://" + u.Host

	c.mu.Lock()
	entry, ok := c.hosts[key]
	if ok && time.Now().After(entry.expiresAt) {
		// Stale: drop it so the next caller refetches.
		ok = false
		delete(c.hosts, key)
	}
	if !ok {
		entry = &cacheEntry{ready: make(chan struct{})}
		c.hosts[key] = entry
	}
	c.mu.Unlock()

	entry.once.Do(func() {
		rules, err := c.fetch(ctx, key+"/robots.txt")
		entry.rules, entry.fetchErr = rules, err
		ttl := c.cacheTTL
		if err != nil {
			ttl = c.failureTTL
		}
		entry.expiresAt = time.Now().Add(ttl)
		close(entry.ready)

		// A fetch aborted by the caller's cancellation says nothing about the
		// host, so drop the entry rather than letting a shutdown disallow it
		// for the whole failure TTL.
		if err != nil && ctx.Err() != nil {
			c.mu.Lock()
			if c.hosts[key] == entry {
				delete(c.hosts, key)
			}
			c.mu.Unlock()
		}
	})

	select {
	case <-entry.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return entry.rules, entry.fetchErr
}

// fetch retrieves and parses a robots.txt. A 4xx is reported as an empty
// (allow-all) rule set; a 5xx or transport error is reported as an error so the
// caller can fail closed.
func (c *Client) fetch(ctx context.Context, robotsURL string) (*ruleSet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "text/plain,*/*;q=0.8")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, c.maxBytes))
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return parse(io.LimitReader(resp.Body, c.maxBytes)), nil
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		// RFC 9309 section 2.3.1.4: unavailable means "no restrictions".
		return &ruleSet{}, nil
	default:
		return nil, fmt.Errorf("robots.txt returned HTTP %d", resp.StatusCode)
	}
}

// productToken extracts the leading product name from a User-Agent header,
// lowercased. "TTVPersonalArchiver/1.0 (+notes)" becomes "ttvpersonalarchiver".
func productToken(userAgent string) string {
	token := strings.TrimSpace(userAgent)
	if index := strings.IndexAny(token, " \t"); index >= 0 {
		token = token[:index]
	}
	if index := strings.Index(token, "/"); index >= 0 {
		token = token[:index]
	}
	return strings.ToLower(token)
}

// group holds the directives that apply to one set of user-agent names.
type group struct {
	agents     []string
	rules      []rule
	crawlDelay time.Duration
}

type rule struct {
	pattern string
	allow   bool
}

type ruleSet struct {
	groups []*group
}

// group selects the most specific group for token, preferring the longest
// matching user-agent name and falling back to the wildcard group. It returns
// the group together with the agent name that matched, and never mutates the
// rule set: a parsed robots.txt is cached and read by every worker at once.
func (r *ruleSet) group(token string) (*group, string) {
	var best *group
	var bestAgent string
	var wildcard *group
	for _, candidate := range r.groups {
		for _, agent := range candidate.agents {
			if agent == "*" {
				if wildcard == nil {
					wildcard = candidate
				}
				continue
			}
			// RFC 9309: a group matches when its name is a prefix of the
			// crawler's product token.
			if !strings.HasPrefix(token, agent) {
				continue
			}
			if best == nil || len(agent) > len(bestAgent) {
				best = candidate
				bestAgent = agent
			}
		}
	}
	if best != nil {
		return best, bestAgent
	}
	if wildcard != nil {
		return wildcard, "*"
	}
	return &group{}, "*"
}

// allows applies the longest-match rule. Ties resolve to allow, per RFC 9309.
func (g *group) allows(path string) (bool, string) {
	bestLen := -1
	bestAllow := true
	bestPattern := ""
	for _, candidate := range g.rules {
		if !matchPattern(candidate.pattern, path) {
			continue
		}
		length := len(candidate.pattern)
		if length > bestLen || (length == bestLen && candidate.allow) {
			bestLen = length
			bestAllow = candidate.allow
			bestPattern = candidate.pattern
		}
	}
	if bestLen < 0 {
		return true, ""
	}
	return bestAllow, bestPattern
}

// matchPattern implements robots.txt path matching: "*" spans any run of
// characters and a trailing "$" anchors the end of the path.
func matchPattern(pattern, path string) bool {
	if pattern == "" {
		return false
	}
	anchored := strings.HasSuffix(pattern, "$")
	if anchored {
		pattern = strings.TrimSuffix(pattern, "$")
	}
	segments := strings.Split(pattern, "*")

	pos := 0
	for i, segment := range segments {
		if segment == "" {
			continue
		}
		if i == 0 {
			// The first segment must sit at the start of the path.
			if !strings.HasPrefix(path[pos:], segment) {
				return false
			}
			pos += len(segment)
			continue
		}
		index := strings.Index(path[pos:], segment)
		if index < 0 {
			return false
		}
		pos += index + len(segment)
	}

	if !anchored {
		return true
	}
	// With "$" the final segment must land exactly on the end of the path.
	last := segments[len(segments)-1]
	if last == "" {
		return true
	}
	return strings.HasSuffix(path, last) && pos == len(path)
}

// parse reads a robots.txt body into groups. Unknown fields are ignored, which
// is what the spec requires of a conforming parser.
func parse(body io.Reader) *ruleSet {
	set := &ruleSet{}
	var current *group
	// startingGroup tracks whether the previous line was a user-agent, so that
	// consecutive user-agent lines accumulate into one group.
	startingGroup := false

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if index := strings.IndexByte(line, '#'); index >= 0 {
			line = line[:index]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		field = strings.ToLower(strings.TrimSpace(field))
		value = strings.TrimSpace(value)

		switch field {
		case "user-agent":
			if value == "" {
				continue
			}
			if current == nil || !startingGroup {
				current = &group{}
				set.groups = append(set.groups, current)
			}
			current.agents = append(current.agents, strings.ToLower(value))
			startingGroup = true
		case "disallow", "allow":
			if current == nil {
				continue
			}
			startingGroup = false
			// An empty Disallow means "allow everything" and carries no rule.
			// An empty Allow is meaningless. Either way there is nothing to add.
			if value == "" {
				continue
			}
			current.rules = append(current.rules, rule{pattern: value, allow: field == "allow"})
		case "crawl-delay":
			if current == nil {
				continue
			}
			startingGroup = false
			if seconds, err := strconv.ParseFloat(value, 64); err == nil && seconds > 0 {
				current.crawlDelay = time.Duration(seconds * float64(time.Second))
			}
		default:
			// Sitemap and any vendor extension: ignored, and does not end a group.
		}
	}
	return set
}
