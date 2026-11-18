package main

import (
 "context"
 "sync"
 "time"
 "github.com/jmoiron/sqlx"
 "github.com/labstack/echo/v4"
)

const responseCacheTTL = 250 * time.Millisecond

type responseCacheKey struct{}
type responseCache struct {
 started time.Time
 users map[int64]User
 streams map[int64]Livestream
 freshUsers map[int64]bool
 freshStreams map[int64]bool
}
type cachedUser struct { value User; expires time.Time }
type cachedStream struct { value Livestream; expires time.Time }
var sharedResponses = struct {
 sync.RWMutex
 users map[int64]cachedUser
 streams map[int64]cachedStream
}{users: make(map[int64]cachedUser), streams: make(map[int64]cachedStream)}

func clearResponseCaches() {
 sharedResponses.Lock()
 sharedResponses.users = make(map[int64]cachedUser)
 sharedResponses.streams = make(map[int64]cachedStream)
 sharedResponses.Unlock()
}

// Never publish uncommitted responses, or extend an existing cache entry's TTL.
// Anchor expiry to request start so slow transactions cannot refresh stale data.
func responseCacheMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
 return func(c echo.Context) error {
  cache := &responseCache{started:time.Now(), users: make(map[int64]User), streams: make(map[int64]Livestream), freshUsers:make(map[int64]bool), freshStreams:make(map[int64]bool)}
  c.SetRequest(c.Request().WithContext(context.WithValue(c.Request().Context(), responseCacheKey{}, cache)))
  err := next(c)
  expires := cache.started.Add(responseCacheTTL)
  if err == nil && c.Response().Status < 400 && time.Now().Before(expires) {
   sharedResponses.Lock()
   if len(sharedResponses.users) > 10000 { sharedResponses.users = make(map[int64]cachedUser) }
   if len(sharedResponses.streams) > 10000 { sharedResponses.streams = make(map[int64]cachedStream) }
   for id := range cache.freshUsers { sharedResponses.users[id] = cachedUser{cache.users[id],expires} }
   for id := range cache.freshStreams { sharedResponses.streams[id] = cachedStream{cache.streams[id],expires} }
   sharedResponses.Unlock()
  }
  return err
 }
}

func lookupUser(ctx context.Context, id int64) (User, bool) {
 cache, _ := ctx.Value(responseCacheKey{}).(*responseCache)
 if cache != nil { if result, ok := cache.users[id]; ok { return result, true } }
 sharedResponses.RLock()
 entry, ok := sharedResponses.users[id]
 sharedResponses.RUnlock()
 if ok && time.Now().Before(entry.expires) {
  if cache != nil { cache.users[id] = entry.value }
  return entry.value, true
 }
 return User{}, false
}
func lookupStream(ctx context.Context, id int64) (Livestream, bool) {
 cache, _ := ctx.Value(responseCacheKey{}).(*responseCache)
 if cache != nil { if result, ok := cache.streams[id]; ok { return result, true } }
 sharedResponses.RLock()
 entry, ok := sharedResponses.streams[id]
 sharedResponses.RUnlock()
 if ok && time.Now().Before(entry.expires) {
  if cache != nil { cache.streams[id] = entry.value }
  return entry.value, true
 }
 return Livestream{}, false
}

func fillUserResponse(ctx context.Context, tx *sqlx.Tx, model UserModel) (User, error) {
 if result, ok := lookupUser(ctx, model.ID); ok && result.Name == model.Name { return result, nil }
 result, err := fillUserResponseUncached(ctx, tx, model)
 cache, _ := ctx.Value(responseCacheKey{}).(*responseCache)
 if err == nil && cache != nil { cache.users[model.ID] = result; cache.freshUsers[model.ID] = true }
 return result, err
}
func fillLivestreamResponse(ctx context.Context, tx *sqlx.Tx, model LivestreamModel) (Livestream, error) {
 if result, ok := lookupStream(ctx, model.ID); ok && result.Owner.ID == model.UserID { return result, nil }
 result, err := fillLivestreamResponseUncached(ctx, tx, model)
 cache, _ := ctx.Value(responseCacheKey{}).(*responseCache)
 if err == nil && cache != nil { cache.streams[model.ID] = result; cache.freshStreams[model.ID] = true }
 return result, err
}
func userResponseByID(ctx context.Context, tx *sqlx.Tx, id int64) (User, error) {
 if result, ok := lookupUser(ctx,id); ok { return result, nil }
 var model UserModel
 if err := tx.GetContext(ctx, &model, "SELECT * FROM users WHERE id = ?", id); err != nil { return User{}, err }
 return fillUserResponse(ctx, tx, model)
}
func livestreamResponseByID(ctx context.Context, tx *sqlx.Tx, id int64) (Livestream, error) {
 if result, ok := lookupStream(ctx,id); ok { return result, nil }
 var model LivestreamModel
 if err := tx.GetContext(ctx, &model, "SELECT * FROM livestreams WHERE id = ?", id); err != nil { return Livestream{}, err }
 return fillLivestreamResponse(ctx, tx, model)
}
