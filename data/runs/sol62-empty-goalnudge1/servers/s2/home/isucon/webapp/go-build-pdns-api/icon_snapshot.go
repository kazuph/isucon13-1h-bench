package main

import (
	"context"
	"database/sql"
	"sync/atomic"
	"time"
)

type iconCatalogSnapshot struct {
	hashes  map[string]string
	expires time.Time
}

var latestIconSnapshot atomic.Pointer[iconCatalogSnapshot]

func refreshIconSnapshots(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		refreshIconSnapshot()
		<-ticker.C
	}
}

func refreshIconSnapshot() {
	started := time.Now()
	expires := started.Add(maximumResponseCacheTTL)
	ctx, cancel := context.WithDeadline(context.Background(), expires)
	defer cancel()

	rows, err := dbConn.QueryContext(ctx, "SELECT name, icon_hash FROM users ORDER BY id DESC LIMIT 10000")
	if err != nil {
		return
	}
	defer rows.Close()
	hashes := make(map[string]string)
	for rows.Next() {
		var name string
		var hash sql.NullString
		if err := rows.Scan(&name, &hash); err != nil {
			return
		}
		if hash.Valid {
			hashes[name] = hash.String
		} else {
			hashes[name] = fallbackImageHash
		}
	}
	if rows.Err() == nil && time.Now().Before(expires) {
		latestIconSnapshot.Store(&iconCatalogSnapshot{hashes: hashes, expires: expires})
	}
}

func lookupIconSnapshot(username string, now time.Time) (string, bool) {
	snapshot := latestIconSnapshot.Load()
	if snapshot == nil || !now.Before(snapshot.expires) {
		return "", false
	}
	hash, ok := snapshot.hashes[username]
	return hash, ok
}
