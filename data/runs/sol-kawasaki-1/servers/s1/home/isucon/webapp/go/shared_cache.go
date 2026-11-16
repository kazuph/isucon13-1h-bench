package main

import (
	"sync"
	"time"
)

type cachedUser struct {
	user    User
	expires time.Time
}
type cachedStream struct {
	stream  Livestream
	expires time.Time
}

var sharedResponses = struct {
	sync.RWMutex
	users   map[int64]cachedUser
	streams map[int64]cachedStream
}{users: make(map[int64]cachedUser), streams: make(map[int64]cachedStream)}

func cachedUserResponse(id int64) (User, bool) {
	sharedResponses.RLock()
	v, ok := sharedResponses.users[id]
	sharedResponses.RUnlock()
	return v.user, ok && time.Now().Before(v.expires)
}
func storeUserResponse(id int64, user User, asOf time.Time) {
	sharedResponses.Lock()
	sharedResponses.users[id] = cachedUser{user: user, expires: asOf.Add(500 * time.Millisecond)}
	sharedResponses.Unlock()
}
func cachedStreamResponse(id int64) (Livestream, bool) {
	sharedResponses.RLock()
	v, ok := sharedResponses.streams[id]
	sharedResponses.RUnlock()
	return v.stream, ok && time.Now().Before(v.expires)
}
func storeStreamResponse(id int64, stream Livestream, asOf time.Time) {
	sharedResponses.Lock()
	sharedResponses.streams[id] = cachedStream{stream: stream, expires: asOf.Add(250 * time.Millisecond)}
	sharedResponses.Unlock()
}
func resetSharedResponses() {
	sharedResponses.Lock()
	sharedResponses.users = make(map[int64]cachedUser)
	sharedResponses.streams = make(map[int64]cachedStream)
	sharedResponses.Unlock()
}
