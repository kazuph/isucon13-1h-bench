package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"sync"

	"github.com/jmoiron/sqlx"
)

type dbx interface {
	GetContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error
	SelectContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error
}

var (
	fallbackImageBytes []byte
	fallbackImageHash  string

	cacheMu         sync.RWMutex
	userByID        = map[int64]*UserModel{}
	userIDByName    = map[string]int64{}
	themeByUserID   = map[int64]ThemeModel{}
	livestreamByID  = map[int64]*LivestreamModel{}
	tagsByStreamID  = map[int64][]Tag{}
	tagByID         = map[int64]Tag{}
	tagIDByName     = map[string]int64{}
	allTagsResponse []*Tag
	iconHashByUser  = map[int64]string{}
	verifiedPw      = map[string]string{}
)

func initStaticCache() {
	b, err := os.ReadFile(fallbackImage)
	if err != nil {
		panic(err)
	}
	fallbackImageBytes = b
	fallbackImageHash = fmt.Sprintf("%x", sha256.Sum256(b))
}

func resetCache() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	userByID = map[int64]*UserModel{}
	userIDByName = map[string]int64{}
	themeByUserID = map[int64]ThemeModel{}
	livestreamByID = map[int64]*LivestreamModel{}
	tagsByStreamID = map[int64][]Tag{}
	tagByID = map[int64]Tag{}
	tagIDByName = map[string]int64{}
	allTagsResponse = nil
	iconHashByUser = map[int64]string{}
	verifiedPw = map[string]string{}
}

func setVerifiedPw(name, pw string) {
	cacheMu.Lock()
	verifiedPw[name] = pw
	cacheMu.Unlock()
}

func isVerifiedPw(name, pw string) bool {
	cacheMu.RLock()
	v, ok := verifiedPw[name]
	cacheMu.RUnlock()
	return ok && v == pw
}

func setIconHash(userID int64, hash string) {
	cacheMu.Lock()
	iconHashByUser[userID] = hash
	cacheMu.Unlock()
}

func loadTags(ctx context.Context) error {
	cacheMu.RLock()
	loaded := allTagsResponse != nil
	cacheMu.RUnlock()
	if loaded {
		return nil
	}
	var tagModels []*TagModel
	if err := dbConn.SelectContext(ctx, &tagModels, "SELECT * FROM tags ORDER BY id"); err != nil {
		return err
	}
	cacheMu.Lock()
	defer cacheMu.Unlock()
	allTagsResponse = make([]*Tag, 0, len(tagModels))
	for _, t := range tagModels {
		tagByID[t.ID] = Tag{ID: t.ID, Name: t.Name}
		tagIDByName[t.Name] = t.ID
		allTagsResponse = append(allTagsResponse, &Tag{ID: t.ID, Name: t.Name})
	}
	return nil
}

func putUser(u *UserModel) {
	cacheMu.Lock()
	userByID[u.ID] = u
	userIDByName[u.Name] = u.ID
	cacheMu.Unlock()
}

func getUserByID(ctx context.Context, db dbx, id int64) (*UserModel, error) {
	cacheMu.RLock()
	u, ok := userByID[id]
	cacheMu.RUnlock()
	if ok {
		return u, nil
	}
	var m UserModel
	if err := db.GetContext(ctx, &m, "SELECT * FROM users WHERE id = ?", id); err != nil {
		return nil, err
	}
	putUser(&m)
	return &m, nil
}

func getUserByName(ctx context.Context, db dbx, name string) (*UserModel, error) {
	cacheMu.RLock()
	id, ok := userIDByName[name]
	var u *UserModel
	if ok {
		u = userByID[id]
	}
	cacheMu.RUnlock()
	if u != nil {
		return u, nil
	}
	var m UserModel
	if err := db.GetContext(ctx, &m, "SELECT * FROM users WHERE name = ?", name); err != nil {
		return nil, err
	}
	putUser(&m)
	return &m, nil
}

func getUsersByIDs(ctx context.Context, db dbx, ids []int64) (map[int64]*UserModel, error) {
	res := make(map[int64]*UserModel, len(ids))
	var missing []int64
	cacheMu.RLock()
	for _, id := range ids {
		if u, ok := userByID[id]; ok {
			res[id] = u
		} else {
			missing = append(missing, id)
		}
	}
	cacheMu.RUnlock()
	if len(missing) > 0 {
		q, args, err := sqlx.In("SELECT * FROM users WHERE id IN (?)", missing)
		if err != nil {
			return nil, err
		}
		var ms []*UserModel
		if err := db.SelectContext(ctx, &ms, q, args...); err != nil {
			return nil, err
		}
		for _, m := range ms {
			putUser(m)
			res[m.ID] = m
		}
	}
	return res, nil
}

