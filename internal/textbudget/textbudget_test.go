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

func TestIsContextLimitErrorMatchesProviderMessage(t *testing.T) {
	err := errors.New("Message too long: 300472 tokens exceeds 44544 context window")
	if !IsContextLimitError(err) {
		t.Fatal("provider context error was not recognised")
	}
	if IsContextLimitError(errors.New("connection refused")) {
		t.Fatal("unrelated error was recognised as a context error")
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
