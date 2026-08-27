package embedder

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
