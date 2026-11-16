package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"github.com/jmoiron/sqlx"
	"os"
	"sync"
)

type responseCacheKey struct{}
type responseCache struct {
	users   map[int64]User
	streams map[int64]Livestream
}

func withResponseCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, responseCacheKey{}, &responseCache{users: make(map[int64]User), streams: make(map[int64]Livestream)})
}
func loadUserResponse(ctx context.Context, tx queryContext, id int64) (User, error) {
	if err := preloadUsers(ctx, tx, []int64{id}); err != nil {
		return User{}, err
	}
	cache := ctx.Value(responseCacheKey{}).(*responseCache)
	u, ok := cache.users[id]
	if !ok {
		return User{}, sql.ErrNoRows
	}
	return u, nil
}
func loadLivestreamResponse(ctx context.Context, tx queryContext, id int64) (Livestream, error) {
	if cache, ok := ctx.Value(responseCacheKey{}).(*responseCache); ok {
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

func preloadUsers(ctx context.Context, tx queryContext, ids []int64) error {
	cache := ctx.Value(responseCacheKey{}).(*responseCache)
	set := make(map[int64]bool)
	keys := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := cache.users[id]; !ok && !set[id] {
			set[id] = true
			keys = append(keys, id)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	query, args, err := sqlx.In(`SELECT u.id,u.name,u.display_name,u.description,t.id AS theme_id,t.dark_mode,COALESCE(i.image_hash,'') AS icon_hash FROM users u JOIN themes t ON t.user_id=u.id LEFT JOIN icons i ON i.user_id=u.id WHERE u.id IN (?)`, keys)
	if err != nil {
		return err
	}
	var rows []struct {
		ID          int64
		Name        string
		DisplayName string `db:"display_name"`
		Description string
		ThemeID     int64  `db:"theme_id"`
		DarkMode    bool   `db:"dark_mode"`
		IconHash    string `db:"icon_hash"`
	}
	if err := tx.SelectContext(ctx, &rows, query, args...); err != nil {
		return err
	}
	for _, r := range rows {
		h := r.IconHash
		if h == "" {
			h = defaultIconHash()
		}
		cache.users[r.ID] = User{ID: r.ID, Name: r.Name, DisplayName: r.DisplayName, Description: r.Description, Theme: Theme{ID: r.ThemeID, DarkMode: r.DarkMode}, IconHash: h}
	}
	return nil
}
func preloadStreams(ctx context.Context, tx queryContext, models []*LivestreamModel) error {
	if len(models) == 0 {
		return nil
	}
	cache := ctx.Value(responseCacheKey{}).(*responseCache)
	uids := make([]int64, 0, len(models))
	ids := make([]int64, 0, len(models))
	for _, m := range models {
		uids = append(uids, m.UserID)
		ids = append(ids, m.ID)
	}
	if err := preloadUsers(ctx, tx, uids); err != nil {
		return err
	}
	query, args, err := sqlx.In(`SELECT lt.livestream_id,t.id,t.name FROM livestream_tags lt JOIN tags t ON t.id=lt.tag_id WHERE lt.livestream_id IN (?) ORDER BY lt.id`, ids)
	if err != nil {
		return err
	}
	var rows []struct {
		LivestreamID int64 `db:"livestream_id"`
		ID           int64
		Name         string
	}
	if err := tx.SelectContext(ctx, &rows, query, args...); err != nil {
		return err
	}
	tags := make(map[int64][]Tag)
	for _, r := range rows {
		tags[r.LivestreamID] = append(tags[r.LivestreamID], Tag{ID: r.ID, Name: r.Name})
	}
	for _, m := range models {
		t := tags[m.ID]
		if t == nil {
			t = []Tag{}
		}
		u, ok := cache.users[m.UserID]
		if !ok {
			return sql.ErrNoRows
		}
		cache.streams[m.ID] = Livestream{ID: m.ID, Owner: u, Title: m.Title, Description: m.Description, PlaylistUrl: m.PlaylistUrl, ThumbnailUrl: m.ThumbnailUrl, StartAt: m.StartAt, EndAt: m.EndAt, Tags: t}
	}
	return nil
}
func preloadTimeline(ctx context.Context, tx queryContext, userIDs, streamIDs []int64) error {
	if err := preloadUsers(ctx, tx, userIDs); err != nil {
		return err
	}
	cache := ctx.Value(responseCacheKey{}).(*responseCache)
	set := make(map[int64]bool)
	keys := make([]int64, 0)
	for _, id := range streamIDs {
		if _, ok := cache.streams[id]; !ok && !set[id] {
			set[id] = true
			keys = append(keys, id)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	query, args, err := sqlx.In("SELECT * FROM livestreams WHERE id IN (?)", keys)
	if err != nil {
		return err
	}
	var models []*LivestreamModel
	if err := tx.SelectContext(ctx, &models, query, args...); err != nil {
		return err
	}
	return preloadStreams(ctx, tx, models)
}

var fallbackHashOnce sync.Once
var fallbackHashValue string
var fallbackImageValue []byte

func defaultIconHash() string {
	fallbackHashOnce.Do(func() {
		image, err := os.ReadFile(fallbackImage)
		if err != nil {
			panic(err)
		}
		fallbackImageValue = image
		fallbackHashValue = fmt.Sprintf("%x", sha256.Sum256(image))
	})
	return fallbackHashValue
}

func defaultIconImage() []byte { defaultIconHash(); return fallbackImageValue }

type queryContext interface {
	GetContext(context.Context, interface{}, string, ...interface{}) error
	SelectContext(context.Context, interface{}, string, ...interface{}) error
}
type readQueries struct{ *sqlx.DB }

func (readQueries) Commit() error   { return nil }
func (readQueries) Rollback() error { return nil }
