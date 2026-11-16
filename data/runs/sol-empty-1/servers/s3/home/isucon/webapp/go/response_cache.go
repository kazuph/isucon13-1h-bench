package main

import (
	"context"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	"time"
)

type responseCacheKey struct{}
type responseCache struct {
	freshUsers   map[int64]bool
	freshStreams map[int64]bool
	started      time.Time
	users        map[int64]User
	streams      map[int64]Livestream
}

func responseCacheMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		cache := &responseCache{freshUsers: map[int64]bool{}, freshStreams: map[int64]bool{}, started: time.Now(), users: make(map[int64]User), streams: make(map[int64]Livestream)}
		c.SetRequest(c.Request().WithContext(context.WithValue(c.Request().Context(), responseCacheKey{}, cache)))
		err := next(c)
		if err == nil {
			publishResponses(cache)
		}
		return err
	}
}
func cacheFrom(ctx context.Context) *responseCache {
	return ctx.Value(responseCacheKey{}).(*responseCache)
}

// Populate user responses in three bounded queries, without transferring icon images.
func preloadUsers(ctx context.Context, tx *sqlx.Tx, ids []int64) error {
	cache := cacheFrom(ctx)
	missing := make([]int64, 0, len(ids))
	seen := map[int64]bool{}
	for _, id := range ids {
		if _, ok := cache.users[id]; ok {
			continue
		}
		if u, ok := cachedUser(id); ok {
			cache.users[id] = u
			continue
		}
		if !seen[id] {
			missing = append(missing, id)
			seen[id] = true
		}
	}
	if len(missing) == 0 {
		return nil
	}
	var users []struct {
		UserModel
		ThemeID  int64  `db:"theme_id"`
		DarkMode bool   `db:"dark_mode"`
		IconHash string `db:"icon_hash"`
	}
	q, args, err := sqlx.In(`SELECT u.*,t.id AS theme_id,t.dark_mode,COALESCE(i.hash,?) AS icon_hash FROM users u JOIN themes t ON t.user_id=u.id LEFT JOIN icons i ON i.user_id=u.id WHERE u.id IN (?)`, fallbackImageHash, missing)
	if err != nil {
		return err
	}
	if err = tx.SelectContext(ctx, &users, q, args...); err != nil {
		return err
	}
	for _, u := range users {
		cache.freshUsers[u.ID] = true
		cache.users[u.ID] = User{ID: u.ID, Name: u.Name, DisplayName: u.DisplayName, Description: u.Description, Theme: Theme{ID: u.ThemeID, DarkMode: u.DarkMode}, IconHash: u.IconHash}
	}

	return nil
}
func preloadStreams(ctx context.Context, tx *sqlx.Tx, models []*LivestreamModel) error {
	cache := cacheFrom(ctx)
	ids := []int64{}
	owners := []int64{}
	for _, m := range models {
		if _, ok := cache.streams[m.ID]; ok {
			continue
		}
		if v, ok := cachedStream(m.ID); ok {
			cache.streams[m.ID] = v
			continue
		}
		ids = append(ids, m.ID)
		owners = append(owners, m.UserID)
	}
	if len(ids) == 0 {
		return nil
	}
	if err := preloadUsers(ctx, tx, owners); err != nil {
		return err
	}
	var tags []struct {
		LivestreamID int64 `db:"livestream_id"`
		ID           int64
		Name         string
	}
	q, args, err := sqlx.In("SELECT lt.livestream_id,t.id,t.name FROM livestream_tags lt JOIN tags t ON t.id=lt.tag_id WHERE lt.livestream_id IN (?) ORDER BY lt.id", ids)
	if err != nil {
		return err
	}
	if err = tx.SelectContext(ctx, &tags, q, args...); err != nil {
		return err
	}
	tagMap := map[int64][]Tag{}
	for _, t := range tags {
		tagMap[t.LivestreamID] = append(tagMap[t.LivestreamID], Tag{ID: t.ID, Name: t.Name})
	}
	for _, m := range models {
		if _, ok := cache.streams[m.ID]; ok {
			continue
		}
		tags := tagMap[m.ID]
		if tags == nil {
			tags = []Tag{}
		}
		cache.freshStreams[m.ID] = true
		cache.streams[m.ID] = Livestream{ID: m.ID, Owner: cache.users[m.UserID], Title: m.Title, Description: m.Description, PlaylistUrl: m.PlaylistUrl, ThumbnailUrl: m.ThumbnailUrl, Tags: tags, StartAt: m.StartAt, EndAt: m.EndAt}
	}
	return nil
}
func loadStream(ctx context.Context, tx *sqlx.Tx, id int64) (Livestream, error) {
	cache := cacheFrom(ctx)
	if s, ok := cache.streams[id]; ok {
		return s, nil
	}
	if s, ok := cachedStream(id); ok {
		cache.streams[id] = s
		return s, nil
	}
	var m LivestreamModel
	if err := tx.GetContext(ctx, &m, "SELECT * FROM livestreams WHERE id=?", id); err != nil {
		return Livestream{}, err
	}
	if err := preloadStreams(ctx, tx, []*LivestreamModel{&m}); err != nil {
		return Livestream{}, err
	}
	return cache.streams[id], nil
}
func loadUser(ctx context.Context, tx *sqlx.Tx, id int64) (User, error) {
	if err := preloadUsers(ctx, tx, []int64{id}); err != nil {
		return User{}, err
	}
	return cacheFrom(ctx).users[id], nil
}
