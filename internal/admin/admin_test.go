package admin

import (
	"testing"
	"time"
)

func TestFormatTimeUsesUTCPlusSeven(t *testing.T) {
	t.Parallel()

	input := time.Date(2026, time.July, 18, 5, 32, 0, 0, time.UTC)
	if got, want := formatTime(input), "18/07/2026 12:32"; got != want {
		t.Fatalf("formatTime() = %q, want %q", got, want)
	}
}

func TestFormatTimeDoesNotDependOnInputLocation(t *testing.T) {
	t.Parallel()

	input := time.Date(2026, time.July, 18, 0, 0, 0, 0, time.FixedZone("UTC-4", -4*60*60))
	if got, want := formatTime(input), "18/07/2026 11:00"; got != want {
		t.Fatalf("formatTime() = %q, want %q", got, want)
	}
}

func TestFormatTimeReturnsDashForZeroValue(t *testing.T) {
	t.Parallel()

	if got, want := formatTime(time.Time{}), "—"; got != want {
		t.Fatalf("formatTime() = %q, want %q", got, want)
	}
}
