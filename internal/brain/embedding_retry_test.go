package brain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEmbeddingRetryDelayUsesExponentialBackoffWithCap(t *testing.T) {
	base := time.Minute
	maximum := 10 * time.Minute
	tests := []struct {
		attempts int
		want     time.Duration
	}{
		{attempts: 1, want: time.Minute},
		{attempts: 2, want: 2 * time.Minute},
		{attempts: 3, want: 4 * time.Minute},
		{attempts: 4, want: 8 * time.Minute},
		{attempts: 5, want: 10 * time.Minute},
		{attempts: 20, want: 10 * time.Minute},
	}
	for _, test := range tests {
		if got := embeddingRetryDelay(base, maximum, test.attempts); got != test.want {
			t.Fatalf("attempt %d delay = %s, want %s", test.attempts, got, test.want)
		}
	}
}

func TestEmbeddingErrorTextIsBounded(t *testing.T) {
	input := strings.Repeat("오", embeddingErrorRuneLimit+100)
	got := embeddingErrorText(errors.New(input))
	if len([]rune(got)) != embeddingErrorRuneLimit {
		t.Fatalf("error rune length = %d, want %d", len([]rune(got)), embeddingErrorRuneLimit)
	}
}
