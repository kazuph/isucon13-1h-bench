package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

const sharedUserTTL = 500 * time.Millisecond

type sharedUserEntry struct {
	user    User
	expires time.Time
}

var sharedUsers = struct {
	sync.RWMutex
	epoch   uint64
	entries map[int64]sharedUserEntry
	names   map[string]int64
}{entries: make(map[int64]sharedUserEntry), names: make(map[string]int64)}

func currentUserCacheEpoch() uint64 {
	sharedUsers.RLock()
	defer sharedUsers.RUnlock()
	return sharedUsers.epoch
}
func sharedUser(id int64) (User, bool) {
	sharedUsers.RLock()
	entry, ok := sharedUsers.entries[id]
	sharedUsers.RUnlock()
	return entry.user, ok && time.Now().Before(entry.expires)
}
func publishSharedUser(ctx context.Context, user User) {
	cache := requestCache(ctx)
	// Never publish a value from an uncommitted write transaction.
	if cache == nil || !cache.shareUsers {
		return
	}
	expires := cache.created.Add(sharedUserTTL)
	if !time.Now().Before(expires) {
		return
	}
	sharedUsers.Lock()
	defer sharedUsers.Unlock()
	// Reject requests begun before an initialization or an icon write.
	if cache.epoch != sharedUsers.epoch {
		return
	}
	if len(sharedUsers.entries) >= 8192 {
		sharedUsers.entries = make(map[int64]sharedUserEntry)
		sharedUsers.names = make(map[string]int64)
	}
	sharedUsers.entries[user.ID] = sharedUserEntry{user: user, expires: expires}
	sharedUsers.names[user.Name] = user.ID
}
func invalidateSharedUser(id int64) {
	sharedUsers.Lock()
	defer sharedUsers.Unlock()
	if entry, ok := sharedUsers.entries[id]; ok {
		delete(sharedUsers.names, entry.user.Name)
	}
	delete(sharedUsers.entries, id)
	sharedUsers.epoch++
}
func resetSharedUsers() {
	resetSharedStreams()
	sharedUsers.Lock()
	defer sharedUsers.Unlock()
	sharedUsers.entries = make(map[int64]sharedUserEntry)
	sharedUsers.names = make(map[string]int64)
	sharedUsers.epoch++
}
func resetSharedUsersHandler(c echo.Context) error {
	host, _, err := net.SplitHostPort(c.Request().RemoteAddr)
	if err != nil {
		return echo.NewHTTPError(http.StatusForbidden)
	}
	if host != "127.0.0.1" && host != "::1" && host != "10.99.0.11" && host != "10.99.0.12" {
		return echo.NewHTTPError(http.StatusForbidden)
	}
	resetSharedUsers()
	return c.NoContent(http.StatusNoContent)
}

var cacheResetHTTP = &http.Client{Timeout: 3 * time.Second}

func resetPeerUserCache(ctx context.Context) error {
	peer := os.Getenv("ISUCON13_CACHE_PEER_URL")
	if peer == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(peer, "/")+"/internal/cache/reset", nil)
	if err != nil {
		return err
	}
	res, err := cacheResetHTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	if res.StatusCode != http.StatusNoContent {
		return fmt.Errorf("peer cache reset HTTP %d", res.StatusCode)
	}
	return nil
}

func sharedUserNamed(name string) (User, bool) {
	sharedUsers.RLock()
	id, ok := sharedUsers.names[name]
	entry, exists := sharedUsers.entries[id]
	sharedUsers.RUnlock()
	return entry.user, ok && exists && time.Now().Before(entry.expires)
}
