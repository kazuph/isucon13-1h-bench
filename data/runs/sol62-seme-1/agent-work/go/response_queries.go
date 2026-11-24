package main

import (
	"context"
	"database/sql"

	"github.com/jmoiron/sqlx"
)

func loadUsers(ctx context.Context, tx *sqlx.Tx, ids []int64) (map[int64]User, error) {
	users := make(map[int64]User, len(ids))
	if len(ids) == 0 {
		return users, nil
	}
	query, args, err := sqlx.In(`
		SELECT u.id, u.name, u.display_name, u.description, t.id AS theme_id,
			t.dark_mode, COALESCE(i.image_hash, ?) AS icon_hash
		FROM users u JOIN themes t ON t.user_id = u.id
		LEFT JOIN icons i ON i.user_id = u.id WHERE u.id IN (?)`, fallbackIconHash, ids)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	for _, row := range rows {
		users[row.ID] = User{
			ID: row.ID, Name: row.Name, DisplayName: row.DisplayName, Description: row.Description,
			Theme: Theme{ID: row.ThemeID, DarkMode: row.DarkMode}, IconHash: row.IconHash,
		}
	}
	for _, id := range ids {
		if _, ok := users[id]; !ok {
			return nil, sql.ErrNoRows
		}
	}
	return users, nil
}

func loadUser(ctx context.Context, tx *sqlx.Tx, id int64) (User, error) {
	users, err := loadUsers(ctx, tx, []int64{id})
	return users[id], err
}

func loadLivestream(ctx context.Context, tx *sqlx.Tx, id int64) (Livestream, error) {
	var model LivestreamModel
	if err := tx.GetContext(ctx, &model, "SELECT * FROM livestreams WHERE id = ?", id); err != nil {
		return Livestream{}, err
	}
	return fillLivestreamResponse(ctx, tx, model)
}

func fillLivestreamList(ctx context.Context, tx *sqlx.Tx, models []LivestreamModel) ([]Livestream, error) {
	streams := make([]Livestream, len(models))
	if len(models) == 0 {
		return streams, nil
	}
	ownerIDs := make([]int64, len(models))
	streamIDs := make([]int64, len(models))
	for i, model := range models {
		ownerIDs[i] = model.UserID
		streamIDs[i] = model.ID
	}
	owners, err := loadUsers(ctx, tx, ownerIDs)
	if err != nil {
		return nil, err
	}
	query, args, err := sqlx.In(`
		SELECT lt.livestream_id, t.id, t.name
		FROM livestream_tags lt LEFT JOIN tags t ON t.id = lt.tag_id
		WHERE lt.livestream_id IN (?) ORDER BY lt.id`, streamIDs)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		StreamID int64  `db:"livestream_id"`
		ID       int64  `db:"id"`
		Name     string `db:"name"`
	}
	if err := tx.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, err
	}
	tags := make(map[int64][]Tag)
	for _, row := range rows {
		tags[row.StreamID] = append(tags[row.StreamID], Tag{ID: row.ID, Name: row.Name})
	}
	for i, model := range models {
		streamTags := tags[model.ID]
		if streamTags == nil {
			streamTags = []Tag{}
		}
		streams[i] = Livestream{
			ID: model.ID, Owner: owners[model.UserID], Title: model.Title, Tags: streamTags,
			Description: model.Description, PlaylistUrl: model.PlaylistUrl, ThumbnailUrl: model.ThumbnailUrl,
			StartAt: model.StartAt, EndAt: model.EndAt,
		}
	}
	return streams, nil
}
