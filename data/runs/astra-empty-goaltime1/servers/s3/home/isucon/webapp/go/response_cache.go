package main

import (
	"context"
	"database/sql"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"net/http"
	"time"
)

// The users and streams maps are transaction-local. Shared caches are captured
// at request start so in-flight reads cannot repopulate a new initialization.
type responseCacheKey struct{}
type responseCache struct {
	started       time.Time
	sharedUsers   *userResponseCache
	sharedStreams *streamMetadataCache
	readOnly      bool
	users         map[int64]User
	streams       map[int64]Livestream
}

func responseCacheMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		cache := &responseCache{started: time.Now(), sharedUsers: activeUserResponses.Load(), sharedStreams: activeStreamMetadata.Load(), readOnly: c.Request().Method == http.MethodGet, users: make(map[int64]User), streams: make(map[int64]Livestream)}
		c.SetRequest(c.Request().WithContext(context.WithValue(c.Request().Context(), responseCacheKey{}, cache)))
		return next(c)
	}
}
func requestCache(ctx context.Context) *responseCache {
	cache, _ := ctx.Value(responseCacheKey{}).(*responseCache)
	return cache
}
func userResponseByID(ctx context.Context, tx *sqlx.Tx, id int64) (User, error) {
	if cache := requestCache(ctx); cache != nil {
		if user, ok := cache.users[id]; ok {
			return user, nil
		}
	}
	if cache := requestCache(ctx); cache != nil {
		if user, ok := cache.sharedUsers.get(id, time.Now()); ok {
			cache.users[id] = user
			return user, nil
		}
	}
	var row userResponseRow
	if err := tx.GetContext(ctx, &row, `SELECT u.*, t.id AS theme_id, t.dark_mode, i.image_hash
        FROM users u JOIN themes t ON t.user_id=u.id LEFT JOIN icons i ON i.user_id=u.id WHERE u.id=?`, id); err != nil {
		return User{}, err
	}
	return row.response(ctx)
}
func livestreamResponseByID(ctx context.Context, tx *sqlx.Tx, id int64) (Livestream, error) {
	if cache := requestCache(ctx); cache != nil {
		if stream, ok := cache.streams[id]; ok {
			return stream, nil
		}
	}
	if cache := requestCache(ctx); cache != nil {
		if metadata, ok := cache.sharedStreams.get(id); ok {
			return fillLivestreamResponse(ctx, tx, metadata.model)
		}
	}
	var model LivestreamModel
	if err := tx.GetContext(ctx, &model, "SELECT * FROM livestreams WHERE id = ?", id); err != nil {
		return Livestream{}, err
	}
	return fillLivestreamResponse(ctx, tx, model)
}

// Preload related data in a fixed number of queries within the same snapshot.
func preloadResponses(ctx context.Context, tx *sqlx.Tx, models []*LivestreamModel, userIDs, streamIDs []int64) error {
	cache := requestCache(ctx)
	if cache == nil {
		return nil
	}
	streams := make(map[int64]*LivestreamModel, len(models))
	for _, model := range models {
		if _, ok := cache.streams[model.ID]; !ok {
			streams[model.ID] = model
		}
	}
	missingStreams := make(map[int64]bool)
	for _, id := range streamIDs {
		if _, ok := cache.streams[id]; ok {
			continue
		}
		if _, ok := streams[id]; !ok {
			if metadata, ok := cache.sharedStreams.get(id); ok {
				model := metadata.model
				streams[id] = &model
			} else {
				missingStreams[id] = true
			}
		}
	}
	if len(missingStreams) != 0 {
		ids := make([]int64, 0, len(missingStreams))
		for id := range missingStreams {
			ids = append(ids, id)
		}
		query, args, err := sqlx.In("SELECT * FROM livestreams WHERE id IN (?)", ids)
		if err != nil {
			return err
		}
		var loaded []*LivestreamModel
		if err := tx.SelectContext(ctx, &loaded, query, args...); err != nil {
			return err
		}
		for _, model := range loaded {
			streams[model.ID] = model
		}
	}
	missingUsers := make(map[int64]bool)
	for _, id := range userIDs {
		if _, ok := cache.users[id]; !ok {
			missingUsers[id] = true
		}
	}
	for _, model := range streams {
		if _, ok := cache.users[model.UserID]; !ok {
			missingUsers[model.UserID] = true
		}
	}
	for id := range missingUsers {
		if user, ok := cache.sharedUsers.get(id, time.Now()); ok {
			cache.users[id] = user
			delete(missingUsers, id)
		}
	}
	if len(missingUsers) != 0 {
		ids := make([]int64, 0, len(missingUsers))
		for id := range missingUsers {
			ids = append(ids, id)
		}
		query, args, err := sqlx.In(`SELECT u.*,t.id AS theme_id,t.dark_mode,i.image_hash
            FROM users u JOIN themes t ON t.user_id=u.id LEFT JOIN icons i ON i.user_id=u.id WHERE u.id IN (?)`, ids)
		if err != nil {
			return err
		}
		var rows []userResponseRow
		if err := tx.SelectContext(ctx, &rows, query, args...); err != nil {
			return err
		}
		for _, row := range rows {
			if _, err := row.response(ctx); err != nil {
				return err
			}
		}
	}
	if len(streams) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(streams))
	tags := make(map[int64][]Tag, len(streams))
	needsStore := make(map[int64]bool)
	for id := range streams {
		if metadata, ok := cache.sharedStreams.get(id); ok {
			tags[id] = metadata.tags
		} else {
			ids = append(ids, id)
			needsStore[id] = true
		}
	}
	if len(ids) != 0 {
		query, args, err := sqlx.In(`SELECT lt.livestream_id,t.id,t.name FROM livestream_tags lt
            JOIN tags t ON t.id=lt.tag_id WHERE lt.livestream_id IN (?) ORDER BY lt.id`, ids)
		if err != nil {
			return err
		}
		var rows []struct {
			StreamID int64  `db:"livestream_id"`
			ID       int64  `db:"id"`
			Name     string `db:"name"`
		}
		if err := tx.SelectContext(ctx, &rows, query, args...); err != nil {
			return err
		}
		for _, row := range rows {
			tags[row.StreamID] = append(tags[row.StreamID], Tag{ID: row.ID, Name: row.Name})
		}
	}
	for id, model := range streams {
		owner, ok := cache.users[model.UserID]
		if !ok {
			return sql.ErrNoRows
		}
		streamTags := tags[id]
		if streamTags == nil {
			streamTags = []Tag{}
		}
		if needsStore[id] {
			rememberStreamMetadata(ctx, *model, streamTags)
		}
		cache.streams[id] = Livestream{ID: id, Owner: owner, Title: model.Title, Description: model.Description,
			PlaylistUrl: model.PlaylistUrl, ThumbnailUrl: model.ThumbnailUrl, StartAt: model.StartAt, EndAt: model.EndAt, Tags: streamTags}
	}
	return nil
}
