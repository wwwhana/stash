package observability

import (
	"net/http"
	"strings"
	"time"
)

// InstrumentHTTP records stable, low-cardinality HTTP request metrics while
// preserving the interfaces used by streaming handlers.
func InstrumentHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		observed := &responseObserver{ResponseWriter: w}
		defer func() {
			RecordHTTP(routeLabel(r.URL.Path), r.Method, observed.statusCode(), time.Since(started))
		}()
		next.ServeHTTP(observed, r)
	})
}

type responseObserver struct {
	http.ResponseWriter
	status      int
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
	return w.ResponseWriter.Write(body)
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

func routeLabel(path string) string {
	switch path {
	case "/", "/mcp", "/sse", "/message", "/metrics", "/healthz", "/readyz",
		"/auth/login", "/auth/callback", "/auth/logout", "/auth/status", "/auth/token":
		return path
	}
	if strings.HasPrefix(path, "/.well-known/oauth-protected-resource") {
		return "/.well-known/oauth-protected-resource"
	}
	if strings.HasPrefix(path, "/auth/") {
		return "/auth/*"
	}
	return "/other"
}
