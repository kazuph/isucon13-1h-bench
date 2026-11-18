package main

import (
	"golang.org/x/text/unicode/norm"
	"regexp"
	"strings"
	"sync"
	"unicode"
)

// The reference compares bound parameters using utf8mb4_general_ci LIKE,
// including SQL wildcards and case/accent folding.
var spamPatterns sync.Map

func foldSpam(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Mn, r) {
			return -1
		}
		return unicode.ToLower(r)
	}, norm.NFD.String(s))
}
func matchesSpam(comment, word string) bool {
	if !strings.ContainsAny(word, "%_\\") {
		return strings.Contains(foldSpam(comment), foldSpam(word))
	}
	value, ok := spamPatterns.Load(word)
	if !ok {
		var b strings.Builder
		b.WriteString("(?s)")
		escaped := false
		for _, r := range foldSpam(word) {
			if escaped {
				b.WriteString(regexp.QuoteMeta(string(r)))
				escaped = false
				continue
			}
			switch r {
			case '\\':
				escaped = true
			case '%':
				b.WriteString(".*")
			case '_':
				b.WriteString(".")
			default:
				b.WriteString(regexp.QuoteMeta(string(r)))
			}
		}
		if escaped {
			b.WriteString(regexp.QuoteMeta("\\"))
		}
		value, _ = spamPatterns.LoadOrStore(word, regexp.MustCompile(b.String()))
	}
	return value.(*regexp.Regexp).MatchString(foldSpam(comment))
}
