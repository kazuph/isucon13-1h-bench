package main

import (
	_ "embed"
	"encoding/binary"
	"strings"
	"sync"
)

// generalCIWeights was measured from the competition MySQL server's utf8mb4_general_ci collation.
//
//go:embed general-ci.bin
var generalCIWeights []byte
var matchCache sync.Map

type likePattern struct {
	plain  string
	wild   bool
	tokens []int32
}

func collationWeight(r rune) rune {
	if r >= 65536 {
		return 0xfffd
	}
	return rune(binary.BigEndian.Uint16(generalCIWeights[int(r)*2:]))
}
func normalized(s string) string {
	r := []rune(s)
	for i, c := range r {
		r[i] = collationWeight(c)
	}
	return string(r)
}
func compileLike(word string) *likePattern {
	p := &likePattern{}
	if !strings.ContainsAny(word, "%_\\") {
		p.plain = normalized(word)
		return p
	}
	p.wild = true
	r := []rune("%" + word + "%")
	for i := 0; i < len(r); i++ {
		c := r[i]
		switch c {
		case '%':
			p.tokens = append(p.tokens, -1)
		case '_':
			p.tokens = append(p.tokens, -2)
		case '\\':
			if i+1 < len(r) {
				i++
				c = r[i]
			}
			p.tokens = append(p.tokens, int32(collationWeight(c)))
		default:
			p.tokens = append(p.tokens, int32(collationWeight(c)))
		}
	}
	return p
}
func matches(s, word string) bool {
	v, ok := matchCache.Load(word)
	if !ok {
		v, _ = matchCache.LoadOrStore(word, compileLike(word))
	}
	p := v.(*likePattern)
	if !p.wild {
		return strings.Contains(normalized(s), p.plain)
	}
	text := []rune(s)
	for i, r := range text {
		text[i] = collationWeight(r)
	}
	si, pi, star, mark := 0, 0, -1, 0
	for si < len(text) {
		if pi < len(p.tokens) && (p.tokens[pi] == -2 || p.tokens[pi] == int32(text[si])) {
			si++
			pi++
		} else if pi < len(p.tokens) && p.tokens[pi] == -1 {
			star = pi
			pi++
			mark = si
		} else if star >= 0 {
			mark++
			si = mark
			pi = star + 1
		} else {
			return false
		}
	}
	for pi < len(p.tokens) && p.tokens[pi] == -1 {
		pi++
	}
	return pi == len(p.tokens)
}
