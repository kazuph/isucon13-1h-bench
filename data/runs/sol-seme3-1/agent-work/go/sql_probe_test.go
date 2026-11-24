package main

import (
	"github.com/labstack/echo/v4"
	"os"
	"testing"
)

func TestSQLLikeReference(t *testing.T) {
	if os.Getenv("SQL_PROBE") == "" {
		t.Skip("manual SQL compatibility probe")
	}
	db, e := connectDB(echo.New().Logger)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e := loadCollationWeights(db); e != nil {
		t.Fatal(e)
	}
	defer func() { useWeights = false }()
	var col string
	db.Get(&col, "SELECT @@collation_connection")
	t.Log("collation", col)
	for _, v := range [][2]string{{"A", "a"}, {"É", "e"}, {"ばか", "はか"}, {"Ｂ", "b"}, {"ß", "s"}, {"œ", "o"}, {"あいう", "あ_う"}, {"カタカナ", "かたかな"}, {"ABC", "abc"}, {"ø", "o"}} {
		var n int
		if e := db.Get(&n, "SELECT ? LIKE CONCAT('%',?,'%')", v[0], v[1]); e != nil {
			t.Fatal(e)
		}
		t.Logf("%q LIKE %q: SQL=%d memory=%v", v[0], v[1], n, likeMatch(v[0], v[1]))
		if likeMatch(v[0], v[1]) != (n == 1) {
			t.Errorf("mismatch %q %q", v[0], v[1])
		}
	}
}
