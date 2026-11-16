package main

import (
	"github.com/labstack/echo/v4"
	"net/http"
	"sync"
	"time"
)

const responseCacheTTL = 500 * time.Millisecond

type timedUser struct {
	value   User
	expires time.Time
}
type timedStream struct {
	value   Livestream
	expires time.Time
}
type timedIcon struct {
	image   []byte
	hash    string
	expires time.Time
}

var sharedResponses = struct {
	sync.RWMutex
	names   map[string]int64
	users   map[int64]timedUser
	streams map[int64]timedStream
	icons   map[string]timedIcon
}{names: map[string]int64{}, users: map[int64]timedUser{}, streams: map[int64]timedStream{}, icons: map[string]timedIcon{}}

func clearSharedResponses() {
	sharedResponses.Lock()
	defer sharedResponses.Unlock()
	sharedResponses.names = map[string]int64{}
	sharedResponses.users = map[int64]timedUser{}
	sharedResponses.streams = map[int64]timedStream{}
	sharedResponses.icons = map[string]timedIcon{}
}
func resetSharedResponses(c echo.Context) error {
	clearSharedResponses()
	return c.NoContent(http.StatusNoContent)
}
func cachedUser(id int64) (User, bool) {
	sharedResponses.RLock()
	defer sharedResponses.RUnlock()
	v, ok := sharedResponses.users[id]
	return v.value, ok && time.Now().Before(v.expires)
}
func cachedStream(id int64) (Livestream, bool) {
	sharedResponses.RLock()
	defer sharedResponses.RUnlock()
	v, ok := sharedResponses.streams[id]
	return v.value, ok && time.Now().Before(v.expires)
}
func publishResponses(cache *responseCache) {
	expiry := cache.started.Add(responseCacheTTL)
	if !time.Now().Before(expiry) {
		return
	}
	sharedResponses.Lock()
	defer sharedResponses.Unlock()
	if len(sharedResponses.users) > 10000 {
		sharedResponses.names = map[string]int64{}
		sharedResponses.users = map[int64]timedUser{}
	}
	if len(sharedResponses.streams) > 20000 {
		sharedResponses.streams = map[int64]timedStream{}
	}
	for id, u := range cache.users {
		if !cache.freshUsers[id] {
			continue
		}
		if prior := sharedResponses.users[id]; prior.expires.Before(expiry) {
			sharedResponses.users[id] = timedUser{u, expiry}
			sharedResponses.names[u.Name] = id
		}
	}
	for id, s := range cache.streams {
		if !cache.freshStreams[id] {
			continue
		}
		if prior := sharedResponses.streams[id]; prior.expires.Before(expiry) {
			sharedResponses.streams[id] = timedStream{s, expiry}
		}
	}
}

func cachedUsername(name string) (User, bool) {
	sharedResponses.RLock()
	defer sharedResponses.RUnlock()
	id, ok := sharedResponses.names[name]
	if !ok {
		return User{}, false
	}
	v, ok := sharedResponses.users[id]
	return v.value, ok && time.Now().Before(v.expires)
}
