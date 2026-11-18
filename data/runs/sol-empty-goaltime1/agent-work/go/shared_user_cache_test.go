package main

import (
	"context"
	"testing"
	"time"
)

func TestSharedUserCachePublicationAndInvalidation(t *testing.T) {
	resetSharedUsers()
	defer resetSharedUsers()
	cache := &responseCache{created: time.Now(), epoch: currentUserCacheEpoch()}
	ctx := context.WithValue(context.Background(), responseCacheKey{}, cache)
	user := User{ID: 42, Name: "cache-test", IconHash: "old"}
	publishSharedUser(ctx, user)
	if _, ok := sharedUser(user.ID); ok {
		t.Fatal("published uncommitted write data")
	}
	cache.shareUsers = true
	publishSharedUser(ctx, user)
	if got, ok := sharedUser(user.ID); !ok || got.IconHash != "old" {
		t.Fatal("read response not cached")
	}
	if named, ok := sharedUserNamed(user.Name); !ok || named.ID != user.ID {
		t.Fatal("name lookup failed")
	}
	invalidateSharedUser(user.ID)
	if _, ok := sharedUserNamed(user.Name); ok {
		t.Fatal("invalidated user still found by name")
	}
	publishSharedUser(ctx, user)
	if _, ok := sharedUser(user.ID); ok {
		t.Fatal("in-flight read resurrected invalidated data")
	}
	cache.epoch = currentUserCacheEpoch()
	user.IconHash = "new"
	publishSharedUser(ctx, user)
	if got, ok := sharedUser(user.ID); !ok || got.IconHash != "new" {
		t.Fatal("new read not cached")
	}
	resetSharedUsers()
	if _, ok := sharedUserNamed(user.Name); ok {
		t.Fatal("reset retained username")
	}
	publishSharedUser(ctx, user)
	if _, ok := sharedUser(user.ID); ok {
		t.Fatal("pre-initialize read resurrected old data")
	}
}
func TestSharedUserCacheAgeStartsAtRequest(t *testing.T) {
	resetSharedUsers()
	defer resetSharedUsers()
	cache := &responseCache{created: time.Now().Add(-sharedUserTTL - time.Second), epoch: currentUserCacheEpoch(), shareUsers: true}
	ctx := context.WithValue(context.Background(), responseCacheKey{}, cache)
	publishSharedUser(ctx, User{ID: 43})
	if _, ok := sharedUser(43); ok {
		t.Fatal("cached a value from an already old request snapshot")
	}
	sharedUsers.Lock()
	sharedUsers.entries[43] = sharedUserEntry{user: User{ID: 43}, expires: time.Now().Add(-time.Second)}
	sharedUsers.Unlock()
	if _, ok := sharedUser(43); ok {
		t.Fatal("served expired cache entry")
	}
}
