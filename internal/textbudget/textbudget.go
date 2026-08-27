// Package textbudget contains conservative, model-independent input sizing
// helpers. OpenAI-compatible APIs do not expose a shared tokenizer, and MCP
// does not negotiate a model's context window, so UTF-8 bytes are used as an
// upper-bound estimate when a caller supplies a token budget.
package textbudget

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// BytesForTokens returns a conservative byte budget for a token budget.
// Byte-level tokenizers cannot produce more tokens than input bytes. This may
// leave capacity unused for models whose tokenizer packs many ASCII bytes into
// one token, but it prevents a language-specific estimate from overflowing a
// smaller model context.
func BytesForTokens(tokens int) int {
	if tokens <= 0 {
		return 0
	}
	return tokens
}

// SplitText preserves all text while splitting only at valid UTF-8 boundaries.
// A non-positive limit disables splitting and returns the original text.
func SplitText(text string, maxBytes int) []string {
	if text == "" || maxBytes <= 0 || len(text) <= maxBytes {
		return []string{text}
	}

	parts := make([]string, 0, (len(text)+maxBytes-1)/maxBytes)
	for len(text) > maxBytes {
		hardCut := maxBytes
		for hardCut > 0 && !utf8.ValidString(text[:hardCut]) {
			hardCut--
		}
		if hardCut == 0 {
			// maxBytes can fall inside a multi-byte rune. Advance to the end of
			// that rune so progress is guaranteed and no bytes are lost.
			_, size := utf8.DecodeRuneInString(text)
			if size <= 0 || size > len(text) {
				size = 1
			}
			hardCut = size
		}
		cut := preferredCut(text[:hardCut])
		if cut == 0 {
			cut = hardCut
		}
		parts = append(parts, text[:cut])
		text = text[cut:]
	}
	if text != "" {
		parts = append(parts, text)
	}
	return parts
}

// preferredCut chooses the last natural boundary in a byte-limited segment.
// Paragraph breaks win over sentence endings, then line breaks. A boundary in
// the first third is ignored so a short opening sentence does not create many
// tiny requests; if no useful boundary exists the caller uses the hard UTF-8
// boundary.
func preferredCut(segment string) int {
	if len(segment) < 2 {
		return 0
	}
	minimum := len(segment) / 3
	paragraph, sentence, line := 0, 0, 0
	for i := 0; i < len(segment); {
		r, size := utf8.DecodeRuneInString(segment[i:])
		if size <= 0 {
			size = 1
		}
		next := i + size
		switch {
		case r == '\n':
			line = next
			if next < len(segment) && segment[next] == '\n' {
				paragraph = next + 1
			}
		case strings.ContainsRune(".!?。！？", r):
			sentenceEnd := next
			// A sentence can end with a closing quote or bracket after the
			// punctuation. Include it before looking for whitespace so the
			// next request never starts with a dangling closer.
			for sentenceEnd < len(segment) {
				closing, closingSize := utf8.DecodeRuneInString(segment[sentenceEnd:])
				if !strings.ContainsRune("\"'”’)]}〉》」』】〕≫", closing) {
					break
				}
				sentenceEnd += closingSize
			}
			if sentenceEnd == len(segment) {
				sentence = sentenceEnd
			} else {
				nextRune, _ := utf8.DecodeRuneInString(segment[sentenceEnd:])
				if unicode.IsSpace(nextRune) {
					for sentenceEnd < len(segment) {
						spaceRune, spaceSize := utf8.DecodeRuneInString(segment[sentenceEnd:])
						if !unicode.IsSpace(spaceRune) {
							break
						}
						sentenceEnd += spaceSize
					}
					sentence = sentenceEnd
				}
			}
		}
		i = next
	}
	for _, candidate := range []int{paragraph, sentence, line} {
		if candidate >= minimum && candidate <= len(segment) {
			return candidate
		}
	}
	return 0
}

// SplitStrings groups strings into chunks whose UTF-8 byte total (including a
// one-byte separator between records) stays within maxBytes. A single record
// larger than maxBytes is split into multiple records so callers never send an
// oversized item just because it was indivisible.
func SplitStrings(values []string, maxBytes int) [][]string {
	if len(values) == 0 {
		return nil
	}
	if maxBytes <= 0 {
		copyValues := append([]string(nil), values...)
		return [][]string{copyValues}
	}

	chunks := make([][]string, 0, (len(values)+1)/2)
	current := make([]string, 0)
	used := 0
	flush := func() {
		if len(current) == 0 {
			return
		}
		chunks = append(chunks, current)
		current = make([]string, 0)
		used = 0
	}

	for _, value := range values {
		for _, part := range SplitText(value, maxBytes) {
			size := len(part)
			separator := 0
			if len(current) > 0 {
				separator = 1
			}
			if len(current) > 0 && used+separator+size > maxBytes {
				flush()
			}
			if len(current) == 0 {
				current = append(current, part)
				used = size
				continue
			}
			current = append(current, part)
			used += 1 + size
		}
	}
	flush()
	return chunks
}
