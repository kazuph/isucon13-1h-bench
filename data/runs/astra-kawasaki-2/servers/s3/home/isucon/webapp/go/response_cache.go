package main

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
)

type responseCacheKey struct{}
type responseCache struct {
	users   map[int64]User
	streams map[int64]Livestream
}

func responseCacheMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		cache := &responseCache{users: make(map[int64]User), streams: make(map[int64]Livestream)}
		c.SetRequest(c.Request().WithContext(context.WithValue(c.Request().Context(), responseCacheKey{}, cache)))
		return next(c)
	}
}
func cacheFor(ctx context.Context) *responseCache {
	cache, _ := ctx.Value(responseCacheKey{}).(*responseCache)
	return cache
}

func userResponseByID(ctx context.Context, tx *sqlx.Tx, id int64) (User, error) {
	if cache := cacheFor(ctx); cache != nil {
		if u, ok := cache.users[id]; ok {
			return u, nil
		}
		if err := preloadResponses(ctx, tx, nil, []int64{id}); err != nil {
			return User{}, err
		}
		if u, ok := cache.users[id]; ok {
			return u, nil
		}
		return User{}, sql.ErrNoRows
	}
	var m UserModel
	if err := tx.GetContext(ctx, &m, "SELECT * FROM users WHERE id = ?", id); err != nil {
		return User{}, err
	}
	return fillUserResponse(ctx, tx, m)
}
func streamResponseByID(ctx context.Context, tx *sqlx.Tx, id int64) (Livestream, error) {
	if cache := cacheFor(ctx); cache != nil {
		if l, ok := cache.streams[id]; ok {
			return l, nil
		}
	}
	var m LivestreamModel
	if err := tx.GetContext(ctx, &m, "SELECT * FROM livestreams WHERE id = ?", id); err != nil {
		return Livestream{}, err
	}
	return fillLivestreamResponse(ctx, tx, m)
}

// Preload response relationships in the same transaction snapshot.
func preloadResponses(ctx context.Context, tx *sqlx.Tx, streams []*LivestreamModel, userIDs []int64) error {
	cache := cacheFor(ctx)
	if cache == nil {
		return nil
	}
	ids := make(map[int64]bool)
	for _, id := range userIDs {
		ids[id] = true
	}
	streamIDs := []int64{}
	for _, s := range streams {
		ids[s.UserID] = true
		streamIDs = append(streamIDs, s.ID)
	}
	userIDs = nil
	for id := range ids {
		if _, ok := cache.users[id]; !ok {
			userIDs = append(userIDs, id)
		}
	}
	if len(userIDs) > 0 {
		var rows []struct {
			ID          int64
			Name        string
			DisplayName string `db:"display_name"`
			Description string
			ThemeID     int64  `db:"theme_id"`
			DarkMode    bool   `db:"dark_mode"`
			IconHash    string `db:"icon_hash"`
		}
		q, args, err := sqlx.In(`SELECT u.id,u.name,u.display_name,u.description,t.id theme_id,t.dark_mode,COALESCE(i.image_hash,?) icon_hash FROM users u JOIN themes t ON t.user_id=u.id LEFT JOIN icons i ON i.user_id=u.id WHERE u.id IN (?)`, fallbackHash, userIDs)
		if err != nil {
			return err
		}
		if err = tx.SelectContext(ctx, &rows, q, args...); err != nil {
			return err
		}
		for _, r := range rows {
			cache.users[r.ID] = User{ID: r.ID, Name: r.Name, DisplayName: r.DisplayName, Description: r.Description, Theme: Theme{ID: r.ThemeID, DarkMode: r.DarkMode}, IconHash: r.IconHash}
		}
	}
	if len(streamIDs) > 0 {
		var rows []struct {
			LivestreamID int64 `db:"livestream_id"`
			ID           int64
			Name         string
		}
		q, args, err := sqlx.In(`SELECT lt.livestream_id,t.id,t.name FROM livestream_tags lt JOIN tags t ON t.id=lt.tag_id WHERE lt.livestream_id IN (?) ORDER BY lt.id`, streamIDs)
		if err != nil {
			return err
		}
		if err = tx.SelectContext(ctx, &rows, q, args...); err != nil {
			return err
		}
		tags := make(map[int64][]Tag)
		for _, r := range rows {
			tags[r.LivestreamID] = append(tags[r.LivestreamID], Tag{ID: r.ID, Name: r.Name})
		}
		for _, s := range streams {
			ts := tags[s.ID]
			if ts == nil {
				ts = []Tag{}
			}
			owner, ok := cache.users[s.UserID]
			if !ok {
				return fmt.Errorf("missing owner %d", s.UserID)
			}
			cache.streams[s.ID] = Livestream{ID: s.ID, Owner: owner, Title: s.Title, Description: s.Description, PlaylistUrl: s.PlaylistUrl, ThumbnailUrl: s.ThumbnailUrl, Tags: ts, StartAt: s.StartAt, EndAt: s.EndAt}
		}
	}
	return nil
}
