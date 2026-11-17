package main

import (
	_ "embed"
	"encoding/binary"
	"strings"
)

// Extracted from the competition MySQL instance with WEIGHT_STRING using
// utf8mb4_general_ci, the original Go driver's connection collation. Each BMP
// character has exactly one 16-bit comparison weight. Supplementary characters
// have weight FFFD in this collation. SQL LIKE wildcards are parsed separately.
//
//go:embed collation.bin
var collationWeights []byte

func sqlWeight(r rune) rune {
	if r > 65535 {
		return 0xfffd
	}
	return rune(binary.BigEndian.Uint16(collationWeights[int(r)*2:]))
}
func sqlFold(s string) string { return strings.Map(sqlWeight, s) }
