package main

import (
	"context"
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

func getUserResponse(ctx context.Context, tx *sqlx.Tx, id int64) (User, error) {
	cache, _ := ctx.Value(responseCacheKey{}).(*responseCache)
	if cache != nil {
		if u, ok := cache.users[id]; ok {
			return u, nil
		}
	}
	var row struct {
		ID          int64  `db:"id"`
		Name        string `db:"name"`
		DisplayName string `db:"display_name"`
		Description string `db:"description"`
		ThemeID     int64  `db:"theme_id"`
		DarkMode    bool   `db:"dark_mode"`
		IconHash    string `db:"icon_hash"`
	}
	err := tx.GetContext(ctx, &row, `SELECT u.id,u.name,u.display_name,u.description,t.id theme_id,t.dark_mode,COALESCE(i.image_hash,?) icon_hash FROM users u JOIN themes t ON t.user_id=u.id LEFT JOIN icons i ON i.user_id=u.id WHERE u.id=?`, fallbackIconHash, id)
	if err != nil {
		return User{}, err
	}
	u := User{ID: row.ID, Name: row.Name, DisplayName: row.DisplayName, Description: row.Description, Theme: Theme{ID: row.ThemeID, DarkMode: row.DarkMode}, IconHash: row.IconHash}
	if cache != nil {
		cache.users[id] = u
	}
	return u, nil
}

func getLivestreamResponse(ctx context.Context, tx *sqlx.Tx, id int64) (Livestream, error) {
	cache, _ := ctx.Value(responseCacheKey{}).(*responseCache)
	if cache != nil {
		if l, ok := cache.streams[id]; ok {
			return l, nil
		}
	}
	var m LivestreamModel
	if err := tx.GetContext(ctx, &m, "SELECT * FROM livestreams WHERE id=?", id); err != nil {
		return Livestream{}, err
	}
	return fillLivestreamResponse(ctx, tx, m)
}

// Preload related objects once per request, in the same database snapshot.
func preloadResponses(ctx context.Context, tx *sqlx.Tx, userIDs, streamIDs []int64, models []*LivestreamModel) error {
	cache := ctx.Value(responseCacheKey{}).(*responseCache)
	if len(streamIDs) > 0 {
		ids := make([]int64, 0, len(streamIDs))
		seen := map[int64]bool{}
		for _, id := range streamIDs {
			if _, ok := cache.streams[id]; !ok && !seen[id] {
				ids = append(ids, id)
				seen[id] = true
			}
		}
		if len(ids) > 0 {
			q, args, err := sqlx.In("SELECT * FROM livestreams WHERE id IN (?)", ids)
			if err != nil {
				return err
			}
			if err = tx.SelectContext(ctx, &models, q, args...); err != nil {
				return err
			}
		}
	}
	for _, m := range models {
		userIDs = append(userIDs, m.UserID)
	}
	ids := make([]int64, 0, len(userIDs))
	seen := map[int64]bool{}
	for _, id := range userIDs {
		if _, ok := cache.users[id]; !ok && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	if len(ids) > 0 {
		var rows []struct {
			ID          int64  `db:"id"`
			Name        string `db:"name"`
			DisplayName string `db:"display_name"`
			Description string `db:"description"`
			ThemeID     int64  `db:"theme_id"`
			DarkMode    bool   `db:"dark_mode"`
			IconHash    string `db:"icon_hash"`
		}
		q, args, err := sqlx.In(`SELECT u.id,u.name,u.display_name,u.description,t.id theme_id,t.dark_mode,COALESCE(i.image_hash,?) icon_hash FROM users u JOIN themes t ON t.user_id=u.id LEFT JOIN icons i ON i.user_id=u.id WHERE u.id IN (?)`, fallbackIconHash, ids)
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
	if len(models) > 0 {
		ids = ids[:0]
		for _, m := range models {
			ids = append(ids, m.ID)
		}
		var rows []struct {
			StreamID int64  `db:"livestream_id"`
			ID       int64  `db:"id"`
			Name     string `db:"name"`
		}
		q, args, err := sqlx.In("SELECT lt.livestream_id,t.id,t.name FROM livestream_tags lt JOIN tags t ON t.id=lt.tag_id WHERE lt.livestream_id IN (?) ORDER BY lt.id", ids)
		if err != nil {
			return err
		}
		if err = tx.SelectContext(ctx, &rows, q, args...); err != nil {
			return err
		}
		tags := map[int64][]Tag{}
		for _, r := range rows {
			tags[r.StreamID] = append(tags[r.StreamID], Tag{ID: r.ID, Name: r.Name})
		}
		for _, m := range models {
			ts := tags[m.ID]
			if ts == nil {
				ts = []Tag{}
			}
			cache.streams[m.ID] = Livestream{ID: m.ID, Owner: cache.users[m.UserID], Title: m.Title, Description: m.Description, PlaylistUrl: m.PlaylistUrl, ThumbnailUrl: m.ThumbnailUrl, StartAt: m.StartAt, EndAt: m.EndAt, Tags: ts}
		}
	}
	return nil
}
