package main

import (
	"context"
	"database/sql"
	"github.com/jmoiron/sqlx"
	"sync"
)

type catalogState struct {
	tags    []*Tag
	users   map[int64]User
	names   map[string]int64
	streams map[int64]Livestream
}

var catalog struct {
	sync.RWMutex
	catalogState
}

func loadCatalog(ctx context.Context, db *sqlx.DB) error {
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	state := catalogState{users: map[int64]User{}, names: map[string]int64{}, streams: map[int64]Livestream{}, tags: []*Tag{}}
	if err := tx.SelectContext(ctx, &state.tags, "SELECT * FROM tags"); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT u.id,u.name,u.display_name,u.description,t.id,t.dark_mode,COALESCE(i.image_hash,?) FROM users u JOIN themes t ON t.user_id=u.id LEFT JOIN icons i ON i.user_id=u.id`, fallbackHash)
	if err != nil {
		return err
	}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Name, &u.DisplayName, &u.Description, &u.Theme.ID, &u.Theme.DarkMode, &u.IconHash); err != nil {
			rows.Close()
			return err
		}
		state.users[u.ID] = u
		state.names[u.Name] = u.ID
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	var models []LivestreamModel
	if err := tx.SelectContext(ctx, &models, "SELECT * FROM livestreams"); err != nil {
		return err
	}
	for _, m := range models {
		state.streams[m.ID] = Livestream{ID: m.ID, Owner: User{ID: m.UserID}, Title: m.Title, Description: m.Description, PlaylistUrl: m.PlaylistUrl, ThumbnailUrl: m.ThumbnailUrl, StartAt: m.StartAt, EndAt: m.EndAt, Tags: []Tag{}}
	}
	rows, err = tx.QueryContext(ctx, "SELECT lt.livestream_id,t.id,t.name FROM livestream_tags lt JOIN tags t ON t.id=lt.tag_id ORDER BY lt.id")
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		var t Tag
		if err := rows.Scan(&id, &t.ID, &t.Name); err != nil {
			rows.Close()
			return err
		}
		s := state.streams[id]
		s.Tags = append(s.Tags, t)
		state.streams[id] = s
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	if err := tx.Commit(); err != nil {
		return err
	}
	catalog.Lock()
	catalog.catalogState = state
	catalog.Unlock()
	return nil
}
func catalogStream(id int64) (Livestream, error) {
	catalog.RLock()
	defer catalog.RUnlock()
	s, ok := catalog.streams[id]
	if !ok {
		return Livestream{}, sql.ErrNoRows
	}
	s.Owner = catalog.users[s.Owner.ID]
	return s, nil
}
func catalogUser(id int64) (User, error) {
	catalog.RLock()
	defer catalog.RUnlock()
	u, ok := catalog.users[id]
	if !ok {
		return User{}, sql.ErrNoRows
	}
	return u, nil
}

func userByName(name string) (User, bool) {
	catalog.RLock()
	defer catalog.RUnlock()
	u, ok := catalog.users[catalog.names[name]]
	return u, ok
}
