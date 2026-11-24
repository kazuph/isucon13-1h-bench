package main

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"time"

	"github.com/jmoiron/sqlx"
)

const userProfileQuery = `
	SELECT u.id, u.name, u.display_name, u.description, u.theme_id, u.dark_mode,
	       COALESCE(u.icon_hash, ?) AS icon_hash
	FROM users u`

type userProfile struct {
	UserModel
	ThemeID  int64  `db:"theme_id"`
	DarkMode bool   `db:"dark_mode"`
	IconHash string `db:"icon_hash"`
}

func (profile userProfile) user() User {
	return User{
		ID: profile.ID, Name: profile.Name, DisplayName: profile.DisplayName,
		Description: profile.Description, IconHash: profile.IconHash,
		Theme: Theme{ID: profile.ThemeID, DarkMode: profile.DarkMode},
	}
}

type requestStartTimeKey struct{}

type profileCacheEntry struct {
	user    User
	expires time.Time
}

var profileCache [4096]atomic.Pointer[profileCacheEntry]

func getCachedProfile(id int64, now time.Time) (User, bool) {
	entry := profileCache[uint64(id)%uint64(len(profileCache))].Load()
	if entry == nil || entry.user.ID != id || !now.Before(entry.expires) {
		return User{}, false
	}
	return entry.user, true
}

func responseCacheExpiry(ctx context.Context) time.Time {
	started, ok := ctx.Value(requestStartTimeKey{}).(time.Time)
	if !ok {
		return time.Time{}
	}
	return started.Add(iconMetadataTTL)
}

func storeCachedProfile(ctx context.Context, user User) {
	expires := responseCacheExpiry(ctx)
	if len(user.Description) <= 4096 && time.Now().Before(expires) {
		profileCache[uint64(user.ID)%uint64(len(profileCache))].Store(&profileCacheEntry{user: user, expires: expires})
	}
}

func loadUser(ctx context.Context, queries sqlx.QueryerContext, id int64) (User, error) {
	if user, ok := getCachedProfile(id, time.Now()); ok {
		return user, nil
	}
	var profile userProfile
	if err := sqlx.GetContext(ctx, queries, &profile, userProfileQuery+" WHERE u.id = ? AND u.theme_id IS NOT NULL", fallbackImageHash, id); err != nil {
		return User{}, err
	}
	user := profile.user()
	storeCachedProfile(ctx, user)
	return user, nil
}

func loadUsers(ctx context.Context, tx *sqlx.Tx, ids []int64) (map[int64]User, error) {
	users := make(map[int64]User, len(ids))
	if len(ids) == 0 {
		return users, nil
	}

	now := time.Now()
	missing := make([]int64, 0, len(ids))
	for _, id := range ids {
		if user, ok := getCachedProfile(id, now); ok {
			users[id] = user
		} else {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return users, nil
	}

	query, args, err := sqlx.In(userProfileQuery+" WHERE u.id IN (?) AND u.theme_id IS NOT NULL", fallbackImageHash, missing)
	if err != nil {
		return nil, err
	}
	var rows []userProfile
	if err := tx.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, err
	}
	for _, row := range rows {
		user := row.user()
		users[row.ID] = user
		storeCachedProfile(ctx, user)
	}
	return users, nil
}

const livestreamProfileQuery = `
		SELECT l.*, u.name AS owner_name, u.display_name AS owner_display_name,
		       u.description AS owner_description, u.theme_id AS owner_theme_id,
		       u.dark_mode AS owner_dark_mode, COALESCE(u.icon_hash, ?) AS owner_icon_hash
		FROM livestreams l
		LEFT JOIN users u ON u.id = l.user_id`

type livestreamProfile struct {
	LivestreamModel
	OwnerName        sql.NullString `db:"owner_name"`
	OwnerDisplayName sql.NullString `db:"owner_display_name"`
	OwnerDescription sql.NullString `db:"owner_description"`
	OwnerThemeID     sql.NullInt64  `db:"owner_theme_id"`
	OwnerDarkMode    sql.NullBool   `db:"owner_dark_mode"`
	OwnerIconHash    string         `db:"owner_icon_hash"`
}

func (row livestreamProfile) livestream() (Livestream, error) {
	if !row.OwnerName.Valid || !row.OwnerThemeID.Valid {
		return Livestream{}, errors.New("failed to find livestream owner profile")
	}
	owner := User{
		ID: row.UserID, Name: row.OwnerName.String, DisplayName: row.OwnerDisplayName.String,
		Description: row.OwnerDescription.String, IconHash: row.OwnerIconHash,
		Theme: Theme{ID: row.OwnerThemeID.Int64, DarkMode: row.OwnerDarkMode.Bool},
	}
	return livestreamResponse(row.LivestreamModel, owner), nil
}

type livestreamCacheEntry struct {
	stream  Livestream
	expires time.Time
}

var livestreamCache [8192]atomic.Pointer[livestreamCacheEntry]

func loadLivestream(ctx context.Context, queries sqlx.QueryerContext, id int64) (Livestream, error) {
	entry := &livestreamCache[uint64(id)%uint64(len(livestreamCache))]
	if cached := entry.Load(); cached != nil && cached.stream.ID == id && time.Now().Before(cached.expires) {
		return cached.stream, nil
	}
	var row livestreamProfile
	if err := sqlx.GetContext(ctx, queries, &row, livestreamProfileQuery+" WHERE l.id = ?", fallbackImageHash, id); err != nil {
		return Livestream{}, err
	}
	stream, err := row.livestream()
	if err != nil {
		return Livestream{}, err
	}
	expires := responseCacheExpiry(ctx)
	if len(stream.Description)+len(stream.Owner.Description)+len(stream.Tags) <= 4096 && time.Now().Before(expires) {
		entry.Store(&livestreamCacheEntry{stream: stream, expires: expires})
	}
	return stream, nil
}

func listLivestreams(ctx context.Context, query string, args ...interface{}) ([]Livestream, error) {
	var rows []livestreamProfile
	args = append([]interface{}{fallbackImageHash}, args...)
	if err := dbConn.SelectContext(ctx, &rows, livestreamProfileQuery+query, args...); err != nil {
		return nil, err
	}
	streams := make([]Livestream, len(rows))
	for i, row := range rows {
		stream, err := row.livestream()
		if err != nil {
			return nil, err
		}
		streams[i] = stream
	}
	return streams, nil
}
