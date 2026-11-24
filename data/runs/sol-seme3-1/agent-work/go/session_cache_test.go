package main

import (
	"github.com/gorilla/sessions"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSessionCacheChecksSignaturesAndExpiry(t *testing.T) {
	e := echo.New()
	store := sessions.NewCookieStore(secret)
	cookieFor := func(exp int64) *http.Cookie {
		r := httptest.NewRequest("GET", "/", nil)
		w := httptest.NewRecorder()
		s, _ := store.Get(r, defaultSessionIDKey)
		s.Values[defaultUserIDKey] = int64(42)
		s.Values[defaultSessionExpiresKey] = exp
		if err := s.Save(r, w); err != nil {
			t.Fatal(err)
		}
		return w.Result().Cookies()[0]
	}
	call := func(cookie *http.Cookie) (int64, error) {
		r := httptest.NewRequest("GET", "/", nil)
		r.AddCookie(cookie)
		c := e.NewContext(r, httptest.NewRecorder())
		var id int64
		var err error
		handler := session.Middleware(store)(func(c echo.Context) error { id, err = memorySession(c); return nil })
		if er := handler(c); er != nil {
			t.Fatal(er)
		}
		return id, err
	}
	valid := cookieFor(time.Now().Add(time.Hour).Unix())
	for i := 0; i < 2; i++ {
		id, err := call(valid)
		if err != nil || id != 42 {
			t.Fatalf("valid cookie rejected: %v %v", id, err)
		}
	}
	forged := *valid
	forged.Value = "x" + forged.Value
	if _, err := call(&forged); err == nil {
		t.Fatal("forged cookie accepted")
	}
	expired := cookieFor(time.Now().Add(-time.Hour).Unix())
	if _, err := call(expired); err == nil {
		t.Fatal("expired cookie accepted")
	}
	authMu.Lock()
	authCache[valid.Value] = cachedSession{UID: 42, Expires: time.Now().Add(-time.Hour).Unix()}
	authMu.Unlock()
	if _, err := call(valid); err == nil {
		t.Fatal("expired cached session accepted")
	}
}
