package main

import _ "embed"

// MySQL utf8mb4_general_ci uses one 16-bit weight per BMP code point.
// Generated from WEIGHT_STRING for every valid BMP code point on the supplied
// MySQL server; supplementary code points all have weight FFFD.
// This preserves the initial prepared-statement LIKE semantics without SQL.
//
//go:embed general_ci.bin
var generalCIWeights []byte

func generalCI(s string) string {
	changed := false
	for _, r := range s {
		if r > 65535 {
			changed = true
			break
		}
		i := int(r) * 2
		if rune(generalCIWeights[i])|rune(generalCIWeights[i+1])<<8 != r {
			changed = true
			break
		}
	}
	if !changed {
		return s
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r > 65535 {
			r = 0xfffd
		} else {
			i := int(r) * 2
			r = rune(generalCIWeights[i]) | rune(generalCIWeights[i+1])<<8
		}
		out = append(out, r)
	}
	return string(out)
}
