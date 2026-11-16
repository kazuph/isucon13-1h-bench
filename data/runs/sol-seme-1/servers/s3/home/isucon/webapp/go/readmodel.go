package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/labstack/echo/v4"
)

// MySQL is the durable source of truth. The application updates this read
// projection only after the corresponding transaction has committed.
var state = struct {
	sync.RWMutex
	users       map[int64]User
	passwords   map[int64]string
	names       map[string]int64
	icons       map[int64][]byte
	streams     map[int64]LivestreamModel
	tags        map[int64][]Tag
	allTags     []*Tag
	ids         []int64
	tagged      map[string][]int64
	owned       map[int64][]int64
	streamScore map[int64]int64
	userScore   map[int64]int64
	totalTip    int64
	comments    map[int64]map[int64]LivecommentModel
	reactions   map[int64][]ReactionModel
	reports     map[int64][]LivecommentReportModel
	words       map[int64][]NGWord
	viewers     map[int64]map[int64]int64
}{}
var streamLocks sync.Map

func lockStream(id int64) func() {
	v, _ := streamLocks.LoadOrStore(id, &sync.Mutex{})
	m := v.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}
func loadReadModel() error {
	var users []UserModel
	var themes []ThemeModel
	var streams []LivestreamModel
	var tags []Tag
	var links []LivestreamTagModel
	var comments []LivecommentModel
	var reactions []ReactionModel
	var reports []LivecommentReportModel
	var words []NGWord
	var viewers []LivestreamViewerModel
	var icons []struct {
		UserID int64  `db:"user_id"`
		Image  []byte `db:"image"`
	}
	for _, q := range []struct {
		dest interface{}
		sql  string
	}{
		{&users, "SELECT * FROM users"}, {&themes, "SELECT * FROM themes"}, {&streams, "SELECT * FROM livestreams"}, {&tags, "SELECT * FROM tags ORDER BY id"}, {&links, "SELECT * FROM livestream_tags ORDER BY id"},
		{&comments, "SELECT * FROM livecomments"}, {&reactions, "SELECT * FROM reactions ORDER BY id"}, {&reports, "SELECT * FROM livecomment_reports ORDER BY id"}, {&words, "SELECT * FROM ng_words ORDER BY id"},
		{&viewers, "SELECT user_id,livestream_id,created_at FROM livestream_viewers_history"}, {&icons, "SELECT user_id,image FROM icons"},
	} {
		if err := dbConn.Select(q.dest, q.sql); err != nil {
			return err
		}
	}
	fallback, err := os.ReadFile(fallbackImage)
	if err != nil {
		return err
	}
	state.Lock()
	defer state.Unlock()
	state.ids = nil
	state.tagged = make(map[string][]int64)
	state.owned = make(map[int64][]int64)
	state.streamScore = make(map[int64]int64)
	state.userScore = make(map[int64]int64)
	state.totalTip = 0
	state.passwords = make(map[int64]string)
	state.users = make(map[int64]User)
	state.names = make(map[string]int64)
	state.icons = make(map[int64][]byte)
	state.streams = make(map[int64]LivestreamModel)
	state.tags = make(map[int64][]Tag)
	state.allTags = make([]*Tag, 0, len(tags))
	state.comments = make(map[int64]map[int64]LivecommentModel)
	state.reactions = make(map[int64][]ReactionModel)
	state.reports = make(map[int64][]LivecommentReportModel)
	state.words = make(map[int64][]NGWord)
	state.viewers = make(map[int64]map[int64]int64)
	themeMap := make(map[int64]Theme)
	for _, t := range themes {
		themeMap[t.UserID] = Theme{ID: t.ID, DarkMode: t.DarkMode}
	}
	for _, i := range icons {
		state.icons[i.UserID] = i.Image
	}
	for _, u := range users {
		state.passwords[u.ID] = u.HashedPassword
		img, ok := state.icons[u.ID]
		if !ok {
			img = fallback
			state.icons[u.ID] = img
		}
		state.users[u.ID] = User{ID: u.ID, Name: u.Name, DisplayName: u.DisplayName, Description: u.Description, Theme: themeMap[u.ID], IconHash: fmt.Sprintf("%x", sha256.Sum256(img))}
		state.names[u.Name] = u.ID
	}
	for _, s := range streams {
		state.streams[s.ID] = s
		state.tags[s.ID] = []Tag{}
	}
	tagMap := make(map[int64]Tag)
	for _, t := range tags {
		tagMap[t.ID] = t
		t := t
		state.allTags = append(state.allTags, &t)
	}
	for _, l := range links {
		state.tags[l.LivestreamID] = append(state.tags[l.LivestreamID], tagMap[l.TagID])
	}
	for _, c := range comments {
		if state.comments[c.LivestreamID] == nil {
			state.comments[c.LivestreamID] = make(map[int64]LivecommentModel)
		}
		state.comments[c.LivestreamID][c.ID] = c
	}
	for _, r := range reactions {
		state.reactions[r.LivestreamID] = append(state.reactions[r.LivestreamID], r)
	}
	for _, r := range reports {
		state.reports[r.LivestreamID] = append(state.reports[r.LivestreamID], r)
	}
	for _, w := range words {
		state.words[w.LivestreamID] = append(state.words[w.LivestreamID], w)
	}
	for _, v := range viewers {
		if state.viewers[v.LivestreamID] == nil {
			state.viewers[v.LivestreamID] = make(map[int64]int64)
		}
		state.viewers[v.LivestreamID][v.UserID]++
	}
	for id, stream := range state.streams {
		state.ids = append(state.ids, id)
		state.owned[stream.UserID] = append(state.owned[stream.UserID], id)
		for _, t := range state.tags[id] {
			state.tagged[t.Name] = append(state.tagged[t.Name], id)
		}
		score := int64(len(state.reactions[id]))
		for _, cm := range state.comments[id] {
			score += cm.Tip
			state.totalTip += cm.Tip
		}
		state.streamScore[id] = score
		state.userScore[stream.UserID] += score
	}
	sort.Slice(state.ids, func(i, j int) bool { return state.ids[i] < state.ids[j] })
	for k, v := range state.owned {
		sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
		state.owned[k] = v
	}
	for k, v := range state.tagged {
		sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
		state.tagged[k] = v
	}

	return nil
}
func memoryStream(id int64) Livestream {
	s := state.streams[id]
	tags := state.tags[id]
	if tags == nil {
		tags = []Tag{}
	}
	return Livestream{ID: s.ID, Owner: state.users[s.UserID], Title: s.Title, Description: s.Description, PlaylistUrl: s.PlaylistUrl, ThumbnailUrl: s.ThumbnailUrl, StartAt: s.StartAt, EndAt: s.EndAt, Tags: tags}
}
func memoryComment(m LivecommentModel) Livecomment {
	return Livecomment{ID: m.ID, User: state.users[m.UserID], Livestream: memoryStream(m.LivestreamID), Comment: m.Comment, Tip: m.Tip, CreatedAt: m.CreatedAt}
}
func memoryReaction(m ReactionModel) Reaction {
	return Reaction{ID: m.ID, User: state.users[m.UserID], Livestream: memoryStream(m.LivestreamID), EmojiName: m.EmojiName, CreatedAt: m.CreatedAt}
}
func pathID(c echo.Context) (int64, error) {
	id, err := strconv.ParseInt(c.Param("livestream_id"), 10, 64)
	if err != nil {
		return 0, echo.NewHTTPError(400, "invalid livestream_id")
	}
	return id, nil
}
func limitValue(c echo.Context) (int, error) {
	if c.QueryParam("limit") == "" {
		return -1, nil
	}
	n, err := strconv.Atoi(c.QueryParam("limit"))
	if err != nil || n < 0 {
		return 0, echo.NewHTTPError(400, "invalid limit")
	}
	return n, nil
}
func memoryGetIcon(c echo.Context) error {
	state.RLock()
	id, ok := state.names[c.Param("username")]
	u := state.users[id]
	img := state.icons[id]
	state.RUnlock()
	if !ok {
		return echo.NewHTTPError(404, "user not found")
	}
	etag := `"` + u.IconHash + `"`
	c.Response().Header().Set("ETag", etag)
	if c.Request().Header.Get("If-None-Match") == etag {
		return c.NoContent(304)
	}
	return c.Blob(200, "image/jpeg", img)
}
func memoryGetUser(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	state.RLock()
	id, ok := state.names[c.Param("username")]
	u := state.users[id]
	state.RUnlock()
	if !ok {
		return echo.NewHTTPError(404, "user not found")
	}
	return c.JSON(200, u)
}
func memoryGetMe(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id := sessionUserID(c)
	state.RLock()
	u, ok := state.users[id]
	state.RUnlock()
	if !ok {
		return echo.NewHTTPError(404, "user not found")
	}
	return c.JSON(200, u)
}
func memoryTheme(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	state.RLock()
	id, ok := state.names[c.Param("username")]
	u := state.users[id]
	state.RUnlock()
	if !ok {
		return echo.NewHTTPError(404, "user not found")
	}
	return c.JSON(200, u.Theme)
}
func memoryTags(c echo.Context) error {
	state.RLock()
	tags := state.allTags
	state.RUnlock()
	return c.JSON(200, TagsResponse{Tags: tags})
}
func memoryGetStream(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := pathID(c)
	if err != nil {
		return err
	}
	state.RLock()
	_, ok := state.streams[id]
	s := memoryStream(id)
	state.RUnlock()
	if !ok {
		return echo.NewHTTPError(404, "stream not found")
	}
	return c.JSON(200, s)
}
func memorySearch(c echo.Context) error {
	n, err := limitValue(c)
	if err != nil {
		return err
	}
	tag := c.QueryParam("tag")
	state.RLock()
	ids := state.ids
	if tag != "" {
		ids = state.tagged[tag]
	}
	length := len(ids)
	if tag == "" && n >= 0 && length > n {
		length = n
	}
	out := make([]Livestream, 0, length)
	for i := len(ids) - 1; i >= len(ids)-length; i-- {
		out = append(out, memoryStream(ids[i]))
	}
	state.RUnlock()
	return c.JSON(200, out)

}
func memoryUserStreams(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	uid := sessionUserID(c)
	state.RLock()
	if c.Param("username") != "" {
		var ok bool
		uid, ok = state.names[c.Param("username")]
		if !ok {
			state.RUnlock()
			return echo.NewHTTPError(404, "user not found")
		}
	}
	ids := state.owned[uid]
	out := make([]Livestream, 0, len(ids))
	for _, id := range ids {
		out = append(out, memoryStream(id))
	}
	state.RUnlock()
	return c.JSON(200, out)

}
func memoryComments(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := pathID(c)
	if err != nil {
		return err
	}
	n, err := limitValue(c)
	if err != nil {
		return err
	}
	state.RLock()
	models := make([]LivecommentModel, 0, len(state.comments[id]))
	for _, m := range state.comments[id] {
		models = append(models, m)
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].CreatedAt == models[j].CreatedAt {
			return models[i].ID > models[j].ID
		}
		return models[i].CreatedAt > models[j].CreatedAt
	})
	if n >= 0 && len(models) > n {
		models = models[:n]
	}
	out := make([]Livecomment, 0, len(models))
	for _, m := range models {
		out = append(out, memoryComment(m))
	}
	state.RUnlock()
	return c.JSON(200, out)
}
func memoryReactions(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := pathID(c)
	if err != nil {
		return err
	}
	n, err := limitValue(c)
	if err != nil {
		return err
	}
	state.RLock()
	models := append([]ReactionModel{}, state.reactions[id]...)
	sort.Slice(models, func(i, j int) bool {
		if models[i].CreatedAt == models[j].CreatedAt {
			return models[i].ID > models[j].ID
		}
		return models[i].CreatedAt > models[j].CreatedAt
	})
	if n >= 0 && len(models) > n {
		models = models[:n]
	}
	out := make([]Reaction, 0, len(models))
	for _, m := range models {
		out = append(out, memoryReaction(m))
	}
	state.RUnlock()
	return c.JSON(200, out)
}
func memoryWords(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := pathID(c)
	if err != nil {
		return err
	}
	uid := sessionUserID(c)
	state.RLock()
	out := make([]NGWord, 0)
	for _, w := range state.words[id] {
		if w.UserID == uid {
			out = append(out, w)
		}
	}
	state.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return c.JSON(200, out)
}
func memoryReports(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := pathID(c)
	if err != nil {
		return err
	}
	uid := sessionUserID(c)
	state.RLock()
	if state.streams[id].UserID != uid {
		state.RUnlock()
		return echo.NewHTTPError(403, "not owner")
	}
	out := make([]LivecommentReport, 0)
	for _, r := range state.reports[id] {
		m, ok := state.comments[id][r.LivecommentID]
		if !ok {
			continue
		}
		out = append(out, LivecommentReport{ID: r.ID, Reporter: state.users[r.UserID], Livecomment: memoryComment(m), CreatedAt: r.CreatedAt})
	}
	state.RUnlock()
	return c.JSON(200, out)
}
func memoryPayment(c echo.Context) error {
	state.RLock()
	total := state.totalTip
	state.RUnlock()
	return c.JSON(200, PaymentResult{TotalTip: total})
}

