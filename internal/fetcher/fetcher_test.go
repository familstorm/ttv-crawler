package fetcher

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

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
