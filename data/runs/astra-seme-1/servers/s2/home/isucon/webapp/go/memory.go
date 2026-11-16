package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type memoryState struct {
	Users      map[int64]UserModel
	Themes     map[int64]Theme
	Icons      map[int64][]byte
	Streams    map[int64]LivestreamModel
	Tags       map[int64]Tag
	StreamTags map[int64][]int64
	Comments   map[int64]LivecommentModel
	Reactions  map[int64]ReactionModel
	Reports    map[int64]LivecommentReportModel
	Words      map[int64]NGWord
	Slots      []ReservationSlotModel
	Viewers    map[int64]map[int64]int64
	Next       map[string]int64
}
type memoryEvent struct {
	Kind     string
	ID       int64
	User     UserModel
	Theme    Theme
	Icon     []byte
	Stream   LivestreamModel
	Tags     []int64
	Comment  LivecommentModel
	Reaction ReactionModel
	Report   LivecommentReportModel
	Word     NGWord
	UID      int64
	SID      int64
}

var state memoryState
var stateMu sync.RWMutex
var journal *os.File
var names map[string]int64
var userResponse map[int64]User
var streamComments map[int64][]int64
var streamReactions map[int64][]int64
var streamWords map[int64][]int64
var streamReports map[int64][]int64
var userStreams map[int64][]int64
var streamIDs []int64
var tagStreams map[string][]int64
var fallbackBytes []byte

var dataDir = "/home/isucon/webapp/state"

