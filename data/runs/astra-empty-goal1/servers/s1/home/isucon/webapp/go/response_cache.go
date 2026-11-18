package main

import (
	"context"
	"github.com/jmoiron/sqlx"
)

// Response data is reused only inside a single request and its transaction.
// No entries survive the request, icon updates, or initialization.
type responseCacheKey struct{}
type responseCache struct {
	users   map[int64]User
	streams map[int64]Livestream
	tags    map[int64][]Tag
}

func newResponseContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, responseCacheKey{}, &responseCache{
		users: make(map[int64]User), streams: make(map[int64]Livestream), tags: make(map[int64][]Tag),
	})
}
func cachedUser(ctx context.Context, tx *sqlx.Tx, id int64) (User, error) {
	if cache, ok := ctx.Value(responseCacheKey{}).(*responseCache); ok {
		if user, found := cache.users[id]; found {
			return user, nil
		}
	}
	_, fallback, err := loadFallbackIcon()
	if err != nil {
		return User{}, err
	}
	var user User
	if err := tx.GetContext(ctx, &user, `SELECT u.id, u.name, u.display_name, u.description,
 t.id AS "theme.id", t.dark_mode AS "theme.dark_mode", COALESCE(i.image_hash, ?) AS icon_hash
 FROM users u JOIN themes t ON t.user_id=u.id LEFT JOIN icons i ON i.user_id=u.id WHERE u.id=?`, fallback, id); err != nil {
		return User{}, err
	}
	if cache, ok := ctx.Value(responseCacheKey{}).(*responseCache); ok {
		cache.users[id] = user
	}
	return user, nil
}
func cachedLivestream(ctx context.Context, tx *sqlx.Tx, id int64) (Livestream, error) {
	if cache, ok := ctx.Value(responseCacheKey{}).(*responseCache); ok {
		if stream, found := cache.streams[id]; found {
			return stream, nil
		}
	}
	var model LivestreamModel
	if err := tx.GetContext(ctx, &model, "SELECT * FROM livestreams WHERE id = ?", id); err != nil {
		return Livestream{}, err
	}
	return fillLivestreamResponse(ctx, tx, model)
}

func preloadUsers(ctx context.Context, tx *sqlx.Tx, ids []int64) error {
	cache := ctx.Value(responseCacheKey{}).(*responseCache)
	unique := make(map[int64]bool)
	missing := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, found := cache.users[id]; !found && !unique[id] {
			unique[id] = true
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	_, fallback, err := loadFallbackIcon()
	if err != nil {
		return err
	}
	query, args, err := sqlx.In(`SELECT u.id, u.name, u.display_name, u.description,
 t.id AS "theme.id", t.dark_mode AS "theme.dark_mode", COALESCE(i.image_hash, ?) AS icon_hash
 FROM users u JOIN themes t ON t.user_id=u.id LEFT JOIN icons i ON i.user_id=u.id WHERE u.id IN (?)`, fallback, missing)
	if err != nil {
		return err
	}
	var users []User
	if err := tx.SelectContext(ctx, &users, query, args...); err != nil {
		return err
	}
	for _, user := range users {
		cache.users[user.ID] = user
	}
	return nil
}

func preloadLivestreams(ctx context.Context, tx *sqlx.Tx, models []*LivestreamModel) error {
	if len(models) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(models))
	userIDs := make([]int64, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
		userIDs = append(userIDs, model.UserID)
	}
	if err := preloadUsers(ctx, tx, userIDs); err != nil {
		return err
	}
	query, args, err := sqlx.In(`SELECT lt.livestream_id, t.id, t.name FROM livestream_tags lt JOIN tags t ON t.id=lt.tag_id WHERE lt.livestream_id IN (?) ORDER BY lt.id`, ids)
	if err != nil {
		return err
	}
	var rows []struct {
		LivestreamID int64  `db:"livestream_id"`
		ID           int64  `db:"id"`
		Name         string `db:"name"`
	}
	if err := tx.SelectContext(ctx, &rows, query, args...); err != nil {
		return err
	}
	cache := ctx.Value(responseCacheKey{}).(*responseCache)
	for _, id := range ids {
		cache.tags[id] = []Tag{}
	}
	for _, row := range rows {
		cache.tags[row.LivestreamID] = append(cache.tags[row.LivestreamID], Tag{ID: row.ID, Name: row.Name})
	}
	return nil
}