func memoryStreamStats(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := pathID(c)
	if err != nil {
		return err
	}
	state.RLock()
	if _, ok := state.streams[id]; !ok {
		state.RUnlock()
		return echo.NewHTTPError(400, "stream not found")
	}
	scores := state.streamScore
	st := LivestreamStatistics{Rank: 1, TotalReactions: int64(len(state.reactions[id])), TotalReports: int64(len(state.reports[id]))}
	for uid, score := range scores {
		if score > scores[id] || (score == scores[id] && uid > id) {
			st.Rank++
		}
	}
	for _, n := range state.viewers[id] {
		st.ViewersCount += n
	}
	for _, cm := range state.comments[id] {
		if cm.Tip > st.MaxTip {
			st.MaxTip = cm.Tip
		}
	}
	state.RUnlock()
	return c.JSON(200, st)
}
func memoryUserStats(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	name := c.Param("username")
	state.RLock()
	uid, ok := state.names[name]
	if !ok {
		state.RUnlock()
		return echo.NewHTTPError(400, "user not found")
	}
	scores := state.userScore
	st := UserStatistics{Rank: 1}
	emoji := make(map[string]int64)
	for _, sid := range state.owned[uid] {
		for _, cm := range state.comments[sid] {
			st.TotalLivecomments++
			st.TotalTip += cm.Tip
		}
		st.TotalReactions += int64(len(state.reactions[sid]))
		for _, r := range state.reactions[sid] {
			emoji[r.EmojiName]++
		}
		for _, n := range state.viewers[sid] {
			st.ViewersCount += n
		}
	}

	for id, u := range state.users {
		if scores[id] > scores[uid] || (scores[id] == scores[uid] && u.Name > name) {
			st.Rank++
		}
	}
	var best int64
	for e, n := range emoji {
		if n > best || (n == best && e > st.FavoriteEmoji) {
			st.FavoriteEmoji = e
			best = n
		}
	}
	state.RUnlock()
	return c.JSON(200, st)
}

