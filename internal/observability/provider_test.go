package observability

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestLoggingRoundTripperRecordsProviderCallWithoutRequestSecrets(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	transport := NewLoggingRoundTripper(logger, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"data":[]}`)),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	}), "embedding", "local-embedding")

	requestURL, err := url.Parse("https://provider.example/v1/embeddings?access_token=must-not-appear")
	if err != nil {
		t.Fatalf("parse request URL: %v", err)
	}
	request := &http.Request{Method: http.MethodPost, URL: requestURL, Header: make(http.Header)}
	request.Header.Set("Authorization", "Bearer must-not-appear")
	request = request.WithContext(WithRequestID(context.Background(), "request-123"))
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	logText := logs.String()
	for _, want := range []string{
		"msg=\"provider api call\"", "component=embedding", "model=local-embedding",
		"method=POST", "provider_host=provider.example", "path=/v1/embeddings",
		"status=200", "request_id=request-123",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("provider log %q does not contain %q", logText, want)
		}
	}
	for _, secret := range []string{"access_token", "must-not-appear", "Authorization"} {
		if strings.Contains(logText, secret) {
			t.Fatalf("provider log leaked %q: %s", secret, logText)
		}
	}
}

func TestLoggingRoundTripperLogsProviderFailureAtWarn(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	transport := NewLoggingRoundTripper(logger, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New(`Post "https://provider.example/v1?api_key=must-not-appear": provider connection failed`)
	}), "reasoner", "plan-model")
	request, err := http.NewRequest(http.MethodPost, "https://provider.example/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if _, err := transport.RoundTrip(request); err == nil {
		t.Fatal("RoundTrip succeeded, want provider failure")
	}
	logText := logs.String()
	for _, want := range []string{"level=WARN", "msg=\"provider api call failed\"", "component=reasoner", "provider connection failed"} {
		if !strings.Contains(logText, want) {
			t.Fatalf("provider failure log %q does not contain %q", logText, want)
		}
	}
	if strings.Contains(logText, "must-not-appear") {
		t.Fatalf("provider failure log leaked a credential: %s", logText)
	}
}
