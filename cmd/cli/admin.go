package main

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/alash3al/stash/internal/bootstrap"
)

var adminRequestProtection = http.NewCrossOriginProtection()

// registerAdminRoutes adds deliberately small, same-origin maintenance
// endpoints. They are not MCP tools: global reindexing is an operator action,
// not something an agent should be able to trigger through a normal namespace.
func registerAdminRoutes(mux *http.ServeMux, bc *bootstrap.Context) {
	mux.Handle("/admin/maintenance/embeddings", adminOnlyHTTP(bc, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		adminEmbeddingStatusHandler(bc, w, r)
	})))
	mux.Handle("/admin/maintenance/embeddings/retry", adminOnlyHTTP(bc, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		adminEmbeddingRetryHandler(bc, w, r)
	})))
	mux.Handle("/admin/maintenance/embeddings/reindex", adminOnlyHTTP(bc, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		adminEmbeddingReindexHandler(bc, w, r)
	})))
	registerLLMAdminRoutes(mux, bc)
}

func adminOnlyHTTP(bc *bootstrap.Context, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bc == nil || bc.Config == nil || bc.Brain == nil {
			writeAdminError(w, http.StatusServiceUnavailable, "service is not initialized")
			return
		}
		if err := adminRequestProtection.Check(r); err != nil {
			writeAdminError(w, http.StatusForbidden, "cross-origin request denied")
			return
		}
		// STASH_AUTH_MODE=none already trusts everyone who can reach the
		// listener with full memory access; the admin pages are the same
		// trust level, so they open without a separate credential.
		if unauthenticatedDeployment(bc) && strings.TrimSpace(bc.Config.AdminToken) == "" && strings.TrimSpace(bc.Config.AdminSubjects) == "" {
			next.ServeHTTP(w, r)
			return
		}
		if adminTokenMatches(r, bc.Config.AdminToken) {
			next.ServeHTTP(w, r)
			return
		}
		if bc.Auth == nil {
			writeAdminError(w, http.StatusUnauthorized, "admin credential is required")
			return
		}
		user, err := bc.Auth.VerifyRequest(r)
		if err != nil || user == "" {
			writeAdminError(w, http.StatusUnauthorized, "authentication is required")
			return
		}
		// A local administrator (STASH_ADMIN_USER or 'stash user set --admin')
		// is the usual operator; STASH_ADMIN_SUBJECTS covers SSO identities.
		if !bc.Auth.IsAdmin(r.Context(), user) {
			writeAdminError(w, http.StatusForbidden, "administrator permission is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// unauthenticatedDeployment reports STASH_AUTH_MODE=none, where the server
// refuses to listen beyond loopback and performs no HTTP authentication.
func unauthenticatedDeployment(bc *bootstrap.Context) bool {
	mode := ""
	if bc.Config != nil {
		mode = strings.ToLower(strings.TrimSpace(bc.Config.AuthMode))
	}
	if mode != "" && mode != "none" {
		return false
	}
	return bc.Auth == nil || bc.Auth.Mode() == "none"
}

func adminTokenMatches(r *http.Request, expected string) bool {
	expected = strings.TrimSpace(expected)
	provided := strings.TrimSpace(r.Header.Get("X-Stash-Admin-Token"))
	if expected == "" || provided == "" || len(expected) != len(provided) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}

func adminEmbeddingStatusHandler(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	status, err := bc.Brain.EmbeddingMaintenanceStatus(r.Context())
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, status)
}

func adminEmbeddingRetryHandler(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	woken, err := bc.Brain.ForceRetryPendingEmbeddings(r.Context())
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	bc.Brain.WakeEmbeddingRetries()
	status, err := bc.Brain.EmbeddingMaintenanceStatus(r.Context())
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{
		"action": "retry",
		"woken":  woken,
		"status": status,
	})
}

func adminEmbeddingReindexHandler(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	queued, err := bc.Brain.QueueEmbeddingReindex(r.Context())
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	bc.Brain.WakeEmbeddingRetries()
	status, err := bc.Brain.EmbeddingMaintenanceStatus(r.Context())
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{
		"action": "reindex",
		"queued": queued,
		"status": status,
	})
}

func writeAdminJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeAdminError(w http.ResponseWriter, status int, message string) {
	writeAdminJSON(w, status, map[string]string{"error": message})
}
