package embedder

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/alash3al/stash/internal/observability"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

// e5 계열 모델은 비대칭 검색용으로 학습돼 있어, 저장 문서에는 "passage: ",
// 검색 질의에는 "query: " 프리픽스를 붙여야 학습 분포와 맞는다.
// 프리픽스를 생략하면 짧은 질의와 긴 문서가 서로 다른 영역에 놓여 정확도가 떨어진다.
// 다른 모델(text-embedding-3 등)은 대칭이라 프리픽스를 붙이면 오히려 해가 되므로
// 모델 이름으로 판별해 e5 일 때만 적용한다.
func needsE5Prefix(model string) bool {
	return strings.Contains(strings.ToLower(model), "e5")
}

// OpenAI uses the OpenAI-compatible SDK to generate embeddings.
// Works with any OpenAI-compatible endpoint: api.openai.com,
// openrouter.ai, local Ollama, Together, vLLM, etc.
// The model string is passed as-is to the API — no stripping or
// transformation. Use the format your endpoint expects:
//
//	OpenRouter:    "openai/text-embedding-3-small"
//	OpenAI direct: "text-embedding-3-small"
//	Ollama:        "nomic-embed-text"
type OpenAI struct {
	client openai.Client
	model  string
	dims   int
}

const defaultRequestTimeout = 2 * time.Minute

// NewOpenAI creates an OpenAI embedder.
// baseURL: the API endpoint (e.g. "https://openrouter.ai/api/v1")
// apiKey:  the API key for the endpoint; empty for endpoints without authentication
// model:   required — the model string for this endpoint (no default)
// dims:    required — the vector dimension for this model (no default)
// Returns error if model is empty or dims <= 0.
func NewOpenAI(baseURL, apiKey, model string, dims int) (*OpenAI, error) {
	return NewOpenAIWithTimeout(baseURL, apiKey, model, dims, defaultRequestTimeout)
}

// NewOpenAIWithTimeout creates an OpenAI-compatible embedder with a bounded
// request attempt. An explicit timeout is important for local endpoints too:
// a dead model server must leave the memory row pending instead of holding a
// request forever.
func NewOpenAIWithTimeout(baseURL, apiKey, model string, dims int, requestTimeout time.Duration) (*OpenAI, error) {
	return NewOpenAIWithTimeoutAndLogger(baseURL, apiKey, model, dims, requestTimeout, nil)
}

// NewOpenAIWithTimeoutAndLogger creates an OpenAI-compatible embedder and
// records each provider request when logger is non-nil. Payloads and
// credentials are never included in those records.
func NewOpenAIWithTimeoutAndLogger(baseURL, apiKey, model string, dims int, requestTimeout time.Duration, logger *slog.Logger) (*OpenAI, error) {
	if model == "" {
		return nil, errors.New("embedder: model is required")
	}
	if dims <= 0 {
		return nil, errors.New("embedder: dims must be greater than zero")
	}
	if requestTimeout <= 0 {
		return nil, errors.New("embedder: request timeout must be greater than zero")
	}

	transport := http.RoundTripper(http.DefaultTransport)
	if logger != nil {
		transport = observability.NewLoggingRoundTripper(logger, transport, "embedding", model)
	}
	options := []option.RequestOption{
		option.WithBaseURL(baseURL),
		option.WithRequestTimeout(requestTimeout),
		// Keep a transport-level deadline as well. Some compatible transports do
		// not propagate the SDK's per-attempt context while waiting for headers.
		option.WithHTTPClient(&http.Client{Timeout: requestTimeout, Transport: transport}),
	}
	if strings.TrimSpace(apiKey) != "" {
		options = append(options, option.WithAPIKey(apiKey))
	}
	client := openai.NewClient(options...)

	return &OpenAI{
		client: client,
		model:  model,
		dims:   dims,
	}, nil
}

// Model returns the model string as passed at construction.
func (o *OpenAI) Model() string {
	return o.model
}

// Dims returns the vector dimensions as passed at construction.
func (o *OpenAI) Dims() int {
	return o.dims
}

// Embed generates a vector embedding for a stored passage.
func (o *OpenAI) Embed(ctx context.Context, text string) ([]float32, error) {
	if needsE5Prefix(o.model) {
		text = "passage: " + text
	}
	return o.embed(ctx, text)
}

// EmbedQuery generates a vector embedding for a search query.
func (o *OpenAI) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	if needsE5Prefix(o.model) {
		text = "query: " + text
	}
	return o.embed(ctx, text)
}

func (o *OpenAI) embed(ctx context.Context, text string) ([]float32, error) {
	resp, err := o.client.Embeddings.New(ctx, openai.EmbeddingNewParams{
		Input: openai.EmbeddingNewParamsInputUnion{
			OfArrayOfStrings: []string{text},
		},
		Model:      o.model,
		Dimensions: openai.Int(int64(o.dims)),
	})
	if err != nil {
		return nil, fmt.Errorf("embedding request for model %q failed: %w", o.model, err)
	}
	if len(resp.Data) == 0 {
		return nil, errors.New("embedder: endpoint returned no embedding")
	}

	embedding := resp.Data[0].Embedding
	if len(embedding) != o.dims {
		return nil, errors.New("embedder: endpoint returned an embedding with unexpected dimensions")
	}
	vec := make([]float32, len(embedding))
	for i := range embedding {
		vec[i] = float32(embedding[i])
	}
	return vec, nil
}
