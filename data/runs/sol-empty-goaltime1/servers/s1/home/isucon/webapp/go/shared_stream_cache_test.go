package main

import (
	"context"
	"testing"
)

func TestStreamCachePublishesOnlyCommittedReads(t *testing.T) {
	resetSharedStreams()
	defer resetSharedStreams()
	cache := &responseCache{streamEpoch: currentStreamCacheEpoch()}
	ctx := context.WithValue(context.Background(), responseCacheKey{}, cache)
	model := LivestreamModel{ID: 55, Title: "stream"}
	tags := []Tag{{ID: 1, Name: "original"}}
	rememberLivestreamModel(ctx, model)
	rememberLivestreamTags(ctx, model.ID, tags)
	if _, ok := sharedLivestreamModel(model.ID); ok {
		t.Fatal("published write transaction model")
	}
	if _, ok := sharedLivestreamTags(model.ID); ok {
		t.Fatal("published write transaction tags")
	}
	cache.shareUsers = true
	rememberLivestreamModel(ctx, model)
	rememberLivestreamTags(ctx, model.ID, tags)
	tags[0].Name = "changed"
	if got, ok := sharedLivestreamModel(model.ID); !ok || got.Title != model.Title {
		t.Fatal("missing committed stream")
	}
	if got, ok := sharedLivestreamTags(model.ID); !ok || got[0].Name != "original" {
		t.Fatal("tags not retained as immutable copy")
	}
	resetSharedStreams()
	rememberLivestreamModel(ctx, model)
	rememberLivestreamTags(ctx, model.ID, tags)
	if _, ok := sharedLivestreamModel(model.ID); ok {
		t.Fatal("pre-initialize request resurrected model")
	}
	if _, ok := sharedLivestreamTags(model.ID); ok {
		t.Fatal("pre-initialize request resurrected tags")
	}
}
