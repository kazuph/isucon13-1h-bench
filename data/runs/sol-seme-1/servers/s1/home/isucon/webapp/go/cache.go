package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"github.com/jmoiron/sqlx"
	"os"
	"sync"
	"time"
)

type cachedUser struct {
	user  User
	image []byte
	until time.Time
}

var userCache = struct {
	sync.RWMutex
	m map[int64]cachedUser
}{m: make(map[int64]cachedUser)}

func resetCaches() {
	resetMetadata()
	userCache.Lock()
	userCache.m = make(map[int64]cachedUser)
	userCache.Unlock()
}
func invalidateUser(id int64) { userCache.Lock(); delete(userCache.m, id); userCache.Unlock() }
func loadCachedUser(ctx context.Context, tx *sqlx.Tx, model UserModel) (cachedUser, error) {
	userCache.RLock()
	item, ok := userCache.m[model.ID]
	userCache.RUnlock()
	if ok && time.Now().Before(item.until) {
		return item, nil
	}
	var theme ThemeModel
	if err := tx.GetContext(ctx, &theme, "SELECT * FROM themes WHERE user_id=?", model.ID); err != nil {
		return item, err
	}
	var img []byte
	if err := tx.GetContext(ctx, &img, "SELECT image FROM icons WHERE user_id=?", model.ID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return item, err
		}
		var err error
		img, err = os.ReadFile(fallbackImage)
		if err != nil {
			return item, err
		}
	}
	item = cachedUser{user: User{ID: model.ID, Name: model.Name, DisplayName: model.DisplayName, Description: model.Description, Theme: Theme{ID: theme.ID, DarkMode: theme.DarkMode}, IconHash: fmt.Sprintf("%x", sha256.Sum256(img))}, image: img, until: time.Now().Add(500 * time.Millisecond)}
	userCache.Lock()
	userCache.m[model.ID] = item
	userCache.Unlock()
	return item, nil
}

var metadataCache = struct {
	sync.RWMutex
	users   map[int64]UserModel
	streams map[int64]LivestreamModel
	tags    map[int64][]Tag
}{users: make(map[int64]UserModel), streams: make(map[int64]LivestreamModel), tags: make(map[int64][]Tag)}

func resetMetadata() {
	metadataCache.Lock()
	defer metadataCache.Unlock()
	metadataCache.users = make(map[int64]UserModel)
	metadataCache.streams = make(map[int64]LivestreamModel)
	metadataCache.tags = make(map[int64][]Tag)
}
func fillUserByID(ctx context.Context, tx *sqlx.Tx, id int64) (User, error) {
	state.RLock()
	u, found := state.users[id]
	state.RUnlock()
	if found {
		return u, nil
	}
	metadataCache.RLock()
	model, ok := metadataCache.users[id]
	metadataCache.RUnlock()
	if !ok {
		if err := tx.GetContext(ctx, &model, "SELECT * FROM users WHERE id=?", id); err != nil {
			return User{}, err
		}
		metadataCache.Lock()
		metadataCache.users[id] = model
		metadataCache.Unlock()
	}
	return fillUserResponse(ctx, tx, model)
}
func fillStreamByID(ctx context.Context, tx *sqlx.Tx, id int64) (Livestream, error) {
	state.RLock()
	_, found := state.streams[id]
	var result Livestream
	if found {
		result = memoryStream(id)
	}
	state.RUnlock()
	if found {
		return result, nil
	}
	metadataCache.RLock()
	model, ok := metadataCache.streams[id]
	metadataCache.RUnlock()
	if !ok {
		if err := tx.GetContext(ctx, &model, "SELECT * FROM livestreams WHERE id=?", id); err != nil {
			return Livestream{}, err
		}
		metadataCache.Lock()
		metadataCache.streams[id] = model
		metadataCache.Unlock()
	}
	return fillLivestreamResponse(ctx, tx, model)
}
func streamTags(ctx context.Context, tx *sqlx.Tx, id int64) ([]Tag, error) {
	metadataCache.RLock()
	tags, ok := metadataCache.tags[id]
	metadataCache.RUnlock()
	if ok {
		return tags, nil
	}
	tags = make([]Tag, 0)
	if err := tx.SelectContext(ctx, &tags, `SELECT t.id,t.name FROM livestream_tags lt JOIN tags t ON t.id=lt.tag_id WHERE lt.livestream_id=? ORDER BY lt.id`, id); err != nil {
		return nil, err
	}
	metadataCache.Lock()
	metadataCache.tags[id] = tags
	metadataCache.Unlock()
	return tags, nil
}
