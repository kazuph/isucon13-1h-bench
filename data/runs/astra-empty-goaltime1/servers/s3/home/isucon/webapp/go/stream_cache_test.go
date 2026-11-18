package main

import (
	"context"
	"testing"
)

func TestStreamMetadataCacheIsolatesWritesAndInitialization(t *testing.T) {
	clearStreamMetadataCache()
	shared := activeStreamMetadata.Load()
	request := &responseCache{sharedStreams: shared}
	ctx := context.WithValue(context.Background(), responseCacheKey{}, request)
	model := LivestreamModel{ID: 42, Title: "original"}
	rememberStreamMetadata(ctx, model, []Tag{{ID: 1, Name: "tag"}})
	if _, ok := shared.get(42); ok {
		t.Fatal("uncommitted reservation was cached")
	}
	request.readOnly = true
	rememberStreamMetadata(ctx, model, []Tag{{ID: 1, Name: "tag"}})
	if got, ok := shared.get(42); !ok || got.model.Title != "original" || len(got.tags) != 1 {
		t.Fatal("committed metadata was not cached correctly")
	}
	clearStreamMetadataCache()
	rememberStreamMetadata(ctx, model, []Tag{{ID: 1, Name: "tag"}})
	if _, ok := activeStreamMetadata.Load().get(42); ok {
		t.Fatal("pre-initialization metadata leaked into the new generation")
	}
}
