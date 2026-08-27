package embedder

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type limitedTestEmbedder struct {
	maxBytes int
	calls    []string
}

func (e *limitedTestEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	e.calls = append(e.calls, text)
	if e.maxBytes > 0 && len(text) > e.maxBytes {
		return nil, errors.New("message too long: input exceeds context window")
	}
	return []float32{1, 0}, nil
}

func (e *limitedTestEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	return e.Embed(ctx, text)
}

func (e *limitedTestEmbedder) Model() string { return "test" }

func (e *limitedTestEmbedder) Dims() int { return 2 }

func TestLimitedEmbeddingSplitsAndCombines(t *testing.T) {
	inner := &limitedTestEmbedder{maxBytes: 20}
	limited := NewLimited(inner, 20)
	text := "첫 문장입니다. 둘째 문장입니다.\n\n세 번째 문장입니다."
	vec, err := limited.Embed(context.Background(), text)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(inner.calls) < 2 {
		t.Fatalf("underlying calls = %d, want multiple chunks", len(inner.calls))
	}
	for _, call := range inner.calls {
		if len(call) > 20 {
			t.Fatalf("chunk size = %d, want <= 20: %q", len(call), call)
		}
	}
	if vec[0] <= 0 || vec[1] != 0 {
		t.Fatalf("combined vector = %#v", vec)
	}
}

func TestLimitedEmbeddingAdaptsWhenLimitUnknown(t *testing.T) {
	inner := &limitedTestEmbedder{maxBytes: 16}
	limited := NewLimited(inner, 0)
	text := strings.Repeat("문장. ", 20)
	if _, err := limited.Embed(context.Background(), text); err != nil {
		t.Fatalf("adaptive Embed: %v", err)
	}
	if len(inner.calls) < 2 {
		t.Fatalf("underlying calls = %d, want adaptive retry", len(inner.calls))
	}
}
