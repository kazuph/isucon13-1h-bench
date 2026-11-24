package main

import (
	"github.com/labstack/echo/v4"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIconCacheExpiryUsesReadStart(t *testing.T) {
	clearIconHashCache()
	cache := activeIconHashes.Load()
	started := time.Now().Add(-iconHashCacheTTL / 2)
	cache.put("alice", `"hash"`, started)
	if !cache.matches("alice", `"hash"`, started.Add(iconHashCacheTTL/2)) {
		t.Fatal("fresh cached ETag did not match")
	}
	if cache.matches("alice", `"hash"`, started.Add(iconHashCacheTTL)) {
		t.Fatal("cached ETag exceeded freshness deadline")
	}
	if cache.matches("alice", "", started.Add(iconHashCacheTTL/2)) {
		t.Fatal("unconditional GET must not use cached 304")
	}
	if cache.matches("alice", `"different"`, started.Add(iconHashCacheTTL/2)) {
		t.Fatal("different ETag matched")
	}
}
func TestIconCacheResetIsolatesInFlightReads(t *testing.T) {
	clearIconHashCache()
	oldCache := activeIconHashes.Load()
	clearIconHashCache()
	oldCache.put("alice", `"old-generation"`, time.Now())
	if activeIconHashes.Load().matches("alice", `"old-generation"`, time.Now()) {
		t.Fatal("pre-initialization read populated new cache")
	}
}
func TestIconCacheResetAccess(t *testing.T) {
	clearIconHashCache()
	activeIconHashes.Load().put("alice", `"hash"`, time.Now())
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/internal/cache-reset", nil)
	req.RemoteAddr = "10.99.0.200:10000"
	err := resetCacheHandler(e.NewContext(req, httptest.NewRecorder()))
	httpErr, ok := err.(*echo.HTTPError)
	if !ok || httpErr.Code != http.StatusForbidden {
		t.Fatalf("external reset accepted: %v", err)
	}
	req.RemoteAddr = "10.99.0.11:10000"
	recorder := httptest.NewRecorder()
	if err := resetCacheHandler(e.NewContext(req, recorder)); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("unexpected reset status %d", recorder.Code)
	}
	if activeIconHashes.Load().matches("alice", `"hash"`, time.Now()) {
		t.Fatal("internal reset did not clear cache")
	}
}
