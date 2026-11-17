package main

import (
 "context"
 "github.com/jmoiron/sqlx"
 "github.com/labstack/echo/v4"
)

type responseCacheKey struct{}
type responseCache struct {
 users map[int64]User
 streams map[int64]Livestream
}

// Each request uses one DB snapshot. Reuse responses only within that request.
func responseCacheMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
 return func(c echo.Context) error {
  cache := &responseCache{users: make(map[int64]User), streams: make(map[int64]Livestream)}
  c.SetRequest(c.Request().WithContext(context.WithValue(c.Request().Context(), responseCacheKey{}, cache)))
  return next(c)
 }
}

func fillUserResponse(ctx context.Context, tx *sqlx.Tx, model UserModel) (User, error) {
 cache, _ := ctx.Value(responseCacheKey{}).(*responseCache)
 if cache != nil { if result, ok := cache.users[model.ID]; ok { return result, nil } }
 result, err := fillUserResponseUncached(ctx, tx, model)
 if err == nil && cache != nil { cache.users[model.ID] = result }
 return result, err
}

func fillLivestreamResponse(ctx context.Context, tx *sqlx.Tx, model LivestreamModel) (Livestream, error) {
 cache, _ := ctx.Value(responseCacheKey{}).(*responseCache)
 if cache != nil { if result, ok := cache.streams[model.ID]; ok { return result, nil } }
 result, err := fillLivestreamResponseUncached(ctx, tx, model)
 if err == nil && cache != nil { cache.streams[model.ID] = result }
 return result, err
}

func userResponseByID(ctx context.Context, tx *sqlx.Tx, id int64) (User, error) {
 if cache, _ := ctx.Value(responseCacheKey{}).(*responseCache); cache != nil {
  if result, ok := cache.users[id]; ok { return result, nil }
 }
 var model UserModel
 if err := tx.GetContext(ctx, &model, "SELECT * FROM users WHERE id = ?", id); err != nil { return User{}, err }
 return fillUserResponse(ctx, tx, model)
}

func livestreamResponseByID(ctx context.Context, tx *sqlx.Tx, id int64) (Livestream, error) {
 if cache, _ := ctx.Value(responseCacheKey{}).(*responseCache); cache != nil {
  if result, ok := cache.streams[id]; ok { return result, nil }
 }
 var model LivestreamModel
 if err := tx.GetContext(ctx, &model, "SELECT * FROM livestreams WHERE id = ?", id); err != nil { return Livestream{}, err }
 return fillLivestreamResponse(ctx, tx, model)
}
