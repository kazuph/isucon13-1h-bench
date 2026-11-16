package main

import (
	"testing"
	"time"
)

func TestCacheHitDoesNotExtendExpiry(t *testing.T) {
	clearSharedResponses()
	defer clearSharedResponses()
	start := time.Now()
	expiry := start.Add(responseCacheTTL)
	sharedResponses.users[1] = timedUser{User{ID: 1, IconHash: "old"}, expiry}
	sharedResponses.streams[1] = timedStream{Livestream{ID: 1}, expiry}
	u, ok := cachedUser(1)
	if !ok {
		t.Fatal("fresh user missing")
	}
	s, ok := cachedStream(1)
	if !ok {
		t.Fatal("fresh stream missing")
	}
	publishResponses(&responseCache{started: start.Add(time.Millisecond), users: map[int64]User{1: u}, streams: map[int64]Livestream{1: s}})
	if sharedResponses.users[1].expires != expiry || sharedResponses.streams[1].expires != expiry {
		t.Fatal("cache hit renewed stale data")
	}
}
func TestExpiredDataIsNotReturned(t *testing.T) {
	clearSharedResponses()
	defer clearSharedResponses()
	sharedResponses.users[1] = timedUser{User{ID: 1}, time.Now().Add(-time.Second)}
	sharedResponses.streams[1] = timedStream{Livestream{ID: 1}, time.Now().Add(-time.Second)}
	if _, ok := cachedUser(1); ok {
		t.Fatal("expired user returned")
	}
	if _, ok := cachedStream(1); ok {
		t.Fatal("expired stream returned")
	}
}
func TestOlderRequestCannotOverwriteNewData(t *testing.T) {
	clearSharedResponses()
	defer clearSharedResponses()
	start := time.Now()
	expiry := start.Add(responseCacheTTL)
	sharedResponses.users[1] = timedUser{User{ID: 1, IconHash: "new"}, expiry}
	publishResponses(&responseCache{started: start.Add(-time.Millisecond), users: map[int64]User{1: {ID: 1, IconHash: "old"}}, freshUsers: map[int64]bool{1: true}})
	if sharedResponses.users[1].value.IconHash != "new" {
		t.Fatal("old request overwrote new data")
	}
}