// The SQL LIKE pattern is preserved for moderation, including wildcard words.
func likePattern(text, pattern string) bool {
	t, p := []rune(text), []rune(pattern)
	prev := make([]bool, len(t)+1)
	prev[0] = true
	for i := 0; i < len(p); i++ {
		r := p[i]
		escaped := false
		if r == '\\' && i+1 < len(p) {
			i++
			r = p[i]
			escaped = true
		}
		next := make([]bool, len(t)+1)
		if r == '%' && !escaped {
			next[0] = prev[0]
		}
		for j := 1; j <= len(t); j++ {
			if r == '%' && !escaped {
				next[j] = prev[j] || next[j-1]
			} else if r == '_' && !escaped {
				next[j] = prev[j-1]
			} else {
				next[j] = prev[j-1] && r == t[j-1]
			}
		}
		prev = next
	}
	return prev[len(t)]
}
func applyModeration(id int64, w NGWord) {
	defer replicate(replicaEvent{Kind: "word", StreamID: id, Word: &w})
	state.Lock()
	defer state.Unlock()
	found := false
	for _, old := range state.words[id] {
		if old.ID == w.ID {
			found = true
			break
		}
	}
	if !found {
		state.words[id] = append(state.words[id], w)
	}
	for cid, c := range state.comments[id] {
		for _, word := range state.words[id] {
			if strings.ContainsAny(word.Word, "%_\\") {
				if likePattern(c.Comment, "%"+word.Word+"%") {
					delete(state.comments[id], cid)
					state.streamScore[id] -= c.Tip
					state.userScore[state.streams[id].UserID] -= c.Tip
					state.totalTip -= c.Tip
					break
				}
			} else if strings.Contains(c.Comment, word.Word) {
				delete(state.comments[id], cid)
				state.streamScore[id] -= c.Tip
				state.userScore[state.streams[id].UserID] -= c.Tip
				state.totalTip -= c.Tip
				break
			}
		}
	}
}
func applyRegistered(u User, password string) {
	defer replicate(replicaEvent{Kind: "user", User: &u, Password: password})
	img, _ := os.ReadFile(fallbackImage)
	state.Lock()
	state.users[u.ID] = u
	state.passwords[u.ID] = password
	state.names[u.Name] = u.ID
	state.icons[u.ID] = img
	state.Unlock()
}
func applyReserved(s LivestreamModel, tags []Tag) {
	defer replicate(replicaEvent{Kind: "stream", Stream: &s, Tags: tags})
	state.Lock()
	if _, exists := state.streams[s.ID]; exists {
		state.Unlock()
		return
	}

	state.streams[s.ID] = s
	state.tags[s.ID] = tags
	state.ids = insertSorted(state.ids, s.ID)
	state.owned[s.UserID] = insertSorted(state.owned[s.UserID], s.ID)
	for _, t := range tags {
		state.tagged[t.Name] = insertSorted(state.tagged[t.Name], s.ID)
	}
	state.streamScore[s.ID] = 0
	state.Unlock()
}
func applyIcon(id int64, img []byte) {
	defer replicate(replicaEvent{Kind: "icon", UserID: id, Image: img})
	state.Lock()
	u := state.users[id]
	u.IconHash = fmt.Sprintf("%x", sha256.Sum256(img))
	state.users[id] = u
	state.icons[id] = img
	state.Unlock()
}
func applyComment(m LivecommentModel) {
	defer replicate(replicaEvent{Kind: "comment", Comment: &m})
	state.Lock()
	if state.comments[m.LivestreamID] == nil {
		state.comments[m.LivestreamID] = make(map[int64]LivecommentModel)
	}
	if _, exists := state.comments[m.LivestreamID][m.ID]; exists {
		state.Unlock()
		return
	}

	state.comments[m.LivestreamID][m.ID] = m
	state.streamScore[m.LivestreamID] += m.Tip
	state.userScore[state.streams[m.LivestreamID].UserID] += m.Tip
	state.totalTip += m.Tip
	state.Unlock()
}
func applyReaction(m ReactionModel) {
	defer replicate(replicaEvent{Kind: "reaction", Reaction: &m})
	state.Lock()
	for _, old := range state.reactions[m.LivestreamID] {
		if old.ID == m.ID {
			state.Unlock()
			return
		}
	}

	state.reactions[m.LivestreamID] = append(state.reactions[m.LivestreamID], m)
	state.streamScore[m.LivestreamID]++
	state.userScore[state.streams[m.LivestreamID].UserID]++
	state.Unlock()
}
func applyReport(m LivecommentReportModel) {
	defer replicate(replicaEvent{Kind: "report", Report: &m})
	state.Lock()
	for _, old := range state.reports[m.LivestreamID] {
		if old.ID == m.ID {
			state.Unlock()
			return
		}
	}

	state.reports[m.LivestreamID] = append(state.reports[m.LivestreamID], m)
	state.Unlock()
}
func applyEnter(id, uid int64) {
	defer replicateViewer(id, uid)
	state.Lock()
	if state.viewers[id] == nil {
		state.viewers[id] = make(map[int64]int64)
	}
	state.viewers[id][uid]++
	state.Unlock()
}
func applyExit(id, uid int64) {
	defer replicateViewer(id, uid)
	state.Lock()
	delete(state.viewers[id], uid)
	state.Unlock()
}

func insertSorted(ids []int64, id int64) []int64 {
	i := sort.Search(len(ids), func(i int) bool { return ids[i] >= id })
	if i < len(ids) && ids[i] == id {
		return ids
	}
	ids = append(ids, 0)
	copy(ids[i+1:], ids[i:])
	ids[i] = id
	return ids
}
