package llm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alash3al/stash/internal/db"
	"github.com/alash3al/stash/internal/secrets"
)

const testSecretsKey = "0000000000000000000000000000000000000000000000000000000000000042"

func newTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("STASH_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("set STASH_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	pool, err := db.OpenPool(ctx, dsn)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(pool.Close)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = pool.Exec(cleanup, `DELETE FROM llm_feature_assignments`)
		_, _ = pool.Exec(cleanup, `DELETE FROM llm_providers WHERE name LIKE 'test-%'`)
	})
	keyring, err := secrets.NewKeyring(testSecretsKey)
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	return NewStore(pool, keyring), ctx
}

func str(v string) *string { return &v }

func TestStoreSealsCredentialsAndRoutesFeatures(t *testing.T) {
	store, ctx := newTestStore(t)
	name := fmt.Sprintf("test-%d", time.Now().UnixNano())
	before, _ := store.Version(ctx)

	provider, err := store.CreateProvider(ctx, ProviderInput{Name: str(name), BaseURL: str("https://models.example/v1/"), APIKey: str("sk-live")})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if !provider.HasAPIKey || provider.BaseURL != "https://models.example/v1" || provider.RequestTimeoutSeconds != 120 {
		t.Fatalf("provider = %+v", provider)
	}

	var sealed string
	if err := store.pool.QueryRow(ctx, `SELECT api_key_sealed FROM llm_providers WHERE id = $1`, provider.ID).Scan(&sealed); err != nil {
		t.Fatalf("read sealed key: %v", err)
	}
	if !secrets.IsSealed(sealed) || strings.Contains(sealed, "sk-live") {
		t.Fatalf("API key is stored readable: %q", sealed)
	}
	if opened, err := store.OpenAPIKey(ctx, provider.ID); err != nil || opened != "sk-live" {
		t.Fatalf("OpenAPIKey = %q, %v", opened, err)
	}

	if _, err := store.CreateProvider(ctx, ProviderInput{Name: str(name), BaseURL: str("https://other.example")}); !errors.Is(err, ErrProviderNameTaken) {
		t.Fatalf("duplicate name error = %v", err)
	}

	assignment, err := store.SetAssignment(ctx, Assignment{Feature: FeatureEmbedding, ProviderID: provider.ID, Model: "embed-x", Dimensions: 16})
	if err != nil {
		t.Fatalf("SetAssignment: %v", err)
	}
	if assignment.Dimensions != 16 || assignment.Model != "embed-x" {
		t.Fatalf("assignment = %+v", assignment)
	}
	if _, err := store.SetAssignment(ctx, Assignment{Feature: FeatureEmbedding, ProviderID: provider.ID, Model: "embed-x"}); !errors.Is(err, ErrInvalidAssignment) {
		t.Fatalf("embedding without dimensions error = %v", err)
	}
	if _, err := store.SetAssignment(ctx, Assignment{Feature: "nope", ProviderID: provider.ID, Model: "m"}); !errors.Is(err, ErrUnknownFeature) {
		t.Fatalf("unknown feature error = %v", err)
	}
	if err := store.DeleteProvider(ctx, provider.ID); !errors.Is(err, ErrProviderInUse) {
		t.Fatalf("delete assigned provider error = %v", err)
	}

	creds, err := store.providerCredentials(ctx)
	if err != nil {
		t.Fatalf("providerCredentials: %v", err)
	}
	if creds[provider.ID].apiKey != "sk-live" || creds[provider.ID].keyError != nil {
		t.Fatalf("credentials = %+v", creds[provider.ID])
	}

	// Clearing the key stores NULL, which the router treats as "no auth".
	updated, err := store.UpdateProvider(ctx, provider.ID, ProviderInput{APIKey: str(""), Enabled: boolPtr(false)})
	if err != nil || updated.HasAPIKey || updated.Enabled {
		t.Fatalf("UpdateProvider = %+v, %v", updated, err)
	}

	if err := store.ClearAssignment(ctx, FeatureEmbedding); err != nil {
		t.Fatalf("ClearAssignment: %v", err)
	}
	if err := store.DeleteProvider(ctx, provider.ID); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}
	after, _ := store.Version(ctx)
	if after < before+5 {
		t.Fatalf("version advanced from %d to %d, want one bump per mutation", before, after)
	}
}

func TestStoreRefusesKeysWithoutKeyring(t *testing.T) {
	store, ctx := newTestStore(t)
	store.keyring = nil
	if _, err := store.CreateProvider(ctx, ProviderInput{Name: str("test-nokey"), BaseURL: str("http://localhost:11434/v1"), APIKey: str("x")}); !errors.Is(err, ErrSecretsKeyRequired) {
		t.Fatalf("CreateProvider error = %v, want ErrSecretsKeyRequired", err)
	}
	provider, err := store.CreateProvider(ctx, ProviderInput{Name: str("test-nokey"), BaseURL: str("http://localhost:11434/v1")})
	if err != nil {
		t.Fatalf("key-less provider should be allowed: %v", err)
	}
	if opened, err := store.OpenAPIKey(ctx, provider.ID); err != nil || opened != "" {
		t.Fatalf("OpenAPIKey for key-less provider = %q, %v", opened, err)
	}
}

func TestRouterImportsEnvironmentAndPrefersDatabaseAssignments(t *testing.T) {
	store, ctx := newTestStore(t)
	env := testEnv()
	factory := &fakeFactory{}
	router := NewRouter(store.pool, store, env, Options{factory: factory})
	if err := router.Reload(ctx); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	provider, assignments, err := router.ImportEnvironment(ctx)
	if err != nil {
		t.Fatalf("ImportEnvironment: %v", err)
	}
	t.Cleanup(func() {
		_, _ = store.pool.Exec(context.Background(), `DELETE FROM llm_feature_assignments WHERE provider_id = $1`, provider.ID)
		_, _ = store.pool.Exec(context.Background(), `DELETE FROM llm_providers WHERE id = $1`, provider.ID)
	})
	if provider.Name != EnvProviderName || !provider.HasAPIKey || len(assignments) != len(Features()) {
		t.Fatalf("import = %+v, %d assignments", provider, len(assignments))
	}
	for _, rt := range router.Status() {
		if rt.Source != SourceDatabase || rt.ProviderID != provider.ID || !rt.Available {
			t.Fatalf("route after import = %+v", rt)
		}
	}

	// A different provider for one feature must win over the environment and
	// survive a second import.
	other, err := store.CreateProvider(ctx, ProviderInput{Name: str("test-other"), BaseURL: str("http://other.local/v1")})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	if _, err := store.SetAssignment(ctx, Assignment{Feature: FeaturePlanValidation, ProviderID: other.ID, Model: "review"}); err != nil {
		t.Fatalf("assign other: %v", err)
	}
	changed, err := router.ReloadIfChanged(ctx)
	if err != nil || !changed {
		t.Fatalf("ReloadIfChanged = %v, %v, want a reload", changed, err)
	}
	if _, _, err := router.ImportEnvironment(ctx); err != nil {
		t.Fatalf("second ImportEnvironment: %v", err)
	}
	for _, rt := range router.Status() {
		if rt.Feature == FeaturePlanValidation && (rt.ProviderID != other.ID || rt.Model != "review" || rt.ReservedTokens != DefaultReservedTokens) {
			t.Fatalf("plan validation route = %+v, want other provider kept", rt)
		}
	}
	if changed, err := router.ReloadIfChanged(ctx); err != nil || changed {
		t.Fatalf("ReloadIfChanged without changes = %v, %v", changed, err)
	}
}

func boolPtr(v bool) *bool { return &v }
