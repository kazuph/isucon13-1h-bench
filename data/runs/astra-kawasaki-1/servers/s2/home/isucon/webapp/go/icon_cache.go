package main

import (
	"sync"
	"time"
)

type iconCacheEntry struct {
	hash    string
	image   []byte
	hasBody bool
	expires time.Time
}

var iconCache = struct {
	sync.RWMutex
	entries map[string]iconCacheEntry
}{entries: make(map[string]iconCacheEntry)}

func cachedIcon(username, validator string) (iconCacheEntry, bool) {
	iconCache.RLock()
	v, ok := iconCache.entries[username]
	iconCache.RUnlock()
	if !ok || !time.Now().Before(v.expires) {
		return iconCacheEntry{}, false
	}
	if validator == "\""+v.hash+"\"" || v.hasBody {
		return v, true
	}
	return iconCacheEntry{}, false
}
func storeIcon(username string, v iconCacheEntry) {
	iconCache.Lock()
	defer iconCache.Unlock()
	if len(iconCache.entries) > 4096 {
		now := time.Now()
		for k, e := range iconCache.entries {
			if now.After(e.expires) {
				delete(iconCache.entries, k)
			}
		}
	}
	iconCache.entries[username] = v
}
func resetIconCache() {
	iconCache.Lock()
	iconCache.entries = make(map[string]iconCacheEntry)
	iconCache.Unlock()
}
