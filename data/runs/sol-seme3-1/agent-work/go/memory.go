package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/sessions"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
)

type memUser struct {
	Model UserModel
	Theme Theme
	Image []byte
	Hash  string
}
type memStream struct {
	Model     LivestreamModel
	Tags      []Tag
	Comments  []int64
	Reactions []int64
	Reports   []int64
	Words     []NGWord
	Viewers   map[int64]int64
	Tips      int64
	Emojis    map[string]int64
}
type state struct {
	Users       map[int64]*memUser
	Names       map[string]int64
	Streams     map[int64]*memStream
	StreamIDs   []int64
	UserStreams map[int64][]int64
	Tags        []Tag
	TagStreams  map[string][]int64
	Comments    map[int64]LivecommentModel
	Reactions   map[int64]ReactionModel
	Reports     map[int64]LivecommentReportModel
	Slots       []ReservationSlotModel
	Next        map[string]int64
	TotalTip    int64
}

var smu sync.RWMutex
var lifecycle sync.RWMutex
var st *state
var fallback []byte
var fallbackHash string
var journal chan logRequest
var httpClient = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{MaxIdleConns: 128, MaxIdleConnsPerHost: 64}}
var workerCounter atomic.Uint64
var logBatches atomic.Uint64
var logNanos atomic.Int64
var logBytes atomic.Int64

type event struct {
	Kind     string
	ID       int64
	User     *memUser                `json:",omitempty"`
	Stream   *memStream              `json:",omitempty"`
	Comment  *LivecommentModel       `json:",omitempty"`
	Reaction *ReactionModel          `json:",omitempty"`
	Report   *LivecommentReportModel `json:",omitempty"`
	Word     *NGWord                 `json:",omitempty"`
	Image    []byte                  `json:",omitempty"`
	UID      int64
	SID      int64
	At       int64
}
type logRequest struct {
	Data  []byte
	Done  chan struct{}
	Reset bool
}

var walPath = "/home/isucon/webapp/state.wal"

