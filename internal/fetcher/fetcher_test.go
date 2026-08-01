package fetcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/familstorm/crawler-truyen-ttv/internal/robots"
)

// stubRobots returns a canned decision so the gate can be tested without a
// network round trip or a browser.
type stubRobots struct {
	decision robots.Decision
	err      error
	calls    int
}

func (s *stubRobots) Check(context.Context, string) (robots.Decision, error) {
	s.calls++
	return s.decision, s.err
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestParseRetryAfterSeconds(t *testing.T) {
	if got := parseRetryAfter("12"); got != 12*time.Second {
		t.Fatalf("retry=%v", got)
	}
}

func TestFetchWithRetryStopsImmediatelyWhenCallerIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	f := &Fetcher{
		limiter: newLimiter(0, 0),
		retries: 3,
	}
	fetchAttempt := func(context.Context, string) (Result, time.Duration, error) {
		attempts++
		cancel()
		return Result{}, 0, fmt.Errorf("Chromium điều hướng: %w", context.Canceled)
	}

	_, err := f.fetchWithRetryUsing(ctx, "https://tangthuvien.org/test", fetchAttempt)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestFetchStopsWhenRobotsDisallows(t *testing.T) {
	stub := &stubRobots{decision: robots.Decision{Allowed: false, Reason: "disallowed by robots.txt rule \"/truyen\""}}
	f := &Fetcher{
		limiter: newLimiter(0, 0),
		retries: 3,
		robots:  stub,
		logger:  testLogger(),
	}

	_, err := f.Fetch(context.Background(), "https://tangthuvien.org/truyen")
	if !errors.Is(err, ErrRobotsDisallowed) {
		t.Fatalf("error = %v, want ErrRobotsDisallowed", err)
	}
	if stub.calls != 1 {
		t.Fatalf("robots checked %d times, want 1", stub.calls)
	}
}

func TestFetchFailsClosedWhenRobotsCheckErrors(t *testing.T) {
	stub := &stubRobots{err: errors.New("connection refused")}
	f := &Fetcher{
		limiter: newLimiter(0, 0),
		retries: 3,
		robots:  stub,
		logger:  testLogger(),
	}

	_, err := f.Fetch(context.Background(), "https://tangthuvien.org/truyen")
	if !errors.Is(err, ErrRobotsDisallowed) {
		t.Fatalf("error = %v, want a fail-closed ErrRobotsDisallowed", err)
	}
}

func TestFetchRejectsForeignHostBeforeConsultingRobots(t *testing.T) {
	stub := &stubRobots{decision: robots.Decision{Allowed: true}}
	f := &Fetcher{
		limiter: newLimiter(0, 0),
		retries: 3,
		robots:  stub,
		logger:  testLogger(),
	}

	if _, err := f.Fetch(context.Background(), "https://example.com/truyen"); err == nil {
		t.Fatal("expected an off-domain request to be refused")
	}
	if stub.calls != 0 {
		t.Fatalf("robots checked %d times for an off-domain URL, want 0", stub.calls)
	}
}

func TestCrawlDelayRaisesLimiterInterval(t *testing.T) {
	stub := &stubRobots{decision: robots.Decision{Allowed: true, CrawlDelay: 7 * time.Second}}
	f := &Fetcher{
		limiter: newLimiter(3*time.Second, 0),
		retries: 1,
		robots:  stub,
		logger:  testLogger(),
	}

	if err := f.checkRobots(context.Background(), "https://tangthuvien.org/truyen"); err != nil {
		t.Fatalf("checkRobots returned error: %v", err)
	}
	f.limiter.mu.Lock()
	interval := f.limiter.interval
	f.limiter.mu.Unlock()
	if interval != 7*time.Second {
		t.Errorf("limiter interval = %v, want 7s (Crawl-delay wins when longer)", interval)
	}
}

func TestCrawlDelayNeverLowersConfiguredInterval(t *testing.T) {
	stub := &stubRobots{decision: robots.Decision{Allowed: true, CrawlDelay: time.Second}}
	f := &Fetcher{
		limiter: newLimiter(5*time.Second, 0),
		retries: 1,
		robots:  stub,
		logger:  testLogger(),
	}

	if err := f.checkRobots(context.Background(), "https://tangthuvien.org/truyen"); err != nil {
		t.Fatalf("checkRobots returned error: %v", err)
	}
	f.limiter.mu.Lock()
	interval := f.limiter.interval
	f.limiter.mu.Unlock()
	if interval != 5*time.Second {
		t.Errorf("limiter interval = %v, want the configured 5s to hold", interval)
	}
}
