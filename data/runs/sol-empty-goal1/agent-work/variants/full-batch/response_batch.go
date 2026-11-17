package main

import (
 "context"
 "crypto/sha256"
 "fmt"
 "os"
 "sync"

 "github.com/jmoiron/sqlx"
)

var defaultIconOnce sync.Once
var defaultIconDigest string
var defaultIconError error

func defaultIconHash() (string, error) {
 defaultIconOnce.Do(func() {
  var image []byte
  image, defaultIconError = os.ReadFile(fallbackImage)
  if defaultIconError == nil { defaultIconDigest = fmt.Sprintf("%x", sha256.Sum256(image)) }
 })
 return defaultIconDigest, defaultIconError
}

func warmUserResponses(ctx context.Context, tx *sqlx.Tx, ids []int64) error {
 cache, _ := ctx.Value(responseCacheKey{}).(*responseCache)
 if cache == nil || len(ids) == 0 { return nil }
 unique := make(map[int64]bool)
 pending := make([]int64, 0, len(ids))
 for _, id := range ids {
  if _, ok := cache.users[id]; !ok && !unique[id] { pending = append(pending, id); unique[id] = true }
 }
 if len(pending) == 0 { return nil }
 hash, err := defaultIconHash()
 if err != nil { return err }
 query, args, err := sqlx.In(`SELECT u.id, u.name, u.display_name, u.description,
 t.id AS theme_id, t.dark_mode, COALESCE(i.image_hash, ?) AS icon_hash
 FROM users u JOIN themes t ON t.user_id=u.id LEFT JOIN icons i ON i.user_id=u.id
 WHERE u.id IN (?)`, hash, pending)
 if err != nil { return err }
 var rows []struct {
  ID int64 `db:"id"`
  Name string `db:"name"`
  DisplayName string `db:"display_name"`
  Description string `db:"description"`
  ThemeID int64 `db:"theme_id"`
  DarkMode bool `db:"dark_mode"`
  IconHash string `db:"icon_hash"`
 }
 if err := tx.SelectContext(ctx, &rows, query, args...); err != nil { return err }
 for _, row := range rows {
  cache.users[row.ID] = User{ID: row.ID, Name: row.Name, DisplayName: row.DisplayName, Description: row.Description,
   Theme: Theme{ID: row.ThemeID, DarkMode: row.DarkMode}, IconHash: row.IconHash}
 }
 return nil
}

func warmLivestreamResponses(ctx context.Context, tx *sqlx.Tx, models []*LivestreamModel) error {
 cache, _ := ctx.Value(responseCacheKey{}).(*responseCache)
 if cache == nil || len(models) == 0 { return nil }
 userIDs := make([]int64, 0, len(models))
 ids := make([]int64, 0, len(models))
 for _, model := range models { userIDs = append(userIDs, model.UserID); ids = append(ids, model.ID) }
 if err := warmUserResponses(ctx, tx, userIDs); err != nil { return err }
 query, args, err := sqlx.In(`SELECT lt.livestream_id, t.id, t.name FROM livestream_tags lt JOIN tags t ON t.id=lt.tag_id WHERE lt.livestream_id IN (?) ORDER BY lt.id`, ids)
 if err != nil { return err }
 var rows []struct {
  LivestreamID int64 `db:"livestream_id"`
  ID int64 `db:"id"`
  Name string `db:"name"`
 }
 if err := tx.SelectContext(ctx, &rows, query, args...); err != nil { return err }
 tags := make(map[int64][]Tag)
 for _, row := range rows { tags[row.LivestreamID] = append(tags[row.LivestreamID], Tag{ID:row.ID, Name:row.Name}) }
 for _, model := range models {
  owner, err := userResponseByID(ctx, tx, model.UserID)
  if err != nil { return err }
  streamTags := tags[model.ID]
  if streamTags == nil { streamTags = []Tag{} }
  cache.streams[model.ID] = Livestream{ID:model.ID, Owner:owner, Title:model.Title, Tags:streamTags, Description:model.Description,
   PlaylistUrl:model.PlaylistUrl, ThumbnailUrl:model.ThumbnailUrl, StartAt:model.StartAt, EndAt:model.EndAt}
 }
 return nil
}
