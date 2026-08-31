package queries

import (
	"strings"
	"testing"
)

func TestKeywordFactsUsesCombinedFactSearchText(t *testing.T) {
	q, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sql, args, err := q.KeywordFacts([]int64{7}, "FACT-42", 10)
	if err != nil {
		t.Fatalf("KeywordFacts: %v", err)
	}
	if len(args) != 5 {
		t.Fatalf("KeywordFacts args = %d, want three query binds, limit, and namespace", len(args))
	}
	for _, want := range []string{
		"word_similarity(",
		"<% search_text",
		"ORDER BY word_similarity(",
		"valid_until IS NULL",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("KeywordFacts SQL does not contain %q:\n%s", want, sql)
		}
	}
	if got := strings.Count(sql, "search_text"); got != 3 {
		t.Fatalf("KeywordFacts SQL references search_text %d times, want 3:\n%s", got, sql)
	}
	if strings.Contains(sql, "word_similarity($1, content)") || strings.Contains(sql, "$1 <% content") {
		t.Fatalf("KeywordFacts SQL still searches only fact content:\n%s", sql)
	}
}
