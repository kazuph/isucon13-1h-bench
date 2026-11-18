package main

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"sync"
	"time"
)

type memoryState struct {
	authUsers    map[string]UserModel
	defaultImage []byte
	defaultHash  string
	users        map[int64]User
	names        map[string]int64
	images       map[int64][]byte
	streams      map[int64]Livestream
	comments     map[int64]LivecommentModel
	reactions    map[int64]ReactionModel
	reports      map[int64]LivecommentReportModel
	words        map[int64][]NGWord
	viewers      []LivestreamViewerModel
	slots        []ReservationSlotModel
	tags         []Tag
	streamIDs    []int64
	userStreams  map[int64][]int64
	tagStreams   map[string][]int64
	commentIDs   map[int64][]int64
	reactionIDs  map[int64][]int64
}

var mem memoryState
var lifecycleMu sync.RWMutex
var memMu sync.RWMutex
var mutationLocks [1024]sync.Mutex

func loadMemory() error {
	m := memoryState{authUsers: map[string]UserModel{}, users: map[int64]User{}, names: map[string]int64{}, images: map[int64][]byte{}, streams: map[int64]Livestream{}, comments: map[int64]LivecommentModel{}, reactions: map[int64]ReactionModel{}, reports: map[int64]LivecommentReportModel{}, words: map[int64][]NGWord{}}
	var users []UserModel
	if e := dbConn.Select(&users, "SELECT * FROM users"); e != nil {
		return e
	}
	var themes []ThemeModel
	if e := dbConn.Select(&themes, "SELECT * FROM themes"); e != nil {
		return e
	}
	tm := map[int64]Theme{}
	for _, t := range themes {
		tm[t.UserID] = Theme{ID: t.ID, DarkMode: t.DarkMode}
	}
	var icons []struct {
		ID     int64  `db:"id"`
		UserID int64  `db:"user_id"`
		Image  []byte `db:"image"`
	}
	if e := dbConn.Select(&icons, "SELECT * FROM icons"); e != nil {
		return e
	}
	for _, i := range icons {
		m.images[i.UserID] = i.Image
	}
	fallback, e := os.ReadFile(fallbackImage)
	if e != nil {
		return e
	}
	m.defaultImage = fallback
	m.defaultHash = fmt.Sprintf("%x", sha256.Sum256(fallback))
	for _, u := range users {
		m.authUsers[u.Name] = u
		im := m.images[u.ID]
		if im == nil {
			im = fallback
		}
		m.images[u.ID] = im
		m.users[u.ID] = User{ID: u.ID, Name: u.Name, DisplayName: u.DisplayName, Description: u.Description, Theme: tm[u.ID], IconHash: fmt.Sprintf("%x", sha256.Sum256(im))}
		m.names[u.Name] = u.ID
	}
	if e := dbConn.Select(&m.tags, "SELECT * FROM tags"); e != nil {
		return e
	}
	tags := map[int64]Tag{}
	for _, t := range m.tags {
		tags[t.ID] = t
	}
	var links []LivestreamTagModel
	if e := dbConn.Select(&links, "SELECT * FROM livestream_tags ORDER BY id"); e != nil {
		return e
	}
	lt := map[int64][]Tag{}
	for _, l := range links {
		lt[l.LivestreamID] = append(lt[l.LivestreamID], tags[l.TagID])
	}
	var streams []LivestreamModel
	if e := dbConn.Select(&streams, "SELECT * FROM livestreams"); e != nil {
		return e
	}
	for _, l := range streams {
		ts := lt[l.ID]
		if ts == nil {
			ts = []Tag{}
		}
		m.streams[l.ID] = Livestream{ID: l.ID, Owner: m.users[l.UserID], Title: l.Title, Description: l.Description, PlaylistUrl: l.PlaylistUrl, ThumbnailUrl: l.ThumbnailUrl, StartAt: l.StartAt, EndAt: l.EndAt, Tags: ts}
	}
	var cs []LivecommentModel
	if e := dbConn.Select(&cs, "SELECT * FROM livecomments"); e != nil {
		return e
	}
	for _, c := range cs {
		m.comments[c.ID] = c
	}
	var rs []ReactionModel
	if e := dbConn.Select(&rs, "SELECT * FROM reactions"); e != nil {
		return e
	}
	for _, r := range rs {
		m.reactions[r.ID] = r
	}
	var reports []LivecommentReportModel
	if e := dbConn.Select(&reports, "SELECT * FROM livecomment_reports"); e != nil {
		return e
	}
	for _, r := range reports {
		m.reports[r.ID] = r
	}
	var words []NGWord
	if e := dbConn.Select(&words, "SELECT * FROM ng_words ORDER BY id"); e != nil {
		return e
	}
	for _, w := range words {
		m.words[w.LivestreamID] = append(m.words[w.LivestreamID], w)
	}
	if e := dbConn.Select(&m.viewers, "SELECT user_id,livestream_id,created_at FROM livestream_viewers_history"); e != nil {
		return e
	}
	if e := dbConn.Select(&m.slots, "SELECT * FROM reservation_slots"); e != nil {
		return e
	}
	m.buildIndexes()
	mem = m
	return nil
}
func memoryInitialize(c echo.Context) error {
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	memMu.Lock()
	defer memMu.Unlock()
	if out, e := exec.Command("../sql/init.sh").CombinedOutput(); e != nil {
		return fmt.Errorf("initialize: %s %w", out, e)
	}
	if e := notifyDNS("/reload", nil); e != nil {
		return e
	}
	if e := loadMemory(); e != nil {
		return e
	}
	if e := notifyIconCache("", nil, false); e != nil {
		return e
	}
	clearPasswordCache()
	return memoryJSON(c, 200, map[string]string{"language": "golang", "lang": "golang"})
}
func streamResponse(id int64) Livestream {
	s := mem.streams[id]
	s.Owner = mem.users[s.Owner.ID]
	return s
}
func commentResponse(m LivecommentModel) Livecomment {
	return Livecomment{ID: m.ID, User: mem.users[m.UserID], Livestream: streamResponse(m.LivestreamID), Comment: m.Comment, Tip: m.Tip, CreatedAt: m.CreatedAt}
}
func reactionResponse(m ReactionModel) Reaction {
	return Reaction{ID: m.ID, User: mem.users[m.UserID], Livestream: streamResponse(m.LivestreamID), EmojiName: m.EmojiName, CreatedAt: m.CreatedAt}
}
func insertMemory(query string, args ...interface{}) (int64, error) {
	r, e := memoryExec(query, args...)
	if e != nil {
		return 0, e
	}
	return r.LastInsertId()
}
func memoryHandler(c echo.Context) error {
	lifecycleMu.RLock()
	defer lifecycleMu.RUnlock()
	path := c.Path()
	method := c.Request().Method
	public := path == "/api/tag" || path == "/api/livestream/search" || path == "/api/user/:username/icon" || path == "/api/payment"
	var uid int64
	if !public {
		if e := verifyUserSession(c); e != nil {
			return e
		}
		sess, _ := session.Get(defaultSessionIDKey, c)
		uid = sess.Values[defaultUserIDKey].(int64)
	}
	id, parseErr := strconv.ParseInt(c.Param("livestream_id"), 10, 64)
	if c.Param("livestream_id") != "" && parseErr != nil {
		return echo.NewHTTPError(400, "invalid livestream_id")
	}
	write := method != "GET"
	if write {
		key := id
		if path == "/api/icon" {
			key = uid + 123
		}
		lk := &mutationLocks[uint64(key)%uint64(len(mutationLocks))]
		lk.Lock()
		defer lk.Unlock()
		memMu.Lock()
		c.Set("memory_lock_mode", true)
		defer func() {
			if _, ok := c.Get("memory_lock_mode").(bool); ok {
				memMu.Unlock()
			}
		}()
	} else {
		memMu.RLock()
		c.Set("memory_lock_mode", false)
		defer func() {
			if _, ok := c.Get("memory_lock_mode").(bool); ok {
				memMu.RUnlock()
			}
		}()
	}
	username := c.Param("username")
	user := mem.users[mem.names[username]]
	limit := int(^uint(0) >> 1)
	if v := c.QueryParam("limit"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 0 {
			return echo.NewHTTPError(400, "invalid limit")
		}
		limit = n
	}
	switch path {
	case "/api/tag":
		return memoryJSON(c, 200, struct {
			Tags []Tag `json:"tags"`
		}{mem.tags})
	case "/api/user/me":
		return memoryJSON(c, 200, mem.users[uid])
	case "/api/user/:username":
		if user.ID == 0 {
			return echo.NewHTTPError(404)
		}
		return memoryJSON(c, 200, user)
	case "/api/user/:username/theme":
		if user.ID == 0 {
			return echo.NewHTTPError(404)
		}
		return memoryJSON(c, 200, user.Theme)
	case "/api/user/:username/icon":
		if user.ID == 0 {
			return echo.NewHTTPError(404)
		}
		if c.Request().Header.Get("If-None-Match") == "\""+user.IconHash+"\"" {
			return c.NoContent(304)
		}
		c.Response().Header().Set("ETag", "\""+user.IconHash+"\"")
		return memoryBlob(c, 200, "image/jpeg", mem.images[user.ID])
	case "/api/icon":
		var req PostIconRequest
		if e := c.Bind(&req); e != nil {
			return e
		}
		results, e := memoryPersist([]persistQuery{makePersistQuery("DELETE FROM icons WHERE user_id=?", uid), makePersistQuery("INSERT INTO icons(user_id,image) VALUES(?,?)", uid, req.Image)})
		if e != nil {
			return e
		}
		iid := results[1].ID
		iconUsername := mem.users[uid].Name
		memMu.Unlock()
		cacheErr := notifyIconCache(iconUsername, req.Image, false)
		memMu.Lock()
		if cacheErr != nil {
			return cacheErr
		}
		mem.images[uid] = req.Image
		u := mem.users[uid]
		u.IconHash = fmt.Sprintf("%x", sha256.Sum256(req.Image))
		mem.users[uid] = u
		return memoryJSON(c, 201, PostIconResponse{ID: iid})
	case "/api/livestream/search", "/api/livestream", "/api/user/:username/livestream":
		ids := mem.streamIDs
		if path == "/api/livestream" {
			ids = mem.userStreams[uid]
		}
		if path == "/api/user/:username/livestream" {
			if user.ID == 0 {
				return echo.NewHTTPError(404)
			}
			ids = mem.userStreams[user.ID]
		}
		if tag := c.QueryParam("tag"); tag != "" {
			ids = mem.tagStreams[tag]
		} else if path == "/api/livestream/search" && len(ids) > limit {
			ids = ids[len(ids)-limit:]
		}
		out := make([]Livestream, 0, len(ids))
		for i := len(ids) - 1; i >= 0; i-- {
			out = append(out, streamResponse(ids[i]))
		}
		return memoryJSON(c, 200, out)
	case "/api/livestream/:livestream_id":
		if mem.streams[id].ID == 0 {
			return echo.NewHTTPError(404)
		}
		return memoryJSON(c, 200, streamResponse(id))
	case "/api/payment":
		var total int64
		for _, c := range mem.comments {
			total += c.Tip
		}
		return memoryJSON(c, 200, PaymentResult{TotalTip: total})
	}
	return memoryActivity(c, uid, id, limit, method)
}
func memoryActivity(c echo.Context, uid, id int64, limit int, method string) error {
	now := time.Now().Unix()
	path := c.Path()
	switch path {
	case "/api/livestream/:livestream_id/livecomment":
		if method == "GET" {
			out := []Livecomment{}
			ids := mem.commentIDs[id]
			for i := len(ids) - 1; i >= 0 && len(out) < limit; i-- {
				if v, ok := mem.comments[ids[i]]; ok {
					out = append(out, commentResponse(v))
				}
			}
			return memoryJSON(c, 200, out)
		}
		var req PostLivecommentRequest
		if e := c.Bind(&req); e != nil {
			return e
		}
		if mem.streams[id].ID == 0 {
			return echo.NewHTTPError(404)
		}
		for _, w := range mem.words[id] {
			if matchesSpam(req.Comment, w.Word) {
				return echo.NewHTTPError(400, "spam")
			}
		}
		cid, e := insertMemory("INSERT INTO livecomments(user_id,livestream_id,comment,tip,created_at) VALUES(?,?,?,?,?)", uid, id, req.Comment, req.Tip, now)
		if e != nil {
			return e
		}
		m := LivecommentModel{ID: cid, UserID: uid, LivestreamID: id, Comment: req.Comment, Tip: req.Tip, CreatedAt: now}
		mem.comments[cid] = m
		mem.commentIDs[id] = append(mem.commentIDs[id], cid)
		return memoryJSON(c, 201, commentResponse(m))
	case "/api/livestream/:livestream_id/reaction":
		if method == "GET" {
			out := []Reaction{}
			ids := mem.reactionIDs[id]
			for i := len(ids) - 1; i >= 0 && len(out) < limit; i-- {
				v := mem.reactions[ids[i]]
				out = append(out, reactionResponse(v))
			}
			return memoryJSON(c, 200, out)
		}
		var req PostReactionRequest
		if e := c.Bind(&req); e != nil {
			return e
		}
		rid, e := insertMemory("INSERT INTO reactions(user_id,livestream_id,emoji_name,created_at) VALUES(?,?,?,?)", uid, id, req.EmojiName, now)
		if e != nil {
			return e
		}
		m := ReactionModel{ID: rid, UserID: uid, LivestreamID: id, EmojiName: req.EmojiName, CreatedAt: now}
		mem.reactions[rid] = m
		mem.reactionIDs[id] = append(mem.reactionIDs[id], rid)
		return memoryJSON(c, 201, reactionResponse(m))
	case "/api/livestream/:livestream_id/ngwords":
		out := []NGWord{}
		for _, w := range mem.words[id] {
			if w.UserID == uid {
				out = append(out, w)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
		return memoryJSON(c, 200, out)
	case "/api/livestream/:livestream_id/moderate":
		if mem.streams[id].Owner.ID != uid {
			return echo.NewHTTPError(400)
		}
		var req ModerateRequest
		if e := c.Bind(&req); e != nil {
			return e
		}
		results, e := memoryPersist([]persistQuery{makePersistQuery("INSERT INTO ng_words(user_id,livestream_id,word,created_at) VALUES(?,?,?,?)", uid, id, req.NGWord, now), makePersistQuery("DELETE FROM livecomments WHERE livestream_id=? AND comment COLLATE utf8mb4_general_ci LIKE CONCAT('%',?,'%') COLLATE utf8mb4_general_ci", id, req.NGWord)})
		if e != nil {
			return e
		}
		wid := results[0].ID
		mem.words[id] = append(mem.words[id], NGWord{ID: wid, UserID: uid, LivestreamID: id, Word: req.NGWord, CreatedAt: now})
		for cid, v := range mem.comments {
			if v.LivestreamID == id && matchesSpam(v.Comment, req.NGWord) {
				delete(mem.comments, cid)
			}
		}
		return memoryJSON(c, 201, map[string]int64{"word_id": wid})
	case "/api/livestream/:livestream_id/enter":
		_, e := insertMemory("INSERT INTO livestream_viewers_history(user_id,livestream_id,created_at) VALUES(?,?,?)", uid, id, now)
		if e != nil {
			return e
		}
		mem.viewers = append(mem.viewers, LivestreamViewerModel{UserID: uid, LivestreamID: id, CreatedAt: now})
		return c.NoContent(200)
	case "/api/livestream/:livestream_id/exit":
		if _, e := memoryExec("DELETE FROM livestream_viewers_history WHERE user_id=? AND livestream_id=?", uid, id); e != nil {
			return e
		}
		out := mem.viewers[:0]
		for _, v := range mem.viewers {
			if v.UserID != uid || v.LivestreamID != id {
				out = append(out, v)
			}
		}
		mem.viewers = out
		return c.NoContent(200)
	case "/api/livestream/:livestream_id/livecomment/:livecomment_id/report":
		cid, _ := strconv.ParseInt(c.Param("livecomment_id"), 10, 64)
		v, ok := mem.comments[cid]
		if !ok || mem.streams[id].ID == 0 {
			return echo.NewHTTPError(404)
		}
		rid, e := insertMemory("INSERT INTO livecomment_reports(user_id,livestream_id,livecomment_id,created_at) VALUES(?,?,?,?)", uid, id, cid, now)
		if e != nil {
			return e
		}
		mem.reports[rid] = LivecommentReportModel{ID: rid, UserID: uid, LivestreamID: id, LivecommentID: cid, CreatedAt: now}
		return memoryJSON(c, 201, LivecommentReport{ID: rid, Reporter: mem.users[uid], Livecomment: commentResponse(v), CreatedAt: now})
	case "/api/livestream/:livestream_id/report":
		if mem.streams[id].Owner.ID != uid {
			return echo.NewHTTPError(403)
		}
		out := []LivecommentReport{}
		for _, r := range mem.reports {
			if r.LivestreamID == id {
				v, ok := mem.comments[r.LivecommentID]
				if !ok {
					continue
				}
				out = append(out, LivecommentReport{ID: r.ID, Reporter: mem.users[r.UserID], Livecomment: commentResponse(v), CreatedAt: r.CreatedAt})
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return memoryJSON(c, 200, out)
	case "/api/livestream/reservation":
		return memoryReserve(c, uid)
	case "/api/user/:username/statistics", "/api/livestream/:livestream_id/statistics":
		return memoryStats(c, id)
	}
	return echo.NewHTTPError(http.StatusNotFound)
}
func memoryReserve(c echo.Context, uid int64) error {
	var req ReserveLivestreamRequest
	if e := c.Bind(&req); e != nil {
		return e
	}
	if req.StartAt >= 1732496400 || req.EndAt <= 1700874000 {
		return echo.NewHTTPError(400)
	}
	for _, s := range mem.slots {
		if s.StartAt >= req.StartAt && s.EndAt <= req.EndAt && s.Slot < 1 {
			return echo.NewHTTPError(400)
		}
	}
	qs := []persistQuery{makePersistQuery("UPDATE reservation_slots SET slot=slot-1 WHERE start_at>=? AND end_at<=?", req.StartAt, req.EndAt), makePersistQuery("INSERT INTO livestreams(user_id,title,description,playlist_url,thumbnail_url,start_at,end_at) VALUES(?,?,?,?,?,?,?)", uid, req.Title, req.Description, req.PlaylistUrl, req.ThumbnailUrl, req.StartAt, req.EndAt)}
	tags := []Tag{}
	for _, tid := range req.Tags {
		qs = append(qs, makePersistQuery("INSERT INTO livestream_tags(livestream_id,tag_id) VALUES(?,?)", persistArg{Kind: "ref", Reference: 1}, tid))
		for _, t := range mem.tags {
			if t.ID == tid {
				tags = append(tags, t)
			}
		}
	}
	results, e := memoryPersist(qs)
	if e != nil {
		return e
	}
	id := results[1].ID
	for i, s := range mem.slots {
		if s.StartAt >= req.StartAt && s.EndAt <= req.EndAt {
			mem.slots[i].Slot--
		}
	}
	s := Livestream{ID: id, Owner: mem.users[uid], Title: req.Title, Description: req.Description, PlaylistUrl: req.PlaylistUrl, ThumbnailUrl: req.ThumbnailUrl, StartAt: req.StartAt, EndAt: req.EndAt, Tags: tags}
	mem.streams[id] = s
	mem.indexStream(s)
	return memoryJSON(c, 201, s)
}
func memoryStats(c echo.Context, id int64) error {
	scores := map[int64]int64{}
	uscores := map[int64]int64{}
	reacts := map[int64]int64{}
	tips := map[int64]int64{}
	counts := map[int64]int64{}
	max := map[int64]int64{}
	for _, r := range mem.reactions {
		scores[r.LivestreamID]++
		reacts[r.LivestreamID]++
	}
	for _, v := range mem.comments {
		scores[v.LivestreamID] += v.Tip
		tips[v.LivestreamID] += v.Tip
		counts[v.LivestreamID]++
		if v.Tip > max[v.LivestreamID] {
			max[v.LivestreamID] = v.Tip
		}
	}
	for sid, s := range mem.streams {
		uscores[s.Owner.ID] += scores[sid]
	}
	if c.Path() == "/api/livestream/:livestream_id/statistics" {
		if mem.streams[id].ID == 0 {
			return echo.NewHTTPError(400)
		}
		out := LivestreamStatistics{Rank: 1, TotalReactions: reacts[id], MaxTip: max[id]}
		for sid := range mem.streams {
			if scores[sid] > scores[id] || (scores[sid] == scores[id] && sid > id) {
				out.Rank++
			}
		}
		for _, v := range mem.viewers {
			if v.LivestreamID == id {
				out.ViewersCount++
			}
		}
		for _, r := range mem.reports {
			if r.LivestreamID == id {
				out.TotalReports++
			}
		}
		return memoryJSON(c, 200, out)
	}
	uid := mem.names[c.Param("username")]
	if uid == 0 {
		return echo.NewHTTPError(400)
	}
	out := UserStatistics{Rank: 1}
	for other, u := range mem.users {
		if uscores[other] > uscores[uid] || (uscores[other] == uscores[uid] && u.Name > mem.users[uid].Name) {
			out.Rank++
		}
	}
	for sid, s := range mem.streams {
		if s.Owner.ID == uid {
			out.TotalReactions += reacts[sid]
			out.TotalTip += tips[sid]
			out.TotalLivecomments += counts[sid]
		}
	}
	for _, v := range mem.viewers {
		if mem.streams[v.LivestreamID].Owner.ID == uid {
			out.ViewersCount++
		}
	}
	emoji := map[string]int64{}
	for _, r := range mem.reactions {
		if mem.streams[r.LivestreamID].Owner.ID == uid {
			emoji[r.EmojiName]++
		}
	}
	var best int64
	for em, n := range emoji {
		if n > best || (n == best && em > out.FavoriteEmoji) {
			best = n
			out.FavoriteEmoji = em
		}
	}
	return memoryJSON(c, 200, out)
}

// Ascending indexes let polling requests walk only the requested newest rows.
func (m *memoryState) buildIndexes() {
	m.streamIDs = nil
	m.userStreams = map[int64][]int64{}
	m.tagStreams = map[string][]int64{}
	m.commentIDs = map[int64][]int64{}
	m.reactionIDs = map[int64][]int64{}
	ids := make([]int64, 0, len(m.streams))
	for id := range m.streams {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		m.indexStream(m.streams[id])
	}
	for id, v := range m.comments {
		m.commentIDs[v.LivestreamID] = append(m.commentIDs[v.LivestreamID], id)
	}
	for _, ids := range m.commentIDs {
		sort.Slice(ids, func(i, j int) bool {
			a, b := m.comments[ids[i]], m.comments[ids[j]]
			if a.CreatedAt == b.CreatedAt {
				return a.ID < b.ID
			}
			return a.CreatedAt < b.CreatedAt
		})
	}
	for id, v := range m.reactions {
		m.reactionIDs[v.LivestreamID] = append(m.reactionIDs[v.LivestreamID], id)
	}
	for _, ids := range m.reactionIDs {
		sort.Slice(ids, func(i, j int) bool {
			a, b := m.reactions[ids[i]], m.reactions[ids[j]]
			if a.CreatedAt == b.CreatedAt {
				return a.ID < b.ID
			}
			return a.CreatedAt < b.CreatedAt
		})
	}
}
func (m *memoryState) indexStream(s Livestream) {
	m.streamIDs = append(m.streamIDs, s.ID)
	m.userStreams[s.Owner.ID] = append(m.userStreams[s.Owner.ID], s.ID)
	for _, t := range s.Tags {
		m.tagStreams[t.Name] = append(m.tagStreams[t.Name], s.ID)
	}
}

// Only the persistence wait releases the read-model lock. Per-entity mutation
// locks keep validations and commits ordered for the same mutable resource.
func memoryExec(q string, args ...interface{}) (sql.Result, error) {
	memMu.Unlock()
	defer memMu.Lock()
	if os.Getenv("ISUCON_PERSIST_URL") != "" {
		results, err := persistBatch([]persistQuery{makePersistQuery(q, args...)})
		if err != nil {
			return nil, err
		}
		if len(results) != 1 {
			return nil, fmt.Errorf("invalid persistence result")
		}
		return results[0], nil
	}
	return dbConn.Exec(q, args...)
}
func memoryBegin() (*sqlx.Tx, error) { memMu.Unlock(); defer memMu.Lock(); return dbConn.Beginx() }
func memoryTxExec(tx *sqlx.Tx, q string, args ...interface{}) (sql.Result, error) {
	memMu.Unlock()
	defer memMu.Lock()
	return tx.Exec(q, args...)
}
func memoryCommit(tx *sqlx.Tx) error { memMu.Unlock(); defer memMu.Lock(); return tx.Commit() }

// Response values contain immutable strings/slices and copies of model rows.
// Release the shared read model before encoding or waiting on the network.
func releaseResponseLock(c echo.Context) {
	if write, ok := c.Get("memory_lock_mode").(bool); ok {
		c.Set("memory_lock_mode", nil)
		if write {
			memMu.Unlock()
		} else {
			memMu.RUnlock()
		}
	}
}
func memoryJSON(c echo.Context, code int, value interface{}) error {
	releaseResponseLock(c)
	return c.JSON(code, value)
}
func memoryBlob(c echo.Context, code int, contentType string, data []byte) error {
	releaseResponseLock(c)
	return c.Blob(code, contentType, data)
}
