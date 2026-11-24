package main

import (
	"bytes"
	"compress/gzip"
	"expvar"
	"strconv"
	"strings"
	"sync"

	"github.com/labstack/echo/v4"
)

type searchKey struct {
	tag   string
	limit int
}
type searchResult struct {
	versions   []uint64
	ids        []int64
	plain      []byte
	once       sync.Once
	compressed []byte
}

var recentSearches = struct {
	sync.Mutex
	rows map[searchKey]*searchResult
}{rows: make(map[searchKey]*searchResult)}
var searchHits = expvar.NewInt("search_cache_hits")
var searchMisses = expvar.NewInt("search_cache_misses")

// Callers hold the catalog read lock. Compare immutable record versions,
// including icon changes, so a cached search is always an exact snapshot.
func searchResponse(ids []int64, key searchKey) (*searchResult, bool) {
	if key.limit >= 0 && len(ids) > key.limit {
		ids = ids[len(ids)-key.limit:]
	}
	if len(ids) > 512 {
		return &searchResult{plain: data.listStreams(ids, -1, true)}, false
	}
	recentSearches.Lock()
	defer recentSearches.Unlock()
	if old := recentSearches.rows[key]; old != nil && len(old.ids) == len(ids) {
		valid := true
		for i, id := range ids {
			if old.ids[i] != id || old.versions[i] != data.wireVersion[id] {
				valid = false
				break
			}
		}
		if valid {
			searchHits.Add(1)
			return old, true
		}
	}
	searchMisses.Add(1)
	r := &searchResult{ids: append([]int64(nil), ids...), versions: make([]uint64, len(ids)), plain: data.listStreams(ids, -1, true)}
	for i, id := range ids {
		r.versions[i] = data.wireVersion[id]
	}
	if len(recentSearches.rows) >= 128 {
		recentSearches.rows = make(map[searchKey]*searchResult)
	}
	recentSearches.rows[key] = r
	return r, false
}
func acceptsGzip(header string) bool {
	for _, token := range strings.Split(header, ",") {
		parts := strings.Split(strings.TrimSpace(token), ";")
		if !strings.EqualFold(strings.TrimSpace(parts[0]), "gzip") {
			continue
		}
		for _, parameter := range parts[1:] {
			k, v, ok := strings.Cut(strings.TrimSpace(parameter), "=")
			if ok && strings.EqualFold(k, "q") {
				q, err := strconv.ParseFloat(v, 64)
				if err != nil || q <= 0 {
					return false
				}
			}
		}
		return true
	}
	return false
}
func (r *searchResult) send(c echo.Context, compress bool) error {
	if compress && len(r.plain) > 1024 {
		c.Response().Header().Add("Vary", "Accept-Encoding")
		if acceptsGzip(c.Request().Header.Get("Accept-Encoding")) {
			r.once.Do(func() {
				var buf bytes.Buffer
				w, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
				_, _ = w.Write(r.plain)
				_ = w.Close()
				r.compressed = buf.Bytes()
			})
			c.Response().Header().Set("Content-Encoding", "gzip")
			return c.JSONBlob(200, r.compressed)
		}
	}
	return c.JSONBlob(200, r.plain)
}
