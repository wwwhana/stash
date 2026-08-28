package observability

import (
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var (
	providerQuerySecret  = regexp.MustCompile(`(?i)((?:api[_-]?key|access[_-]?token|token|authorization)=)[^&\s"]+`)
	providerBearerSecret = regexp.MustCompile(`(?i)(bearer\s+)[^\s"]+`)
)

// LoggingRoundTripper records calls made to an OpenAI-compatible provider.
// Request and response bodies, authorization headers, and query strings are
// deliberately excluded: the log is for operation history, not payload
// capture.
type LoggingRoundTripper struct {
	base      http.RoundTripper
	logger    *slog.Logger
	component string
	model     string
}

// NewLoggingRoundTripper wraps base with provider-call logging. A nil base
// uses http.DefaultTransport. A nil logger leaves the transport uninstrumented
// while preserving normal HTTP behavior.
func NewLoggingRoundTripper(logger *slog.Logger, base http.RoundTripper, component, model string) http.RoundTripper {
	if logger == nil {
		return base
	}
	if base == nil {
		base = http.DefaultTransport
	}
	return &LoggingRoundTripper{
		base:      base,
		logger:    logger,
		component: boundedLogValue(component, 64),
		model:     boundedLogValue(model, 128),
	}
}

func (t *LoggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	started := time.Now()
	resp, err := t.base.RoundTrip(req)
	elapsed := time.Since(started)
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}

	result := "ok"
	level := slog.LevelInfo
	message := "provider api call"
	attrs := []slog.Attr{
		slog.String("component", t.component),
		slog.String("model", t.model),
		slog.String("method", req.Method),
		slog.String("provider_host", providerHost(req)),
		slog.String("path", accessLogPath(req.URL.Path)),
		slog.Int("status", status),
		slog.Float64("duration_ms", float64(elapsed)/float64(time.Millisecond)),
	}
	if requestID := RequestID(req.Context()); requestID != "" {
		attrs = append(attrs, slog.String("request_id", requestID))
	}
	if err != nil {
		result = "error"
		level = slog.LevelWarn
		message = "provider api call failed"
		attrs = append(attrs, slog.String("error", safeProviderError(err)))
	} else if status >= http.StatusBadRequest {
		result = "http_error"
		level = slog.LevelWarn
		message = "provider api call failed"
	}
	RecordProviderAPICall(t.component, result, elapsed)
	t.logger.LogAttrs(req.Context(), level, message, attrs...)
	return resp, err
}

func providerHost(req *http.Request) string {
	if req == nil || req.URL == nil {
		return ""
	}
	return boundedLogValue(req.URL.Hostname(), 128)
}

func boundedLogValue(value string, max int) string {
	value = strings.TrimSpace(value)
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[:max]
}

func safeProviderError(err error) string {
	if err == nil {
		return ""
	}
	text := boundedLogValue(err.Error(), 2000)
	text = providerQuerySecret.ReplaceAllString(text, `$1[redacted]`)
	return providerBearerSecret.ReplaceAllString(text, `${1}[redacted]`)
}
