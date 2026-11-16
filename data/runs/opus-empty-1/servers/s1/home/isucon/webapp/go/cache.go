package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

var (
	cacheMu sync.RWMutex

	usersByID   map[int64]*UserModel
	usersByName map[string]*UserModel
	themesByUID map[int64]ThemeModel
	iconHashUID map[int64]string
	iconDataUID map[int64][]byte

	tagsByID   map[int64]*TagModel
	tagsByName map[string]int64
	tagList    []*Tag

	livestreamsByID map[int64]*LivestreamModel
	lsTags          map[int64][]int64

	// key: livestream_id -> list of (user_id, word)
	ngByLs map[int64][]ngEntry

	fallbackHash  string
	fallbackBytes []byte
)

func init() {
	resetCacheMaps()
}

func resetCacheMaps() {
	usersByID = map[int64]*UserModel{}
	usersByName = map[string]*UserModel{}
	themesByUID = map[int64]ThemeModel{}
	iconHashUID = map[int64]string{}
	iconDataUID = map[int64][]byte{}
	tagsByID = map[int64]*TagModel{}
	tagsByName = map[string]int64{}
	tagList = nil
	livestreamsByID = map[int64]*LivestreamModel{}
	lsTags = map[int64][]int64{}
	ngByLs = map[int64][]ngEntry{}
}

type ngEntry struct {
	UserID int64  `db:"user_id"`
	LsID   int64  `db:"livestream_id"`
	Word   string `db:"word"`
}

func cacheAddNG(e ngEntry) {
	cacheMu.Lock()
	ngByLs[e.LsID] = append(ngByLs[e.LsID], e)
	cacheMu.Unlock()
}

// returns true if comment hits an NG word registered by userID for livestream lsID
func hitsNG(userID, lsID int64, comment string) bool {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	for _, e := range ngByLs[lsID] {
		if e.UserID == userID && strings.Contains(comment, e.Word) {
			return true
		}
	}
	return false
}

func loadFallback() error {
	b, err := os.ReadFile(fallbackImage)
	if err != nil {
		return err
	}
	fallbackBytes = b
	fallbackHash = fmt.Sprintf("%x", sha256.Sum256(b))
	return nil
}

func loadCaches(ctx context.Context) error {
	var users []*UserModel
	if err := dbConn.SelectContext(ctx, &users, "SELECT * FROM users"); err != nil {
		return err
	}
	var themes []ThemeModel
	if err := dbConn.SelectContext(ctx, &themes, "SELECT * FROM themes"); err != nil {
		return err
	}
	type iconRow struct {
		UserID int64  `db:"user_id"`
		Image  []byte `db:"image"`
	}
	var icons []iconRow
	if err := dbConn.SelectContext(ctx, &icons, "SELECT user_id, image FROM icons ORDER BY id"); err != nil {
		return err
	}
	var tags []*TagModel
	if err := dbConn.SelectContext(ctx, &tags, "SELECT * FROM tags ORDER BY id"); err != nil {
		return err
	}
	var lss []*LivestreamModel
	if err := dbConn.SelectContext(ctx, &lss, "SELECT * FROM livestreams"); err != nil {
		return err
	}
	var lts []LivestreamTagModel
	if err := dbConn.SelectContext(ctx, &lts, "SELECT * FROM livestream_tags ORDER BY id"); err != nil {
		return err
	}

	var ngs []ngEntry
	if err := dbConn.SelectContext(ctx, &ngs, "SELECT user_id, livestream_id, word FROM ng_words ORDER BY id"); err != nil {
		return err
	}

	cacheMu.Lock()
	defer cacheMu.Unlock()
	resetCacheMaps()
	for _, e := range ngs {
		ngByLs[e.LsID] = append(ngByLs[e.LsID], e)
	}
	for _, u := range users {
		usersByID[u.ID] = u
		usersByName[u.Name] = u
	}
	for _, t := range themes {
		themesByUID[t.UserID] = t
	}
	for _, ic := range icons {
		iconDataUID[ic.UserID] = ic.Image
		iconHashUID[ic.UserID] = fmt.Sprintf("%x", sha256.Sum256(ic.Image))
	}
	for _, t := range tags {
		tagsByID[t.ID] = t
		tagsByName[t.Name] = t.ID
		tagList = append(tagList, &Tag{ID: t.ID, Name: t.Name})
	}
	for _, l := range lss {
		livestreamsByID[l.ID] = l
	}
	for _, lt := range lts {
		lsTags[lt.LivestreamID] = append(lsTags[lt.LivestreamID], lt.TagID)
	}
	return nil
}

func cacheAddUser(u *UserModel, t ThemeModel) {
	cacheMu.Lock()
	usersByID[u.ID] = u
	usersByName[u.Name] = u
	themesByUID[u.ID] = t
	cacheMu.Unlock()
}

