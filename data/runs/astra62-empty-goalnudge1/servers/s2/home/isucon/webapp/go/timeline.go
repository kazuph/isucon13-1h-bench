package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"

	"github.com/labstack/echo/v4"
)

const timelinePageSize = 128

type timelineShard[T any] struct {
	sync.Mutex
	streams map[int64][]T
}

type timeline[T any] struct {
	shards [256]timelineShard[T]
	query  string
	keys   func(T) (int64, int64)
}

var comments = timeline[LivecommentModel]{query: "SELECT * FROM livecomments WHERE livestream_id=? AND moderated=FALSE ORDER BY created_at DESC,id DESC", keys: func(m LivecommentModel) (int64, int64) { return m.CreatedAt, m.ID }}
var reactionFeed = timeline[ReactionModel]{query: "SELECT * FROM reactions WHERE livestream_id=? ORDER BY created_at DESC,id DESC", keys: func(m ReactionModel) (int64, int64) { return m.CreatedAt, m.ID }}

func (t *timeline[T]) shard(id int64) *timelineShard[T] {
	return &t.shards[uint64(id)%uint64(len(t.shards))]
}

func (t *timeline[T]) reset() {
	for i := range t.shards {
		s := &t.shards[i]
		s.Lock()
		s.streams = make(map[int64][]T)
		s.Unlock()
	}
}
func (t *timeline[T]) invalidate(id int64) {
	s := t.shard(id)
	s.Lock()
	delete(s.streams, id)
	s.Unlock()
}

func (t *timeline[T]) push(id int64, value T) {
	s := t.shard(id)
	s.Lock()
	defer s.Unlock()
	items, ok := s.streams[id]
	if !ok {
		return
	}
	timestamp, key := t.keys(value)
	index := sort.Search(len(items), func(i int) bool { at, k := t.keys(items[i]); return at < timestamp || at == timestamp && k <= key })
	if index == timelinePageSize {
		return
	}
	if index < len(items) {
		_, k := t.keys(items[index])
		if k == key {
			return
		}
	}
	if len(items) < timelinePageSize {
		items = append(items, value)
	}
	copy(items[index+1:], items[index:])
	items[index] = value
	s.streams[id] = items
}

func (t *timeline[T]) page(ctx context.Context, id int64, raw string) ([]T, error) {
	limit := -1
	if raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return nil, echo.NewHTTPError(400, "limit must be a nonnegative integer")
		}
		limit = n
	}
	catalog.RLock()
	_, exists := catalog.streams[id]
	catalog.RUnlock()
	if !exists {
		return []T{}, nil
	}
	if limit == 0 {
		return []T{}, nil
	}
	s := t.shard(id)
	s.Lock()
	items, ok := s.streams[id]
	if !ok {
		if err := dbConn.SelectContext(ctx, &items, t.query+" LIMIT ?", id, timelinePageSize); err != nil {
			s.Unlock()
			return nil, err
		}
		s.streams[id] = items
	}
	if limit >= 0 && limit <= len(items) || len(items) < timelinePageSize {
		if limit >= 0 && limit < len(items) {
			items = items[:limit]
		}
		page := append([]T{}, items...)
		s.Unlock()
		return page, nil
	}
	s.Unlock()
	query := t.query
	if limit >= 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	var all []T
	if err := dbConn.SelectContext(ctx, &all, query, id); err != nil {
		return nil, err
	}
	return all, nil
}
