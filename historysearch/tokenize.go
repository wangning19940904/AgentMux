package historysearch

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Terms splits text into the keys the index stores and a query looks up.
// The same function tokenizes documents and queries, so a query term
// matches exactly the documents that contain it.
//
//   - A run of letters or digits in an alphabetic script (Latin, Cyrillic,
//     Greek, …) is one lower-cased word: "Migration" → "migration",
//     "k8s" → "k8s". English stop words are dropped.
//   - A run of Han, Hiragana, Katakana or Hangul characters is written
//     without spaces, so it is split into overlapping pairs of characters:
//     "数据库迁移" → "数据", "据库", "库迁", "迁移". A run of one character is
//     kept as it is. Most Chinese words are two characters long, so a
//     two-character query finds every text that contains it.
//   - Everything else (spaces, punctuation, symbols) separates terms.
//
// Mixed text works because a script change ends a run: "部署k8s集群" →
// "部署", "k8s", "集群". The result has no duplicates and keeps first-seen
// order. Terms longer than maxTermBytes are cut so one long token cannot
// bloat the index.
func Terms(text string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(term string) {
		if term == "" || seen[term] {
			return
		}
		seen[term] = true
		out = append(out, term)
	}
	var word strings.Builder
	var cjk []rune
	flushWord := func() {
		if word.Len() == 0 {
			return
		}
		term := word.String()
		word.Reset()
		if stopWords[term] {
			return
		}
		add(clip(term))
	}
	flushCJK := func() {
		switch len(cjk) {
		case 0:
			return
		case 1:
			add(string(cjk))
		default:
			for i := 0; i+1 < len(cjk); i++ {
				add(string(cjk[i : i+2]))
			}
		}
		cjk = cjk[:0]
	}
	for _, r := range text {
		switch {
		case isCJK(r):
			flushWord()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r):
			flushCJK()
			word.WriteRune(unicode.ToLower(r))
		default:
			flushWord()
			flushCJK()
		}
	}
	flushWord()
	flushCJK()
	return out
}

// maxTermBytes caps one stored term.
const maxTermBytes = 64

func clip(term string) string {
	if len(term) <= maxTermBytes {
		return term
	}
	cut := maxTermBytes
	for cut > 0 && !utf8.RuneStart(term[cut]) {
		cut--
	}
	return term[:cut]
}

// isCJK reports scripts written without spaces between words.
func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r)
}

// stopWords are English words too common to say anything about a match.
// Chinese needs no list: a two-character pair is already specific, and a
// query's coverage threshold absorbs the common ones.
var stopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "but": true, "by": true, "did": true, "do": true, "does": true,
	"for": true, "from": true, "had": true, "has": true, "have": true,
	"how": true, "i": true, "if": true, "in": true, "into": true, "is": true,
	"it": true, "its": true, "me": true, "my": true, "of": true, "on": true,
	"or": true, "our": true, "so": true, "that": true, "the": true,
	"their": true, "them": true, "then": true, "there": true, "these": true,
	"they": true, "this": true, "to": true, "us": true, "was": true,
	"we": true, "were": true, "what": true, "when": true, "where": true,
	"which": true, "who": true, "why": true, "will": true, "with": true,
	"you": true, "your": true,
}
