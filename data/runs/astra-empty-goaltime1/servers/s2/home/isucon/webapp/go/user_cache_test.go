package main

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestUserCacheDoesNotPublishWriteTransactions(t *testing.T) {
	shared := &userResponseCache{entries: make(map[int64]cachedUserResponse)}
	request := &responseCache{started: time.Now(), sharedUsers: shared, users: make(map[int64]User)}
	ctx := context.WithValue(context.Background(), responseCacheKey{}, request)
	row := userResponseRow{UserModel: UserModel{ID: 42, Name: "uncommitted"}, ImageHash: sql.NullString{String: "hash", Valid: true}}
	if _, err := row.response(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := shared.get(42, time.Now()); ok {
		t.Fatal("uncommitted write transaction leaked into shared cache")
	}
	request.readOnly = true
	if _, err := row.response(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := shared.get(42, time.Now()); !ok {
		t.Fatal("read-only result was not cached")
	}
	if _, ok := shared.getByName("uncommitted", time.Now()); !ok {
		t.Fatal("name lookup did not match cached user")
	}
	if _, ok := shared.getByName("uncommitted", request.started.Add(time.Second)); ok {
		t.Fatal("name lookup exceeded profile deadline")
	}
	if _, ok := shared.get(42, request.started.Add(time.Second)); ok {
		t.Fatal("profile remained cached beyond the update deadline")
	}
}
