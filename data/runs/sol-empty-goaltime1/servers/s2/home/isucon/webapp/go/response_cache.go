package main

import (
	"context"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"time"
)

// All entries live only for one request and its database snapshot.
type responseCacheKey struct{}
type responseCache struct {
	streamEpoch  uint64
	created      time.Time
	epoch        uint64
	shareUsers   bool
	users        map[int64]User
	streams      map[int64]Livestream
	streamModels map[int64]LivestreamModel
}

func withResponseCache(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		cache := &responseCache{created: time.Now(), epoch: currentUserCacheEpoch(), streamEpoch: currentStreamCacheEpoch(), shareUsers: c.Request().Method == "GET"}
		c.SetRequest(c.Request().WithContext(context.WithValue(c.Request().Context(), responseCacheKey{}, cache)))
		return next(c)
	}
}
func requestCache(ctx context.Context) *responseCache {
	cache, _ := ctx.Value(responseCacheKey{}).(*responseCache)
	return cache
}
func loadLivestreamModel(ctx context.Context, tx *sqlx.Tx, id int64) (LivestreamModel, error) {
	cache := requestCache(ctx)
	if cache != nil {
		if m, ok := cache.streamModels[id]; ok {
			return m, nil
		}
	}
	if model, ok := sharedLivestreamModel(id); ok {
		return model, nil
	}
	var m LivestreamModel
	err := tx.GetContext(ctx, &m, "SELECT * FROM livestreams WHERE id = ?", id)
	if err == nil && cache != nil {
		if cache.streamModels == nil {
			cache.streamModels = make(map[int64]LivestreamModel)
		}
		cache.streamModels[id] = m
	}
	if err == nil {
		rememberLivestreamModel(ctx, m)
	}
	return m, err
}
