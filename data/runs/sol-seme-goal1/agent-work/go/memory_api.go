package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/sessions"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

func bad(s string) error     { return echo.NewHTTPError(400, s) }
func missing(s string) error { return echo.NewHTTPError(404, s) }
func decode(c echo.Context, v interface{}) error {
	if err := json.NewDecoder(c.Request().Body).Decode(v); err != nil {
		return bad("invalid JSON")
	}
	return nil
}
func authenticated(c echo.Context) (int64, error) {
	if err := verifyUserSession(c); err != nil {
		return 0, err
	}
	s, _ := session.Get(defaultSessionIDKey, c)
	return s.Values[defaultUserIDKey].(int64), nil
}
func idsLimit(c echo.Context, ids []int64, created func(int64) int64) ([]int64, error) {
	ids = append([]int64{}, ids...)
	sort.Slice(ids, func(i, j int) bool {
		a, b := created(ids[i]), created(ids[j])
		if a == b {
			return ids[i] < ids[j]
		}
		return a > b
	})
	if l := c.QueryParam("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 0 {
			return nil, bad("invalid limit")
		}
		if n < len(ids) {
			ids = ids[:n]
		}
	}
	return ids, nil
}
func memoryRoutes(e *echo.Echo) {
	e.GET("/internal/stats", func(c echo.Context) error {
		return c.JSON(200, map[string]uint64{"sync_count": walSyncCount.Load(), "sync_nanos": walSyncNanos.Load(), "wal_items": walItemCount.Load()})
	})
	e.POST("/api/initialize", memoryInitialize)
	e.Any("/api/*", memoryAPI)
	e.GET("/internal/names", func(c echo.Context) error {
		mem.RLock()
		defer mem.RUnlock()
		names := make([]string, 0, len(mem.Names))
		for name := range mem.Names {
			names = append(names, name)
		}
		return c.JSON(200, names)
	})
}
func memoryInitialize(c echo.Context) error {
	mem.Lock()
	defer mem.Unlock()
	// Drain outstanding log writes before resetting its generation.
	done := make(chan error, 1)
	mem.queue <- durableItem{Done: done}
	if err := <-done; err != nil {
		return err
	}
	if out, err := exec.Command("../sql/init.sh").CombinedOutput(); err != nil {
		return fmt.Errorf("initialize: %s: %w", out, err)
	}
	if err := mem.loadInitial(); err != nil {
		return err
	}
	mem.rebuild()
	if err := mem.newGeneration(); err != nil {
		return err
	}
	if err := syncDNS(); err != nil {
		return err
	}
	return c.JSON(200, map[string]string{"language": "golang", "lang": "golang"})
}
func syncDNS() error {
	names := make([]string, 0, len(mem.Names))
	for name := range mem.Names {
		names = append(names, name)
	}
	return sendDNS(names, true)
} // DNS implementation supplies exact-name synchronization.
func memoryAPI(c echo.Context) error {
	p := strings.Split(strings.Trim(c.Request().URL.Path, "/"), "/")
	method := c.Request().Method
	if len(p) < 2 {
		return missing("unknown endpoint")
	}
	if p[1] == "login" && method == "POST" {
		return memoryLogin(c)
	}
	if p[1] == "register" && method == "POST" {
		return memoryRegister(c)
	}
	var uid int64
	if p[1] != "tag" && p[1] != "payment" && !(p[1] == "livestream" && len(p) > 2 && p[2] == "search") && !(p[1] == "user" && len(p) > 3 && p[3] == "icon") {
		var err error
		uid, err = authenticated(c)
		if err != nil {
			return err
		}
	}
	// The lock protects indexed state and response materialization. Network writes happen after unlocking.
	mem.Lock()
	var out interface{}
	code := 200
	var event *mutation
	var resultErr error
	switch p[1] {
	case "tag":
		tags := make([]*Tag, 0, len(mem.S.Tags))
		ids := make([]int64, 0, len(mem.S.Tags))
		for id := range mem.S.Tags {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			v := mem.S.Tags[id]
			tags = append(tags, &Tag{ID: v.ID, Name: v.Name})
		}
		out = TagsResponse{Tags: tags}
	case "payment":
		var total int64
		for _, a := range mem.Agg {
			total += a.Tips
		}
		out = PaymentResult{TotalTip: total}
	case "icon":
		if method != "POST" {
			resultErr = missing("unknown endpoint")
			break
		}
		var req PostIconRequest
		if resultErr = decode(c, &req); resultErr != nil {
			break
		}
		id := mem.next("icon")
		event = &mutation{Kind: "icon", ID: uid, Image: req.Image, Theme: ThemeModel{ID: id}}
		out = PostIconResponse{ID: id}
		code = 201
	case "user":
		if len(p) < 3 {
			resultErr = missing("user required")
			break
		}
		id := mem.Names[p[2]]
		if p[2] == "me" {
			id = uid
		}
		if id == 0 {
			resultErr = missing("user not found")
			break
		}
		if len(p) == 3 {
			out = mem.user(id)
			break
		}
		switch p[3] {
		case "icon":
			image, ok := mem.S.Icons[id]
			if !ok {
				image = mem.Fallback
			}
			hash := mem.Hashes[id]
			mem.Unlock()
			c.Response().Header().Set("ETag", `"`+hash+`"`)
			if c.Request().Header.Get("If-None-Match") == `"`+hash+`"` {
				return c.NoContent(304)
			}
			return c.Blob(200, "image/jpeg", image)
		case "theme":
			t := mem.S.Themes[id]
			out = Theme{ID: t.ID, DarkMode: t.DarkMode}
		case "livestream":
			ids := append([]int64{}, mem.UserStreams[id]...)
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			ss := make([]Livestream, 0, len(ids))
			for _, sid := range ids {
				ss = append(ss, mem.stream(sid))
			}
			out = ss
		case "statistics":
			out = mem.userStatistics(id)
		default:
			resultErr = missing("unknown endpoint")
		}
	case "livestream":
		if len(p) == 2 {
			ids := append([]int64{}, mem.UserStreams[uid]...)
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
			ss := make([]Livestream, 0, len(ids))
			for _, id := range ids {
				ss = append(ss, mem.stream(id))
			}
			out = ss
			break
		}
		if p[2] == "search" {
			tag := c.QueryParam("tag")
			ids := mem.AllStreams
			if tag != "" {
				ids = mem.TagStreams[tag]
			}
			n := len(ids)
			if tag == "" && c.QueryParam("limit") != "" {
				limit, err := strconv.Atoi(c.QueryParam("limit"))
				if err != nil || limit < 0 {
					resultErr = bad("invalid limit")
					break
				}
				if limit < n {
					n = limit
				}
			}
			ss := make([]Livestream, 0, n)
			for i := len(ids) - 1; i >= len(ids)-n; i-- {
				ss = append(ss, mem.stream(ids[i]))
			}
			out = ss
			break
		}
		if p[2] == "reservation" {
			var req ReserveLivestreamRequest
			if resultErr = decode(c, &req); resultErr != nil {
				break
			}
			if req.StartAt >= 1732496400 || req.EndAt <= 1700874000 {
				resultErr = bad("bad reservation time range")
				break
			}
			for _, s := range mem.S.Slots {
				if s.StartAt >= req.StartAt && s.EndAt <= req.EndAt && s.Slot < 1 {
					resultErr = bad("no reservation capacity")
					break
				}
			}
			if resultErr != nil {
				break
			}
			id := mem.next("stream")
			s := LivestreamModel{ID: id, UserID: uid, Title: req.Title, Description: req.Description, PlaylistUrl: req.PlaylistUrl, ThumbnailUrl: req.ThumbnailUrl, StartAt: req.StartAt, EndAt: req.EndAt}
			event = &mutation{Kind: "stream", Stream: s, Tags: req.Tags}
			code = 201
			break
		}
		sid, err := strconv.ParseInt(p[2], 10, 64)
		if err != nil {
			resultErr = bad("invalid livestream_id")
			break
		}
		stream, exists := mem.S.Streams[sid]
		if len(p) == 3 {
			if !exists {
				resultErr = missing("livestream not found")
			} else {
				out = mem.stream(sid)
			}
			break
		}
		switch p[3] {
		case "statistics":
			if !exists {
				resultErr = bad("livestream not found")
			} else {
				out = mem.streamStatistics(sid)
			}
		case "enter", "exit":
			kind := p[3]
			event = &mutation{Kind: kind, Viewer: LivestreamViewerModel{UserID: uid, LivestreamID: sid, CreatedAt: time.Now().Unix()}}
		case "ngwords":
			words := []*NGWord{}
			for _, id := range mem.StreamWords[sid] {
				w := mem.S.Words[id]
				if w.UserID == uid {
					wc := w
					words = append(words, &wc)
				}
			}
			sort.Slice(words, func(i, j int) bool {
				if words[i].CreatedAt == words[j].CreatedAt {
					return words[i].ID < words[j].ID
				}
				return words[i].CreatedAt > words[j].CreatedAt
			})
			out = words
		case "moderate":
			if !exists || stream.UserID != uid {
				resultErr = bad("cannot moderate other streamer's livestream")
				break
			}
			var req ModerateRequest
			if resultErr = decode(c, &req); resultErr != nil {
				break
			}
			w := NGWord{ID: mem.next("word"), UserID: uid, LivestreamID: sid, Word: req.NGWord, CreatedAt: time.Now().Unix()}
			deleted := []int64{}
			for _, id := range mem.StreamComments[sid] {
				if lc, ok := mem.S.Comments[id]; ok && sqlLikeContains(lc.Comment, w.Word) {
					deleted = append(deleted, id)
				}
			}
			event = &mutation{Kind: "word", Word: w, Deleted: deleted}
			out = map[string]int64{"word_id": w.ID}
			code = 201
		case "report":
			if !exists {
				resultErr = missing("livestream not found")
				break
			}
			if stream.UserID != uid {
				resultErr = echo.NewHTTPError(403, "cannot get other streamer's reports")
				break
			}
			rr := make([]LivecommentReport, 0, len(mem.StreamReports[sid]))
			for _, id := range mem.StreamReports[sid] {
				rr = append(rr, mem.report(id))
			}
			out = rr
		case "livecomment":
			if len(p) >= 6 && p[5] == "report" {
				cid, err := strconv.ParseInt(p[4], 10, 64)
				if err != nil {
					resultErr = bad("invalid comment_id")
					break
				}
				if _, ok := mem.S.Comments[cid]; !ok || !exists {
					resultErr = missing("comment not found")
					break
				}
				r := LivecommentReportModel{ID: mem.next("report"), UserID: uid, LivestreamID: sid, LivecommentID: cid, CreatedAt: time.Now().Unix()}
				event = &mutation{Kind: "report", Report: r}
				code = 201
				break
			}
			if method == "POST" {
				if !exists {
					resultErr = missing("livestream not found")
					break
				}
				var req PostLivecommentRequest
				if resultErr = decode(c, &req); resultErr != nil {
					break
				}
				for _, id := range mem.StreamWords[sid] {
					if sqlLikeContains(req.Comment, mem.S.Words[id].Word) {
						resultErr = bad("このコメントがスパム判定されました")
						break
					}
				}
				if resultErr != nil {
					break
				}
				lc := LivecommentModel{ID: mem.next("comment"), UserID: uid, LivestreamID: sid, Comment: req.Comment, Tip: req.Tip, CreatedAt: time.Now().Unix()}
				event = &mutation{Kind: "comment", Comment: lc}
				code = 201
			} else {
				ids := []int64{}
				for _, id := range mem.StreamComments[sid] {
					if _, ok := mem.S.Comments[id]; ok {
						ids = append(ids, id)
					}
				}
				ids, resultErr = idsLimit(c, ids, func(id int64) int64 { return mem.S.Comments[id].CreatedAt })
				if resultErr != nil {
					break
				}
				cc := make([]Livecomment, 0, len(ids))
				for _, id := range ids {
					cc = append(cc, mem.comment(id))
				}
				out = cc
			}
		case "reaction":
			if method == "POST" {
				var req PostReactionRequest
				if resultErr = decode(c, &req); resultErr != nil {
					break
				}
				if !exists {
					resultErr = missing("livestream not found")
					break
				}
				r := ReactionModel{ID: mem.next("reaction"), UserID: uid, LivestreamID: sid, EmojiName: req.EmojiName, CreatedAt: time.Now().Unix()}
				event = &mutation{Kind: "reaction", Reaction: r}
				code = 201
			} else {
				ids, err := idsLimit(c, mem.StreamReactions[sid], func(id int64) int64 { return mem.S.Reactions[id].CreatedAt })
				if err != nil {
					resultErr = err
					break
				}
				rr := make([]Reaction, 0, len(ids))
				for _, id := range ids {
					rr = append(rr, mem.reaction(id))
				}
				out = rr
			}
		default:
			resultErr = missing("unknown endpoint")
		}
	default:
		resultErr = missing("unknown endpoint")
	}
	var durable chan error
	if resultErr == nil && event != nil {
		mem.apply(*event)
		durable = mem.persist(*event)
		switch event.Kind {
		case "stream":
			out = mem.stream(event.Stream.ID)
		case "comment":
			out = mem.comment(event.Comment.ID)
		case "reaction":
			out = mem.reaction(event.Reaction.ID)
		case "report":
			out = mem.report(event.Report.ID)
		}
	}
	mem.Unlock()
	if resultErr != nil {
		return resultErr
	}
	if durable != nil {
		if err := <-durable; err != nil {
			return err
		}
	}
	if out == nil {
		return c.NoContent(code)
	}
	return c.JSON(code, out)
}
func sqlLikeContains(s, pattern string) bool {
	// The SQL operation is LIKE CONCAT('%', word, '%') under utf8mb4_bin.
	if !strings.ContainsAny(pattern, "%_\\") {
		return strings.Contains(s, pattern)
	}
	text := []rune(s)
	pat := []rune("%" + pattern + "%")
	prev := make([]bool, len(text)+1)
	prev[0] = true
	for i := 0; i < len(pat); i++ {
		ch := pat[i]
		escaped := false
		if ch == '\\' && i+1 < len(pat) {
			i++
			ch = pat[i]
			escaped = true
		}
		next := make([]bool, len(text)+1)
		if ch == '%' && !escaped {
			next[0] = prev[0]
			for j := 1; j <= len(text); j++ {
				next[j] = prev[j] || next[j-1]
			}
		} else {
			for j := 1; j <= len(text); j++ {
				next[j] = prev[j-1] && ((ch == '_' && !escaped) || ch == text[j-1])
			}
		}
		prev = next
	}
	return prev[len(text)]
}
func (m *memoryEngine) userStatistics(id int64) UserStatistics {
	st := UserStatistics{Rank: 1}
	emoji := map[string]int64{}
	score := int64(0)
	for _, sid := range m.UserStreams[id] {
		a := m.Agg[sid]
		if a == nil {
			continue
		}
		st.ViewersCount += a.Viewers
		st.TotalReactions += a.Reactions
		st.TotalLivecomments += a.Comments
		st.TotalTip += a.Tips
		for k, v := range a.Emoji {
			emoji[k] += v
		}
	}
	score = st.TotalReactions + st.TotalTip
	name := m.S.Users[id].Name
	for oid, u := range m.S.Users {
		if oid == id {
			continue
		}
		var other int64
		for _, sid := range m.UserStreams[oid] {
			if a := m.Agg[sid]; a != nil {
				other += a.Reactions + a.Tips
			}
		}
		if other > score || (other == score && u.Name > name) {
			st.Rank++
		}
	}
	var count int64
	for k, v := range emoji {
		if v > count || (v == count && k > st.FavoriteEmoji) {
			st.FavoriteEmoji = k
			count = v
		}
	}
	return st
}
func (m *memoryEngine) streamStatistics(id int64) LivestreamStatistics {
	a := m.Agg[id]
	if a == nil {
		a = &streamAggregate{}
	}
	st := LivestreamStatistics{Rank: 1, ViewersCount: a.Viewers, TotalReactions: a.Reactions, TotalReports: a.Reports, MaxTip: a.MaxTip}
	score := a.Reactions + a.Tips
	for sid := range m.S.Streams {
		other := int64(0)
		if b := m.Agg[sid]; b != nil {
			other = b.Reactions + b.Tips
		}
		if other > score || (other == score && sid > id) {
			st.Rank++
		}
	}
	return st
}
func memoryLogin(c echo.Context) error {
	var req LoginRequest
	if err := decode(c, &req); err != nil {
		return err
	}
	mem.RLock()
	id := mem.Names[req.Username]
	u := mem.S.Users[id]
	mem.RUnlock()
	if id == 0 {
		return echo.NewHTTPError(401, "invalid username or password")
	}
	check, err := bcryptRPC(cryptoRequest{Password: req.Password, Hash: u.HashedPassword})
	if err != nil {
		return err
	}
	if !check.OK {
		return echo.NewHTTPError(401, "invalid username or password")
	}
	sess, err := session.Get(defaultSessionIDKey, c)
	if err != nil {
		return echo.NewHTTPError(401, "invalid session")
	}
	sess.Options = &sessions.Options{Domain: "u.isucon.dev", MaxAge: 60000, Path: "/"}
	sess.Values[defaultSessionIDKey] = uuid.NewString()
	sess.Values[defaultUserIDKey] = id
	sess.Values[defaultUsernameKey] = u.Name
	sess.Values[defaultSessionExpiresKey] = time.Now().Add(time.Hour).Unix()
	if err := sess.Save(c.Request(), c.Response()); err != nil {
		return err
	}
	return c.NoContent(200)
}
func memoryRegister(c echo.Context) error {
	var req PostUserRequest
	if err := decode(c, &req); err != nil {
		return err
	}
	if req.Name == "pipe" {
		return bad("the username 'pipe' is reserved")
	}
	hashed, err := bcryptRPC(cryptoRequest{Password: req.Password, Generate: true})
	if err != nil {
		return err
	}
	hash := hashed.Hash
	mem.Lock()
	if _, ok := mem.Names[req.Name]; ok {
		mem.Unlock()
		return echo.NewHTTPError(500, "duplicate username")
	}
	id := mem.next("user")
	tid := mem.next("theme")
	event := mutation{Kind: "user", User: UserModel{ID: id, Name: req.Name, DisplayName: req.DisplayName, Description: req.Description, HashedPassword: string(hash)}, Theme: ThemeModel{ID: tid, UserID: id, DarkMode: req.Theme.DarkMode}}
	mem.apply(event)
	done := mem.persist(event)
	user := mem.user(id)
	mem.Unlock()
	if err := <-done; err != nil {
		return err
	}
	if err := sendDNS([]string{req.Name}, false); err != nil {
		return err
	}
	return c.JSON(201, user)
}