func newMemoryState() memoryState {
	return memoryState{Users: map[int64]UserModel{}, Themes: map[int64]Theme{}, Icons: map[int64][]byte{}, Streams: map[int64]LivestreamModel{}, Tags: map[int64]Tag{}, StreamTags: map[int64][]int64{}, Comments: map[int64]LivecommentModel{}, Reactions: map[int64]ReactionModel{}, Reports: map[int64]LivecommentReportModel{}, Words: map[int64]NGWord{}, Viewers: map[int64]map[int64]int64{}, Next: map[string]int64{}}
}
func advance(k string, id int64) {
	if id > state.Next[k] {
		state.Next[k] = id
	}
}
func rebuildUser(id int64) {
	u := state.Users[id]
	b := state.Icons[id]
	if b == nil {
		b = fallbackBytes
	}
	userResponse[id] = User{ID: id, Name: u.Name, DisplayName: u.DisplayName, Description: u.Description, Theme: state.Themes[id], IconHash: fmt.Sprintf("%x", sha256.Sum256(b))}
	names[u.Name] = id
}
func rebuildIndexes() {
	names = map[string]int64{}
	userResponse = map[int64]User{}
	streamComments = map[int64][]int64{}
	streamReactions = map[int64][]int64{}
	streamWords = map[int64][]int64{}
	streamReports = map[int64][]int64{}
	userStreams = map[int64][]int64{}
	streamIDs = nil
	tagStreams = map[string][]int64{}
	for id := range state.Users {
		rebuildUser(id)
		advance("user", id)
		advance("theme", state.Themes[id].ID)
	}
	for id, s := range state.Streams {
		streamIDs = append(streamIDs, id)
		for _, t := range state.StreamTags[id] {
			tagStreams[state.Tags[t].Name] = append(tagStreams[state.Tags[t].Name], id)
		}
		userStreams[s.UserID] = append(userStreams[s.UserID], id)
		advance("stream", id)
	}
	for id, c := range state.Comments {
		streamComments[c.LivestreamID] = append(streamComments[c.LivestreamID], id)
		advance("comment", id)
	}
	for id, r := range state.Reactions {
		streamReactions[r.LivestreamID] = append(streamReactions[r.LivestreamID], id)
		advance("reaction", id)
	}
	for id, w := range state.Words {
		streamWords[w.LivestreamID] = append(streamWords[w.LivestreamID], id)
		advance("word", id)
	}
	for id, r := range state.Reports {
		streamReports[r.LivestreamID] = append(streamReports[r.LivestreamID], id)
		advance("report", id)
	}
	sort.Slice(streamIDs, func(i, j int) bool { return streamIDs[i] < streamIDs[j] })
	for _, ids := range tagStreams {
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	}
	for _, ids := range userStreams {
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	}

}
func loadSeed() error {
	state = newMemoryState()
	var users []UserModel
	if err := dbConn.Select(&users, "SELECT * FROM users"); err != nil {
		return err
	}
	for _, u := range users {
		state.Users[u.ID] = u
	}
	var themes []ThemeModel
	if err := dbConn.Select(&themes, "SELECT * FROM themes"); err != nil {
		return err
	}
	for _, t := range themes {
		state.Themes[t.UserID] = Theme{ID: t.ID, DarkMode: t.DarkMode}
	}
	var streams []LivestreamModel
	if err := dbConn.Select(&streams, "SELECT * FROM livestreams"); err != nil {
		return err
	}
	for _, s := range streams {
		state.Streams[s.ID] = s
	}
	var tags []TagModel
	if err := dbConn.Select(&tags, "SELECT * FROM tags"); err != nil {
		return err
	}
	for _, t := range tags {
		state.Tags[t.ID] = Tag{ID: t.ID, Name: t.Name}
	}
	var st []LivestreamTagModel
	if err := dbConn.Select(&st, "SELECT * FROM livestream_tags ORDER BY id"); err != nil {
		return err
	}
	for _, t := range st {
		state.StreamTags[t.LivestreamID] = append(state.StreamTags[t.LivestreamID], t.TagID)
	}
	var cs []LivecommentModel
	if err := dbConn.Select(&cs, "SELECT * FROM livecomments"); err != nil {
		return err
	}
	for _, c := range cs {
		state.Comments[c.ID] = c
	}
	var rs []ReactionModel
	if err := dbConn.Select(&rs, "SELECT * FROM reactions"); err != nil {
		return err
	}
	for _, r := range rs {
		state.Reactions[r.ID] = r
	}
	var ws []NGWord
	if err := dbConn.Select(&ws, "SELECT * FROM ng_words"); err != nil {
		return err
	}
	for _, w := range ws {
		state.Words[w.ID] = w
	}
	if err := dbConn.Select(&state.Slots, "SELECT * FROM reservation_slots ORDER BY id"); err != nil {
		return err
	}
	rebuildIndexes()
	return nil
}
func checkpoint() error {
	if journal != nil {
		journal.Close()
		journal = nil
	}
	f, err := os.OpenFile(dataDir+"/checkpoint.tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err = gob.NewEncoder(f).Encode(state); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	f.Close()
	// Initialization replaces both files while requests are excluded. A checkpoint is only made by initialize.
	if err = os.Rename(dataDir+"/checkpoint.tmp", dataDir+"/checkpoint.gob"); err != nil {
		return err
	}
	journal, err = os.OpenFile(dataDir+"/events.jsonl", os.O_CREATE|os.O_TRUNC|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	if err = journal.Sync(); err != nil {
		return err
	}
	d, err := os.Open(dataDir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func startMemory() error {
	var err error
	fallbackBytes, err = os.ReadFile(fallbackImage)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dataDir, 0700); err != nil {
		return err
	}
	f, err := os.Open(dataDir + "/checkpoint.gob")
	if os.IsNotExist(err) {
		if err = loadSeed(); err != nil {
			return err
		}
		return checkpoint()
	}
	if err != nil {
		return err
	}
	err = gob.NewDecoder(f).Decode(&state)
	f.Close()
	if err != nil {
		return err
	}
	rebuildIndexes()
	journal, err = os.OpenFile(dataDir+"/events.jsonl", os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	r := bufio.NewReader(journal)
	var offset int64
	for {
		line, e := r.ReadBytes('\n')
		if e == io.EOF {
			if len(line) > 0 {
				if err = journal.Truncate(offset); err != nil {
					return err
				}
			}
			break
		}
		if e != nil {
			return e
		}
		var ev memoryEvent
		if err = json.Unmarshal(line, &ev); err != nil {
			return err
		}
		applyEvent(ev)
		offset += int64(len(line))
	}
	return nil
}
func persist(ev memoryEvent) error {
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	n, err := journal.Write(b)
	if err != nil {
		return err
	}
	if n != len(b) {
		return io.ErrShortWrite
	}
	if err = journal.Sync(); err != nil {
		return err
	}
	applyEvent(ev)
	return nil
}
func applyEvent(e memoryEvent) {
	switch e.Kind {
	case "user":
		state.Users[e.User.ID] = e.User
		state.Themes[e.User.ID] = e.Theme
		advance("user", e.User.ID)
		advance("theme", e.Theme.ID)
		rebuildUser(e.User.ID)
	case "icon":
		state.Icons[e.UID] = e.Icon
		advance("icon", e.ID)
		rebuildUser(e.UID)
	case "stream":
		streamIDs = append(streamIDs, e.Stream.ID)
		for _, t := range e.Tags {
			tagStreams[state.Tags[t].Name] = append(tagStreams[state.Tags[t].Name], e.Stream.ID)
		}
		state.Streams[e.Stream.ID] = e.Stream
		state.StreamTags[e.Stream.ID] = e.Tags
		userStreams[e.Stream.UserID] = append(userStreams[e.Stream.UserID], e.Stream.ID)
		advance("stream", e.Stream.ID)
		for i := range state.Slots {
			slot := &state.Slots[i]
			if slot.StartAt >= e.Stream.StartAt && slot.EndAt <= e.Stream.EndAt {
				slot.Slot--
			}
		}
	case "comment":
		state.Comments[e.Comment.ID] = e.Comment
		streamComments[e.Comment.LivestreamID] = append(streamComments[e.Comment.LivestreamID], e.Comment.ID)
		advance("comment", e.Comment.ID)
	case "reaction":
		state.Reactions[e.Reaction.ID] = e.Reaction
		streamReactions[e.Reaction.LivestreamID] = append(streamReactions[e.Reaction.LivestreamID], e.Reaction.ID)
		advance("reaction", e.Reaction.ID)
	case "word":
		state.Words[e.Word.ID] = e.Word
		streamWords[e.Word.LivestreamID] = append(streamWords[e.Word.LivestreamID], e.Word.ID)
		advance("word", e.Word.ID)
		for _, id := range streamComments[e.Word.LivestreamID] {
			if c, ok := state.Comments[id]; ok {
				for _, wid := range streamWords[e.Word.LivestreamID] {
					if matchesWord(c.Comment, state.Words[wid].Word) {
						delete(state.Comments, id)
						break
					}
				}
			}
		}
	case "report":
		state.Reports[e.Report.ID] = e.Report
		streamReports[e.Report.LivestreamID] = append(streamReports[e.Report.LivestreamID], e.Report.ID)
		advance("report", e.Report.ID)
	case "enter":
		if state.Viewers[e.SID] == nil {
			state.Viewers[e.SID] = map[int64]int64{}
		}
		state.Viewers[e.SID][e.UID]++
	case "exit":
		delete(state.Viewers[e.SID], e.UID)
	}
}

// utf8mb4_bin LIKE: support SQL wildcard and escaping, including arbitrary user-supplied words.
func matchesWord(s, word string) bool {
	if !strings.ContainsAny(word, "%_\\") {
		return strings.Contains(s, word)
	}
	p := []rune("%" + word + "%")
	r := []rune(s)
	prev := make([]bool, len(r)+1)
	prev[0] = true
	for i := 0; i < len(p); i++ {
		ch := p[i]
		escaped := false
		if ch == '\\' && i+1 < len(p) {
			i++
			ch = p[i]
			escaped = true
		}
		next := make([]bool, len(r)+1)
		if ch == '%' && !escaped {
			next[0] = prev[0]
			for j := 1; j <= len(r); j++ {
				next[j] = prev[j] || next[j-1]
			}
		} else {
			for j := 1; j <= len(r); j++ {
				next[j] = prev[j-1] && (ch == r[j-1] || (ch == '_' && !escaped))
			}
		}
		prev = next
	}
	return prev[len(r)]
}
func memStream(id int64) Livestream {
	s := state.Streams[id]
	tags := make([]Tag, 0, len(state.StreamTags[id]))
	for _, t := range state.StreamTags[id] {
		tags = append(tags, state.Tags[t])
	}
	return Livestream{ID: s.ID, Owner: userResponse[s.UserID], Title: s.Title, Description: s.Description, PlaylistUrl: s.PlaylistUrl, ThumbnailUrl: s.ThumbnailUrl, Tags: tags, StartAt: s.StartAt, EndAt: s.EndAt}
}
func memComment(id int64) Livecomment {
	c := state.Comments[id]
	return Livecomment{ID: c.ID, User: userResponse[c.UserID], Livestream: memStream(c.LivestreamID), Comment: c.Comment, Tip: c.Tip, CreatedAt: c.CreatedAt}
}
func memReaction(id int64) Reaction {
	r := state.Reactions[id]
	return Reaction{ID: r.ID, User: userResponse[r.UserID], Livestream: memStream(r.LivestreamID), EmojiName: r.EmojiName, CreatedAt: r.CreatedAt}
}
func memSession(c echo.Context) (int64, error) {
	if err := verifyUserSession(c); err != nil {
		return 0, err
	}
	s, _ := session.Get(defaultSessionIDKey, c)
	return s.Values[defaultUserIDKey].(int64), nil
}
func memID(c echo.Context) (int64, error) {
	id, err := strconv.ParseInt(c.Param("livestream_id"), 10, 64)
	if err != nil {
		return 0, echo.NewHTTPError(400, "invalid livestream id")
	}
	return id, nil
}
func memLimit(c echo.Context, n int) (int, error) {
	if v := c.QueryParam("limit"); v != "" {
		l, e := strconv.Atoi(v)
		if e != nil || l < 0 {
			return 0, echo.NewHTTPError(400, "invalid limit")
		}
		if l < n {
			return l, nil
		}
	}
	return n, nil
}
func memInitialize(c echo.Context) error {
	defer lockState(c, true)()
	if _, err := dbConn.Exec("SELECT 1"); err != nil {
		return err
	}
	// Seed SQL remains the source of initial data; mutable runtime data is journaled locally.
	if out, err := runSeed(); err != nil {
		return fmt.Errorf("seed: %s: %w", out, err)
	}
	if err := loadSeed(); err != nil {
		return err
	}
	if err := checkpoint(); err != nil {
		return err
	}
	if err := pushNames(); err != nil {
		return err
	}
	return memoryJSON(c, 200, map[string]string{"language": "golang", "lang": "golang"})
}
func memRegister(c echo.Context) error {
	var req PostUserRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(400, "invalid body")
	}
	if req.Name == "pipe" {
		return echo.NewHTTPError(400, "reserved name")
	}
	hash, err := passwordWork("hash", req.Password, "")
	if err != nil {
		return err
	}
	defer lockState(c, true)()
	if names[req.Name] != 0 {
		return echo.NewHTTPError(500, "duplicate username")
	}
	id := state.Next["user"] + 1
	ev := memoryEvent{Kind: "user", User: UserModel{ID: id, Name: req.Name, DisplayName: req.DisplayName, Description: req.Description, HashedPassword: hash}, Theme: Theme{ID: state.Next["theme"] + 1, DarkMode: req.Theme.DarkMode}}
	if err = persist(ev); err != nil {
		return err
	}
	if err = pushOneName(req.Name); err != nil {
		return err
	}
	return memoryJSON(c, 201, userResponse[id])
}
func memGetUser(c echo.Context) error {
	uid, err := memSession(c)
	if err != nil {
		return err
	}
	defer lockState(c, false)()
	if c.Path() != "/api/user/me" {
		uid = names[c.Param("username")]
	}
	if uid == 0 {
		return echo.NewHTTPError(404, "user not found")
	}
	return memoryJSON(c, 200, userResponse[uid])
}
func memTheme(c echo.Context) error {
	if _, err := memSession(c); err != nil {
		return err
	}
	defer lockState(c, false)()
	id := names[c.Param("username")]
	if id == 0 {
		return echo.NewHTTPError(404, "user not found")
	}
	return memoryJSON(c, 200, state.Themes[id])
}
func memIcon(c echo.Context) error {
	defer lockState(c, false)()
	id := names[c.Param("username")]
	if id == 0 {
		return echo.NewHTTPError(404, "user not found")
	}
	etag := "\"" + userResponse[id].IconHash + "\""
	c.Response().Header().Set("ETag", etag)
	if c.Request().Header.Get("If-None-Match") == etag {
		return memoryNoContent(c, 304)
	}
	b := state.Icons[id]
	if b == nil {
		b = fallbackBytes
	}
	return memoryBlob(c, 200, "image/jpeg", b)
}
func memPostIcon(c echo.Context) error {
	uid, err := memSession(c)
	if err != nil {
		return err
	}
	var req PostIconRequest
	if err = json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(400, "invalid body")
	}
	defer lockState(c, true)()
	id := state.Next["icon"] + 1
	if err = persist(memoryEvent{Kind: "icon", ID: id, UID: uid, Icon: req.Image}); err != nil {
		return err
	}
	return memoryJSON(c, 201, PostIconResponse{ID: id})
}
func memTags(c echo.Context) error {
	defer lockState(c, false)()
	tags := make([]*Tag, 0, len(state.Tags))
	for _, t := range state.Tags {
		v := t
		tags = append(tags, &v)
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i].ID < tags[j].ID })
	return memoryJSON(c, 200, TagsResponse{Tags: tags})
}
func memReserve(c echo.Context) error {
	uid, err := memSession(c)
	if err != nil {
		return err
	}
	var req ReserveLivestreamRequest
	if err = json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(400, "invalid body")
	}
	if req.StartAt >= 1732496400 || req.EndAt <= 1700874000 {
		return echo.NewHTTPError(400, "bad reservation time range")
	}
	defer lockState(c, true)()
	for _, slot := range state.Slots {
		if slot.StartAt >= req.StartAt && slot.EndAt <= req.EndAt && slot.Slot < 1 {
			return echo.NewHTTPError(400, "no slots")
		}
	}
	id := state.Next["stream"] + 1
	s := LivestreamModel{ID: id, UserID: uid, Title: req.Title, Description: req.Description, PlaylistUrl: req.PlaylistUrl, ThumbnailUrl: req.ThumbnailUrl, StartAt: req.StartAt, EndAt: req.EndAt}
	if err = persist(memoryEvent{Kind: "stream", Stream: s, Tags: req.Tags}); err != nil {
		return err
	}
	return memoryJSON(c, 201, memStream(id))
}
func memStreams(c echo.Context) error {
	var uid int64
	var err error
	search := c.Path() == "/api/livestream/search"
	if !search {
		uid, err = memSession(c)
		if err != nil {
			return err
		}
	}
	defer lockState(c, false)()

	ids := []int64{}
	if search {
		source := streamIDs
		tag := c.QueryParam("tag")
		if tag != "" {
			source = tagStreams[tag]
		}
		n := len(source)
		if tag == "" {
			var e error
			n, e = memLimit(c, n)
			if e != nil {
				return e
			}
		}
		ids = make([]int64, 0, n)
		for i := len(source) - 1; i >= len(source)-n; i-- {
			ids = append(ids, source[i])
		}
	} else {
		if c.Param("username") != "" {
			uid = names[c.Param("username")]
			if uid == 0 {
				return echo.NewHTTPError(404, "user not found")
			}
		}
		ids = userStreams[uid]
	}
	out := make([]Livestream, 0, len(ids))
	for _, id := range ids {
		out = append(out, memStream(id))
	}
	return memoryJSON(c, 200, out)
}
func memGetStream(c echo.Context) error {
	if _, e := memSession(c); e != nil {
		return e
	}
	id, e := memID(c)
	if e != nil {
		return e
	}
	defer lockState(c, false)()
	if _, ok := state.Streams[id]; !ok {
		return echo.NewHTTPError(404, "stream not found")
	}
	return memoryJSON(c, 200, memStream(id))
}
func memEnterExit(c echo.Context) error {
	uid, err := memSession(c)
	if err != nil {
		return err
	}
	id, err := memID(c)
	if err != nil {
		return err
	}
	defer lockState(c, true)()
	kind := "enter"
	if c.Request().Method == "DELETE" {
		kind = "exit"
	}
	if err = persist(memoryEvent{Kind: kind, UID: uid, SID: id}); err != nil {
		return err
	}
	return memoryNoContent(c, 200)
}
func memComments(c echo.Context) error {
	if _, err := memSession(c); err != nil {
		return err
	}
	sid, err := memID(c)
	if err != nil {
		return err
	}
	defer lockState(c, false)()
	ids := make([]int64, 0, len(streamComments[sid]))
	for _, id := range streamComments[sid] {
		if _, ok := state.Comments[id]; ok {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := state.Comments[ids[i]], state.Comments[ids[j]]
		if a.CreatedAt == b.CreatedAt {
			return a.ID > b.ID
		}
		return a.CreatedAt > b.CreatedAt
	})
	n, err := memLimit(c, len(ids))
	if err != nil {
		return err
	}
	out := make([]Livecomment, 0, n)
	for _, id := range ids[:n] {
		out = append(out, memComment(id))
	}
	return memoryJSON(c, 200, out)
}
func memPostComment(c echo.Context) error {
	uid, err := memSession(c)
	if err != nil {
		return err
	}
	sid, err := memID(c)
	if err != nil {
		return err
	}
	var req PostLivecommentRequest
	if err = json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(400, "invalid body")
	}
	defer lockState(c, true)()
	if _, ok := state.Streams[sid]; !ok {
		return echo.NewHTTPError(404, "stream not found")
	}
	for _, id := range streamWords[sid] {
		if matchesWord(req.Comment, state.Words[id].Word) {
			return echo.NewHTTPError(400, "このコメントがスパム判定されました")
		}
	}
	id := state.Next["comment"] + 1
	v := LivecommentModel{ID: id, UserID: uid, LivestreamID: sid, Comment: req.Comment, Tip: req.Tip, CreatedAt: time.Now().Unix()}
	if err = persist(memoryEvent{Kind: "comment", Comment: v}); err != nil {
		return err
	}
	return memoryJSON(c, 201, memComment(id))
}
func memReactions(c echo.Context) error {
	if _, err := memSession(c); err != nil {
		return err
	}
	sid, err := memID(c)
	if err != nil {
		return err
	}
	defer lockState(c, false)()
	ids := append([]int64{}, streamReactions[sid]...)
	sort.Slice(ids, func(i, j int) bool {
		a, b := state.Reactions[ids[i]], state.Reactions[ids[j]]
		if a.CreatedAt == b.CreatedAt {
			return a.ID > b.ID
		}
		return a.CreatedAt > b.CreatedAt
	})
	n, err := memLimit(c, len(ids))
	if err != nil {
		return err
	}
	out := make([]Reaction, 0, n)
	for _, id := range ids[:n] {
		out = append(out, memReaction(id))
	}
	return memoryJSON(c, 200, out)
}
func memPostReaction(c echo.Context) error {
	uid, err := memSession(c)
	if err != nil {
		return err
	}
	sid, err := memID(c)
	if err != nil {
		return err
	}
	var req PostReactionRequest
	if err = json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(400, "invalid body")
	}
	defer lockState(c, true)()
	if _, ok := state.Streams[sid]; !ok {
		return echo.NewHTTPError(404, "stream not found")
	}
	id := state.Next["reaction"] + 1
	v := ReactionModel{ID: id, UserID: uid, LivestreamID: sid, EmojiName: req.EmojiName, CreatedAt: time.Now().Unix()}
	if err = persist(memoryEvent{Kind: "reaction", Reaction: v}); err != nil {
		return err
	}
	return memoryJSON(c, 201, memReaction(id))
}
func memWords(c echo.Context) error {
	uid, err := memSession(c)
	if err != nil {
		return err
	}
	sid, err := memID(c)
	if err != nil {
		return err
	}
	defer lockState(c, false)()
	out := []NGWord{}
	for _, id := range streamWords[sid] {
		w := state.Words[id]
		if w.UserID == uid {
			out = append(out, w)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return memoryJSON(c, 200, out)
}
func memModerate(c echo.Context) error {
	uid, err := memSession(c)
	if err != nil {
		return err
	}
	sid, err := memID(c)
	if err != nil {
		return err
	}
	var req ModerateRequest
	if err = json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(400, "invalid body")
	}
	defer lockState(c, true)()
	if state.Streams[sid].UserID != uid {
		return echo.NewHTTPError(400, "not owner")
	}
	id := state.Next["word"] + 1
	w := NGWord{ID: id, UserID: uid, LivestreamID: sid, Word: req.NGWord, CreatedAt: time.Now().Unix()}
	if err = persist(memoryEvent{Kind: "word", Word: w}); err != nil {
		return err
	}
	return memoryJSON(c, 201, map[string]int64{"word_id": id})
}
func memPostReport(c echo.Context) error {
	uid, err := memSession(c)
	if err != nil {
		return err
	}
	sid, err := memID(c)
	if err != nil {
		return err
	}
	cid, err := strconv.ParseInt(c.Param("livecomment_id"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(400, "invalid comment id")
	}
	defer lockState(c, true)()
	if _, ok := state.Comments[cid]; !ok {
		return echo.NewHTTPError(404, "comment not found")
	}
	if _, ok := state.Streams[sid]; !ok {
		return echo.NewHTTPError(404, "stream not found")
	}
	id := state.Next["report"] + 1
	r := LivecommentReportModel{ID: id, UserID: uid, LivestreamID: sid, LivecommentID: cid, CreatedAt: time.Now().Unix()}
	if err = persist(memoryEvent{Kind: "report", Report: r}); err != nil {
		return err
	}
	return memoryJSON(c, 201, LivecommentReport{ID: id, Reporter: userResponse[uid], Livecomment: memComment(cid), CreatedAt: r.CreatedAt})
}
func memReports(c echo.Context) error {
	uid, err := memSession(c)
	if err != nil {
		return err
	}
	sid, err := memID(c)
	if err != nil {
		return err
	}
	defer lockState(c, false)()
	if state.Streams[sid].UserID != uid {
		return echo.NewHTTPError(403, "not owner")
	}
	out := []LivecommentReport{}
	for _, id := range streamReports[sid] {
		r := state.Reports[id]
		if _, ok := state.Comments[r.LivecommentID]; !ok {
			continue
		}
		out = append(out, LivecommentReport{ID: id, Reporter: userResponse[r.UserID], Livecomment: memComment(r.LivecommentID), CreatedAt: r.CreatedAt})
	}
	return memoryJSON(c, 200, out)
}
func memPayment(c echo.Context) error {
	defer lockState(c, false)()
	var sum int64
	for _, v := range state.Comments {
		sum += v.Tip
	}
	return memoryJSON(c, 200, PaymentResult{TotalTip: sum})
}

var cryptoClient = &http.Client{Timeout: 10 * time.Second}

func passwordWork(op, password, hash string) (string, error) {
	b, _ := json.Marshal(map[string]string{"op": op, "password": password, "hash": hash})
	resp, err := cryptoClient.Post("http://10.99.0.13:8090/password", "application/json", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != 200 {
		if resp.StatusCode == 401 {
			return "", bcrypt.ErrMismatchedHashAndPassword
		}
		return "", fmt.Errorf("bcrypt worker: %s", out)
	}
	return string(out), nil
}

// Response values are snapshots. Release state before encoding or socket writes.
func lockState(c echo.Context, write bool) func() {
	if write {
		stateMu.Lock()
	} else {
		stateMu.RLock()
	}
	done := false
	unlock := func() {
		if !done {
			done = true
			if write {
				stateMu.Unlock()
			} else {
				stateMu.RUnlock()
			}
		}
	}
	c.Set("stateUnlock", unlock)
	return unlock
}
func releaseState(c echo.Context) {
	if f, ok := c.Get("stateUnlock").(func()); ok {
		f()
	}
}
func memoryJSON(c echo.Context, code int, v interface{}) error {
	releaseState(c)
	return c.JSON(code, v)
}
func memoryBlob(c echo.Context, code int, contentType string, b []byte) error {
	releaseState(c)
	return c.Blob(code, contentType, b)
}
func memoryNoContent(c echo.Context, code int) error { releaseState(c); return c.NoContent(code) }
