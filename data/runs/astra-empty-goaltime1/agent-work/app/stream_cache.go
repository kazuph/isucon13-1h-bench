package main

import (
	"context"
	"sync"
	"sync/atomic"
)

// Streams and their tag assignments are immutable after reservation. Cache
// only metadata, never the owner response whose icon hash changes over time.
type streamMetadata struct {
	model LivestreamModel
	tags  []Tag
}
type streamMetadataCache struct {
	sync.RWMutex
	entries map[int64]streamMetadata
}

var activeStreamMetadata atomic.Pointer[streamMetadataCache]

func init() { clearStreamMetadataCache() }
func clearStreamMetadataCache() {
	activeStreamMetadata.Store(&streamMetadataCache{entries: make(map[int64]streamMetadata)})
}
func (cache *streamMetadataCache) get(id int64) (streamMetadata, bool) {
	cache.RLock()
	value, ok := cache.entries[id]
	cache.RUnlock()
	return value, ok
}
func rememberStreamMetadata(ctx context.Context, model LivestreamModel, tags []Tag) {
	request := requestCache(ctx)
	if request == nil || !request.readOnly {
		return
	}
	cache := request.sharedStreams
	cache.Lock()
	if len(cache.entries) >= 20000 {
		cache.entries = make(map[int64]streamMetadata)
	}
	cache.entries[model.ID] = streamMetadata{model: model, tags: append([]Tag{}, tags...)}
	cache.Unlock()
}
