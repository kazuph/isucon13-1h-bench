package main

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

type activityState struct {
	sync.RWMutex
	streams  map[int64]*streamActivity
	comments map[int64]LivecommentModel
	totalTip int64
}
type streamActivity struct {
	comments     []LivecommentModel
	reactions    []ReactionModel
	reports      []LivecommentReportModel
	words        []NGWord
	patterns     []wordPattern
	viewers      map[int64]int64
	viewersCount int64
	tip          int64
	maxTip       int64
	emoji        map[string]int64
}
type wordPattern struct {
	literal string
	pattern *regexp.Regexp
}

func patternFor(word string) wordPattern {
	if !strings.ContainsAny(word, "%_\\") {
		return wordPattern{literal: word}
	}
	var b strings.Builder
	b.WriteString("(?s)")
	escaped := false
	for _, r := range word {
		if escaped {
			b.WriteString(regexp.QuoteMeta(string(r)))
			escaped = false
			continue
		}
		switch r {
		case '\\':
			escaped = true
		case '%':
			b.WriteString(".*")
		case '_':
			b.WriteByte('.')
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	if escaped {
		b.WriteString(`\\`)
	}
	return wordPattern{pattern: regexp.MustCompile(b.String())}
}
func (w wordPattern) matches(comment string) bool {
	if w.pattern != nil {
		return w.pattern.MatchString(comment)
	}
	return strings.Contains(comment, w.literal)
}

var activity activityState
var moderationLocks [256]sync.RWMutex

func (a *activityState) stream(id int64) *streamActivity {
	s := a.streams[id]
	if s == nil {
		s = &streamActivity{viewers: make(map[int64]int64), emoji: make(map[string]int64)}
		a.streams[id] = s
	}
	return s
}
func (a *activityState) addComment(m LivecommentModel) {
	s := a.stream(m.LivestreamID)
	s.comments = append(s.comments, m)
	s.tip += m.Tip
	a.totalTip += m.Tip
	if m.Tip > s.maxTip {
		s.maxTip = m.Tip
	}
	a.comments[m.ID] = m
}
func (a *activityState) addReaction(m ReactionModel) {
	s := a.stream(m.LivestreamID)
	s.reactions = append(s.reactions, m)
	s.emoji[m.EmojiName]++
}
func loadActivity() error {
	activity.Lock()
	defer activity.Unlock()
	activity.streams = make(map[int64]*streamActivity)
	activity.comments = make(map[int64]LivecommentModel)
	activity.totalTip = 0
	var comments []struct {
		LivecommentModel
		Hidden bool
	}
	if err := dbConn.Select(&comments, "SELECT * FROM livecomments ORDER BY created_at,id"); err != nil {
		return err
	}
	for _, m := range comments {
		if m.Hidden {
			activity.comments[m.ID] = m.LivecommentModel
		} else {
			activity.addComment(m.LivecommentModel)
		}
	}
	var reactions []ReactionModel
	if err := dbConn.Select(&reactions, "SELECT * FROM reactions ORDER BY created_at,id"); err != nil {
		return err
	}
	for _, m := range reactions {
		activity.addReaction(m)
	}
	var reports []LivecommentReportModel
	if err := dbConn.Select(&reports, "SELECT * FROM livecomment_reports ORDER BY id"); err != nil {
		return err
	}
	for _, m := range reports {
		s := activity.stream(m.LivestreamID)
		s.reports = append(s.reports, m)
	}
	var words []NGWord
	if err := dbConn.Select(&words, "SELECT * FROM ng_words ORDER BY created_at,id"); err != nil {
		return err
	}
	for _, m := range words {
		s := activity.stream(m.LivestreamID)
		s.words = append(s.words, m)
		s.patterns = append(s.patterns, patternFor(m.Word))
	}
	var viewers []LivestreamViewerModel
	if err := dbConn.Select(&viewers, "SELECT user_id,livestream_id,created_at FROM livestream_viewers_history"); err != nil {
		return err
	}
	for _, v := range viewers {
		s := activity.stream(v.LivestreamID)
		s.viewers[v.UserID]++
		s.viewersCount++
	}
	return nil
}
func streamRequest(c echo.Context) (streamID, userID int64, err error) {
	if err = verifyUserSession(c); err != nil {
		return
	}
	streamID, err = strconv.ParseInt(c.Param("livestream_id"), 10, 64)
	if err != nil {
		err = echo.NewHTTPError(400, "invalid livestream id")
		return
	}
	userID = c.Get(defaultUserIDKey).(int64)
	return
}
func parseBody(c echo.Context, dst interface{}) error {
	if err := json.NewDecoder(c.Request().Body).Decode(dst); err != nil {
		return echo.NewHTTPError(400, "invalid request body")
	}
	return nil
}
func commentView(m LivecommentModel) Livecomment {
	user, _ := lookupUser(m.UserID)
	stream, _ := lookupStream(m.LivestreamID)
	return Livecomment{ID: m.ID, User: user, Livestream: stream, Comment: m.Comment, Tip: m.Tip, CreatedAt: m.CreatedAt}
}
func reactionView(m ReactionModel) Reaction {
	user, _ := lookupUser(m.UserID)
	stream, _ := lookupStream(m.LivestreamID)
	return Reaction{ID: m.ID, User: user, Livestream: stream, EmojiName: m.EmojiName, CreatedAt: m.CreatedAt}
}
func postComment(c echo.Context) error {
	id, uid, err := streamRequest(c)
	if err != nil {
		return err
	}
	var req PostLivecommentRequest
	if err = parseBody(c, &req); err != nil {
		return err
	}
	if _, err := lookupStream(id); err != nil {
		return echo.NewHTTPError(404, "livestream not found")
	}
	mu := &moderationLocks[uint64(id)%256]
	mu.RLock()
	defer mu.RUnlock()
	activity.RLock()
	s := activity.streams[id]
	spam := false
	if s != nil {
		for _, w := range s.patterns {
			if w.matches(req.Comment) {
				spam = true
				break
			}
		}
	}
	activity.RUnlock()
	if spam {
		return echo.NewHTTPError(400, "このコメントがスパム判定されました")
	}
	m := LivecommentModel{UserID: uid, LivestreamID: id, Comment: req.Comment, Tip: req.Tip, CreatedAt: time.Now().Unix()}
	m.ID, err = commitEvent(activityEvent{Kind: "comment", Stream: id, User: uid, At: m.CreatedAt, Text: m.Comment, Value: m.Tip})
	if err != nil {
		return err
	}
	return c.JSON(201, commentView(m))
}
func postReaction(c echo.Context) error {
	id, uid, err := streamRequest(c)
	if err != nil {
		return err
	}
	var req PostReactionRequest
	if err = parseBody(c, &req); err != nil {
		return err
	}
	if _, err := lookupStream(id); err != nil {
		return echo.NewHTTPError(404, "livestream not found")
	}

	m := ReactionModel{UserID: uid, LivestreamID: id, EmojiName: req.EmojiName, CreatedAt: time.Now().Unix()}
	m.ID, err = commitEvent(activityEvent{Kind: "reaction", Stream: id, User: uid, At: m.CreatedAt, Text: m.EmojiName})
	if err != nil {
		return err
	}
	return c.JSON(201, reactionView(m))
}
func timeline(c echo.Context, reactions bool) error {
	id, _, err := streamRequest(c)
	if err != nil {
		return err
	}
	limit := int(^uint(0) >> 1)
	if raw := c.QueryParam("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 0 {
			return echo.NewHTTPError(400, "invalid limit")
		}
	}
	activity.RLock()
	data.RLock()
	s := activity.streams[id]
	var out []byte
	if s != nil && reactions {
		rows := s.reactions
		if len(rows) > limit {
			rows = rows[len(rows)-limit:]
		}
		out = make([]byte, 0, len(rows)*700+2)
		out = append(out, '[')
		for i := len(rows) - 1; i >= 0; i-- {
			if i < len(rows)-1 {
				out = append(out, ',')
			}
			m := rows[i]
			out = appendTimelineFields(out, m.ID, m.UserID, id, m.CreatedAt)
			out = append(out, `,"emoji_name":`...)
			quote, _ := json.Marshal(m.EmojiName)
			out = append(out, quote...)
			out = append(out, '}')
		}
	} else if s != nil {
		rows := s.comments
		if len(rows) > limit {
			rows = rows[len(rows)-limit:]
		}
		out = make([]byte, 0, len(rows)*700+2)
		out = append(out, '[')
		for i := len(rows) - 1; i >= 0; i-- {
			if i < len(rows)-1 {
				out = append(out, ',')
			}
			m := rows[i]
			out = appendTimelineFields(out, m.ID, m.UserID, id, m.CreatedAt)
			out = append(out, `,"comment":`...)
			quote, _ := json.Marshal(m.Comment)
			out = append(out, quote...)
			out = append(out, `,"tip":`...)
			out = strconv.AppendInt(out, m.Tip, 10)
			out = append(out, '}')
		}
	} else {
		out = []byte{'['}
	}
	data.RUnlock()
	activity.RUnlock()
	return c.JSONBlob(200, append(out, ']'))
}
func moderate(c echo.Context) error {
	id, uid, err := streamRequest(c)
	if err != nil {
		return err
	}
	stream, err := lookupStream(id)
	if err != nil || stream.Owner.ID != uid {
		return echo.NewHTTPError(400, "not stream owner")
	}
	var req ModerateRequest
	if err = parseBody(c, &req); err != nil {
		return err
	}
	mu := &moderationLocks[uint64(id)%256]
	mu.Lock()
	defer mu.Unlock()
	wordID, err := commitEvent(activityEvent{Kind: "moderate", Stream: id, User: uid, At: time.Now().Unix(), Text: req.NGWord})
	if err != nil {
		return err
	}
	return c.JSON(201, map[string]int64{"word_id": wordID})
}
func postReport(c echo.Context) error {
	id, uid, err := streamRequest(c)
	if err != nil {
		return err
	}
	cid, err := strconv.ParseInt(c.Param("livecomment_id"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(400, "invalid comment id")
	}

	activity.RLock()
	comment, ok := activity.comments[cid]
	activity.RUnlock()
	if !ok || comment.LivestreamID != id {
		return echo.NewHTTPError(404, "comment not found")
	}
	m := LivecommentReportModel{UserID: uid, LivestreamID: id, LivecommentID: cid, CreatedAt: time.Now().Unix()}
	m.ID, err = commitEvent(activityEvent{Kind: "report", Stream: id, User: uid, At: m.CreatedAt, Value: cid})
	if err != nil {
		return err
	}
	u, _ := lookupUser(uid)
	return c.JSON(201, LivecommentReport{ID: m.ID, Reporter: u, Livecomment: commentView(comment), CreatedAt: m.CreatedAt})
}
func listReports(c echo.Context) error {
	id, uid, err := streamRequest(c)
	if err != nil {
		return err
	}
	stream, err := lookupStream(id)
	if err != nil {
		return echo.NewHTTPError(404, "stream not found")
	}
	if stream.Owner.ID != uid {
		return echo.NewHTTPError(403, "not stream owner")
	}
	activity.RLock()
	out := []LivecommentReport{}
	if s := activity.streams[id]; s != nil {
		out = make([]LivecommentReport, 0, len(s.reports))
		for _, r := range s.reports {
			u, _ := lookupUser(r.UserID)
			out = append(out, LivecommentReport{ID: r.ID, Reporter: u, Livecomment: commentView(activity.comments[r.LivecommentID]), CreatedAt: r.CreatedAt})
		}
	}
	activity.RUnlock()
	return c.JSON(200, out)
}
func listWords(c echo.Context) error {
	id, uid, err := streamRequest(c)
	if err != nil {
		return err
	}
	activity.RLock()
	out := []NGWord{}
	if s := activity.streams[id]; s != nil {
		for i := len(s.words) - 1; i >= 0; i-- {
			if s.words[i].UserID == uid {
				out = append(out, s.words[i])
			}
		}
	}
	activity.RUnlock()
	return c.JSON(200, out)
}
func visit(c echo.Context, enter bool) error {
	id, uid, err := streamRequest(c)
	if err != nil {
		return err
	}

	kind := "exit"
	if enter {
		kind = "enter"
	}
	if _, err = commitEvent(activityEvent{Kind: kind, Stream: id, User: uid, At: time.Now().Unix()}); err != nil {
		return err
	}
	return c.NoContent(200)
}
func streamStats(c echo.Context) error {
	id, _, err := streamRequest(c)
	if err != nil {
		return err
	}
	if _, err = lookupStream(id); err != nil {
		return echo.NewHTTPError(400, "stream not found")
	}
	activity.RLock()
	data.RLock()
	result := LivestreamStatistics{Rank: 1}
	var score int64
	if s := activity.streams[id]; s != nil {
		result.ViewersCount = s.viewersCount
		result.TotalReactions = int64(len(s.reactions))
		result.TotalReports = int64(len(s.reports))
		result.MaxTip = s.maxTip
		score = s.tip + result.TotalReactions
	}
	for _, other := range data.allStreams {
		var value int64
		if v := activity.streams[other]; v != nil {
			value = v.tip + int64(len(v.reactions))
		}
		if value > score || value == score && other > id {
			result.Rank++
		}
	}
	data.RUnlock()
	activity.RUnlock()
	return c.JSON(200, result)
}
func userStats(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	name := c.Param("username")
	activity.RLock()
	data.RLock()
	uid, ok := data.names[name]
	if !ok {
		data.RUnlock()
		activity.RUnlock()
		return echo.NewHTTPError(400, "user not found")
	}
	result := UserStatistics{Rank: 1}
	emoji := make(map[string]int64)
	for _, id := range data.ownerStreams[uid] {
		if s := activity.streams[id]; s != nil {
			result.TotalTip += s.tip
			result.ViewersCount += s.viewersCount
			result.TotalReactions += int64(len(s.reactions))
			result.TotalLivecomments += int64(len(s.comments))
			for k, v := range s.emoji {
				emoji[k] += v
			}
		}
	}
	var max int64
	for k, v := range emoji {
		if v > max || (v == max && k > result.FavoriteEmoji) {
			max = v
			result.FavoriteEmoji = k
		}
	}
	scores := make(map[int64]int64)
	for id, s := range activity.streams {
		scores[data.streams[id].Owner.ID] += s.tip + int64(len(s.reactions))
	}
	score := result.TotalTip + result.TotalReactions
	for id, u := range data.users {
		v := scores[id]
		if v > score || v == score && u.Name > name {
			result.Rank++
		}
	}
	data.RUnlock()
	activity.RUnlock()
	return c.JSON(200, result)
}
func payment(c echo.Context) error {
	activity.RLock()
	v := activity.totalTip
	activity.RUnlock()
	return c.JSON(200, PaymentResult{TotalTip: v})
}

func appendTimelineFields(out []byte, eventID, userID, streamID, createdAt int64) []byte {
	out = append(out, `{"id":`...)
	out = strconv.AppendInt(out, eventID, 10)
	out = append(out, `,"user":`...)
	out = append(out, data.userJSON[userID]...)
	out = append(out, `,"livestream":`...)
	out = append(out, data.streamJSON[streamID]...)
	out = append(out, `,"created_at":`...)
	return strconv.AppendInt(out, createdAt, 10)
}
