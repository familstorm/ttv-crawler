package fetcher

import (
	"testing"
	"time"
)

func TestUserAgentToken(t *testing.T) {
	if got := userAgentToken("TTVPersonalArchiver/1.0 (+personal)"); got != "TTVPersonalArchiver" {
		t.Fatalf("token=%q", got)
	}
}

func TestParseRetryAfterSeconds(t *testing.T) {
	if got := parseRetryAfter("12"); got != 12*time.Second {
		t.Fatalf("retry=%v", got)
	}
}
