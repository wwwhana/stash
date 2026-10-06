package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alash3al/stash/internal/bootstrap"
	"github.com/alash3al/stash/internal/llm"
)

const maxAdminJSONBody = 64 * 1024

// registerLLMAdminRoutes exposes the provider registry. Changing which model
// serves a feature is an operator decision, so these stay off the MCP surface
// and behind the same admin gate as embedding maintenance.
func registerLLMAdminRoutes(mux *http.ServeMux, bc *bootstrap.Context) {
	handle := func(pattern string, handler func(*bootstrap.Context, http.ResponseWriter, *http.Request)) {
		mux.Handle(pattern, adminOnlyHTTP(bc, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if bc.LLM == nil || bc.LLMStore == nil {
				writeAdminError(w, http.StatusServiceUnavailable, "model routing is not initialized")
				return
			}
			handler(bc, w, r)
		})))
	}
	handle("GET /admin/llm/status", adminLLMStatusHandler)
	handle("GET /admin/llm/providers", adminLLMListProvidersHandler)
	handle("POST /admin/llm/providers", adminLLMCreateProviderHandler)
	handle("PUT /admin/llm/providers/{id}", adminLLMUpdateProviderHandler)
	handle("DELETE /admin/llm/providers/{id}", adminLLMDeleteProviderHandler)
	handle("POST /admin/llm/probe", adminLLMProbeHandler)
	handle("PUT /admin/llm/assignments/{feature}", adminLLMSetAssignmentHandler)
	handle("DELETE /admin/llm/assignments/{feature}", adminLLMClearAssignmentHandler)
	handle("POST /admin/llm/import-environment", adminLLMImportEnvironmentHandler)
}

// adminLLMStatus is the one document the console needs to render the page.
type adminLLMStatus struct {
	Features        []llm.FeatureInfo `json:"features"`
	Routes          []llm.RouteStatus `json:"routes"`
	Providers       []llm.Provider    `json:"providers"`
	Assignments     []llm.Assignment  `json:"assignments"`
	Version         int64             `json:"version"`
	SecretsEnabled  bool              `json:"secrets_enabled"`
	EnvironmentBase string            `json:"environment_base_url,omitempty"`
}

func adminLLMStatusHandler(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	writeLLMStatus(bc, w, r)
}

func writeLLMStatus(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	providers, err := bc.LLMStore.ListProviders(r.Context())
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	assignments, err := bc.LLMStore.ListAssignments(r.Context())
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	status := adminLLMStatus{
		Features:       llm.Features(),
		Routes:         bc.LLM.Status(),
		Providers:      providers,
		Assignments:    assignments,
		Version:        bc.LLM.Version(),
		SecretsEnabled: bc.LLMStore.CanStoreSecrets(),
	}
	if bc.Config != nil {
		status.EnvironmentBase = strings.TrimSpace(bc.Config.OpenAIBaseURL)
	}
	writeAdminJSON(w, http.StatusOK, status)
}

func adminLLMListProvidersHandler(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	providers, err := bc.LLMStore.ListProviders(r.Context())
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeAdminJSON(w, http.StatusOK, providers)
}

func adminLLMCreateProviderHandler(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	var input llm.ProviderInput
	if !decodeAdminJSON(w, r, &input) {
		return
	}
	provider, err := bc.LLMStore.CreateProvider(r.Context(), input)
	if err != nil {
		writeLLMError(w, err)
		return
	}
	reloadLLM(bc, w, r)
	writeAdminJSON(w, http.StatusCreated, provider)
}

func adminLLMUpdateProviderHandler(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := adminPathID(w, r)
	if !ok {
		return
	}
	var input llm.ProviderInput
	if !decodeAdminJSON(w, r, &input) {
		return
	}
	provider, err := bc.LLMStore.UpdateProvider(r.Context(), id, input)
	if err != nil {
		writeLLMError(w, err)
		return
	}
	reloadLLM(bc, w, r)
	writeAdminJSON(w, http.StatusOK, provider)
}

