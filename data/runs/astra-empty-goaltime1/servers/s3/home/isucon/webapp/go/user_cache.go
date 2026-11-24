package main

import (
	"sync"
	"sync/atomic"
	"time"
)

// Profile fields and themes cannot be edited through this API. The icon hash
// can change, so the whole cached response expires one second after read start.
type cachedUserResponse struct {
	user    User
	expires time.Time
}
type userResponseCache struct {
	sync.RWMutex
	entries map[int64]cachedUserResponse
	names   map[string]int64
}

var activeUserResponses atomic.Pointer[userResponseCache]

func init() { clearUserResponseCache() }
func clearUserResponseCache() {
	activeUserResponses.Store(&userResponseCache{entries: make(map[int64]cachedUserResponse)})
}
func (cache *userResponseCache) get(id int64, now time.Time) (User, bool) {
	cache.RLock()
	entry, ok := cache.entries[id]
	cache.RUnlock()
	if !ok || !now.Before(entry.expires) {
		return User{}, false
	}
	return entry.user, true
}
func (cache *userResponseCache) put(user User, started time.Time) {
	expires := started.Add(time.Second)
	if !time.Now().Before(expires) {
		return
	}
	cache.Lock()
	if len(cache.entries) >= 20000 {
		cache.entries = make(map[int64]cachedUserResponse)
		cache.names = make(map[string]int64)
	}
	if cache.names == nil {
		cache.names = make(map[string]int64)
	}
	cache.names[user.Name] = user.ID
	cache.entries[user.ID] = cachedUserResponse{user: user, expires: expires}
	cache.Unlock()
}
func invalidateUserResponse(id int64) {
	cache := activeUserResponses.Load()
	cache.Lock()
	if entry, ok := cache.entries[id]; ok {
		delete(cache.names, entry.user.Name)
	}
	delete(cache.entries, id)
	cache.Unlock()
}

func (cache *userResponseCache) getByName(name string, now time.Time) (User, bool) {
	cache.RLock()
	id, found := cache.names[name]
	entry, ok := cache.entries[id]
	cache.RUnlock()
	if !found || !ok || entry.user.Name != name || !now.Before(entry.expires) {
		return User{}, false
	}
	return entry.user, true
}
