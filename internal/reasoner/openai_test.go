package reasoner

import "testing"

func TestNewOpenAIAllowsEmptyAPIKey(t *testing.T) {
	got, err := NewOpenAI("http://localhost:1234/v1", "", "local-reasoner")
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	if got.model != "local-reasoner" {
		t.Fatalf("model = %q, want local-reasoner", got.model)
	}
}