func adminLLMDeleteProviderHandler(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	id, ok := adminPathID(w, r)
	if !ok {
		return
	}
	if err := bc.LLMStore.DeleteProvider(r.Context(), id); err != nil {
		writeLLMError(w, err)
		return
	}
	reloadLLM(bc, w, r)
	writeAdminJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// adminLLMProbeHandler checks connectivity either for a stored provider (by
// id, using its sealed key) or for form values that have not been saved yet.
func adminLLMProbeHandler(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	var input struct {
		ProviderID *int64  `json:"provider_id"`
		BaseURL    string  `json:"base_url"`
		APIKey     *string `json:"api_key"`
	}
	if !decodeAdminJSON(w, r, &input) {
		return
	}
	baseURL, apiKey, timeout := strings.TrimSpace(input.BaseURL), "", 30*time.Second
	if input.APIKey != nil {
		apiKey = *input.APIKey
	}
	if input.ProviderID != nil {
		provider, err := bc.LLMStore.GetProvider(r.Context(), *input.ProviderID)
		if err != nil {
			writeLLMError(w, err)
			return
		}
		if baseURL == "" {
			baseURL = provider.BaseURL
		}
		if input.APIKey == nil {
			stored, err := bc.LLMStore.OpenAPIKey(r.Context(), provider.ID)
			if err != nil {
				writeLLMError(w, err)
				return
			}
			apiKey = stored
		}
		timeout = provider.Timeout()
	}
	writeAdminJSON(w, http.StatusOK, llm.Probe(r.Context(), baseURL, apiKey, timeout))
}

func adminLLMSetAssignmentHandler(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	feature, err := llm.ParseFeature(r.PathValue("feature"))
	if err != nil {
		writeLLMError(w, err)
		return
	}
	var input llm.Assignment
	if !decodeAdminJSON(w, r, &input) {
		return
	}
	input.Feature = feature
	assignment, err := bc.LLMStore.SetAssignment(r.Context(), input)
	if err != nil {
		writeLLMError(w, err)
		return
	}
	reloadLLM(bc, w, r)
	writeAdminJSON(w, http.StatusOK, assignment)
}

func adminLLMClearAssignmentHandler(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	feature, err := llm.ParseFeature(r.PathValue("feature"))
	if err != nil {
		writeLLMError(w, err)
		return
	}
	if err := bc.LLMStore.ClearAssignment(r.Context(), feature); err != nil {
		writeLLMError(w, err)
		return
	}
	reloadLLM(bc, w, r)
	writeAdminJSON(w, http.StatusOK, map[string]any{"cleared": feature})
}

func adminLLMImportEnvironmentHandler(bc *bootstrap.Context, w http.ResponseWriter, r *http.Request) {
	provider, assignments, err := bc.LLM.ImportEnvironment(r.Context())
	if err != nil {
		writeLLMError(w, err)
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"provider": provider, "assignments": assignments, "routes": bc.LLM.Status()})
}

// reloadLLM applies a change immediately; the version ticker covers other
// processes. A reload failure is logged, not returned, because the write
// already succeeded and the status endpoint shows the resulting route error.
func reloadLLM(bc *bootstrap.Context, _ http.ResponseWriter, r *http.Request) {
	if err := bc.LLM.Reload(r.Context()); err != nil && bc.Logger != nil {
		bc.Logger.Error("reload model routing after admin change", "error", err)
	}
}

func adminPathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeAdminError(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

func decodeAdminJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAdminJSONBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func writeLLMError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, llm.ErrProviderNotFound):
		status = http.StatusNotFound
	case errors.Is(err, llm.ErrProviderInUse), errors.Is(err, llm.ErrProviderNameTaken):
		status = http.StatusConflict
	case errors.Is(err, llm.ErrInvalidProvider), errors.Is(err, llm.ErrInvalidAssignment), errors.Is(err, llm.ErrUnknownFeature):
		status = http.StatusBadRequest
	case errors.Is(err, llm.ErrSecretsKeyRequired):
		status = http.StatusPreconditionFailed
	}
	writeAdminError(w, status, err.Error())
}
