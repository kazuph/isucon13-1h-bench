package main

import (
	"bytes"
	"encoding/json"
	"golang.org/x/crypto/bcrypt"
	"net/http/httptest"
	"testing"
)

func TestFreshBcryptAndVerifiedPairCache(t *testing.T) {
	cacheEnabled = true
	invoke := func(path string, v request) (int, string) {
		b, _ := json.Marshal(v)
		r := httptest.NewRequest("POST", path, bytes.NewReader(b))
		w := httptest.NewRecorder()
		if path == "/hash" {
			hashHandler(w, r)
		} else {
			compareHandler(w, r)
		}
		return w.Code, w.Body.String()
	}
	code, a := invoke("/hash", request{Password: "password"})
	if code != 200 {
		t.Fatal(code)
	}
	_, b := invoke("/hash", request{Password: "password"})
	if a == b {
		t.Fatal("hash salt was reused")
	}
	for _, h := range []string{a, b} {
		cost, e := bcrypt.Cost([]byte(h))
		if e != nil || cost != bcrypt.MinCost {
			t.Fatal("bcrypt cost changed")
		}
		if e := bcrypt.CompareHashAndPassword([]byte(h), []byte("password")); e != nil {
			t.Fatal(e)
		}
	}
	hits := cacheHits.Load()
	if c, _ := invoke("/compare", request{Hash: a, Password: "password"}); c != 200 {
		t.Fatal(c)
	}
	if cacheHits.Load() != hits+1 {
		t.Fatal("known pair did not hit")
	}
	if c, _ := invoke("/compare", request{Hash: a, Password: "wrong"}); c != 401 {
		t.Fatal("wrong password accepted")
	}
	if c, _ := invoke("/compare", request{Hash: "invalid" + a, Password: "password"}); c == 200 {
		t.Fatal("invalid hash accepted")
	}
}
