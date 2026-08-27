package textbudget

import "strings"

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
