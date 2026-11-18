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
	"sync/atomic"
	"time"

	"github.com/labstack/echo/v4"
)

const iconHashCacheTTL = time.Second

type iconHashEntry struct {
	etag    string
	expires time.Time
}
type iconHashCache struct {
	sync.RWMutex
	entries map[string]iconHashEntry
}

var activeIconHashes atomic.Pointer[iconHashCache]

func init() { clearIconHashCache() }
func clearIconHashCache() {
	activeIconHashes.Store(&iconHashCache{entries: make(map[string]iconHashEntry)})
}
func (cache *iconHashCache) matches(username, etag string, now time.Time) bool {
	if etag == "" {
		return false
	}
	cache.RLock()
	entry, ok := cache.entries[username]
	cache.RUnlock()
	return ok && entry.etag == etag && now.Before(entry.expires)
}
func (cache *iconHashCache) put(username, etag string, started time.Time) {
	expires := started.Add(iconHashCacheTTL)
	if !time.Now().Before(expires) {
		return
	}
	cache.Lock()
	if len(cache.entries) >= 20000 {
		cache.entries = make(map[string]iconHashEntry)
	}
	cache.entries[username] = iconHashEntry{etag: etag, expires: expires}
	cache.Unlock()
}
func invalidateIconHash(username string) {
	cache := activeIconHashes.Load()
	cache.Lock()
	delete(cache.entries, username)
	cache.Unlock()
}
func resetCacheHandler(c echo.Context) error {
	host, _, err := net.SplitHostPort(c.Request().RemoteAddr)
	ip := net.ParseIP(host)
	if err != nil || !(ip.IsLoopback() || ip.Equal(net.ParseIP("10.99.0.11")) || ip.Equal(net.ParseIP("10.99.0.12")) || ip.Equal(net.ParseIP("10.99.0.13"))) {
		return echo.NewHTTPError(http.StatusForbidden, "internal endpoint")
	}
	clearIconHashCache()
	clearUserResponseCache()
	clearStreamMetadataCache()
	clearTagCatalog()
	return c.NoContent(http.StatusNoContent)
}

var cacheResetClient = &http.Client{Timeout: 3 * time.Second}

func resetResponseCaches(ctx context.Context) error {
	clearIconHashCache()
	clearUserResponseCache()
	clearStreamMetadataCache()
	clearTagCatalog()
	peers := os.Getenv("ISUCON_CACHE_RESET_URLS")
	if peers == "" {
		peers = "http://10.99.0.11:8080/internal/cache-reset,http://10.99.0.12:8080/internal/cache-reset,http://10.99.0.13:8080/internal/cache-reset"
	}
	for _, peer := range strings.Split(peers, ",") {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, peer, nil)
		if err != nil {
			return err
		}
		resp, err := cacheResetClient.Do(req)
		if err != nil {
			return err
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			return fmt.Errorf("cache reset at %s returned %d", peer, resp.StatusCode)
		}
	}
	return nil
}