func startJournal() {
	journal = make(chan logRequest, 8192)
	go func() {
		f, e := os.OpenFile(walPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			log.Fatal(e)
		}
		interval, _ := strconv.Atoi(os.Getenv("WAL_SYNC_INTERVAL_MS"))
		if interval > 0 {
			go func() {
				ticker := time.NewTicker(time.Duration(interval) * time.Millisecond)
				defer ticker.Stop()
				for range ticker.C {
					if err := f.Sync(); err != nil {
						log.Fatal(err)
					}
				}
			}()
		}
		for r := range journal {
			batch := []logRequest{r}
			if r.Reset {
				if e = f.Truncate(0); e != nil {
					log.Fatal(e)
				}
			} else {
				for len(batch) < 512 {
					select {
					case n := <-journal:
						batch = append(batch, n)
					default:
						goto flush
					}
				}
			}
		flush:
			for _, q := range batch {
				if len(q.Data) > 0 {
					if _, e = f.Write(q.Data); e != nil {
						log.Fatal(e)
					}
				}
			}
			syncStart := time.Now()
			if interval == 0 || r.Reset {
				if e = f.Sync(); e != nil {
					log.Fatal(e)
				}
			}
			logBatches.Add(1)
			logNanos.Add(time.Since(syncStart).Nanoseconds())
			for _, q := range batch {
				logBytes.Add(int64(len(q.Data)))
				close(q.Done)
			}
		}
	}()
}
func record(e event) chan struct{} {
	b, err := json.Marshal(e)
	if err != nil {
		panic(err)
	}
	d := make(chan struct{})
	journal <- logRequest{Data: append(b, '\n'), Done: d}
	return d
}
func resetJournal()          { d := make(chan struct{}); journal <- logRequest{Done: d, Reset: true}; <-d }
func next(kind string) int64 { st.Next[kind]++; return st.Next[kind] }
func bump(kind string, id int64) {
	if id > st.Next[kind] {
		st.Next[kind] = id
	}
}
func newState() *state {
	return &state{Users: map[int64]*memUser{}, Names: map[string]int64{}, Streams: map[int64]*memStream{}, UserStreams: map[int64][]int64{}, TagStreams: map[string][]int64{}, Comments: map[int64]LivecommentModel{}, Reactions: map[int64]ReactionModel{}, Reports: map[int64]LivecommentReportModel{}, Next: map[string]int64{}}
}
func newStream(m LivestreamModel, tags []Tag) *memStream {
	return &memStream{Model: m, Tags: tags, Viewers: map[int64]int64{}, Emojis: map[string]int64{}}
}
func addStream(s *memStream) {
	st.Streams[s.Model.ID] = s
	st.StreamIDs = append(st.StreamIDs, s.Model.ID)
	st.UserStreams[s.Model.UserID] = append(st.UserStreams[s.Model.UserID], s.Model.ID)
	bump("stream", s.Model.ID)
	for _, t := range s.Tags {
		st.TagStreams[t.Name] = append(st.TagStreams[t.Name], s.Model.ID)
	}
}
func removeComment(id int64) {
	c, ok := st.Comments[id]
	if !ok {
		return
	}
	delete(st.Comments, id)
	st.TotalTip -= c.Tip
	if s := st.Streams[c.LivestreamID]; s != nil {
		s.Tips -= c.Tip
	}
}
func apply(e event) {
	switch e.Kind {
	case "user":
		st.Users[e.User.Model.ID] = e.User
		st.Names[e.User.Model.Name] = e.User.Model.ID
		bump("user", e.User.Model.ID)
		bump("theme", e.User.Theme.ID)
	case "icon":
		u := st.Users[e.UID]
		u.Image = append([]byte{}, e.Image...)
		u.Hash = fmt.Sprintf("%x", sha256.Sum256(e.Image))
		bump("icon", e.ID)
	case "stream":
		addStream(e.Stream)
		for i := range st.Slots {
			v := &st.Slots[i]
			if v.StartAt >= e.Stream.Model.StartAt && v.EndAt <= e.Stream.Model.EndAt {
				v.Slot--
			}
		}
	case "comment":
		c := *e.Comment
		st.Comments[c.ID] = c
		s := st.Streams[c.LivestreamID]
		s.Comments = append(s.Comments, c.ID)
		s.Tips += c.Tip
		st.TotalTip += c.Tip
		bump("comment", c.ID)
	case "reaction":
		r := *e.Reaction
		st.Reactions[r.ID] = r
		s := st.Streams[r.LivestreamID]
		s.Reactions = append(s.Reactions, r.ID)
		s.Emojis[r.EmojiName]++
		bump("reaction", r.ID)
	case "report":
		r := *e.Report
		st.Reports[r.ID] = r
		st.Streams[r.LivestreamID].Reports = append(st.Streams[r.LivestreamID].Reports, r.ID)
		bump("report", r.ID)
	case "word":
		w := *e.Word
		s := st.Streams[w.LivestreamID]
		s.Words = append(s.Words, w)
		bump("word", w.ID)
		for _, id := range s.Comments {
			if c, ok := st.Comments[id]; ok && likeMatch(c.Comment, w.Word) {
				removeComment(id)
			}
		}
	case "enter":
		st.Streams[e.SID].Viewers[e.UID]++
	case "exit":
		delete(st.Streams[e.SID].Viewers, e.UID)
	}
}
func loadState() error {
	st = newState()
	var users []UserModel
	if e := dbConn.Select(&users, "SELECT * FROM users"); e != nil {
		return e
	}
	for _, u := range users {
		v := u
		st.Users[u.ID] = &memUser{Model: v, Hash: fallbackHash}
		st.Names[u.Name] = u.ID
		bump("user", u.ID)
	}
	var themes []ThemeModel
	if e := dbConn.Select(&themes, "SELECT * FROM themes"); e != nil {
		return e
	}
	for _, t := range themes {
		st.Users[t.UserID].Theme = Theme{ID: t.ID, DarkMode: t.DarkMode}
		bump("theme", t.ID)
	}
	var icons []struct {
		ID    int64  `db:"id"`
		UID   int64  `db:"user_id"`
		Image []byte `db:"image"`
	}
	if e := dbConn.Select(&icons, "SELECT * FROM icons"); e != nil {
		return e
	}
	for _, v := range icons {
		u := st.Users[v.UID]
		u.Image = v.Image
		u.Hash = fmt.Sprintf("%x", sha256.Sum256(v.Image))
		bump("icon", v.ID)
	}
	var tags []TagModel
	if e := dbConn.Select(&tags, "SELECT * FROM tags ORDER BY id"); e != nil {
		return e
	}
	tm := map[int64]Tag{}
	for _, t := range tags {
		v := Tag{ID: t.ID, Name: t.Name}
		st.Tags = append(st.Tags, v)
		tm[t.ID] = v
	}
	var streams []LivestreamModel
	if e := dbConn.Select(&streams, "SELECT * FROM livestreams ORDER BY id"); e != nil {
		return e
	}
	for _, s := range streams {
		addStream(newStream(s, []Tag{}))
	}
	var links []LivestreamTagModel
	if e := dbConn.Select(&links, "SELECT * FROM livestream_tags ORDER BY id"); e != nil {
		return e
	}
	for _, l := range links {
		s := st.Streams[l.LivestreamID]
		s.Tags = append(s.Tags, tm[l.TagID])
		st.TagStreams[tm[l.TagID].Name] = append(st.TagStreams[tm[l.TagID].Name], s.Model.ID)
	}
	for _, ids := range st.TagStreams {
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	}
	if e := dbConn.Select(&st.Slots, "SELECT * FROM reservation_slots ORDER BY id"); e != nil {
		return e
	}
	var cs []LivecommentModel
	if e := dbConn.Select(&cs, "SELECT * FROM livecomments ORDER BY id"); e != nil {
		return e
	}
	for i := range cs {
		apply(event{Kind: "comment", Comment: &cs[i]})
	}
	var rs []ReactionModel
	if e := dbConn.Select(&rs, "SELECT * FROM reactions ORDER BY id"); e != nil {
		return e
	}
	for i := range rs {
		apply(event{Kind: "reaction", Reaction: &rs[i]})
	}
	var reports []LivecommentReportModel
	if e := dbConn.Select(&reports, "SELECT * FROM livecomment_reports ORDER BY id"); e != nil {
		return e
	}
	for i := range reports {
		apply(event{Kind: "report", Report: &reports[i]})
	}
	var words []NGWord
	if e := dbConn.Select(&words, "SELECT * FROM ng_words ORDER BY id"); e != nil {
		return e
	}
	for _, w := range words {
		st.Streams[w.LivestreamID].Words = append(st.Streams[w.LivestreamID].Words, w)
		bump("word", w.ID)
	}
	var viewers []struct {
		ID  int64 `db:"id"`
		UID int64 `db:"user_id"`
		SID int64 `db:"livestream_id"`
		At  int64 `db:"created_at"`
	}
	if e := dbConn.Select(&viewers, "SELECT * FROM livestream_viewers_history"); e != nil {
		return e
	}
	for _, v := range viewers {
		apply(event{Kind: "enter", UID: v.UID, SID: v.SID})
	}
	return nil
}
func replay() error {
	f, e := os.OpenFile(walPath, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	r := bufio.NewReader(f)
	var offset int64
	for {
		b, err := r.ReadBytes('\n')
		if err == io.EOF {
			if len(b) > 0 {
				return f.Truncate(offset)
			}
			break
		}
		if err != nil {
			return err
		}
		var ev event
		if e = json.Unmarshal(b, &ev); e != nil {
			return fmt.Errorf("WAL at %d: %w", offset, e)
		}
		apply(ev)
		offset += int64(len(b))
	}
	return nil
}

// SQL LIKE with the connection collation, including wildcard and escape semantics.
func likeMatch(text, word string) bool {
	text = collationFold(text)
	word = collationFold(word)
	if !strings.ContainsAny(word, "%_\\") {
		return strings.Contains(text, word)
	}
	t := []rune(text)
	p := []rune("%" + word + "%")
	prev := make([]bool, len(t)+1)
	prev[0] = true
	for i := 0; i < len(p); i++ {
		c := p[i]
		escaped := false
		if c == '\\' && i+1 < len(p) {
			i++
			c = p[i]
			escaped = true
		}
		cur := make([]bool, len(t)+1)
		if c == '%' && !escaped {
			cur[0] = prev[0]
			for j := 1; j <= len(t); j++ {
				cur[j] = prev[j] || cur[j-1]
			}
		} else {
			for j := 1; j <= len(t); j++ {
				cur[j] = prev[j-1] && (c == t[j-1] || (c == '_' && !escaped))
			}
		}
		prev = cur
	}
	return prev[len(t)]
}
func userResponse(id int64) User {
	u := st.Users[id]
	return User{ID: id, Name: u.Model.Name, DisplayName: u.Model.DisplayName, Description: u.Model.Description, Theme: u.Theme, IconHash: u.Hash}
}
func streamResponse(id int64) Livestream {
	s := st.Streams[id]
	m := s.Model
	return Livestream{ID: id, Owner: userResponse(m.UserID), Title: m.Title, Description: m.Description, PlaylistUrl: m.PlaylistUrl, ThumbnailUrl: m.ThumbnailUrl, Tags: s.Tags, StartAt: m.StartAt, EndAt: m.EndAt}
}
func commentResponse(c LivecommentModel) Livecomment {
	return Livecomment{ID: c.ID, User: userResponse(c.UserID), Livestream: streamResponse(c.LivestreamID), Comment: c.Comment, Tip: c.Tip, CreatedAt: c.CreatedAt}
}
func reactionResponse(r ReactionModel) Reaction {
	return Reaction{ID: r.ID, User: userResponse(r.UserID), Livestream: streamResponse(r.LivestreamID), EmojiName: r.EmojiName, CreatedAt: r.CreatedAt}
}
func reportResponse(r LivecommentReportModel) LivecommentReport {
	return LivecommentReport{ID: r.ID, Reporter: userResponse(r.UserID), Livecomment: commentResponse(st.Comments[r.LivecommentID]), CreatedAt: r.CreatedAt}
}
func bad(msg string) error { return echo.NewHTTPError(400, msg) }
func notFound() error      { return echo.NewHTTPError(404, "not found") }
func decode(c echo.Context, v interface{}) error {
	if e := json.NewDecoder(c.Request().Body).Decode(v); e != nil {
		return bad("invalid JSON")
	}
	return nil
}
func limitValue(c echo.Context) (int, error) {
	if c.QueryParam("limit") == "" {
		return -1, nil
	}
	n, e := strconv.Atoi(c.QueryParam("limit"))
	if e != nil || n < 0 {
		return 0, bad("invalid limit")
	}
	return n, nil
}
func syncNames() error {
	names := make([]string, 0, len(st.Names)+1)
	names = append(names, "pipe")
	for n := range st.Names {
		names = append(names, n)
	}
	b, _ := json.Marshal(names)
	r, e := httpClient.Post("http://10.99.0.11:8090/names", "application/json", bytes.NewReader(b))
	if e != nil {
		return e
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return fmt.Errorf("DNS update: %d", r.StatusCode)
	}
	return nil
}
func addName(n string) error {
	r, e := httpClient.Post("http://10.99.0.11:8090/name", "text/plain", strings.NewReader(n))
	if e != nil {
		return e
	}
	io.Copy(io.Discard, r.Body)
	r.Body.Close()
	return nil
}
func passwordWork(password, hash string) (string, error) {
	host := "10.99.0.13"
	if workerCounter.Add(1)%2 == 0 {
		host = "10.99.0.12"
	}
	b, _ := json.Marshal(map[string]string{"password": password, "hash": hash})
	r, e := httpClient.Post("http://"+host+":8090/bcrypt", "application/json", bytes.NewReader(b))
	if e != nil {
		if hash == "" {
			v, e := bcrypt.GenerateFromPassword([]byte(password), bcryptDefaultCost)
			return string(v), e
		}
		return "", bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	}
	defer r.Body.Close()
	out, e := io.ReadAll(r.Body)
	if e != nil {
		return "", e
	}
	if r.StatusCode != 200 {
		return "", bcrypt.ErrMismatchedHashAndPassword
	}
	return string(out), nil
}
func memoryInitialize(c echo.Context) error {
	lifecycle.Lock()
	defer lifecycle.Unlock()
	smu.Lock()
	defer smu.Unlock()
	if _, e := os.Stat(baselinePath); e == nil {
		if e = loadBaseline(); e != nil {
			return e
		}
	} else {
		if b, e := exec.Command("../sql/init.sh").CombinedOutput(); e != nil {
			return fmt.Errorf("initialize: %s: %w", b, e)
		}
		if e := loadState(); e != nil {
			return e
		}
	}
	resetJournal()
	if e := syncNames(); e != nil {
		return e
	}
	return c.JSON(200, map[string]string{"language": "golang", "lang": "golang"})
}
func memoryRegister(c echo.Context) error {
	var req PostUserRequest
	if e := decode(c, &req); e != nil {
		return e
	}
	if req.Name == "pipe" {
		return bad("reserved username")
	}
	hash, e := passwordWork(req.Password, "")
	if e != nil {
		return e
	}
	smu.Lock()
	if _, ok := st.Names[req.Name]; ok {
		smu.Unlock()
		return echo.NewHTTPError(500, "duplicate username")
	}
	u := &memUser{Model: UserModel{ID: next("user"), Name: req.Name, DisplayName: req.DisplayName, Description: req.Description, HashedPassword: hash}, Theme: Theme{ID: next("theme"), DarkMode: req.Theme.DarkMode}, Hash: fallbackHash}
	ev := event{Kind: "user", User: u}
	apply(ev)
	d := record(ev)
	out := userResponse(u.Model.ID)
	smu.Unlock()
	<-d
	if e := addName(req.Name); e != nil {
		return e
	}
	return c.JSON(201, out)
}
func memoryLogin(c echo.Context) error {
	var req LoginRequest
	if e := decode(c, &req); e != nil {
		return e
	}
	smu.RLock()
	id := st.Names[req.Username]
	u := st.Users[id]
	var hash string
	if u != nil {
		hash = u.Model.HashedPassword
	}
	smu.RUnlock()
	if u == nil {
		return echo.NewHTTPError(401, "invalid username or password")
	}
	if _, e := passwordWork(req.Password, hash); e != nil {
		return echo.NewHTTPError(401, "invalid username or password")
	}
	sess, e := session.Get(defaultSessionIDKey, c)
	if e != nil {
		return echo.NewHTTPError(401, "invalid session")
	}
	sess.Options = &sessions.Options{Domain: "u.isucon.dev", MaxAge: 60000, Path: "/"}
	sess.Values[defaultSessionIDKey] = fmt.Sprint(time.Now().UnixNano())
	sess.Values[defaultUserIDKey] = id
	sess.Values[defaultUsernameKey] = req.Username
	sess.Values[defaultSessionExpiresKey] = time.Now().Add(time.Hour).Unix()
	if e := sess.Save(c.Request(), c.Response()); e != nil {
		return e
	}
	return c.NoContent(200)
}
func memoryAPI(c echo.Context) error {
	if c.Request().URL.Path == "/api/initialize" && c.Request().Method == "POST" {
		return memoryInitialize(c)
	}
	lifecycle.RLock()
	defer lifecycle.RUnlock()
	path := strings.Split(strings.Trim(c.Request().URL.Path, "/"), "/")
	method := c.Request().Method
	if len(path) < 2 {
		return notFound()
	}
	if path[1] == "register" && method == "POST" {
		return memoryRegister(c)
	}
	if path[1] == "login" && method == "POST" {
		return memoryLogin(c)
	}
	public := path[1] == "tag" || path[1] == "payment" || (path[1] == "livestream" && len(path) > 2 && path[2] == "search") || (path[1] == "user" && len(path) == 4 && path[3] == "icon")
	var uid int64
	if !public {
		var err error
		uid, err = memorySession(c)
		if err != nil {
			return err
		}
	}
	var body interface{}
	if method == "POST" {
		switch path[1] {
		case "icon":
			body = &PostIconRequest{}
		case "livestream":
			if len(path) > 2 {
				switch path[2] {
				case "reservation":
					body = &ReserveLivestreamRequest{}
				default:
					if len(path) > 3 {
						switch path[3] {
						case "livecomment":
							if len(path) == 4 {
								body = &PostLivecommentRequest{}
							}
						case "reaction":
							body = &PostReactionRequest{}
						case "moderate":
							body = &ModerateRequest{}
						}
					}
				}
			}
		}
		if body != nil {
			if e := decode(c, body); e != nil {
				return e
			}
		}
	}
	// A single state lock makes every mutation and its journal order serializable.
	unlock := smu.Unlock
	if method == "GET" {
		smu.RLock()
		unlock = smu.RUnlock
	} else {
		smu.Lock()
	}
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()
	respond := func(status int, out interface{}, ev *event) error {
		var d chan struct{}
		if ev != nil {
			apply(*ev)
			d = record(*ev)
		}
		unlock()
		locked = false
		data, e := json.Marshal(out)
		if d != nil {
			<-d
		}
		if e != nil {
			return e
		}
		return c.JSONBlob(status, data)
	}
	if path[1] == "tag" {
		return respond(200, struct {
			Tags []Tag `json:"tags"`
		}{st.Tags}, nil)
	}
	if path[1] == "payment" {
		return respond(200, PaymentResult{TotalTip: st.TotalTip}, nil)
	}
	if path[1] == "icon" && method == "POST" {
		if st.Users[uid] == nil {
			return notFound()
		}
		req := body.(*PostIconRequest)
		id := next("icon")
		return respond(201, PostIconResponse{ID: id}, &event{Kind: "icon", ID: id, UID: uid, Image: req.Image})
	}
	if path[1] == "user" && len(path) >= 3 {
		id := st.Names[path[2]]
		if path[2] == "me" && len(path) == 3 {
			id = uid
		}
		u := st.Users[id]
		if u == nil {
			return notFound()
		}
		if len(path) == 3 {
			return respond(200, userResponse(id), nil)
		}
		switch path[3] {
		case "icon":
			image := u.Image
			if image == nil {
				image = fallback
			}
			hash := `"` + u.Hash + `"`
			unlock()
			locked = false
			c.Response().Header().Set("ETag", hash)
			if c.Request().Header.Get("If-None-Match") == hash {
				return c.NoContent(304)
			}
			return c.Blob(200, "image/jpeg", image)
		case "theme":
			return respond(200, u.Theme, nil)
		case "livestream":
			out := []Livestream{}
			ids := st.UserStreams[id]
			for _, sid := range ids {
				out = append(out, streamResponse(sid))
			}
			return respond(200, out, nil)
		case "statistics":
			return respond(200, userStats(id), nil)
		}
	}
	if path[1] != "livestream" {
		return notFound()
	}
	if len(path) == 2 {
		out := []Livestream{}
		ids := st.UserStreams[uid]
		for _, id := range ids {
			out = append(out, streamResponse(id))
		}
		return respond(200, out, nil)
	}
	if path[2] == "reservation" && method == "POST" {
		req := body.(*ReserveLivestreamRequest)
		if req.StartAt >= 1732496400 || req.EndAt <= 1700874000 {
			return bad("bad reservation time range")
		}
		for _, s := range st.Slots {
			if s.StartAt >= req.StartAt && s.EndAt <= req.EndAt && s.Slot < 1 {
				return bad("no reservation slots")
			}
		}
		tags := []Tag{}
		for _, tid := range req.Tags {
			found := false
			for _, t := range st.Tags {
				if t.ID == tid {
					tags = append(tags, t)
					found = true
					break
				}
			}
			if !found {
				return bad("unknown tag")
			}
		}
		id := next("stream")
		m := LivestreamModel{ID: id, UserID: uid, Title: req.Title, Description: req.Description, PlaylistUrl: req.PlaylistUrl, ThumbnailUrl: req.ThumbnailUrl, StartAt: req.StartAt, EndAt: req.EndAt}
		ev := event{Kind: "stream", Stream: newStream(m, tags)}
		apply(ev)
		d := record(ev)
		out := streamResponse(id)
		unlock()
		locked = false
		data, e := json.Marshal(out)
		<-d
		if e != nil {
			return e
		}
		return c.JSONBlob(201, data)
	}
	if path[2] == "search" {
		n, e := limitValue(c)
		if e != nil {
			return e
		}
		ids := st.StreamIDs
		if tag := c.QueryParam("tag"); tag != "" {
			ids = st.TagStreams[tag]
			n = -1
		}
		count := len(ids)
		if n >= 0 && count > n {
			count = n
		}
		out := make([]Livestream, 0, count)
		for i := len(ids) - 1; i >= len(ids)-count; i-- {
			out = append(out, streamResponse(ids[i]))
		}
		return respond(200, out, nil)
	}
	sid, e := strconv.ParseInt(path[2], 10, 64)
	if e != nil {
		return bad("invalid livestream_id")
	}
	s := st.Streams[sid]
	if s == nil {
		return notFound()
	}
	if len(path) == 3 {
		return respond(200, streamResponse(sid), nil)
	}
	switch path[3] {
	case "enter":
		return respond(200, nil, &event{Kind: "enter", UID: uid, SID: sid, At: time.Now().Unix()})
	case "exit":
		return respond(200, nil, &event{Kind: "exit", UID: uid, SID: sid})
	case "statistics":
		return respond(200, streamStats(sid), nil)
	case "ngwords":
		out := []NGWord{}
		for _, w := range s.Words {
			if w.UserID == uid {
				out = append(out, w)
			}
		}
		sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
		return respond(200, out, nil)
	case "moderate":
		if s.Model.UserID != uid {
			return bad("not owner")
		}
		req := body.(*ModerateRequest)
		w := NGWord{ID: next("word"), UserID: uid, LivestreamID: sid, Word: req.NGWord, CreatedAt: time.Now().Unix()}
		return respond(201, map[string]int64{"word_id": w.ID}, &event{Kind: "word", Word: &w})
	case "report":
		if s.Model.UserID != uid {
			return echo.NewHTTPError(403, "not owner")
		}
		out := []LivecommentReport{}
		for _, id := range s.Reports {
			r := st.Reports[id]
			if _, ok := st.Comments[r.LivecommentID]; ok {
				out = append(out, reportResponse(r))
			}
		}
		return respond(200, out, nil)
	case "livecomment":
		if len(path) == 6 && path[5] == "report" && method == "POST" {
			cid, e := strconv.ParseInt(path[4], 10, 64)
			if e != nil {
				return bad("invalid comment id")
			}
			if _, ok := st.Comments[cid]; !ok {
				return notFound()
			}
			r := LivecommentReportModel{ID: next("report"), UserID: uid, LivestreamID: sid, LivecommentID: cid, CreatedAt: time.Now().Unix()}
			return respond(201, reportResponse(r), &event{Kind: "report", Report: &r})
		}
		if method == "POST" {
			req := body.(*PostLivecommentRequest)
			for _, w := range s.Words {
				if likeMatch(req.Comment, w.Word) {
					return bad("このコメントがスパム判定されました")
				}
			}
			v := LivecommentModel{ID: next("comment"), UserID: uid, LivestreamID: sid, Comment: req.Comment, Tip: req.Tip, CreatedAt: time.Now().Unix()}
			return respond(201, commentResponse(v), &event{Kind: "comment", Comment: &v})
		}
		n, e := limitValue(c)
		if e != nil {
			return e
		}
		ids := []int64{}
		for _, id := range s.Comments {
			if _, ok := st.Comments[id]; ok {
				ids = append(ids, id)
			}
		}
		sort.SliceStable(ids, func(i, j int) bool { return st.Comments[ids[i]].CreatedAt > st.Comments[ids[j]].CreatedAt })
		if n >= 0 && len(ids) > n {
			ids = ids[:n]
		}
		out := make([]Livecomment, 0, len(ids))
		for _, id := range ids {
			out = append(out, commentResponse(st.Comments[id]))
		}
		return respond(200, out, nil)
	case "reaction":
		if method == "POST" {
			req := body.(*PostReactionRequest)
			v := ReactionModel{ID: next("reaction"), UserID: uid, LivestreamID: sid, EmojiName: req.EmojiName, CreatedAt: time.Now().Unix()}
			return respond(201, reactionResponse(v), &event{Kind: "reaction", Reaction: &v})
		}
		n, e := limitValue(c)
		if e != nil {
			return e
		}
		ids := append([]int64(nil), s.Reactions...)
		sort.SliceStable(ids, func(i, j int) bool { return st.Reactions[ids[i]].CreatedAt > st.Reactions[ids[j]].CreatedAt })
		if n >= 0 && len(ids) > n {
			ids = ids[:n]
		}
		out := make([]Reaction, 0, len(ids))
		for _, id := range ids {
			out = append(out, reactionResponse(st.Reactions[id]))
		}
		return respond(200, out, nil)
	}
	return notFound()
}
func streamScore(s *memStream) int64 { return int64(len(s.Reactions)) + s.Tips }
func streamStats(id int64) LivestreamStatistics {
	s := st.Streams[id]
	out := LivestreamStatistics{Rank: 1, TotalReactions: int64(len(s.Reactions)), TotalReports: int64(len(s.Reports))}
	score := streamScore(s)
	for oid, o := range st.Streams {
		v := streamScore(o)
		if v > score || (v == score && oid > id) {
			out.Rank++
		}
	}
	for _, n := range s.Viewers {
		out.ViewersCount += n
	}
	for _, cid := range s.Comments {
		if c, ok := st.Comments[cid]; ok && c.Tip > out.MaxTip {
			out.MaxTip = c.Tip
		}
	}
	return out
}
func userStats(id int64) UserStatistics {
	out := UserStatistics{Rank: 1}
	scores := map[int64]int64{}
	emojis := map[string]int64{}
	for _, s := range st.Streams {
		scores[s.Model.UserID] += streamScore(s)
		if s.Model.UserID != id {
			continue
		}
		out.TotalReactions += int64(len(s.Reactions))
		out.TotalTip += s.Tips
		for _, n := range s.Viewers {
			out.ViewersCount += n
		}
		for _, cid := range s.Comments {
			if _, ok := st.Comments[cid]; ok {
				out.TotalLivecomments++
			}
		}
		for e, n := range s.Emojis {
			emojis[e] += n
		}
	}
	score := scores[id]
	name := st.Users[id].Model.Name
	for oid, u := range st.Users {
		v := scores[oid]
		if v > score || (v == score && u.Model.Name > name) {
			out.Rank++
		}
	}
	var best int64
	for e, n := range emojis {
		if n > best || (n == best && e > out.FavoriteEmoji) {
			best = n
			out.FavoriteEmoji = e
		}
	}
	return out
}
func main() {
	go func() { log.Print(http.ListenAndServe("127.0.0.1:6060", nil)) }()
	var err error
	fallback, err = os.ReadFile(fallbackImage)
	if err != nil {
		log.Fatal(err)
	}
	fallbackHash = fmt.Sprintf("%x", sha256.Sum256(fallback))
	e := echo.New()
	e.HideBanner = true
	e.Debug = false
	cookieStore := sessions.NewCookieStore(secret)
	cookieStore.Options.Domain = "*.u.isucon.dev"
	e.Use(session.Middleware(cookieStore))
	e.HTTPErrorHandler = errorResponseHandler
	if _, statErr := os.Stat(baselinePath); statErr == nil && os.Getenv("EXPORT_BASELINE") == "" {
		if err = loadSavedWeights(); err != nil {
			log.Fatal(err)
		}
		if err = loadBaseline(); err != nil {
			log.Fatal(err)
		}
	} else {
		dbConn, err = connectDB(e.Logger)
		if err != nil {
			log.Fatal(err)
		}
		if err = loadCollationWeights(dbConn); err != nil {
			log.Fatal(err)
		}
		if err = loadState(); err != nil {
			log.Fatal(err)
		}
		if os.Getenv("EXPORT_BASELINE") == "1" {
			if err = saveBaseline(); err != nil {
				log.Fatal(err)
			}
			log.Print("baseline snapshot exported")
			return
		}
	}
	if err = replay(); err != nil {
		log.Fatal(err)
	}
	startJournal()
	e.Any("/api/*", memoryAPI)
	e.GET("/internal/digest", func(c echo.Context) error {
		lifecycle.RLock()
		defer lifecycle.RUnlock()
		smu.RLock()
		defer smu.RUnlock()
		digest, err := stateDigest()
		if err != nil {
			return err
		}
		return c.JSON(200, map[string]interface{}{"digest": digest, "users": len(st.Users), "streams": len(st.Streams), "comments": len(st.Comments), "reactions": len(st.Reactions), "tip": st.TotalTip})
	})
	e.GET("/internal/metrics", func(c echo.Context) error {
		return c.JSON(200, map[string]interface{}{"wal_batches": logBatches.Load(), "wal_sync_ns": logNanos.Load(), "wal_bytes": logBytes.Load()})
	})

	e.GET("/internal/names", func(c echo.Context) error {
		smu.RLock()
		defer smu.RUnlock()
		names := []string{"pipe"}
		for n := range st.Names {
			names = append(names, n)
		}
		return c.JSON(200, names)
	})
	log.Printf("loaded users=%d streams=%d comments=%d", len(st.Users), len(st.Streams), len(st.Comments))
	if os.Getenv("TLS_NATIVE") == "1" {
		startNativeTLS(e)
	}
	log.Fatal(e.Start(":8080"))
}
