package main

import (
	"strings"

	"github.com/jmoiron/sqlx"
)

// MySQL's utf8mb4_general_ci LIKE compares one 16-bit weight per character.
// Use the server's actual weights so the in-memory literal search agrees with SQL.
var moderationWeights [1 << 16]rune

func loadModerationWeights(db *sqlx.DB) error {
	for i := range moderationWeights {
		moderationWeights[i] = rune(i)
	}
	const query = `WITH RECURSIVE bytes AS (
 SELECT 0 AS n UNION ALL SELECT n+1 FROM bytes WHERE n<255
 ), points AS (
 SELECT hi.n*256+lo.n AS n FROM bytes hi CROSS JOIN bytes lo
 ), weights AS (
 SELECT n,HEX(WEIGHT_STRING(CONVERT(CONVERT(UNHEX(LPAD(HEX(n),4,'0')) USING ucs2) USING utf8mb4) COLLATE utf8mb4_general_ci)) AS w
 FROM points WHERE n NOT BETWEEN 55296 AND 57343
 ) SELECT n,CONV(w,16,10) FROM weights WHERE w<>LPAD(HEX(n),4,'0')`
	rows, err := db.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var n int
		var weight rune
		if err := rows.Scan(&n, &weight); err != nil {
			return err
		}
		moderationWeights[n] = weight
	}
	return rows.Err()
}
func foldModerationText(text string) string {
	var result strings.Builder
	changed := false
	for i, r := range text {
		weight := rune(0xfffd)
		if r < 1<<16 {
			weight = moderationWeights[r]
		}
		if !changed && r != weight {
			result.Grow(len(text))
			result.WriteString(text[:i])
			changed = true
		}
		if changed {
			result.WriteRune(weight)
		}
	}
	if !changed {
		return text
	}
	return result.String()
}
