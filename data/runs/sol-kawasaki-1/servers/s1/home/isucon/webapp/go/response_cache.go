package main

import (
	"context"
	"github.com/jmoiron/sqlx"
	"time"
)

func loadUserResponse(ctx context.Context, tx responseReader, id int64) (User, error) {
	cache, _ := ctx.Value(responseCacheKey{}).(*responseCache)
	if cache != nil {
		if u, ok := cache.users[id]; ok {
			return u, nil
		}
	}
	if u, ok := cachedUserResponse(id); ok {
		if cache != nil {
			cache.users[id] = u
		}
		return u, nil
	}
	asOf := time.Now()
	if cache != nil {
		asOf = cache.asOf
	}
	var u UserModel
	if err := tx.GetContext(ctx, &u, "SELECT * FROM users WHERE id=?", id); err != nil {
		return User{}, err
	}
	user, err := fillUserResponse(ctx, tx, u)
	if err == nil {
		storeUserResponse(id, user, asOf)
	}
	return user, err
}

func warmUsers(ctx context.Context, tx responseReader, ids []int64) error {
	cache, _ := ctx.Value(responseCacheKey{}).(*responseCache)
	if cache == nil {
		return nil
	}
	unique := make(map[int64]bool)
	missing := []int64{}
	for _, id := range ids {
		if _, ok := cache.users[id]; !ok && !unique[id] {
			unique[id] = true
			if u, ok := cachedUserResponse(id); ok {
				cache.users[id] = u
			} else {
				missing = append(missing, id)
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	query, args, err := sqlx.In(`SELECT u.id,u.name,u.display_name,u.description,t.id theme_id,t.dark_mode,COALESCE(i.image_hash,?) icon_hash FROM users u JOIN themes t ON t.user_id=u.id LEFT JOIN icons i ON i.user_id=u.id WHERE u.id IN (?)`, fallbackIconHash, missing)
	if err != nil {
		return err
	}
	var rows []struct {
		ID          int64  `db:"id"`
		Name        string `db:"name"`
		DisplayName string `db:"display_name"`
		Description string `db:"description"`
		ThemeID     int64  `db:"theme_id"`
		DarkMode    bool   `db:"dark_mode"`
		IconHash    string `db:"icon_hash"`
	}
	if err := tx.SelectContext(ctx, &rows, query, args...); err != nil {
		return err
	}
	for _, r := range rows {
		cache.users[r.ID] = User{ID: r.ID, Name: r.Name, DisplayName: r.DisplayName, Description: r.Description, Theme: Theme{ID: r.ThemeID, DarkMode: r.DarkMode}, IconHash: r.IconHash}
		storeUserResponse(r.ID, cache.users[r.ID], cache.asOf)
	}
	return nil
}

func warmStreams(ctx context.Context, tx responseReader, models []*LivestreamModel) error {
	cache, _ := ctx.Value(responseCacheKey{}).(*responseCache)
	if cache == nil || len(models) == 0 {
		return nil
	}
	missingModels := make([]*LivestreamModel, 0, len(models))
	for _, m := range models {
		if stream, ok := cachedStreamResponse(m.ID); ok {
			cache.streams[m.ID] = stream
		} else {
			missingModels = append(missingModels, m)
		}
	}
	models = missingModels
	if len(models) == 0 {
		return nil
	}
	ids := []int64{}
	users := []int64{}
	for _, m := range models {
		ids = append(ids, m.ID)
		users = append(users, m.UserID)
	}
	if err := warmUsers(ctx, tx, users); err != nil {
		return err
	}
	query, args, err := sqlx.In(`SELECT lt.livestream_id,t.id,t.name FROM livestream_tags lt JOIN tags t ON t.id=lt.tag_id WHERE lt.livestream_id IN (?) ORDER BY lt.id`, ids)
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
	tags := make(map[int64][]Tag)
	for _, r := range rows {
		tags[r.LivestreamID] = append(tags[r.LivestreamID], Tag{ID: r.ID, Name: r.Name})
	}
	for _, m := range models {
		owner, err := loadUserResponse(ctx, tx, m.UserID)
		if err != nil {
			return err
		}
		streamTags := tags[m.ID]
		if streamTags == nil {
			streamTags = []Tag{}
		}
		cache.streams[m.ID] = Livestream{ID: m.ID, Owner: owner, Title: m.Title, Description: m.Description, PlaylistUrl: m.PlaylistUrl, ThumbnailUrl: m.ThumbnailUrl, Tags: streamTags, StartAt: m.StartAt, EndAt: m.EndAt}
		storeStreamResponse(m.ID, cache.streams[m.ID], cache.asOf)
	}
	return nil
}

func loadStreamResponse(ctx context.Context, tx responseReader, id int64) (Livestream, error) {
	cache, _ := ctx.Value(responseCacheKey{}).(*responseCache)
	if cache != nil {
		if stream, ok := cache.streams[id]; ok {
			return stream, nil
		}
	}
	if stream, ok := cachedStreamResponse(id); ok {
		if cache != nil {
			cache.streams[id] = stream
		}
		return stream, nil
	}
	asOf := time.Now()
	if cache != nil {
		asOf = cache.asOf
	}
	var model LivestreamModel
	if err := tx.GetContext(ctx, &model, "SELECT * FROM livestreams WHERE id=?", id); err != nil {
		return Livestream{}, err
	}
	stream, err := fillLivestreamResponse(ctx, tx, model)
	if err == nil {
		storeStreamResponse(id, stream, asOf)
	}
	return stream, err
}

// 一覧や表示用の読み取りは各SQLのスナップショットで完結する。
type responseReader interface {
	GetContext(context.Context, interface{}, string, ...interface{}) error
	SelectContext(context.Context, interface{}, string, ...interface{}) error
}
