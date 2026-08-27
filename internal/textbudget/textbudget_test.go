package textbudget

import (
	"errors"
	"strings"
	"testing"
)

func TestSplitTextPrefersParagraphAndSentenceBoundaries(t *testing.T) {
	text := "첫 문장입니다. 둘째 문장입니다.\n\n새 문단의 문장입니다. 마지막입니다."
	parts := SplitText(text, len([]byte("첫 문장입니다. 둘째 문장입니다.")))
	if len(parts) < 2 {
		t.Fatalf("SplitText returned %d part(s), want multiple parts", len(parts))
	}
	for i, part := range parts[:len(parts)-1] {
		trimmed := strings.TrimSpace(part)
		if !strings.HasSuffix(trimmed, ".") && !strings.HasSuffix(trimmed, "\n") {
			t.Fatalf("part %d ends mid-sentence: %q", i, part)
		}
	}
	if got := strings.Join(parts, ""); got != text {
		t.Fatalf("split changed text: %q", got)
	}
}

func TestSplitTextPrefersParagraphBeforeLaterSentence(t *testing.T) {
	text := "첫 문장입니다. 둘째 문장입니다.\n\n새 문단의 첫 문장입니다. 다음 문장입니다."
	limit := len([]byte("첫 문장입니다. 둘째 문장입니다.\n\n새 문단의 첫"))
	parts := SplitText(text, limit)
	if len(parts) < 2 {
		t.Fatalf("SplitText returned %d part(s), want multiple parts", len(parts))
	}
	if !strings.HasSuffix(parts[0], "\n\n") {
		t.Fatalf("first part ended after a sentence instead of paragraph: %q", parts[0])
	}
	if got := strings.Join(parts, ""); got != text {
		t.Fatalf("split changed text: %q", got)
	}
}

func TestSplitTextKeepsClosingQuoteWithSentence(t *testing.T) {
	text := "첫 문장입니다. \"인용문도 끝났습니다.\" 다음 문장입니다."
	limit := len([]byte("첫 문장입니다. \"인용문도 끝났습니다.\" 다"))
	parts := SplitText(text, limit)
	if len(parts) < 2 {
		t.Fatalf("SplitText returned %d part(s), want multiple parts", len(parts))
	}
	if !strings.Contains(parts[0], "끝났습니다.\"") || !strings.HasSuffix(parts[0], " ") {
		t.Fatalf("closing quote was separated from sentence: %q", parts[0])
	}
}

func TestIsContextLimitErrorMatchesProviderMessage(t *testing.T) {
	err := errors.New("Message too long: 300472 tokens exceeds 44544 context window")
	if !IsContextLimitError(err) {
		t.Fatal("provider context error was not recognised")
	}
	if IsContextLimitError(errors.New("connection refused")) {
		t.Fatal("unrelated error was recognised as a context error")
	}
	if got, ok := ContextLimitTokens(err); !ok || got != 44544 {
		t.Fatalf("ContextLimitTokens = %d, %v; want 44544, true", got, ok)
	}
	if got, ok := InputBudgetFromContextError(err, 4096); !ok || got != 40448 {
		t.Fatalf("InputBudgetFromContextError = %d, %v; want 40448, true", got, ok)
	}
	if got, ok := ContextLimitTokens(errors.New("maximum context length is 128000 tokens")); !ok || got != 128000 {
		t.Fatalf("OpenAI context limit = %d, %v; want 128000, true", got, ok)
	}
}

func TestSplitStringsKeepsEveryRecordAndLimit(t *testing.T) {
	values := []string{"alpha. beta.", "한글 문장입니다.", strings.Repeat("x", 20)}
	chunks := SplitStrings(values, 20)
	var joined []string
	for _, chunk := range chunks {
		used := 0
		for i, value := range chunk {
			if i > 0 {
				used++
			}
			used += len(value)
		}
		if used > 20 {
			t.Fatalf("chunk uses %d bytes, want <= 20: %#v", used, chunk)
		}
		joined = append(joined, chunk...)
	}
	if got := strings.Join(joined, ""); got != strings.Join(values, "") {
		t.Fatalf("records changed: %q", got)
	}
}
