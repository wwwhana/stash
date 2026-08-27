package observability

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// InstrumentHTTP records stable, low-cardinality HTTP request metrics while
// preserving the interfaces used by streaming handlers. Access records are
// emitted at debug level so normal production logs stay quiet until an
// operator explicitly sets STASH_LOG_LEVEL=debug.
func InstrumentHTTP(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		observed := &responseObserver{ResponseWriter: w}
		requestID := safeRequestID(r.Header.Get("X-Request-ID"))
		if requestID == "" {
			requestID = newRequestID()
		}
		if requestID != "" {
			observed.Header().Set("X-Request-ID", requestID)
		}
		defer func() {
			elapsed := time.Since(started)
			route := routeLabel(r.URL.Path)
			RecordHTTP(route, r.Method, observed.statusCode(), elapsed)
			if logger != nil {
				logger.Debug("http access",
					slog.String("request_id", requestID),
					slog.String("method", r.Method),
					slog.String("path", accessLogPath(r.URL.Path)),
					slog.String("route", route),
					slog.Int("status", observed.statusCode()),
					slog.Int64("bytes", observed.bytes),
					slog.Float64("duration_ms", float64(elapsed)/float64(time.Millisecond)),
					slog.String("remote_addr", r.RemoteAddr),
				)
			}
		}()
		next.ServeHTTP(observed, r)
	})
}

type responseObserver struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func (w *responseObserver) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseObserver) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(body)
	w.bytes += int64(n)
	return n, err
}

func (w *responseObserver) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap lets http.ResponseController reach optional capabilities on the
// underlying writer without making those capabilities part of this wrapper's
// public API.
func (w *responseObserver) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *responseObserver) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func accessLogPath(path string) string {
	const maxPathBytes = 256
	if len(path) <= maxPathBytes {
		return path
	}
	return path[:maxPathBytes]
}

func safeRequestID(value string) string {
	if value == "" || len(value) > 128 {
		return ""
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return ""
	}
	return value
}

func newRequestID() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(raw[:])
}

func routeLabel(path string) string {
	switch path {
	case "/", "/mcp", "/sse", "/message", "/metrics", "/healthz", "/readyz",
		"/auth/login", "/auth/callback", "/auth/logout", "/auth/status", "/auth/token",
		"/authorize", "/oauth/callback", "/oauth/token", "/oauth/register":
		return path
	}
	if strings.HasPrefix(path, "/.well-known/oauth-protected-resource") {
		return "/.well-known/oauth-protected-resource"
	}
	if strings.HasPrefix(path, "/.well-known/oauth-authorization-server") || strings.HasPrefix(path, "/.well-known/openid-configuration") {
		return "/.well-known/oauth-authorization-server"
	}
	if strings.HasPrefix(path, "/auth/") {
		return "/auth/*"
	}
	return "/other"
}
