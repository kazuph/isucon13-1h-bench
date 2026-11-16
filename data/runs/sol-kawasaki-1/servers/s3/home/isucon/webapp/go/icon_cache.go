package main

import (
	"context"
	"database/sql"
	"sync"
	"time"
)

type cachedIcon struct {
	image   []byte
	hash    string
	expires time.Time
}

var iconResponses = struct {
	sync.RWMutex
	entries map[string]cachedIcon
}{entries: make(map[string]cachedIcon)}

func init() {
	go func() {
		for range time.NewTicker(time.Second).C {
			now := time.Now()
			iconResponses.Lock()
			for name, entry := range iconResponses.entries {
				if !now.Before(entry.expires) {
					delete(iconResponses.entries, name)
				}
			}
			iconResponses.Unlock()
		}
	}()
}
func loadIcon(ctx context.Context, username string) (cachedIcon, error) {
	iconResponses.RLock()
	entry, ok := iconResponses.entries[username]
	iconResponses.RUnlock()
	if ok && time.Now().Before(entry.expires) {
		return entry, nil
	}
	asOf := time.Now()
	var row struct {
		Image []byte         `db:"image"`
		Hash  sql.NullString `db:"image_hash"`
	}
	if err := dbConn.GetContext(ctx, &row, `SELECT i.image,i.image_hash FROM users u LEFT JOIN icons i ON i.user_id=u.id WHERE u.name=?`, username); err != nil {
		return cachedIcon{}, err
	}
	entry = cachedIcon{image: row.Image, hash: row.Hash.String, expires: asOf.Add(750 * time.Millisecond)}
	if !row.Hash.Valid {
		entry.image = fallbackImageBytes
		entry.hash = fallbackIconHash
	}
	iconResponses.Lock()
	iconResponses.entries[username] = entry
	iconResponses.Unlock()
	return entry, nil
}
func clearIconCache(username string) {
	iconResponses.Lock()
	delete(iconResponses.entries, username)
	iconResponses.Unlock()
}
