package textbudget

import (
	"regexp"
	"strconv"
	"strings"
)

var contextLimitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)maximum\s+(?:context\s+length|input(?:\s+token)?(?:\s+length)?)\s+(?:is|of|=|:)\s*([0-9][0-9,]*)`),
	regexp.MustCompile(`(?i)context\s+window\s+(?:is|of|=|:)\s*([0-9][0-9,]*)`),
	regexp.MustCompile(`(?i)maximum\s+number\s+of\s+tokens\s+(?:is|of|=|:)\s*([0-9][0-9,]*)`),
	regexp.MustCompile(`(?i)exceeds\s+([0-9][0-9,]*)\s+(?:tokens?\s+)?context(?:\s+window|\s+length)?`),
}

// IsContextLimitError recognises the common messages returned by
// OpenAI-compatible servers when the request exceeds a model's input window.
// Providers do not share an error type, so this intentionally matches only
// context/token wording and leaves unrelated failures untouched.
func IsContextLimitError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"context length",
		"context window",
		"maximum context",
		"maximum input",
		"message too long",
		"too many tokens",
		"input token",
		"token limit",
		"prompt is too long",
		"prompt too long",
		"prompt length",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

// ContextLimitTokens extracts a provider-reported maximum context size when
// the error includes one. OpenAI-compatible servers do not use one error
// schema, so this is deliberately a small set of common phrases. The value is
// the limit, not the number of tokens in the rejected request.
func ContextLimitTokens(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	message := err.Error()
	for _, pattern := range contextLimitPatterns {
		matches := pattern.FindStringSubmatch(message)
		if len(matches) < 2 {
			continue
		}
		value, parseErr := strconv.Atoi(strings.ReplaceAll(matches[1], ",", ""))
		if parseErr == nil && value > 0 {
			return value, true
		}
	}
	return 0, false
}

// InputBudgetFromContextError converts a provider-reported context limit into
// a conservative input byte budget after reserving space for instructions and
// the model's output. It returns false when the provider did not include a
// usable limit.
func InputBudgetFromContextError(err error, reservedTokens int) (int, bool) {
	limit, ok := ContextLimitTokens(err)
	if !ok {
		return 0, false
	}
	if reservedTokens < 0 {
		reservedTokens = 0
	}
	limit -= reservedTokens
	if limit <= 0 {
		return 0, false
	}
	return BytesForTokens(limit), true
}
