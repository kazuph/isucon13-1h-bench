package main

import (
	"context"
	"database/sql"
	"github.com/jmoiron/sqlx"
)

type readDB interface {
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
	GetContext(context.Context, interface{}, string, ...interface{}) error
}

func queryUsers(ctx context.Context, tx readDB, ids []int64) (map[int64]User, error) {
	users := make(map[int64]User, len(ids))
	if len(ids) == 0 {
		return users, nil
	}
	query, args, err := sqlx.In(`SELECT u.id,u.name,u.display_name,u.description,t.id,t.dark_mode,COALESCE(i.image_hash,?) FROM users u JOIN themes t ON t.user_id=u.id LEFT JOIN icons i ON i.user_id=u.id WHERE u.id IN (?)`, defaultIconHash, ids)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Name, &u.DisplayName, &u.Description, &u.Theme.ID, &u.Theme.DarkMode, &u.IconHash); err != nil {
			return nil, err
		}
		users[u.ID] = u
	}
	return users, rows.Err()
}

func queryStreams(ctx context.Context, tx readDB, models []*LivestreamModel) ([]Livestream, error) {
	streams := make([]Livestream, len(models))
	if len(models) == 0 {
		return streams, nil
	}
	ids := make([]int64, len(models))
	owners := make([]int64, len(models))
	for i, m := range models {
		ids[i] = m.ID
		owners[i] = m.UserID
	}
	users, err := queryUsers(ctx, tx, owners)
	if err != nil {
		return nil, err
	}
	query, args, err := sqlx.In("SELECT lt.livestream_id,t.id,t.name FROM livestream_tags lt JOIN tags t ON t.id=lt.tag_id WHERE lt.livestream_id IN (?) ORDER BY lt.id", ids)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tags := make(map[int64][]Tag)
	for rows.Next() {
		var id int64
		var tag Tag
		if err := rows.Scan(&id, &tag.ID, &tag.Name); err != nil {
			return nil, err
		}
		tags[id] = append(tags[id], tag)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, m := range models {
		owner, ok := users[m.UserID]
		if !ok {
			return nil, sql.ErrNoRows
		}
		ts := tags[m.ID]
		if ts == nil {
			ts = []Tag{}
		}
		streams[i] = Livestream{ID: m.ID, Owner: owner, Title: m.Title, Description: m.Description, Tags: ts, PlaylistUrl: m.PlaylistUrl, ThumbnailUrl: m.ThumbnailUrl, StartAt: m.StartAt, EndAt: m.EndAt}
	}
	return streams, nil
}

func loadUsers(ctx context.Context, tx readDB, ids []int64) (map[int64]User, error) {
	users := make(map[int64]User, len(ids))
	for _, id := range ids {
		u, ok := cachedUser(id)
		if !ok {
			return queryUsers(ctx, tx, ids)
		}
		users[id] = u
	}
	return users, nil
}
func loadStreams(ctx context.Context, tx readDB, models []*LivestreamModel) ([]Livestream, error) {
	streams := make([]Livestream, len(models))
	for i, m := range models {
		s, ok := cachedStream(m.ID)
		if !ok {
			return queryStreams(ctx, tx, models)
		}
		streams[i] = s
	}
	return streams, nil
}
func streamByID(ctx context.Context, tx readDB, id int64) (Livestream, error) {
	if s, ok := cachedStream(id); ok {
		return s, nil
	}
	var m LivestreamModel
	if err := tx.GetContext(ctx, &m, "SELECT * FROM livestreams WHERE id=?", id); err != nil {
		return Livestream{}, err
	}
	streams, err := loadStreams(ctx, tx, []*LivestreamModel{&m})
	if err != nil {
		return Livestream{}, err
	}
	return streams[0], nil
}
