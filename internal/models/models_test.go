package models

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPublicMemoryModelsDoNotSerializeEmbeddings(t *testing.T) {
	data, err := json.Marshal(Fact{
		ID:          7,
		NamespaceID: 11,
		Content:     "uses postgres",
	})
	if err != nil {
		t.Fatalf("marshal fact: %v", err)
	}

	got := string(data)
	if strings.Contains(got, `"Embedding"`) || strings.Contains(got, `"embedding"`) {
		t.Fatalf("embedding leaked into public JSON: %s", got)
	}
	if !strings.Contains(got, `"namespace_id":11`) {
		t.Fatalf("snake_case field missing from public JSON: %s", got)
	}
}