func cacheSetIcon(uid int64, img []byte) {
	h := fmt.Sprintf("%x", sha256.Sum256(img))
	cacheMu.Lock()
	iconDataUID[uid] = img
	iconHashUID[uid] = h
	cacheMu.Unlock()
}

func cacheAddLivestream(l *LivestreamModel, tagIDs []int64) {
	cacheMu.Lock()
	livestreamsByID[l.ID] = l
	lsTags[l.ID] = tagIDs
	cacheMu.Unlock()
}

func getUserByID(ctx context.Context, id int64) (*UserModel, error) {
	cacheMu.RLock()
	u, ok := usersByID[id]
	cacheMu.RUnlock()
	if ok {
		return u, nil
	}
	u = &UserModel{}
	if err := dbConn.GetContext(ctx, u, "SELECT * FROM users WHERE id = ?", id); err != nil {
		return nil, err
	}
	var t ThemeModel
	if err := dbConn.GetContext(ctx, &t, "SELECT * FROM themes WHERE user_id = ?", id); err != nil {
		return nil, err
	}
	cacheAddUser(u, t)
	return u, nil
}

func getUserByName(ctx context.Context, name string) (*UserModel, error) {
	cacheMu.RLock()
	u, ok := usersByName[name]
	cacheMu.RUnlock()
	if ok {
		return u, nil
	}
	u = &UserModel{}
	if err := dbConn.GetContext(ctx, u, "SELECT * FROM users WHERE name = ?", name); err != nil {
		return nil, err
	}
	var t ThemeModel
	if err := dbConn.GetContext(ctx, &t, "SELECT * FROM themes WHERE user_id = ?", u.ID); err != nil {
		return nil, err
	}
	cacheAddUser(u, t)
	return u, nil
}

func getTheme(uid int64) (ThemeModel, bool) {
	cacheMu.RLock()
	t, ok := themesByUID[uid]
	cacheMu.RUnlock()
	return t, ok
}

func getIconHash(uid int64) string {
	cacheMu.RLock()
	h, ok := iconHashUID[uid]
	cacheMu.RUnlock()
	if !ok {
		return fallbackHash
	}
	return h
}

func getLivestreamByID(ctx context.Context, id int64) (*LivestreamModel, error) {
	cacheMu.RLock()
	l, ok := livestreamsByID[id]
	cacheMu.RUnlock()
	if ok {
		return l, nil
	}
	l = &LivestreamModel{}
	if err := dbConn.GetContext(ctx, l, "SELECT * FROM livestreams WHERE id = ?", id); err != nil {
		return nil, err
	}
	var tagIDs []int64
	if err := dbConn.SelectContext(ctx, &tagIDs, "SELECT tag_id FROM livestream_tags WHERE livestream_id = ? ORDER BY id", id); err != nil {
		return nil, err
	}
	cacheAddLivestream(l, tagIDs)
	return l, nil
}

func buildUser(ctx context.Context, u *UserModel) (User, error) {
	t, ok := getTheme(u.ID)
	if !ok {
		if err := dbConn.GetContext(ctx, &t, "SELECT * FROM themes WHERE user_id = ?", u.ID); err != nil {
			return User{}, err
		}
		cacheMu.Lock()
		themesByUID[u.ID] = t
		cacheMu.Unlock()
	}
	return User{
		ID:          u.ID,
		Name:        u.Name,
		DisplayName: u.DisplayName,
		Description: u.Description,
		Theme: Theme{
			ID:       t.ID,
			DarkMode: t.DarkMode,
		},
		IconHash: getIconHash(u.ID),
	}, nil
}

func buildUserByID(ctx context.Context, id int64) (User, error) {
	u, err := getUserByID(ctx, id)
	if err != nil {
		return User{}, err
	}
	return buildUser(ctx, u)
}

func buildLivestream(ctx context.Context, l *LivestreamModel) (Livestream, error) {
	owner, err := buildUserByID(ctx, l.UserID)
	if err != nil {
		return Livestream{}, err
	}
	cacheMu.RLock()
	ids := lsTags[l.ID]
	tags := make([]Tag, 0, len(ids))
	var missing bool
	for _, id := range ids {
		t, ok := tagsByID[id]
		if !ok {
			missing = true
			break
		}
		tags = append(tags, Tag{ID: t.ID, Name: t.Name})
	}
	cacheMu.RUnlock()
	if missing {
		return Livestream{}, errors.New("tag not found")
	}
	return Livestream{
		ID:           l.ID,
		Owner:        owner,
		Title:        l.Title,
		Tags:         tags,
		Description:  l.Description,
		PlaylistUrl:  l.PlaylistUrl,
		ThumbnailUrl: l.ThumbnailUrl,
		StartAt:      l.StartAt,
		EndAt:        l.EndAt,
	}, nil
}

func buildLivestreamByID(ctx context.Context, id int64) (Livestream, error) {
	l, err := getLivestreamByID(ctx, id)
	if err != nil {
		return Livestream{}, err
	}
	return buildLivestream(ctx, l)
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }
