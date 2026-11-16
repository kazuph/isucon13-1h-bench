package main

import (
	"fmt"
	"github.com/labstack/echo/v4"
	"net/http"
	"sort"
	"strings"
	"time"
)

func routes(e *echo.Echo) {
	e.GET("/api/tag", read(func(c echo.Context) error { return c.JSON(200, map[string]interface{}{"tags": st.Tags}) }, false))
	e.GET("/api/user/me", read(func(c echo.Context) error {
		if st.Users[uid(c)] == nil {
			return echo.NewHTTPError(404, "user not found")
		}
		return c.JSON(200, user(uid(c)))
	}, true))
	e.GET("/api/user/:username", read(func(c echo.Context) error {
		id := st.Names[c.Param("username")]
		if id == 0 {
			return echo.NewHTTPError(404, "user not found")
		}
		return c.JSON(200, user(id))
	}, true))
	e.GET("/api/user/:username/theme", read(func(c echo.Context) error {
		id := st.Names[c.Param("username")]
		if id == 0 {
			return echo.NewHTTPError(404, "user not found")
		}
		return c.JSON(200, st.Users[id].Theme)
	}, true))
	e.GET("/api/user/:username/icon", read(func(c echo.Context) error {
		u := st.Users[st.Names[c.Param("username")]]
		if u == nil {
			return echo.NewHTTPError(404, "user not found")
		}
		etag := `"` + u.Hash + `"`
		c.Response().Header().Set("ETag", etag)
		if c.Request().Header.Get("If-None-Match") == etag {
			return c.NoContent(304)
		}
		b := u.Image
		if b == nil {
			b = defaultImage
		}
		return c.Blob(200, "image/jpeg", b)
	}, false))
	e.POST("/api/icon", write(func(c echo.Context) error {
		var req PostIconRequest
		if err := bind(c, &req); err != nil {
			return err
		}
		id := next("icon")
		if err := record(Event{Kind: "icon", ID: id, UserID: uid(c), Image: req.Image}); err != nil {
			return err
		}
		return done(c, 201, PostIconResponse{ID: id})
	}))
	e.GET("/api/livestream/search", read(search, false))
	e.GET("/api/livestream", read(func(c echo.Context) error { return listStreams(c, userStreams[uid(c)], false) }, true))
	e.GET("/api/user/:username/livestream", read(func(c echo.Context) error {
		id := st.Names[c.Param("username")]
		if id == 0 {
			return echo.NewHTTPError(404, "user not found")
		}
		return listStreams(c, userStreams[id], false)
	}, true))
	e.GET("/api/livestream/:livestream_id", read(func(c echo.Context) error {
		id, err := num(c, "livestream_id")
		if err != nil {
			return err
		}
		if st.Streams[id] == nil {
			return echo.NewHTTPError(404, "stream not found")
		}
		return c.JSON(200, stream(id))
	}, true))
	e.POST("/api/livestream/reservation", write(reserve))
	e.POST("/api/livestream/:livestream_id/enter", write(func(c echo.Context) error { return viewer(c, "enter") }))
	e.DELETE("/api/livestream/:livestream_id/exit", write(func(c echo.Context) error { return viewer(c, "exit") }))
	e.GET("/api/livestream/:livestream_id/livecomment", read(comments, true))
	e.POST("/api/livestream/:livestream_id/livecomment", write(postComment))
	e.GET("/api/livestream/:livestream_id/reaction", read(reactions, true))
	e.POST("/api/livestream/:livestream_id/reaction", write(postReaction))
	e.GET("/api/livestream/:livestream_id/report", read(reports, true))
	e.POST("/api/livestream/:livestream_id/livecomment/:livecomment_id/report", write(postReport))
	e.GET("/api/livestream/:livestream_id/ngwords", read(words, true))
	e.POST("/api/livestream/:livestream_id/moderate", write(moderate))
	e.GET("/api/user/:username/statistics", read(userStatistics, true))
	e.GET("/api/livestream/:livestream_id/statistics", read(streamStatistics, true))
	e.GET("/api/payment", read(func(c echo.Context) error { return c.JSON(200, PaymentResult{TotalTip: totalTips}) }, false))
}
func listStreams(c echo.Context, ids []int64, reverse bool) error {
	result := make([]Livestream, 0, len(ids))
	if reverse {
		for i := len(ids) - 1; i >= 0; i-- {
			result = append(result, stream(ids[i]))
		}
	} else {
		for _, id := range ids {
			result = append(result, stream(id))
		}
	}
	return c.JSON(200, result)
}
func search(c echo.Context) error {
	ids := streamIDs
	if t := c.QueryParam("tag"); t != "" {
		ids = tagStreams[t]
	} else {
		n, e := limit(c)
		if e != nil {
			return e
		}
		if n < len(ids) {
			ids = ids[len(ids)-n:]
		}
	}
	return listStreams(c, ids, true)
}
func reserve(c echo.Context) error {
	var r ReserveLivestreamRequest
	if e := bind(c, &r); e != nil {
		return e
	}
	if r.StartAt >= 1732496400 || r.EndAt <= 1700874000 {
		return echo.NewHTTPError(400, "bad reservation time range")
	}
	for _, s := range st.Slots {
		if s.StartAt >= r.StartAt && s.EndAt <= r.EndAt && s.Slot < 1 {
			return echo.NewHTTPError(400, "no slots")
		}
	}
	tags := make([]Tag, 0, len(r.Tags))
	for _, id := range r.Tags {
		found := false
		for _, t := range st.Tags {
			if t.ID == id {
				tags = append(tags, t)
				found = true
				break
			}
		}
		if !found {
			return echo.NewHTTPError(500, "tag not found")
		}
	}
	id := next("stream")
	s := &StreamData{Model: LivestreamModel{ID: id, UserID: uid(c), Title: r.Title, Description: r.Description, PlaylistUrl: r.PlaylistUrl, ThumbnailUrl: r.ThumbnailUrl, StartAt: r.StartAt, EndAt: r.EndAt}, Tags: tags}
	if e := record(Event{Kind: "stream", Stream: s}); e != nil {
		return e
	}
	return done(c, 201, stream(id))
}
func viewer(c echo.Context, kind string) error {
	id, e := num(c, "livestream_id")
	if e != nil {
		return e
	}
	if e = record(Event{Kind: kind, StreamID: id, UserID: uid(c), Time: time.Now().Unix()}); e != nil {
		return e
	}
	return done(c, 200, nil)
}
func comments(c echo.Context) error {
	id, e := num(c, "livestream_id")
	if e != nil {
		return e
	}
	n, e := limit(c)
	if e != nil {
		return e
	}
	out := []Livecomment{}
	ids := commentIDs[id]
	for i := len(ids) - 1; i >= 0 && len(out) < n; i-- {
		if m, ok := st.Comments[ids[i]]; ok {
			out = append(out, comment(m))
		}
	}
	return c.JSON(200, out)
}
func reactions(c echo.Context) error {
	id, e := num(c, "livestream_id")
	if e != nil {
		return e
	}
	n, e := limit(c)
	if e != nil {
		return e
	}
	out := []Reaction{}
	ids := reactionIDs[id]
	for i := len(ids) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, reaction(st.Reactions[ids[i]]))
	}
	return c.JSON(200, out)
}

// SQL LIKE semantics, including wildcard and escape handling, with utf8mb4_bin case sensitivity.
func like(text, word string) bool {
	if !strings.ContainsAny(word, "%_\\") {
		return strings.Contains(text, word)
	}
	s := []rune(text)
	p := []rune("%" + word + "%")
	dp := make([]bool, len(s)+1)
	dp[0] = true
	for i := 0; i < len(p); i++ {
		ch := p[i]
		escaped := false
		if ch == '\\' && i+1 < len(p) {
			i++
			ch = p[i]
			escaped = true
		}
		nd := make([]bool, len(s)+1)
		if ch == '%' && !escaped {
			nd[0] = dp[0]
			for j := 1; j <= len(s); j++ {
				nd[j] = dp[j] || nd[j-1]
			}
		} else {
			for j := 1; j <= len(s); j++ {
				nd[j] = dp[j-1] && ((ch == '_' && !escaped) || s[j-1] == ch)
			}
		}
		dp = nd
	}
	return dp[len(s)]
}
func postComment(c echo.Context) error {
	id, e := num(c, "livestream_id")
	if e != nil {
		return e
	}
	s := st.Streams[id]
	if s == nil {
		return echo.NewHTTPError(404, "stream not found")
	}
	var r PostLivecommentRequest
	if e = bind(c, &r); e != nil {
		return e
	}
	for _, wid := range wordIDs[id] {
		w := st.Words[wid]
		if w.UserID == s.Model.UserID && like(r.Comment, w.Word) {
			return echo.NewHTTPError(400, "このコメントがスパム判定されました")
		}
	}
	m := LivecommentModel{ID: next("comment"), UserID: uid(c), LivestreamID: id, Comment: r.Comment, Tip: r.Tip, CreatedAt: time.Now().Unix()}
	if e = record(Event{Kind: "comment", Comment: &m}); e != nil {
		return e
	}
	return done(c, 201, comment(m))
}
func postReaction(c echo.Context) error {
	id, e := num(c, "livestream_id")
	if e != nil {
		return e
	}
	if st.Streams[id] == nil {
		return echo.NewHTTPError(404, "stream not found")
	}
	var r PostReactionRequest
	if e = bind(c, &r); e != nil {
		return e
	}
	m := ReactionModel{ID: next("reaction"), UserID: uid(c), LivestreamID: id, EmojiName: r.EmojiName, CreatedAt: time.Now().Unix()}
	if e = record(Event{Kind: "reaction", Reaction: &m}); e != nil {
		return e
	}
	return done(c, 201, reaction(m))
}
func postReport(c echo.Context) error {
	id, e := num(c, "livestream_id")
	if e != nil {
		return e
	}
	cid, e := num(c, "livecomment_id")
	if e != nil {
		return e
	}
	if st.Streams[id] == nil {
		return echo.NewHTTPError(404, "stream not found")
	}
	if _, ok := st.Comments[cid]; !ok {
		return echo.NewHTTPError(404, "comment not found")
	}
	r := LivecommentReportModel{ID: next("report"), UserID: uid(c), LivestreamID: id, LivecommentID: cid, CreatedAt: time.Now().Unix()}
	if e = record(Event{Kind: "report", Report: &r}); e != nil {
		return e
	}
	return done(c, 201, report(r))
}
func reports(c echo.Context) error {
	id, e := num(c, "livestream_id")
	if e != nil {
		return e
	}
	s := st.Streams[id]
	if s == nil {
		return echo.NewHTTPError(404, "stream not found")
	}
	if s.Model.UserID != uid(c) {
		return echo.NewHTTPError(403, "not owner")
	}
	out := []LivecommentReport{}
	for _, rid := range reportIDs[id] {
		r := st.Reports[rid]
		if _, ok := st.Comments[r.LivecommentID]; ok {
			out = append(out, report(r))
		}
	}
	return c.JSON(200, out)
}
func words(c echo.Context) error {
	id, e := num(c, "livestream_id")
	if e != nil {
		return e
	}
	out := []NGWord{}
	ids := wordIDs[id]
	for i := len(ids) - 1; i >= 0; i-- {
		w := st.Words[ids[i]]
		if w.UserID == uid(c) {
			out = append(out, w)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return c.JSON(200, out)
}
func moderate(c echo.Context) error {
	id, e := num(c, "livestream_id")
	if e != nil {
		return e
	}
	s := st.Streams[id]
	if s == nil || s.Model.UserID != uid(c) {
		return echo.NewHTTPError(400, "not owner")
	}
	var r ModerateRequest
	if e = bind(c, &r); e != nil {
		return e
	}
	w := NGWord{ID: next("word"), UserID: uid(c), LivestreamID: id, Word: r.NGWord, CreatedAt: time.Now().Unix()}
	deleted := []int64{}
	for _, cid := range commentIDs[id] {
		if m, ok := st.Comments[cid]; ok && like(m.Comment, r.NGWord) {
			deleted = append(deleted, cid)
		}
	}
	if e = record(Event{Kind: "word", Word: &w, Delete: deleted}); e != nil {
		return e
	}
	return done(c, 201, map[string]int64{"word_id": w.ID})
}
func userStatistics(c echo.Context) error {
	id := st.Names[c.Param("username")]
	if id == 0 {
		return echo.NewHTTPError(400, "user not found")
	}
	a := userStats[id]
	score := a.Reactions + a.Tips
	rank := int64(1)
	for oid, b := range userStats {
		if b.Reactions+b.Tips > score || (b.Reactions+b.Tips == score && st.Users[oid].Model.Name > c.Param("username")) {
			rank++
		}
	}
	emoji := ""
	var count int64
	for name, n := range a.Emoji {
		if n > count || (n == count && name > emoji) {
			count = n
			emoji = name
		}
	}
	return c.JSON(200, UserStatistics{Rank: rank, ViewersCount: a.Viewers, TotalReactions: a.Reactions, TotalLivecomments: a.Comments, TotalTip: a.Tips, FavoriteEmoji: emoji})
}
func streamStatistics(c echo.Context) error {
	id, e := num(c, "livestream_id")
	if e != nil {
		return e
	}
	if st.Streams[id] == nil {
		return echo.NewHTTPError(400, "stream not found")
	}
	a := streamStats[id]
	score := a.Reactions + a.Tips
	rank := int64(1)
	for oid, b := range streamStats {
		if b.Reactions+b.Tips > score || (b.Reactions+b.Tips == score && oid > id) {
			rank++
		}
	}
	return c.JSON(http.StatusOK, LivestreamStatistics{Rank: rank, ViewersCount: a.Viewers, TotalReactions: a.Reactions, TotalReports: a.Reports, MaxTip: a.MaxTip})
}

var _ = fmt.Sprint
