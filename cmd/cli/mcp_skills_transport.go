package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const maxMCPRequestBodyBytes = 1 << 20

type stashSkillsTransportContextKey struct{}

func withStashSkillsTransport(ctx context.Context) context.Context {
	return context.WithValue(ctx, stashSkillsTransportContextKey{}, true)
}

func stashSkillsTransportAvailable(ctx context.Context) bool {
	available, _ := ctx.Value(stashSkillsTransportContextKey{}).(bool)
	return available
}

type stashSkillsMessageHandler func(context.Context, json.RawMessage) (mcp.JSONRPCMessage, bool)

type noStoreResponseWriter struct {
	http.ResponseWriter
}

func (w *noStoreResponseWriter) WriteHeader(status int) {
	w.Header().Set("Cache-Control", "no-store")
	w.ResponseWriter.WriteHeader(status)
}

func (w *noStoreResponseWriter) Write(content []byte) (int, error) {
	w.Header().Set("Cache-Control", "no-store")
	return w.ResponseWriter.Write(content)
}

func (w *noStoreResponseWriter) Flush() {
	w.Header().Set("Cache-Control", "no-store")
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *noStoreResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// stashSkillsHTTPTransport handles only the SEP-2640 request methods that
// mcp-go v0.49.0 cannot register. Every other request is restored byte-for-byte
// and passed to Streamable HTTP so its session, notification, batch, and SSE
// behavior stays authoritative.
type stashSkillsHTTPTransport struct {
	next            http.Handler
	sessionResolver server.SessionIdManagerResolver
	handleMessage   stashSkillsMessageHandler
}

func newStashSkillsHTTPTransport(next http.Handler, sessionResolver server.SessionIdManagerResolver) http.Handler {
	if next == nil {
		panic("create Stash skills transport with a nil HTTP handler")
	}
	if sessionResolver == nil {
		panic("create Stash skills transport with a nil session resolver")
	}
	return &stashSkillsHTTPTransport{
		next:            next,
		sessionResolver: sessionResolver,
		handleMessage:   handleStashSkillsMessage,
	}
}

func (t *stashSkillsHTTPTransport) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w = &noStoreResponseWriter{ResponseWriter: w}
	r = r.WithContext(withStashSkillsTransport(r.Context()))
	if r.Method != http.MethodPost {
		t.next.ServeHTTP(w, r)
		return
	}

	// JSON-RPC responses can contain user-specific resource metadata or content.
	// The underlying transport replaces this with no-cache if it upgrades a POST
	// response to an SSE stream.
	w.Header().Set("Cache-Control", "no-store")
	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, maxMCPRequestBodyBytes)
	}
	if !isStashSkillsJSONRequest(r.Header.Get("Content-Type")) {
		t.next.ServeHTTP(w, r)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeStashSkillsTransportError(w, http.StatusBadRequest, mcp.PARSE_ERROR, fmt.Sprintf("read request body error: %v", err))
		return
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))

	var envelope struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || !isStashSkillsMethod(envelope.Method) || isStashSkillsNotification(envelope.ID) {
		t.next.ServeHTTP(w, r)
		return
	}

	manager := t.sessionResolver.ResolveSessionIdManager(r)
	terminated, err := manager.Validate(r.Header.Get(server.HeaderKeySessionID))
	if err != nil {
		http.Error(w, "Invalid session ID", http.StatusNotFound)
		return
	}
	if terminated {
		http.Error(w, "Session terminated", http.StatusNotFound)
		return
	}

	response, handled := t.handleMessage(r.Context(), body)
	if !handled {
		t.next.ServeHTTP(w, r)
		return
	}
	writeStashSkillsTransportResponse(w, response)
}

func isStashSkillsJSONRequest(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && mediaType == "application/json"
}

func isStashSkillsMethod(method string) bool {
	switch method {
	case "skills/list", "skills/get", "resources/directory/read":
		return true
	default:
		return false
	}
}

func isStashSkillsNotification(id json.RawMessage) bool {
	return len(id) == 0 || bytes.Equal(bytes.TrimSpace(id), []byte("null"))
}

func writeStashSkillsTransportResponse(w http.ResponseWriter, response mcp.JSONRPCMessage) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}

func writeStashSkillsTransportError(w http.ResponseWriter, status, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(mcp.NewJSONRPCError(mcp.NewRequestId(nil), code, message, nil))
}
