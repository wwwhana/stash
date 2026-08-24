package embedder

import "testing"

func TestNewOpenAIAllowsEmptyAPIKey(t *testing.T) {
	got, err := NewOpenAI("http://localhost:1234/v1", "", "local-embedding", 3)
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	if got.Model() != "local-embedding" || got.Dims() != 3 {
		t.Fatalf("embedder metadata = (%q, %d)", got.Model(), got.Dims())
	}
}
