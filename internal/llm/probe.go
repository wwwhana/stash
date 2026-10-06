package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ProbeResult is the outcome of one connectivity check against a provider.
type ProbeResult struct {
	OK         bool     `json:"ok"`
	StatusCode int      `json:"status_code,omitempty"`
	LatencyMS  int64    `json:"latency_ms"`
	Models     []string `json:"models,omitempty"`
	Error      string   `json:"error,omitempty"`
}

const maxProbeModels = 500

// Probe lists the models an OpenAI-compatible endpoint exposes. It is the
// cheapest request that proves the URL, credential, and network path at once,
// and the returned IDs feed the model picker in the console.
func Probe(ctx context.Context, baseURL, apiKey string, timeout time.Duration) ProbeResult {
	started := time.Now()
	result := ProbeResult{}
	finish := func() ProbeResult {
		result.LatencyMS = time.Since(started).Milliseconds()
		return result
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if err := validateBaseURL(baseURL); err != nil {
		result.Error = err.Error()
		return finish()
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		result.Error = err.Error()
		return finish()
	}
	if key := strings.TrimSpace(apiKey); key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		result.Error = err.Error()
		return finish()
	}
	defer response.Body.Close()
	result.StatusCode = response.StatusCode
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		result.Error = fmt.Sprintf("read response: %v", err)
		return finish()
	}
	if response.StatusCode != http.StatusOK {
		result.Error = fmt.Sprintf("endpoint returned HTTP %d", response.StatusCode)
		return finish()
	}
	var listing struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &listing); err != nil {
		result.Error = "endpoint did not return an OpenAI-compatible model list"
		return finish()
	}
	for _, entry := range listing.Data {
		if id := strings.TrimSpace(entry.ID); id != "" {
			result.Models = append(result.Models, id)
		}
	}
	sort.Strings(result.Models)
	if len(result.Models) > maxProbeModels {
		result.Models = result.Models[:maxProbeModels]
	}
	result.OK = true
	return finish()
}
