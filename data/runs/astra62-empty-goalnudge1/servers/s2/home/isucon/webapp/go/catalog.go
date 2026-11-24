package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"github.com/jmoiron/sqlx"
	"sort"
	"sync"
)

type catalogState struct {
	streamJSON map[int64][]byte
	tagsJSON   []byte
	tagByID    map[int64]Tag
	words      map[wordOwner][]NGWord
	allStreams []int64
	byOwner    map[int64][]int64
	byTag      map[string][]int64
	tags       []*Tag
	users      map[int64]User
	names      map[string]int64
	streams    map[int64]Livestream
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
	state := catalogState{words: make(map[wordOwner][]NGWord), byOwner: map[int64][]int64{}, byTag: map[string][]int64{}, users: map[int64]User{}, names: map[string]int64{}, streams: map[int64]Livestream{}, tags: []*Tag{}}
	if err := tx.SelectContext(ctx, &state.tags, "SELECT * FROM tags"); err != nil {
		return err
	}
	state.tagByID = make(map[int64]Tag, len(state.tags))
	for _, t := range state.tags {
		state.tagByID[t.ID] = *t
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
	state.streamJSON = make(map[int64][]byte, len(state.streams))
	state.tagsJSON, err = json.Marshal(TagsResponse{Tags: state.tags})
	if err != nil {
		return err
	}
	for _, stream := range state.streams {
		state.indexStream(stream)
		state.renderStream(stream.ID)
	}
	sort.Slice(state.allStreams, func(i, j int) bool { return state.allStreams[i] < state.allStreams[j] })
	for _, ids := range state.byOwner {
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	}
	for _, ids := range state.byTag {
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	}
	var words []NGWord
	if err := tx.SelectContext(ctx, &words, "SELECT * FROM ng_words ORDER BY created_at DESC,id DESC"); err != nil {
		return err
	}
	for _, w := range words {
		w.searchText = foldModerationText(w.Word)
		k := wordOwner{w.LivestreamID, w.UserID}
		state.words[k] = append(state.words[k], w)
	}
	var slots []ReservationSlotModel
	if err := tx.SelectContext(ctx, &slots, "SELECT * FROM reservation_slots ORDER BY start_at,id"); err != nil {
		return err
	}
	snapshot, err := loadMetrics(ctx, tx)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := publishIcons(ctx, state.users); err != nil {
		return err
	}
	catalog.Lock()
	forgetIconObjects()
	comments.reset()
	reactionFeed.reset()
	metrics.Lock()
	metrics.streams = snapshot
	metrics.Unlock()
	reservationSlots.Lock()
	reservationSlots.rows = slots
	reservationSlots.Unlock()
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

func (state *catalogState) indexStream(s Livestream) {
	state.allStreams = append(state.allStreams, s.ID)
	state.byOwner[s.Owner.ID] = append(state.byOwner[s.Owner.ID], s.ID)
	for _, tag := range s.Tags {
		state.byTag[tag.Name] = append(state.byTag[tag.Name], s.ID)
	}
}

func insertOrdered(ids []int64, id int64) []int64 {
	pos := sort.Search(len(ids), func(i int) bool { return ids[i] >= id })
	ids = append(ids, 0)
	copy(ids[pos+1:], ids[pos:])
	ids[pos] = id
	return ids
}

func (state *catalogState) renderStream(id int64) {
	stream := state.streams[id]
	stream.Owner = state.users[stream.Owner.ID]
	// Livestream contains only scalar values, structs and slices with JSON representations.
	data, err := json.Marshal(stream)
	if err != nil {
		panic(err)
	}
	state.streamJSON[id] = data
}

func (state *catalogState) addStream(s Livestream) {
	state.streams[s.ID] = s
	state.renderStream(s.ID)
	state.allStreams = insertOrdered(state.allStreams, s.ID)
	state.byOwner[s.Owner.ID] = insertOrdered(state.byOwner[s.Owner.ID], s.ID)
	for _, tag := range s.Tags {
		state.byTag[tag.Name] = insertOrdered(state.byTag[tag.Name], s.ID)
	}
}

func listStreamsJSON(owner int64, tag string, limit int) []byte {
	catalog.RLock()
	ids := catalog.allStreams
	if owner != 0 {
		ids = catalog.byOwner[owner]
	} else if tag != "" {
		ids = catalog.byTag[tag]
	}
	n := len(ids)
	if limit >= 0 && limit < n {
		n = limit
	}
	parts := make([][]byte, n)
	for i := range parts {
		j := len(ids) - 1 - i
		if owner != 0 {
			j = i
		}
		parts[i] = catalog.streamJSON[ids[j]]
	}
	catalog.RUnlock()
	size := 2
	for _, p := range parts {
		size += len(p) + 1
	}
	buf := bytes.NewBuffer(make([]byte, 0, size))
	buf.WriteByte('[')
	for i, p := range parts {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(p)
	}
	buf.WriteByte(']')
	return buf.Bytes()
}

type wordOwner struct{ stream, user int64 }

func wordsFor(stream, user int64) []NGWord {
	catalog.RLock()
	defer catalog.RUnlock()
	return catalog.words[wordOwner{stream, user}]
}
func publishWord(word NGWord) {
	catalog.Lock()
	defer catalog.Unlock()
	word.searchText = foldModerationText(word.Word)
	key := wordOwner{word.LivestreamID, word.UserID}
	// Readers keep their immutable slice after releasing the catalog lock.
	catalog.words[key] = append([]NGWord{word}, catalog.words[key]...)
}
