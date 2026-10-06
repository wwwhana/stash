package main

import (
	"errors"
	"net/http"
	"strings"

	"github.com/alash3al/stash/internal/auth"
	"github.com/alash3al/stash/internal/bootstrap"
)

// registerSSOAdminRoutes exposes the SSO provider table. Which identity
// provider may sign people in is an operator decision, so it sits behind the
// same admin gate as model routing.
func registerSSOAdminRoutes(mux *http.ServeMux, bc *bootstrap.Context) {
	handle := func(pattern string, handler func(*bootstrap.Context, http.ResponseWriter, *http.Request)) {
		mux.Handle(pattern, adminOnlyHTTP(bc, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if bc.Auth == nil {
				writeAdminError(w, http.StatusServiceUnavailable, "SSO needs STASH_AUTH_MODE=token or oauth")
				return
			}
			handler(bc, w, r)
		})))
	}
	handle("GET /admin/sso/status", adminSSOStatusHandler)
	handle("GET /admin/sso/providers", adminSSOListHandler)
	handle("POST /admin/sso/providers", adminSSOCreateHandler)
	handle("PUT /admin/sso/providers/{id}", adminSSOUpdateHandler)
	handle("DELETE /admin/sso/providers/{id}", adminSSODeleteHandler)
	handle("POST /admin/sso/providers/{id}/test", adminSSOTestHandler)
	handle("POST /admin/sso/import-environment", adminSSOImportHandler)
}

// adminSSOStatus is the one document the console needs for the SSO section.
type adminSSOStatus struct {
	Providers         []auth.SSOProvider `json:"providers"`
	SecretsEnabled    bool               `json:"secrets_enabled"`
	CookieSecure      bool               `json:"cookie_secure"`
	EnvironmentIssuer string             `json:"environment_issuer,omitempty"`
	CallbackPath      string             `json:"callback_path"`
}

func adminSSOStatusHandler(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	writeSSOStatus(bc, w, r)
}

func writeSSOStatus(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	providers, err := bc.Auth.ListSSOProviders(r.Context())
	if err != nil {
		writeSSOError(w, err)
		return
	}
	status := adminSSOStatus{Providers: providers, SecretsEnabled: bc.Auth.CanStoreSSOSecrets(), CallbackPath: "/auth/callback"}
	if bc.Config != nil {
		status.CookieSecure = bc.Config.AuthCookieSecure
		status.EnvironmentIssuer = strings.TrimSpace(bc.Config.AuthIssuer)
	}
	writeAdminJSON(w, http.StatusOK, status)
}

func adminSSOListHandler(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	providers, err := bc.Auth.ListSSOProviders(r.Context())
	if err != nil {
		writeSSOError(w, err)
		return
	}
	writeAdminJSON(w, http.StatusOK, providers)
}

func adminSSOCreateHandler(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	var input auth.SSOProviderInput
	if !decodeAdminJSON(w, r, &input) {
		return
	}
	provider, err := bc.Auth.CreateSSOProvider(r.Context(), input)
	if err != nil {
		writeSSOError(w, err)
		return
	}
	writeAdminJSON(w, http.StatusCreated, provider)
}

func adminSSOUpdateHandler(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := adminPathID(w, r)
	if !ok {
		return
	}
	var input auth.SSOProviderInput
	if !decodeAdminJSON(w, r, &input) {
		return
	}
	provider, err := bc.Auth.UpdateSSOProvider(r.Context(), id, input)
	if err != nil {
		writeSSOError(w, err)
		return
	}
	writeAdminJSON(w, http.StatusOK, provider)
}

func adminSSODeleteHandler(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := adminPathID(w, r)
	if !ok {
		return
	}
	if err := bc.Auth.DeleteSSOProvider(r.Context(), id); err != nil {
		writeSSOError(w, err)
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// adminSSOTestHandler runs discovery for one stored provider and reports
// the outcome without changing what the login page offers.
func adminSSOTestHandler(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := adminPathID(w, r)
	if !ok {
		return
	}
	if err := bc.Auth.TestSSOProvider(r.Context(), id); err != nil {
		if errors.Is(err, auth.ErrSSOProviderNotFound) || errors.Is(err, auth.ErrSSOUnavailable) {
			writeSSOError(w, err)
			return
		}
		writeAdminJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func adminSSOImportHandler(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	provider, imported, err := bc.Auth.ImportEnvironmentSSO(r.Context())
	if err != nil {
		writeSSOError(w, err)
		return
	}
	if !imported {
		writeAdminJSON(w, http.StatusOK, map[string]any{"imported": false})
		return
	}
	writeAdminJSON(w, http.StatusCreated, map[string]any{"imported": true, "provider": provider})
}

func writeSSOError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, auth.ErrSSOProviderNotFound):
		status = http.StatusNotFound
	case errors.Is(err, auth.ErrSSOSlugTaken):
		status = http.StatusConflict
	case errors.Is(err, auth.ErrInvalidSSOProvider), errors.Is(err, auth.ErrSSOSecretsUnavailable):
		status = http.StatusBadRequest
	case errors.Is(err, auth.ErrSSOUnavailable):
		status = http.StatusServiceUnavailable
	}
	writeAdminError(w, status, strings.TrimPrefix(err.Error(), "auth: "))
}
