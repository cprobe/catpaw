package aiclient

import "unicode/utf8"

// EstimateTokensChinese provides a conservative token estimate for mixed
// Chinese/English text. BPE tokenizers encode Chinese characters at roughly
// 1 token per 1.5–2 characters; we use 1 token ≈ 2 characters for safety.
func EstimateTokensChinese(text string) int {
	n := utf8.RuneCountInString(text)
	if n == 0 {
		return 0
	}
	// 1 token ≈ 2 runes (conservative for Chinese-heavy text)
	tokens := (n + 1) / 2
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}
