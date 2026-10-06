package main

import (
	"errors"
	"net/http"
	"strings"

	"github.com/alash3al/stash/internal/auth"
	"github.com/alash3al/stash/internal/bootstrap"
)

// registerUserAdminRoutes lets an administrator manage people and their API
// tokens from the console: who exists, how they sign in, whether they are
// administrators, and which tokens are live.
func registerUserAdminRoutes(mux *http.ServeMux, bc *bootstrap.Context) {
	handle := func(pattern string, handler func(*bootstrap.Context, http.ResponseWriter, *http.Request)) {
		mux.Handle(pattern, adminOnlyHTTP(bc, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if bc.Auth == nil {
				writeAdminError(w, http.StatusServiceUnavailable, "user accounts need STASH_AUTH_MODE=token or oauth")
				return
			}
			handler(bc, w, r)
		})))
	}
	handle("GET /admin/users", adminUsersListHandler)
	handle("POST /admin/users", adminUsersCreateHandler)
	handle("PUT /admin/users/{username}", adminUsersUpdateHandler)
	handle("POST /admin/users/{username}/password", adminUsersPasswordHandler)
	handle("DELETE /admin/users/{username}", adminUsersDeleteHandler)
	handle("GET /admin/users/{username}/tokens", adminUserTokensHandler)
	handle("POST /admin/users/{username}/tokens/{id}/revoke", adminUserRevokeTokenHandler)
}

// adminActor is the signed-in administrator, used to refuse changes that
// would lock them out of the very page they are on.
func adminActor(bc *bootstrap.Context, r *http.Request) string {
	if bc.Auth == nil {
		return ""
	}
	user, err := bc.Auth.VerifyRequest(r)
	if err != nil {
		return ""
	}
	return user
}

func adminUsername(w http.ResponseWriter, r *http.Request) (string, bool) {
	username := strings.TrimSpace(r.PathValue("username"))
	if username == "" {
		writeAdminError(w, http.StatusBadRequest, "username is required")
		return "", false
	}
	return username, true
}

func adminUsersListHandler(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	users, err := bc.Auth.ListUsers(r.Context())
	if err != nil {
		writeUserError(w, err)
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"users": users, "actor": adminActor(bc, r)})
}

type adminUserInput struct {
	Username    string  `json:"username"`
	DisplayName string  `json:"display_name"`
	Password    *string `json:"password"`
	IsAdmin     bool    `json:"is_admin"`
}

func adminUsersCreateHandler(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	var input adminUserInput
	if !decodeAdminJSON(w, r, &input) {
		return
	}
	var (
		user auth.User
		err  error
	)
	if input.Password != nil && *input.Password != "" {
		user, err = bc.Auth.CreateLocalUser(r.Context(), input.Username, *input.Password, input.DisplayName, input.IsAdmin)
	} else {
		user, err = bc.Auth.CreateUser(r.Context(), input.Username, input.DisplayName, input.IsAdmin)
	}
	if err != nil {
		writeUserError(w, err)
		return
	}
	writeAdminJSON(w, http.StatusCreated, user)
}

type adminUserUpdate struct {
	DisplayName *string `json:"display_name"`
	IsAdmin     *bool   `json:"is_admin"`
	Disabled    *bool   `json:"disabled"`
}

func adminUsersUpdateHandler(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	username, ok := adminUsername(w, r)
	if !ok {
		return
	}
	var input adminUserUpdate
	if !decodeAdminJSON(w, r, &input) {
		return
	}
	if username == adminActor(bc, r) && ((input.IsAdmin != nil && !*input.IsAdmin) || (input.Disabled != nil && *input.Disabled)) {
		writeAdminError(w, http.StatusConflict, "you cannot remove your own administrator access or disable yourself")
		return
	}
	user, err := bc.Auth.UpdateUser(r.Context(), username, auth.UserUpdate{DisplayName: input.DisplayName, IsAdmin: input.IsAdmin, Disabled: input.Disabled})
	if err != nil {
		writeUserError(w, err)
		return
	}
	writeAdminJSON(w, http.StatusOK, user)
}

func adminUsersPasswordHandler(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	username, ok := adminUsername(w, r)
	if !ok {
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if !decodeAdminJSON(w, r, &input) {
		return
	}
	if err := bc.Auth.SetLocalPassword(r.Context(), username, input.Password); err != nil {
		writeUserError(w, err)
		return
	}
	user, err := bc.Auth.GetUser(r.Context(), username)
	if err != nil {
		writeUserError(w, err)
		return
	}
	writeAdminJSON(w, http.StatusOK, user)
}

func adminUsersDeleteHandler(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	username, ok := adminUsername(w, r)
	if !ok {
		return
	}
	if username == adminActor(bc, r) {
		writeAdminError(w, http.StatusConflict, "you cannot delete your own account")
		return
	}
	if err := bc.Auth.DeleteUser(r.Context(), username); err != nil {
		writeUserError(w, err)
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"deleted": username})
}

func adminUserTokensHandler(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	username, ok := adminUsername(w, r)
	if !ok {
		return
	}
	tokens, err := bc.Auth.ListAPITokens(r.Context(), username)
	if err != nil {
		writeUserError(w, err)
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"username": username, "tokens": tokens})
}

func adminUserRevokeTokenHandler(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	username, ok := adminUsername(w, r)
	if !ok {
		return
	}
	id, ok := adminPathID(w, r)
	if !ok {
		return
	}
	revokedAt, err := bc.Auth.RevokeAPIToken(r.Context(), username, id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeAdminError(w, http.StatusNotFound, "API token not found")
			return
		}
		writeUserError(w, err)
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"id": id, "revoked": true, "revoked_at": revokedAt, "expires_at": revokedAt})
}

func writeUserError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, auth.ErrUserNotFound):
		status = http.StatusNotFound
	case errors.Is(err, auth.ErrUserExists):
		status = http.StatusConflict
	case errors.Is(err, auth.ErrInvalidUsername), errors.Is(err, auth.ErrWeakPassword):
		status = http.StatusBadRequest
	case errors.Is(err, auth.ErrAccountsUnavailable):
		status = http.StatusServiceUnavailable
	}
	writeAdminError(w, status, strings.TrimPrefix(err.Error(), "auth: "))
}
