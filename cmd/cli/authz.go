package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"github.com/alash3al/stash/internal/auth"
	"github.com/alash3al/stash/internal/bootstrap"
	"github.com/alash3al/stash/internal/models"
	"github.com/alash3al/stash/internal/observability"
)

type contextKey string

const (
	keyMode    contextKey = "mode"
	keySSOUser contextKey = "sso_user"
)

// httpContextFunc only copies identity that was already verified by the HTTP
// middleware. It must never treat an unverified header as a user identity.
func httpContextFunc(ctx context.Context, _ *http.Request) context.Context {
	if mode, ok := ctx.Value(keyMode).(string); ok && mode != "" {
		return ctx
	}
	return context.WithValue(ctx, keyMode, "remote")
}

func authenticatedHTTP(provider *auth.Provider, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if provider == nil {
			observability.RecordAuthCheck(r.URL.Path, "disabled")
			ctx := context.WithValue(r.Context(), keyMode, "local")
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		if !provider.HTTPAuthEnabled() {
			observability.RecordAuthCheck(r.URL.Path, "unsupported")
			http.Error(w, "STDIO authentication is only available on the STDIO transport", http.StatusNotImplemented)
			return
		}

		user, err := provider.VerifyRequest(r)
		if err != nil || user == "" {
			observability.RecordAuthCheck(r.URL.Path, "rejected")
			provider.MCPUnauthorized(w, r)
			return
		}

		observability.RecordAuthCheck(r.URL.Path, "accepted")
		ctx := context.WithValue(r.Context(), keyMode, "remote")
		ctx = context.WithValue(ctx, keySSOUser, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func resolveNamespaces(ctx context.Context, nsRaw string) ([]string, error) {
	var rawList []string
	for _, ns := range strings.Split(nsRaw, ",") {
		if ns = strings.TrimSpace(ns); ns != "" {
			rawList = append(rawList, ns)
		}
	}
	if len(rawList) == 0 {
		rawList = []string{"/"}
	}

	mode, _ := ctx.Value(keyMode).(string)
	if mode != "remote" {
		observability.RecordNamespaceScope(mode, "unscoped")
		return rawList, nil
	}

	user, ok := ctx.Value(keySSOUser).(string)
	if !ok || user == "" {
		observability.RecordNamespaceScope("remote", "denied")
		return nil, fmt.Errorf("unauthorized: verified identity is required")
	}
	owner := namespaceOwnerKey(user)
	observability.RecordNamespaceScope("remote", "user")

	isolated := make([]string, 0, len(rawList))
	for _, ns := range rawList {
		clean := strings.Trim(strings.TrimSpace(ns), "/")
		if clean == "" {
			isolated = append(isolated, "/sso/"+owner)
			continue
		}
		isolated = append(isolated, "/sso/"+owner+"/"+clean)
	}
	return isolated, nil
}

func namespaceOwnerKey(user string) string {
	sum := sha256.Sum256([]byte("stash-namespace:" + user))
	return "u_" + hex.EncodeToString(sum[:16])
}

// logicalNamespaceSlug converts an authenticated user's internal namespace
// path back to the path exposed by the API. The /sso/u_<hash> prefix is an
// implementation detail and must not become part of the user's namespace
// paths or MCP prompts.
func logicalNamespaceSlug(ctx context.Context, slug string) (string, bool) {
	mode, _ := ctx.Value(keyMode).(string)
	if mode != "remote" {
		return slug, true
	}

	user, ok := ctx.Value(keySSOUser).(string)
	if !ok || user == "" {
		return "", false
	}
	prefix := "/sso/" + namespaceOwnerKey(user)
	if slug == prefix {
		return "/", true
	}
	if strings.HasPrefix(slug, prefix+"/") {
		return strings.TrimPrefix(slug, prefix), true
	}
	// A remote query should only return this user's subtree. Do not expose an
	// internal path if a future query accidentally violates that invariant.
	return "", false
}

func logicalNamespaces(ctx context.Context, namespaces []models.Namespace) []models.Namespace {
	mode, _ := ctx.Value(keyMode).(string)
	if mode != "remote" {
		return namespaces
	}

	result := make([]models.Namespace, 0, len(namespaces))
	for _, namespace := range namespaces {
		logicalSlug, ok := logicalNamespaceSlug(ctx, namespace.Slug)
		if !ok {
			continue
		}
		// Auto-created parent namespaces use their slug as the name. Keep those
		// names logical too, while preserving user-provided display names.
		if namespace.Name == namespace.Slug {
			namespace.Name = logicalSlug
		}
		namespace.Slug = logicalSlug
		result = append(result, namespace)
	}
	return result
}

func resolveSingleNamespace(ctx context.Context, raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("namespace is required")
	}
	nss, err := resolveNamespaces(ctx, raw)
	if err != nil {
		return "", err
	}
	if len(nss) != 1 {
		return "", fmt.Errorf("exactly one namespace is required")
	}
	return nss[0], nil
}

func exactNamespaceID(ctx context.Context, bc *bootstrap.Context, raw string) (string, int64, error) {
	namespace, err := resolveSingleNamespace(ctx, raw)
	if err != nil {
		return "", 0, err
	}
	ns, err := bc.Brain.GetNamespace(ctx, namespace)
	if err != nil {
		return "", 0, err
	}
	return namespace, ns.ID, nil
}

func authorizeNamespaceID(ctx context.Context, bc *bootstrap.Context, namespaceID int64) error {
	mode, _ := ctx.Value(keyMode).(string)
	if mode != "remote" {
		observability.RecordNamespaceAuthorization("local")
		return nil
	}
	ids, err := authenticatedNamespaceIDs(ctx, bc)
	if err != nil {
		observability.RecordNamespaceAuthorization("error")
		return err
	}
	for _, id := range ids {
		if id == namespaceID {
			observability.RecordNamespaceAuthorization("allowed")
			return nil
		}
	}
	observability.RecordNamespaceAuthorization("denied")
	return fmt.Errorf("forbidden: object is outside the authenticated namespace")
}

func authenticatedNamespaceIDs(ctx context.Context, bc *bootstrap.Context) ([]int64, error) {
	owned, err := resolveNamespaces(ctx, "/")
	if err != nil {
		return nil, err
	}
	return bc.Brain.ResolveNamespaceIDs(ctx, owned)
}

func authorizeRelatedNamespace(ctx context.Context, bc *bootstrap.Context, expected, actual int64) error {
	if err := authorizeNamespaceID(ctx, bc, actual); err != nil {
		return err
	}
	if mode, _ := ctx.Value(keyMode).(string); mode == "remote" && expected != actual {
		return fmt.Errorf("forbidden: related objects must share a namespace")
	}
	return nil
}
