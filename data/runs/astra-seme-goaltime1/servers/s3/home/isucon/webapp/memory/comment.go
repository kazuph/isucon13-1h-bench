package main

import (
	"github.com/labstack/echo/v4"
	"sort"
	"time"
)

func getLivecommentsHandler(c echo.Context) error {
	if e := verifyUserSession(c); e != nil {
		return e
	}
	id, e := paramID(c, "livestream_id")
	if e != nil {
		return e
	}
	n, e := limit(c)
	if e != nil {
		return e
	}
	mu.RLock()
	defer mu.RUnlock()
	ids := make([]int64, 0, len(streamComments[id]))
	for _, i := range streamComments[id] {
		if _, ok := state.Comments[i]; ok {
			ids = append(ids, i)
		}
	}
	sort.SliceStable(ids, func(i, j int) bool { return state.Comments[ids[i]].CreatedAt > state.Comments[ids[j]].CreatedAt })
	if len(ids) > n {
		ids = ids[:n]
	}
	out := make([]Livecomment, 0, len(ids))
	for _, i := range ids {
		out = append(out, commentResponse(i))
	}
	return c.JSON(200, out)
}
func postLivecommentHandler(c echo.Context) error {
	if e := verifyUserSession(c); e != nil {
		return e
	}
	id, e := paramID(c, "livestream_id")
	if e != nil {
		return e
	}
	var r PostLivecommentRequest
	if e := decode(c, &r); e != nil {
		return e
	}
	mu.Lock()
	defer mu.Unlock()
	s, ok := state.Streams[id]
	if !ok {
		return echo.NewHTTPError(404)
	}
	for _, i := range streamWords[id] {
		w := state.Words[i]
		if w.UserID == s.UserID && matches(r.Comment, w.Word) {
			return echo.NewHTTPError(400, "このコメントがスパム判定されました")
		}
	}
	v := LivecommentModel{ID: next("comment"), UserID: uid(c), LivestreamID: id, Comment: r.Comment, Tip: r.Tip, CreatedAt: time.Now().Unix()}
	if e := commit(Event{Kind: "comment", Comment: v}); e != nil {
		return e
	}
	return c.JSON(201, commentResponse(v.ID))
}
func getReactionsHandler(c echo.Context) error {
	if e := verifyUserSession(c); e != nil {
		return e
	}
	id, e := paramID(c, "livestream_id")
	if e != nil {
		return e
	}
	n, e := limit(c)
	if e != nil {
		return e
	}
	mu.RLock()
	defer mu.RUnlock()
	ids := append([]int64{}, streamReactions[id]...)
	sort.SliceStable(ids, func(i, j int) bool { return state.Reactions[ids[i]].CreatedAt > state.Reactions[ids[j]].CreatedAt })
	if len(ids) > n {
		ids = ids[:n]
	}
	out := make([]Reaction, 0, len(ids))
	for _, i := range ids {
		out = append(out, reactionResponse(i))
	}
	return c.JSON(200, out)
}
func postReactionHandler(c echo.Context) error {
	if e := verifyUserSession(c); e != nil {
		return e
	}
	id, e := paramID(c, "livestream_id")
	if e != nil {
		return e
	}
	var r PostReactionRequest
	if e := decode(c, &r); e != nil {
		return e
	}
	mu.Lock()
	defer mu.Unlock()
	if _, ok := state.Streams[id]; !ok {
		return echo.NewHTTPError(404)
	}
	v := ReactionModel{ID: next("reaction"), UserID: uid(c), LivestreamID: id, EmojiName: r.EmojiName, CreatedAt: time.Now().Unix()}
	if e := commit(Event{Kind: "reaction", Reaction: v}); e != nil {
		return e
	}
	return c.JSON(201, reactionResponse(v.ID))
}
func getNgwords(c echo.Context) error {
	if e := verifyUserSession(c); e != nil {
		return e
	}
	id, e := paramID(c, "livestream_id")
	if e != nil {
		return e
	}
	mu.RLock()
	defer mu.RUnlock()
	out := make([]NGWord, 0)
	for _, i := range streamWords[id] {
		v := state.Words[i]
		if v.UserID == uid(c) {
			out = append(out, v)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return c.JSON(200, out)
}
func moderateHandler(c echo.Context) error {
	if e := verifyUserSession(c); e != nil {
		return e
	}
	id, e := paramID(c, "livestream_id")
	if e != nil {
		return e
	}
	var r ModerateRequest
	if e := decode(c, &r); e != nil {
		return e
	}
	mu.Lock()
	defer mu.Unlock()
	s, ok := state.Streams[id]
	if !ok || s.UserID != uid(c) {
		return echo.NewHTTPError(400, "cannot moderate another stream")
	}
	w := NGWord{ID: next("word"), UserID: uid(c), LivestreamID: id, Word: r.NGWord, CreatedAt: time.Now().Unix()}
	if e := commit(Event{Kind: "word", Word: w}); e != nil {
		return e
	}
	return c.JSON(201, map[string]int64{"word_id": w.ID})
}
func reportLivecommentHandler(c echo.Context) error {
	if e := verifyUserSession(c); e != nil {
		return e
	}
	id, e := paramID(c, "livestream_id")
	if e != nil {
		return e
	}
	cid, e := paramID(c, "livecomment_id")
	if e != nil {
		return e
	}
	mu.Lock()
	defer mu.Unlock()
	if _, ok := state.Streams[id]; !ok {
		return echo.NewHTTPError(404)
	}
	if _, ok := state.Comments[cid]; !ok {
		return echo.NewHTTPError(404)
	}
	v := LivecommentReportModel{ID: next("report"), UserID: uid(c), LivestreamID: id, LivecommentID: cid, CreatedAt: time.Now().Unix()}
	if e := commit(Event{Kind: "report", Report: v}); e != nil {
		return e
	}
	return c.JSON(201, reportResponse(v.ID))
}
func getLivecommentReportsHandler(c echo.Context) error {
	if e := verifyUserSession(c); e != nil {
		return e
	}
	id, e := paramID(c, "livestream_id")
	if e != nil {
		return e
	}
	mu.RLock()
	defer mu.RUnlock()
	s, ok := state.Streams[id]
	if !ok {
		return echo.NewHTTPError(404)
	}
	if s.UserID != uid(c) {
		return echo.NewHTTPError(403)
	}
	ids := []int64{}
	for i, v := range state.Reports {
		if v.LivestreamID == id {
			ids = append(ids, i)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]LivecommentReport, 0, len(ids))
	for _, i := range ids {
		if _, ok := state.Comments[state.Reports[i].LivecommentID]; !ok {
			return echo.NewHTTPError(500, "reported comment removed")
		}
		out = append(out, reportResponse(i))
	}
	return c.JSON(200, out)
}
