package main

import (
	"database/sql"
	"fmt"
	_ "github.com/go-sql-driver/mysql"
	"testing"
)

func TestLikeMatchesMySQL(t *testing.T) {
	db, err := sql.Open("mysql", "isucon:isucon@tcp(10.99.0.13:3306)/isupipe?interpolateParams=true")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	texts := []string{"", "a", "Abc", "a_b", "a%b", "a\\b", "こんにちは世界", "虹蜂群", "é", "e", "a\nb", "a ", "aaa", "😀🎉"}
	words := []string{"", "a", "A", "_", "%", "a_", "a%", "a\\", "\\%", "\\_", "\\\\", "世界", "虹蜂群", "é", "e", "😀", "%b", "_%_"}
	for _, text := range texts {
		for _, word := range words {
			var want bool
			if err := db.QueryRow("SELECT CAST(? AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_bin LIKE CONCAT('%', CAST(? AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_bin, '%')", text, word).Scan(&want); err != nil {
				t.Fatal(err)
			}
			got := likeWord(text, word)
			if got != want {
				t.Error(fmt.Sprintf("text=%q word=%q got=%v want=%v", text, word, got, want))
			}
		}
	}
}
