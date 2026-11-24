package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

var origin = os.Getenv("ISUPIPE_ORIGIN")
var originClient = &http.Client{Timeout: 40 * time.Second}
var lifecycle sync.RWMutex
var catalog struct {
	sync.RWMutex
	users   map[int64]User
	names   map[string]int64
	all     []int64
	byOwner map[int64][]int64
	byTag   map[string][]int64
	tags    []Tag
	streams map[int64]Livestream
}
var moderationLocks [256]sync.RWMutex
var iconWrites [64]sync.Mutex

func reloadCatalog(ctx context.Context) error {
	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var ids []int64
	if err = tx.SelectContext(ctx, &ids, "SELECT id FROM users"); err != nil {
		return err
	}
	users, err := queryUsers(ctx, tx, ids)
	if err != nil {
		return err
	}
	var models []*LivestreamModel
	if err = tx.SelectContext(ctx, &models, "SELECT * FROM livestreams"); err != nil {
		return err
	}
	streams := make(map[int64]Livestream, len(models))
	for len(models) > 0 {
		n := len(models)
		if n > 500 {
			n = 500
		}
		batch, err := queryStreams(ctx, tx, models[:n])
		if err != nil {
			return err
		}
		for _, s := range batch {
			streams[s.ID] = s
		}
		models = models[n:]
	}
	var tags []Tag
	if err = tx.SelectContext(ctx, &tags, "SELECT id,name FROM tags ORDER BY id"); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	names := make(map[string]int64, len(users))
	for id, u := range users {
		names[u.Name] = id
	}
	catalog.Lock()
	defer catalog.Unlock()
	catalog.users = users
	catalog.names = names
	catalog.streams = streams
	catalog.tags = tags
	catalog.all = nil
	catalog.byOwner = make(map[int64][]int64)
	catalog.byTag = make(map[string][]int64)
	ordered := make([]int64, 0, len(streams))
	for id := range streams {
		ordered = append(ordered, id)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	for _, id := range ordered {
		indexStream(streams[id])
	}
	images.Lock()
	images.data = nil
	images.size = 0
	images.Unlock()
	return nil
}

func cachedUser(id int64) (User, bool) {
	catalog.RLock()
	defer catalog.RUnlock()
	u, ok := catalog.users[id]
	return u, ok
}
func namedUser(name string) (User, bool) {
	catalog.RLock()
	defer catalog.RUnlock()
	id, ok := catalog.names[name]
	return catalog.users[id], ok
}
func cachedStream(id int64) (Livestream, bool) {
	catalog.RLock()
	defer catalog.RUnlock()
	s, ok := catalog.streams[id]
	if ok {
		s.Owner = catalog.users[s.Owner.ID]
	}
	return s, ok
}

func forwardCatalogWrite(c echo.Context) error {
	req, err := http.NewRequestWithContext(c.Request().Context(), http.MethodPost, origin+c.Path(), c.Request().Body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", c.Request().Header.Get("Content-Type"))
	res, err := originClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode/100 == 2 {
		if c.Path() == "/api/initialize" {
			if err := reloadCatalog(c.Request().Context()); err != nil {
				return err
			}
		} else {
			var u User
			if err := json.Unmarshal(data, &u); err != nil {
				return err
			}
			catalog.Lock()
			catalog.users[u.ID] = u
			catalog.names[u.Name] = u.ID
			catalog.Unlock()
		}
	}
	return c.Blob(res.StatusCode, res.Header.Get("Content-Type"), data)
}

func catalogMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if c.Path() == "/api/initialize" {
			lifecycle.Lock()
			defer lifecycle.Unlock()
		} else {
			lifecycle.RLock()
			defer lifecycle.RUnlock()
		}
		return next(c)
	}
}

func rememberStream(s Livestream) {
	if origin != "" {
		catalog.Lock()
		catalog.streams[s.ID] = s
		indexStream(s)
		catalog.Unlock()
	}
}
func rememberIcon(id int64, hash string) {
	catalog.Lock()
	defer catalog.Unlock()
	if u, ok := catalog.users[id]; ok {
		u.IconHash = hash
		catalog.users[id] = u
	}
}
func bootstrapCatalog(e *echo.Echo) {
	if origin == "" {
		return
	}
	if err := reloadCatalog(context.Background()); err != nil {
		panic(fmt.Errorf("loading catalog: %w", err))
	}
	e.Use(catalogMiddleware)
	e.POST("/api/register", forwardCatalogWrite)
	e.POST("/api/initialize", forwardCatalogWrite)
}

var images struct {
	sync.RWMutex
	data map[string][]byte
	size int
}

func iconBytes(hash string) ([]byte, bool) {
	images.RLock()
	defer images.RUnlock()
	b, ok := images.data[hash]
	return b, ok
}
func cacheIcon(hash string, b []byte) {
	images.Lock()
	defer images.Unlock()
	if _, ok := images.data[hash]; ok {
		return
	}
	if images.data == nil || images.size+len(b) > 64<<20 {
		images.data = make(map[string][]byte)
		images.size = 0
	}
	images.data[hash] = b
	images.size += len(b)
}

func addID(ids []int64, id int64) []int64 {
	if len(ids) == 0 || ids[len(ids)-1] <= id {
		return append(ids, id)
	}
	i := sort.Search(len(ids), func(i int) bool { return ids[i] >= id })
	ids = append(ids, 0)
	copy(ids[i+1:], ids[i:])
	ids[i] = id
	return ids
}
func indexStream(s Livestream) {
	catalog.all = addID(catalog.all, s.ID)
	catalog.byOwner[s.Owner.ID] = addID(catalog.byOwner[s.Owner.ID], s.ID)
	for _, t := range s.Tags {
		catalog.byTag[t.Name] = addID(catalog.byTag[t.Name], s.ID)
	}
}
func streamList(c echo.Context, owner int64) error {
	tag := c.QueryParam("tag")
	limit := -1
	if owner == 0 && tag == "" && c.QueryParam("limit") != "" {
		v, err := strconv.Atoi(c.QueryParam("limit"))
		if err != nil || v < 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid limit")
		}
		limit = v
	}
	catalog.RLock()
	ids := catalog.all
	if owner != 0 {
		ids = catalog.byOwner[owner]
	} else if tag != "" {
		ids = catalog.byTag[tag]
	}
	count := len(ids)
	if limit >= 0 && count > limit {
		count = limit
	}
	streams := make([]Livestream, count)
	for i := range streams {
		j := len(ids) - 1 - i
		if owner != 0 {
			j = i
		}
		s := catalog.streams[ids[j]]
		s.Owner = catalog.users[s.Owner.ID]
		streams[i] = s
	}
	catalog.RUnlock()
	return c.JSON(http.StatusOK, streams)
}
