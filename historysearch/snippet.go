package historysearch

import (
	"strings"
	"unicode"
)

// snippetRadius is how many characters a snippet keeps on each side of the
// first match.
const snippetRadius = 90

// snippet cuts content around the first place one of the wordings, or else
// one of their terms, occurs. Short content comes back whole. The cut never
// splits a character, and an ellipsis marks each side that was cut.
func snippet(content string, wordings []string) string {
	content = collapseSpace(content)
	runes := []rune(content)
	if len(runes) <= 2*snippetRadius {
		return content
	}
	lower := []rune(strings.ToLower(content))
	if len(lower) != len(runes) {
		// A case mapping that changes length (rare) would misplace the cut.
		lower = runes
	}
	at := -1
	for _, wording := range wordings {
		if at = runeIndex(lower, []rune(strings.ToLower(strings.TrimSpace(wording)))); at >= 0 {
			break
		}
	}
	if at < 0 {
		for _, wording := range wordings {
			for _, term := range Terms(wording) {
				if i := runeIndex(lower, []rune(term)); i >= 0 && (at < 0 || i < at) {
					at = i
				}
			}
		}
	}
	if at < 0 {
		at = 0
	}
	start, end := at-snippetRadius, at+snippetRadius
	if start < 0 {
		end -= start
		start = 0
	}
	if end > len(runes) {
		start -= end - len(runes)
		end = len(runes)
		if start < 0 {
			start = 0
		}
	}
	out := strings.TrimSpace(string(runes[start:end]))
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}

func runeIndex(haystack, needle []rune) int {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return -1
	}
outer:
	for i := 0; i+len(needle) <= len(haystack); i++ {
		for j, r := range needle {
			if haystack[i+j] != r {
				continue outer
			}
		}
		return i
	}
	return -1
}

func collapseSpace(text string) string {
	var out strings.Builder
	space := false
	for _, r := range strings.TrimSpace(text) {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space {
			out.WriteByte(' ')
			space = false
		}
		out.WriteRune(r)
	}
	return out.String()
}
