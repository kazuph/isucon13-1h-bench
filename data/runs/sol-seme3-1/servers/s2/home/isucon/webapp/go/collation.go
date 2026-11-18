package main

import (
	"encoding/binary"
	"fmt"
	"github.com/jmoiron/sqlx"
	"strings"
)

var mysqlWeights [65536]rune
var useWeights bool

// Read the exact comparison weights used by the reference application's
// prepared LIKE expressions. This is performed only at process startup.
func loadCollationWeights(db *sqlx.DB) error {
	chars := make([]rune, 0, 65536-2048)
	for r := rune(0); r < 65536; r++ {
		if r >= 0xd800 && r <= 0xdfff {
			continue
		}
		chars = append(chars, r)
	}
	var weights []byte
	if err := db.Get(&weights, "SELECT WEIGHT_STRING(CAST(? AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_general_ci)", string(chars)); err != nil {
		return err
	}
	if len(weights) != len(chars)*2 {
		return fmt.Errorf("unexpected collation weights length %d for %d characters", len(weights), len(chars))
	}
	for i, r := range chars {
		mysqlWeights[r] = rune(binary.BigEndian.Uint16(weights[2*i:]))
	}
	useWeights = true
	return nil
}
func collationFold(s string) string {
	if !useWeights {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r >= 65536 {
			return 0xfffd
		}
		return mysqlWeights[r]
	}, s)
}
