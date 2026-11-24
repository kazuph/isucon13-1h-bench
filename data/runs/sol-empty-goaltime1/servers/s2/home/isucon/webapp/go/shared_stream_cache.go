package main

import (
	"context"
	"github.com/jmoiron/sqlx"
	"sync"
)

// Stream metadata and tag associations have no update API. Initialize clears
// both nodes, while write transactions never publish uncommitted reservations.
var sharedStreams = struct {
	sync.RWMutex
	epoch  uint64
	models map[int64]LivestreamModel
	tags   map[int64][]Tag
}{models: make(map[int64]LivestreamModel), tags: make(map[int64][]Tag)}

func currentStreamCacheEpoch() uint64 {
	sharedStreams.RLock()
	defer sharedStreams.RUnlock()
	return sharedStreams.epoch
}
func resetSharedStreams() {
	sharedStreams.Lock()
	defer sharedStreams.Unlock()
	sharedStreams.epoch++
	sharedStreams.models = make(map[int64]LivestreamModel)
	sharedStreams.tags = make(map[int64][]Tag)
}
func sharedLivestreamModel(id int64) (LivestreamModel, bool) {
	sharedStreams.RLock()
	defer sharedStreams.RUnlock()
	model, ok := sharedStreams.models[id]
	return model, ok
}
func rememberLivestreamModel(ctx context.Context, model LivestreamModel) {
	cache := requestCache(ctx)
	if cache == nil || !cache.shareUsers {
		return
	}
	sharedStreams.Lock()
	defer sharedStreams.Unlock()
	if cache.streamEpoch != sharedStreams.epoch {
		return
	}
	if len(sharedStreams.models) >= 16384 {
		sharedStreams.models = make(map[int64]LivestreamModel)
		sharedStreams.tags = make(map[int64][]Tag)
	}
	sharedStreams.models[model.ID] = model
}
func sharedLivestreamTags(id int64) ([]Tag, bool) {
	sharedStreams.RLock()
	defer sharedStreams.RUnlock()
	tags, ok := sharedStreams.tags[id]
	return tags, ok
}
func rememberLivestreamTags(ctx context.Context, id int64, tags []Tag) {
	cache := requestCache(ctx)
	if cache == nil || !cache.shareUsers {
		return
	}
	sharedStreams.Lock()
	defer sharedStreams.Unlock()
	if cache.streamEpoch != sharedStreams.epoch {
		return
	}
	if len(sharedStreams.tags) >= 16384 {
		sharedStreams.models = make(map[int64]LivestreamModel)
		sharedStreams.tags = make(map[int64][]Tag)
	}
	sharedStreams.tags[id] = append([]Tag{}, tags...)
}
func loadLivestreamTags(ctx context.Context, tx *sqlx.Tx, id int64) ([]Tag, error) {
	if tags, ok := sharedLivestreamTags(id); ok {
		return tags, nil
	}
	tags := []Tag{}
	if err := tx.SelectContext(ctx, &tags, "SELECT t.id, t.name FROM livestream_tags lt JOIN tags t ON t.id=lt.tag_id WHERE lt.livestream_id=? ORDER BY lt.id", id); err != nil {
		return nil, err
	}
	rememberLivestreamTags(ctx, id, tags)
	return tags, nil
}
