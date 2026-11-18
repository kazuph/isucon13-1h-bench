package main

import (
	"database/sql"
	"math/rand"
	"os"
	"testing"
)

func TestMySQLLikeSemantics(t *testing.T) {
	for _, v := range []struct {
		s, w string
		want bool
	}{{"A", "a", true}, {"あ", "ア", false}, {"é", "e", true}, {"SS", "ß", true}, {"a_b", "_", true}, {"a%b", "%", true}, {"a\\b", "\\", false}, {"abc%", "\\", true}, {"abc", "x", false}, {"abc", "a_c", true}, {"abbbc", "a%c", true}, {"abbbc", "a_d", false}, {"abc", "", true}, {"😃", "😀", true}} {
		if got := matches(v.s, v.w); got != v.want {
			t.Errorf("%q LIKE concat('%%',%q,'%%'): got %v want %v", v.s, v.w, got, v.want)
		}
	}
}

func TestLikeDifferentialMySQL(t *testing.T) {
	dsn := os.Getenv("MYSQL_COMPARE_DSN")
	if dsn == "" {
		t.Skip("set MYSQL_COMPARE_DSN for empirical MySQL differential test")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rng := rand.New(rand.NewSource(23))
	alphabet := []rune("abABßséÉあアかカがガＡａ \\%_😀😃")
	text := func() string {
		r := make([]rune, rng.Intn(10))
		for i := range r {
			r[i] = alphabet[rng.Intn(len(alphabet))]
		}
		return string(r)
	}
	for i := 0; i < 5000; i++ {
		s, w := text(), text()
		var expected bool
		if err := db.QueryRow("SELECT ? LIKE CONCAT('%', ?, '%')", s, w).Scan(&expected); err != nil {
			t.Fatal(err)
		}
		if got := matches(s, w); got != expected {
			t.Errorf("%q LIKE concat('%%',%q,'%%'): got %v, mysql %v", s, w, got, expected)
		}
	}
}
