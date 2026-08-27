package embedder

import (
	"context"
	"fmt"
	"math"

	"github.com/alash3al/stash/internal/textbudget"
)

const maxAdaptiveEmbeddingSplits = 8

// Limited keeps embedding input inside the configured model window. Long
// passages are embedded in natural UTF-8 chunks and combined into one vector;
// this preserves the existing one-row-per-memory schema while avoiding a
// provider-side context error. If no limit is configured, a context error is
// retried with progressively smaller chunks so a model can still protect
// itself when its limit is not published by the endpoint.
type Limited struct {
	inner         Embedder
	maxInputBytes int
}

// NewLimited wraps an embedder with a model input token budget. The conversion
// to bytes is deliberately conservative because the OpenAI-compatible API may
// use any tokenizer.
func NewLimited(inner Embedder, maxInputTokens int) *Limited {
	return &Limited{
		inner:         inner,
		maxInputBytes: textbudget.BytesForTokens(maxInputTokens),
	}
}

func (l *Limited) Embed(ctx context.Context, text string) ([]float32, error) {
	return l.embed(ctx, text, l.inner.Embed, 0)
}

func (l *Limited) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	return l.embed(ctx, text, l.inner.EmbedQuery, 0)
}

func (l *Limited) embed(ctx context.Context, text string, call func(context.Context, string) ([]float32, error), depth int) ([]float32, error) {
	parts := textbudget.SplitText(text, l.maxInputBytes)
	if len(parts) == 1 {
		vec, err := call(ctx, text)
		if err == nil {
			return vec, nil
		}
		if !textbudget.IsContextLimitError(err) || len(text) < 2 || depth >= maxAdaptiveEmbeddingSplits {
			return nil, err
		}
		// The endpoint did not publish a usable limit. Halve the text and let
		// the same natural-boundary splitter keep the pieces readable.
		parts = textbudget.SplitText(text, len(text)/2)
		if len(parts) <= 1 {
			return nil, err
		}
	}

	return l.combine(ctx, parts, call, depth)
}

func (l *Limited) combine(ctx context.Context, parts []string, call func(context.Context, string) ([]float32, error), depth int) ([]float32, error) {
	var sum []float32
	var totalWeight float64
	for i, part := range parts {
		vec, err := l.embed(ctx, part, call, depth+1)
		if err != nil {
			return nil, fmt.Errorf("embed chunk %d/%d: %w", i+1, len(parts), err)
		}
		if len(vec) != l.Dims() {
			return nil, fmt.Errorf("embed chunk %d/%d returned %d dimensions, want %d", i+1, len(parts), len(vec), l.Dims())
		}
		weight := float64(len(part))
		if weight == 0 {
			weight = 1
		}
		if sum == nil {
			sum = make([]float32, len(vec))
		}
		for j, value := range vec {
			sum[j] += float32(float64(value) * weight)
		}
		totalWeight += weight
	}
	if len(sum) == 0 || totalWeight == 0 {
		return nil, fmt.Errorf("embedder: no vectors returned for chunks")
	}

	var norm float64
	for i := range sum {
		sum[i] = float32(float64(sum[i]) / totalWeight)
		norm += float64(sum[i]) * float64(sum[i])
	}
	if norm > 0 {
		norm = math.Sqrt(norm)
		for i := range sum {
			sum[i] = float32(float64(sum[i]) / norm)
		}
	}
	return sum, nil
}

func (l *Limited) Model() string { return l.inner.Model() }

func (l *Limited) Dims() int { return l.inner.Dims() }

// MaxInputBytes exposes the effective conservative budget for diagnostics and
// tests. Zero means the endpoint's limit is unknown and adaptive splitting is
// enabled only after a context error.
func (l *Limited) MaxInputBytes() int { return l.maxInputBytes }