func getThemes(ctx context.Context, db dbx, ids []int64) (map[int64]ThemeModel, error) {
	res := make(map[int64]ThemeModel, len(ids))
	var missing []int64
	cacheMu.RLock()
	for _, id := range ids {
		if t, ok := themeByUserID[id]; ok {
			res[id] = t
		} else {
			missing = append(missing, id)
		}
	}
	cacheMu.RUnlock()
	if len(missing) > 0 {
		q, args, err := sqlx.In("SELECT * FROM themes WHERE user_id IN (?)", missing)
		if err != nil {
			return nil, err
		}
		var ms []ThemeModel
		if err := db.SelectContext(ctx, &ms, q, args...); err != nil {
			return nil, err
		}
		cacheMu.Lock()
		for _, m := range ms {
			if _, ok := themeByUserID[m.UserID]; !ok {
				themeByUserID[m.UserID] = m
			}
			if _, ok := res[m.UserID]; !ok {
				res[m.UserID] = m
			}
		}
		cacheMu.Unlock()
	}
	return res, nil
}

func getIconHashes(ctx context.Context, db dbx, ids []int64) (map[int64]string, error) {
	res := make(map[int64]string, len(ids))
	cacheMu.RLock()
	for _, id := range ids {
		if h, ok := iconHashByUser[id]; ok {
			res[id] = h
		} else {
			res[id] = fallbackImageHash
		}
	}
	cacheMu.RUnlock()
	return res, nil
}

func uniqIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// fillUsers builds User responses for given ids.
func fillUsers(ctx context.Context, db dbx, ids []int64) (map[int64]User, error) {
	ids = uniqIDs(ids)
	users, err := getUsersByIDs(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	themes, err := getThemes(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	hashes, err := getIconHashes(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	res := make(map[int64]User, len(ids))
	for _, id := range ids {
		u, ok := users[id]
		if !ok {
			return nil, fmt.Errorf("user %d not found", id)
		}
		t, ok := themes[id]
		if !ok {
			return nil, fmt.Errorf("theme of user %d not found", id)
		}
		res[id] = User{
			ID:          u.ID,
			Name:        u.Name,
			DisplayName: u.DisplayName,
			Description: u.Description,
			Theme:       Theme{ID: t.ID, DarkMode: t.DarkMode},
			IconHash:    hashes[id],
		}
	}
	return res, nil
}

func fillUser(ctx context.Context, db dbx, id int64) (User, error) {
	m, err := fillUsers(ctx, db, []int64{id})
	if err != nil {
		return User{}, err
	}
	return m[id], nil
}

func putLivestream(l *LivestreamModel) {
	cacheMu.Lock()
	livestreamByID[l.ID] = l
	cacheMu.Unlock()
}

func getLivestreamByID(ctx context.Context, db dbx, id int64) (*LivestreamModel, error) {
	cacheMu.RLock()
	l, ok := livestreamByID[id]
	cacheMu.RUnlock()
	if ok {
		return l, nil
	}
	var m LivestreamModel
	if err := db.GetContext(ctx, &m, "SELECT * FROM livestreams WHERE id = ?", id); err != nil {
		return nil, err
	}
	putLivestream(&m)
	return &m, nil
}

func getLivestreamTags(ctx context.Context, db dbx, ids []int64) (map[int64][]Tag, error) {
	if err := loadTags(ctx); err != nil {
		return nil, err
	}
	res := make(map[int64][]Tag, len(ids))
	var missing []int64
	cacheMu.RLock()
	for _, id := range ids {
		if t, ok := tagsByStreamID[id]; ok {
			res[id] = t
		} else {
			missing = append(missing, id)
		}
	}
	cacheMu.RUnlock()
	if len(missing) > 0 {
		q, args, err := sqlx.In("SELECT * FROM livestream_tags WHERE livestream_id IN (?) ORDER BY id", missing)
		if err != nil {
			return nil, err
		}
		var ms []LivestreamTagModel
		if err := db.SelectContext(ctx, &ms, q, args...); err != nil {
			return nil, err
		}
		cacheMu.Lock()
		tmp := make(map[int64][]Tag, len(missing))
		for _, id := range missing {
			tmp[id] = []Tag{}
		}
		for _, m := range ms {
			tmp[m.LivestreamID] = append(tmp[m.LivestreamID], tagByID[m.TagID])
		}
		for id, t := range tmp {
			tagsByStreamID[id] = t
			res[id] = t
		}
		cacheMu.Unlock()
	}
	return res, nil
}

func fillLivestreams(ctx context.Context, db dbx, models []*LivestreamModel) ([]Livestream, error) {
	ownerIDs := make([]int64, 0, len(models))
	lsIDs := make([]int64, 0, len(models))
	for _, m := range models {
		ownerIDs = append(ownerIDs, m.UserID)
		lsIDs = append(lsIDs, m.ID)
	}
	users, err := fillUsers(ctx, db, ownerIDs)
	if err != nil {
		return nil, err
	}
	tags, err := getLivestreamTags(ctx, db, uniqIDs(lsIDs))
	if err != nil {
		return nil, err
	}
	res := make([]Livestream, len(models))
	for i, m := range models {
		res[i] = Livestream{
			ID:           m.ID,
			Owner:        users[m.UserID],
			Title:        m.Title,
			Tags:         tags[m.ID],
			Description:  m.Description,
			PlaylistUrl:  m.PlaylistUrl,
			ThumbnailUrl: m.ThumbnailUrl,
			StartAt:      m.StartAt,
			EndAt:        m.EndAt,
		}
	}
	return res, nil
}

func fillLivestream(ctx context.Context, db dbx, m *LivestreamModel) (Livestream, error) {
	r, err := fillLivestreams(ctx, db, []*LivestreamModel{m})
	if err != nil {
		return Livestream{}, err
	}
	return r[0], nil
}

// fillLivecomments: all comments may belong to different livestreams
func fillLivecomments(ctx context.Context, db dbx, models []LivecommentModel) ([]Livecomment, error) {
	userIDs := make([]int64, 0, len(models))
	lsMap := map[int64]*LivestreamModel{}
	for _, m := range models {
		userIDs = append(userIDs, m.UserID)
		if _, ok := lsMap[m.LivestreamID]; !ok {
			l, err := getLivestreamByID(ctx, db, m.LivestreamID)
			if err != nil {
				return nil, err
			}
			lsMap[m.LivestreamID] = l
		}
	}
	lsModels := make([]*LivestreamModel, 0, len(lsMap))
	for _, l := range lsMap {
		lsModels = append(lsModels, l)
	}
	lss, err := fillLivestreams(ctx, db, lsModels)
	if err != nil {
		return nil, err
	}
	lsResp := make(map[int64]Livestream, len(lss))
	for _, l := range lss {
		lsResp[l.ID] = l
	}
	users, err := fillUsers(ctx, db, userIDs)
	if err != nil {
		return nil, err
	}
	res := make([]Livecomment, len(models))
	for i, m := range models {
		res[i] = Livecomment{
			ID:         m.ID,
			User:       users[m.UserID],
			Livestream: lsResp[m.LivestreamID],
			Comment:    m.Comment,
			Tip:        m.Tip,
			CreatedAt:  m.CreatedAt,
		}
	}
	return res, nil
}

func fillReactions(ctx context.Context, db dbx, models []ReactionModel) ([]Reaction, error) {
	userIDs := make([]int64, 0, len(models))
	lsMap := map[int64]*LivestreamModel{}
	for _, m := range models {
		userIDs = append(userIDs, m.UserID)
		if _, ok := lsMap[m.LivestreamID]; !ok {
			l, err := getLivestreamByID(ctx, db, m.LivestreamID)
			if err != nil {
				return nil, err
			}
			lsMap[m.LivestreamID] = l
		}
	}
	lsModels := make([]*LivestreamModel, 0, len(lsMap))
	for _, l := range lsMap {
		lsModels = append(lsModels, l)
	}
	lss, err := fillLivestreams(ctx, db, lsModels)
	if err != nil {
		return nil, err
	}
	lsResp := make(map[int64]Livestream, len(lss))
	for _, l := range lss {
		lsResp[l.ID] = l
	}
	users, err := fillUsers(ctx, db, userIDs)
	if err != nil {
		return nil, err
	}
	res := make([]Reaction, len(models))
	for i, m := range models {
		res[i] = Reaction{
			ID:         m.ID,
			EmojiName:  m.EmojiName,
			User:       users[m.UserID],
			Livestream: lsResp[m.LivestreamID],
			CreatedAt:  m.CreatedAt,
		}
	}
	return res, nil
}
