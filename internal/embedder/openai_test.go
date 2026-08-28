package embedder

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewOpenAIAllowsEmptyAPIKey(t *testing.T) {
	got, err := NewOpenAI("http://localhost:1234/v1", "", "local-embedding", 3)
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	if got.Model() != "local-embedding" || got.Dims() != 3 {
		t.Fatalf("embedder metadata = (%q, %d)", got.Model(), got.Dims())
	}
}

func TestOpenAIRequestTimeoutStopsUnavailableProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		// Do not write a response before the client's request deadline. The
		// handler returns shortly afterwards so httptest.Server.Close can finish.
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	client, err := NewOpenAIWithTimeout(server.URL+"/v1", "", "local-embedding", 3, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("NewOpenAIWithTimeout: %v", err)
	}
	started := time.Now()
	_, err = client.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("Embed succeeded against a provider that never responds")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Embed error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Embed took %s, want it to stop promptly", elapsed)
	}
}

func TestOpenAIProviderCallIsLogged(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2,0.3],"index":0}],"model":"local-embedding","usage":{"prompt_tokens":1,"total_tokens":1}}`))
	}))
	defer server.Close()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	client, err := NewOpenAIWithTimeoutAndLogger(server.URL+"/v1", "", "local-embedding", 3, time.Second, logger)
	if err != nil {
		t.Fatalf("NewOpenAIWithTimeoutAndLogger: %v", err)
	}
	if _, err := client.Embed(context.Background(), "hello"); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	for _, want := range []string{"msg=\"provider api call\"", "component=embedding", "model=local-embedding", "path=/v1/embeddings", "status=200"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("provider log %q does not contain %q", logs.String(), want)
		}
	}
}
