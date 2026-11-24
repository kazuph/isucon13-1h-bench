package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/sessions"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

func fail(code int, msg string) error { return echo.NewHTTPError(code, msg) }
func decode(c echo.Context, v interface{}) error {
	if err := json.NewDecoder(c.Request().Body).Decode(v); err != nil {
		return fail(400, "invalid JSON")
	}
	return nil
}
func identity(c echo.Context) int64 {
	s, _ := session.Get(defaultSessionIDKey, c)
	return s.Values[defaultUserIDKey].(int64)
}
func streamID(c echo.Context) (int64, error) {
	id, e := strconv.ParseInt(c.Param("livestream_id"), 10, 64)
	if e != nil {
		return 0, fail(400, "invalid livestream id")
	}
	return id, nil
}
func getLimit(c echo.Context) (int, error) {
	if c.QueryParam("limit") == "" {
		return int(^uint(0) >> 1), nil
	}
	n, e := strconv.Atoi(c.QueryParam("limit"))
	if e != nil || n < 0 {
		return 0, fail(400, "invalid limit")
	}
	return n, nil
}
func memInitialize(c echo.Context) error {
	memMu.Lock()
	defer memMu.Unlock()
	if out, err := exec.Command("../sql/init.sh").CombinedOutput(); err != nil {
		return fail(500, fmt.Sprintf("initialize: %s %v", out, err))
	}
	s, err := importStore()
	if err != nil {
		return err
	}
	if err = installStore(s); err != nil {
		return err
	}
	return c.JSON(200, map[string]string{"language": "golang", "lang": "golang"})
}
func memRegister(c echo.Context) error {
	var req PostUserRequest
	if err := decode(c, &req); err != nil {
		return err
	}
	if req.Name == "pipe" {
		return fail(400, "reserved username")
	}
	hash, err := passwordHash([]byte(req.Password))
	if err != nil {
		return err
	}
	memMu.Lock()
	defer memMu.Unlock()
	if _, ok := mem.Names[req.Name]; ok {
		return fail(500, "duplicate username")
	}
	id := mem.next("user")
	u := &memUser{Model: UserModel{ID: id, Name: req.Name, DisplayName: req.DisplayName, Description: req.Description, HashedPassword: string(hash)}, Response: User{ID: id, Name: req.Name, DisplayName: req.DisplayName, Description: req.Description, Theme: Theme{ID: mem.next("theme"), DarkMode: req.Theme.DarkMode}, IconHash: defaultHash}}
	if err = persistEvent(memEvent{Kind: "user", User: u}); err != nil {
		return err
	}
	return c.JSON(201, u.Response)
}
func memLogin(c echo.Context) error {
	var req LoginRequest
	if err := decode(c, &req); err != nil {
		return err
	}
	memMu.RLock()
	u := mem.Users[mem.Names[req.Username]]
	var model UserModel
	if u != nil {
		model = u.Model
	}
	memMu.RUnlock()
	if u == nil {
		return fail(401, "invalid username or password")
	}
	if err := passwordVerify([]byte(model.HashedPassword), []byte(req.Password)); err != nil {
		return fail(401, "invalid username or password")
	}
	sess, err := session.Get(defaultSessionIDKey, c)
	if err != nil {
		return fail(401, "invalid session")
	}
	sess.Options = &sessions.Options{Domain: "u.isucon.dev", MaxAge: 60000, Path: "/"}
	sess.Values[defaultSessionIDKey] = uuid.NewString()
	sess.Values[defaultUserIDKey] = model.ID
	sess.Values[defaultUsernameKey] = model.Name
	sess.Values[defaultSessionExpiresKey] = time.Now().Add(time.Hour).Unix()
	if err = sess.Save(c.Request(), c.Response()); err != nil {
		return err
	}
	return c.NoContent(200)
}
func memGetUser(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	memMu.RLock()
	defer memMu.RUnlock()
	id := mem.Names[c.Param("username")]
	if c.Path() == "/api/user/me" {
		id = identity(c)
	}
	u := mem.Users[id]
	if u == nil {
		return fail(404, "user not found")
	}
	if c.Path() == "/api/user/:username/theme" {
		return c.JSON(200, u.Response.Theme)
	}
	return c.JSON(200, u.Response)
}
func memGetIcon(c echo.Context) error {
	memMu.RLock()
	u := mem.Users[mem.Names[c.Param("username")]]
	if u == nil {
		memMu.RUnlock()
		return fail(404, "user not found")
	}
	hash := u.Response.IconHash
	image := u.Image
	if !u.HasIcon {
		image = defaultImage
	}
	memMu.RUnlock()
	etag := "\"" + hash + "\""
	c.Response().Header().Set("ETag", etag)
	if c.Request().Header.Get("If-None-Match") == etag {
		return c.NoContent(304)
	}
	return c.Blob(200, "image/jpeg", image)
}
func memPostIcon(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	var req PostIconRequest
	if err := decode(c, &req); err != nil {
		return err
	}
	if req.Image == nil {
		return fail(400, "image must not be null")
	}
	memMu.Lock()
	defer memMu.Unlock()
	id := mem.next("icon")
	if err := persistEvent(memEvent{Kind: "icon", ID: id, UserID: identity(c), Image: req.Image}); err != nil {
		return err
	}
	return c.JSON(201, PostIconResponse{ID: id})
}
func memTags(c echo.Context) error {
	memMu.RLock()
	defer memMu.RUnlock()
	return c.JSON(200, map[string]interface{}{"tags": mem.Tags})
}
func memReserve(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	var req ReserveLivestreamRequest
	if err := decode(c, &req); err != nil {
		return err
	}
	if req.StartAt >= time.Date(2024, 11, 25, 1, 0, 0, 0, time.UTC).Unix() || req.EndAt <= time.Date(2023, 11, 25, 1, 0, 0, 0, time.UTC).Unix() {
		return fail(400, "bad reservation time range")
	}
	memMu.Lock()
	defer memMu.Unlock()
	for _, slot := range mem.Slots {
		if slot.StartAt >= req.StartAt && slot.EndAt <= req.EndAt && slot.Slot < 1 {
			return fail(400, "no reservation slots")
		}
	}
	for _, id := range req.Tags {
		found := false
		for _, t := range mem.Tags {
			if t.ID == id {
				found = true
				break
			}
		}
		if !found {
			return fail(500, "tag not found")
		}
	}
	m := &LivestreamModel{ID: mem.next("stream"), UserID: identity(c), Title: req.Title, Description: req.Description, PlaylistUrl: req.PlaylistUrl, ThumbnailUrl: req.ThumbnailUrl, StartAt: req.StartAt, EndAt: req.EndAt}
	if err := persistEvent(memEvent{Kind: "stream", Stream: m, Tags: req.Tags}); err != nil {
		return err
	}
	return c.JSON(201, mem.streamResponse(mem.Streams[m.ID]))
}
func memSearch(c echo.Context) error {
	limit, err := getLimit(c)
	if err != nil {
		return err
	}
	memMu.RLock()
	defer memMu.RUnlock()
	tag := c.QueryParam("tag")
	if tag != "" {
		limit = int(^uint(0) >> 1)
	}
	ids := mem.StreamIDs
	if tag != "" {
		ids = mem.TagStreams[tag]
	}
	result := make([]Livestream, 0)
	for i := len(ids) - 1; i >= 0 && len(result) < limit; i-- {
		result = append(result, mem.streamResponse(mem.Streams[ids[i]]))
	}
	return c.JSON(200, result)
}
func memUserStreams(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	memMu.RLock()
	defer memMu.RUnlock()
	uid := identity(c)
	if c.Param("username") != "" {
		uid = mem.Names[c.Param("username")]
		if uid == 0 {
			return fail(404, "user not found")
		}
	}
	result := []Livestream{}
	for _, id := range mem.UserStreams[uid] {
		result = append(result, mem.streamResponse(mem.Streams[id]))
	}
	return c.JSON(200, result)
}
func memStreamGet(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := streamID(c)
	if err != nil {
		return err
	}
	memMu.RLock()
	defer memMu.RUnlock()
	l := mem.Streams[id]
	if l == nil {
		return fail(404, "livestream not found")
	}
	return c.JSON(200, mem.streamResponse(l))
}
func memEnterExit(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := streamID(c)
	if err != nil {
		return err
	}
	memMu.Lock()
	defer memMu.Unlock()
	if mem.Streams[id] == nil {
		return fail(404, "livestream not found")
	}
	kind := "enter"
	if c.Request().Method == http.MethodDelete {
		kind = "exit"
	}
	if err = persistEvent(memEvent{Kind: kind, Viewer: &LivestreamViewerModel{UserID: identity(c), LivestreamID: id, CreatedAt: time.Now().Unix()}}); err != nil {
		return err
	}
	return c.NoContent(200)
}
func memComments(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := streamID(c)
	if err != nil {
		return err
	}
	limit, err := getLimit(c)
	if err != nil {
		return err
	}
	memMu.RLock()
	defer memMu.RUnlock()
	result := []Livecomment{}
	if l := mem.Streams[id]; l != nil {
		ids := append([]int64(nil), l.Comments...)
		sort.SliceStable(ids, func(i, j int) bool {
			a, b := mem.Comments[ids[i]], mem.Comments[ids[j]]
			if a.CreatedAt == b.CreatedAt {
				return a.ID > b.ID
			}
			return a.CreatedAt > b.CreatedAt
		})
		if len(ids) > limit {
			ids = ids[:limit]
		}
		for _, id := range ids {
			result = append(result, mem.commentResponse(mem.Comments[id]))
		}
	}
	return c.JSON(200, result)
}
func memPostComment(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := streamID(c)
	if err != nil {
		return err
	}
	var req PostLivecommentRequest
	if err = decode(c, &req); err != nil {
		return err
	}
	memMu.Lock()
	defer memMu.Unlock()
	l := mem.Streams[id]
	if l == nil {
		return fail(404, "livestream not found")
	}
	for _, w := range l.Words {
		if w.UserID == l.Model.UserID && likeContains(req.Comment, w.Word) {
			return fail(400, "このコメントがスパム判定されました")
		}
	}
	m := &LivecommentModel{ID: mem.next("comment"), UserID: identity(c), LivestreamID: id, Comment: req.Comment, Tip: req.Tip, CreatedAt: time.Now().Unix()}
	if err = persistEvent(memEvent{Kind: "comment", Comment: m}); err != nil {
		return err
	}
	return c.JSON(201, mem.commentResponse(*m))
}
func memReactions(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := streamID(c)
	if err != nil {
		return err
	}
	limit, err := getLimit(c)
	if err != nil {
		return err
	}
	memMu.RLock()
	defer memMu.RUnlock()
	result := []Reaction{}
	if l := mem.Streams[id]; l != nil {
		ids := append([]int64(nil), l.Reactions...)
		sort.SliceStable(ids, func(i, j int) bool {
			a, b := mem.Reactions[ids[i]], mem.Reactions[ids[j]]
			if a.CreatedAt == b.CreatedAt {
				return a.ID > b.ID
			}
			return a.CreatedAt > b.CreatedAt
		})
		if len(ids) > limit {
			ids = ids[:limit]
		}
		for _, id := range ids {
			result = append(result, mem.reactionResponse(mem.Reactions[id]))
		}
	}
	return c.JSON(200, result)
}
func memPostReaction(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := streamID(c)
	if err != nil {
		return err
	}
	var req PostReactionRequest
	if err = decode(c, &req); err != nil {
		return err
	}
	memMu.Lock()
	defer memMu.Unlock()
	if mem.Streams[id] == nil {
		return fail(404, "livestream not found")
	}
	m := &ReactionModel{ID: mem.next("reaction"), UserID: identity(c), LivestreamID: id, EmojiName: req.EmojiName, CreatedAt: time.Now().Unix()}
	if err = persistEvent(memEvent{Kind: "reaction", Reaction: m}); err != nil {
		return err
	}
	return c.JSON(201, mem.reactionResponse(*m))
}
func memWords(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := streamID(c)
	if err != nil {
		return err
	}
	memMu.RLock()
	defer memMu.RUnlock()
	words := []NGWord{}
	if l := mem.Streams[id]; l != nil {
		for _, w := range l.Words {
			if w.UserID == identity(c) {
				words = append(words, w)
			}
		}
	}
	sort.Slice(words, func(i, j int) bool {
		if words[i].CreatedAt == words[j].CreatedAt {
			return words[i].ID > words[j].ID
		}
		return words[i].CreatedAt > words[j].CreatedAt
	})
	return c.JSON(200, words)
}
func memModerate(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := streamID(c)
	if err != nil {
		return err
	}
	var req ModerateRequest
	if err = decode(c, &req); err != nil {
		return err
	}
	memMu.Lock()
	defer memMu.Unlock()
	l := mem.Streams[id]
	if l == nil || l.Model.UserID != identity(c) {
		return fail(400, "cannot moderate another stream")
	}
	w := &NGWord{ID: mem.next("word"), UserID: identity(c), LivestreamID: id, Word: req.NGWord, CreatedAt: time.Now().Unix()}
	if err = persistEvent(memEvent{Kind: "word", Word: w}); err != nil {
		return err
	}
	return c.JSON(201, map[string]int64{"word_id": w.ID})
}
func memPostReport(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := streamID(c)
	if err != nil {
		return err
	}
	cid, err := strconv.ParseInt(c.Param("livecomment_id"), 10, 64)
	if err != nil {
		return fail(400, "invalid comment id")
	}
	memMu.Lock()
	defer memMu.Unlock()
	if mem.Streams[id] == nil {
		return fail(404, "livestream not found")
	}
	if _, ok := mem.Comments[cid]; !ok {
		return fail(404, "livecomment not found")
	}
	r := &LivecommentReportModel{ID: mem.next("report"), UserID: identity(c), LivestreamID: id, LivecommentID: cid, CreatedAt: time.Now().Unix()}
	response := mem.reportResponse(*r)
	if err = persistEvent(memEvent{Kind: "report", Report: r}); err != nil {
		return err
	}
	return c.JSON(201, response)
}
func memReports(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := streamID(c)
	if err != nil {
		return err
	}
	memMu.RLock()
	defer memMu.RUnlock()
	l := mem.Streams[id]
	if l == nil {
		return fail(500, "livestream not found")
	}
	if l.Model.UserID != identity(c) {
		return fail(403, "cannot get another stream's reports")
	}
	reports := []LivecommentReport{}
	for _, id := range l.Reports {
		r := mem.Reports[id]
		if _, ok := mem.Comments[r.LivecommentID]; !ok {
			return fail(500, "livecomment not found")
		}
		reports = append(reports, mem.reportResponse(r))
	}
	return c.JSON(200, reports)
}
func memPayment(c echo.Context) error {
	memMu.RLock()
	v := mem.TotalTip
	memMu.RUnlock()
	return c.JSON(200, PaymentResult{TotalTip: v})
}
func memStreamStats(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := streamID(c)
	if err != nil {
		return err
	}
	memMu.RLock()
	defer memMu.RUnlock()
	l := mem.Streams[id]
	if l == nil {
		return fail(400, "livestream not found")
	}
	rank := int64(1)
	score := l.Tips + l.ReactionCount
	for oid, o := range mem.Streams {
		v := o.Tips + o.ReactionCount
		if v > score || (v == score && oid > id) {
			rank++
		}
	}
	return c.JSON(200, LivestreamStatistics{Rank: rank, ViewersCount: l.ViewerCount, TotalReactions: l.ReactionCount, TotalReports: int64(len(l.Reports)), MaxTip: l.MaxTip})
}
func memUserStats(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	memMu.RLock()
	defer memMu.RUnlock()
	id := mem.Names[c.Param("username")]
	if id == 0 {
		return fail(400, "user not found")
	}
	scores := make(map[int64]int64, len(mem.Users))
	stats := UserStatistics{Rank: 1}
	emoji := map[string]int64{}
	for _, l := range mem.Streams {
		scores[l.Model.UserID] += l.Tips + l.ReactionCount
		if l.Model.UserID == id {
			stats.TotalTip += l.Tips
			stats.TotalReactions += l.ReactionCount
			stats.TotalLivecomments += l.CommentCount
			stats.ViewersCount += l.ViewerCount
			for e, n := range l.Emoji {
				emoji[e] += n
			}
		}
	}
	score := scores[id]
	for uid, u := range mem.Users {
		v := scores[uid]
		if v > score || (v == score && u.Model.Name > c.Param("username")) {
			stats.Rank++
		}
	}
	max := int64(0)
	for e, n := range emoji {
		if n > max || (n == max && e > stats.FavoriteEmoji) {
			max = n
			stats.FavoriteEmoji = e
		}
	}
	return c.JSON(200, stats)
}
