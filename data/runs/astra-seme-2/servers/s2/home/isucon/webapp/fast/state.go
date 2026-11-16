package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/gob"
	"encoding/json"
	"fmt"
	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

type UserData struct {
	Model UserModel
	Theme Theme
	Image []byte
	Hash  string
}
type StreamData struct {
	Model LivestreamModel
	Tags  []Tag
}
type State struct {
	Users     map[int64]*UserData
	Names     map[string]int64
	Streams   map[int64]*StreamData
	Tags      []Tag
	Comments  map[int64]LivecommentModel
	Reactions map[int64]ReactionModel
	Reports   map[int64]LivecommentReportModel
	Words     map[int64]NGWord
	Viewers   map[int64]map[int64][]int64
	Slots     []ReservationSlotModel
	Next      map[string]int64
}
type Event struct {
	Kind     string
	ID       int64
	User     *UserData
	Stream   *StreamData
	Comment  *LivecommentModel
	Reaction *ReactionModel
	Report   *LivecommentReportModel
	Word     *NGWord
	Image    []byte
	UserID   int64
	StreamID int64
	Time     int64
	Delete   []int64
}

var dnsUsers = map[string]bool{}
var mu sync.RWMutex
var st *State
var wal *os.File
var walMu sync.RWMutex
var syncMu sync.Mutex
var walSeq atomic.Uint64
var durableSeq atomic.Uint64
var defaultImage []byte
var defaultHash string
var commentIDs map[int64][]int64
var reactionIDs map[int64][]int64
var wordIDs map[int64][]int64
var reportIDs map[int64][]int64
var userStreams map[int64][]int64
var tagStreams map[string][]int64
var streamIDs []int64
var streamStats map[int64]*Aggregate
var userStats map[int64]*Aggregate
var totalTips int64

type Aggregate struct {
	Reactions, Tips, Comments, Viewers, Reports, MaxTip int64
	Emoji                                               map[string]int64
}

func agg(m map[int64]*Aggregate, id int64) *Aggregate {
	if m[id] == nil {
		m[id] = &Aggregate{Emoji: map[string]int64{}}
	}
	return m[id]
}
func fresh() *State {
	return &State{Users: map[int64]*UserData{}, Names: map[string]int64{}, Streams: map[int64]*StreamData{}, Tags: []Tag{}, Comments: map[int64]LivecommentModel{}, Reactions: map[int64]ReactionModel{}, Reports: map[int64]LivecommentReportModel{}, Words: map[int64]NGWord{}, Viewers: map[int64]map[int64][]int64{}, Next: map[string]int64{}}
}
func next(kind string) int64 { st.Next[kind]++; return st.Next[kind] }
func maxID(kind string, id int64) {
	if st.Next[kind] < id {
		st.Next[kind] = id
	}
}
func hash(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
func user(id int64) User {
	u := st.Users[id]
	if u == nil {
		return User{}
	}
	return User{ID: id, Name: u.Model.Name, DisplayName: u.Model.DisplayName, Description: u.Model.Description, Theme: u.Theme, IconHash: u.Hash}
}
func stream(id int64) Livestream {
	s := st.Streams[id]
	if s == nil {
		return Livestream{Tags: []Tag{}}
	}
	m := s.Model
	return Livestream{ID: id, Owner: user(m.UserID), Title: m.Title, Description: m.Description, PlaylistUrl: m.PlaylistUrl, ThumbnailUrl: m.ThumbnailUrl, Tags: s.Tags, StartAt: m.StartAt, EndAt: m.EndAt}
}
func comment(m LivecommentModel) Livecomment {
	return Livecomment{ID: m.ID, User: user(m.UserID), Livestream: stream(m.LivestreamID), Comment: m.Comment, Tip: m.Tip, CreatedAt: m.CreatedAt}
}
func reaction(m ReactionModel) Reaction {
	return Reaction{ID: m.ID, User: user(m.UserID), Livestream: stream(m.LivestreamID), EmojiName: m.EmojiName, CreatedAt: m.CreatedAt}
}
func report(m LivecommentReportModel) LivecommentReport {
	return LivecommentReport{ID: m.ID, Reporter: user(m.UserID), Livecomment: comment(st.Comments[m.LivecommentID]), CreatedAt: m.CreatedAt}
}
func loadSQL() error {
	db, e := sqlx.Connect("mysql", "isucon:isucon@tcp(127.0.0.1:3306)/isupipe?parseTime=true")
	if e != nil {
		return e
	}
	defer db.Close()
	s := fresh()
	var us []UserModel
	if e = db.Select(&us, "SELECT * FROM users"); e != nil {
		return e
	}
	for _, u := range us {
		u := u
		s.Users[u.ID] = &UserData{Model: u, Hash: defaultHash}
		s.Names[u.Name] = u.ID
	}
	var ts []ThemeModel
	if e = db.Select(&ts, "SELECT * FROM themes"); e != nil {
		return e
	}
	for _, t := range ts {
		if u := s.Users[t.UserID]; u != nil {
			u.Theme = Theme{ID: t.ID, DarkMode: t.DarkMode}
		}
	}
	var icons []struct {
		ID     int64  `db:"id"`
		UserID int64  `db:"user_id"`
		Image  []byte `db:"image"`
	}
	if e = db.Select(&icons, "SELECT * FROM icons"); e != nil {
		return e
	}
	for _, i := range icons {
		if u := s.Users[i.UserID]; u != nil {
			u.Image = i.Image
			u.Hash = hash(i.Image)
		}
	}
	var tags []TagModel
	if e = db.Select(&tags, "SELECT * FROM tags ORDER BY id"); e != nil {
		return e
	}
	tm := map[int64]Tag{}
	for _, t := range tags {
		v := Tag{ID: t.ID, Name: t.Name}
		tm[t.ID] = v
		s.Tags = append(s.Tags, v)
	}
	var ls []LivestreamModel
	if e = db.Select(&ls, "SELECT * FROM livestreams"); e != nil {
		return e
	}
	for _, l := range ls {
		s.Streams[l.ID] = &StreamData{Model: l, Tags: []Tag{}}
	}
	var lt []LivestreamTagModel
	if e = db.Select(&lt, "SELECT * FROM livestream_tags ORDER BY id"); e != nil {
		return e
	}
	for _, t := range lt {
		if l := s.Streams[t.LivestreamID]; l != nil {
			l.Tags = append(l.Tags, tm[t.TagID])
		}
	}
	if e = db.Select(&s.Slots, "SELECT * FROM reservation_slots ORDER BY id"); e != nil {
		return e
	}
	var cs []LivecommentModel
	if e = db.Select(&cs, "SELECT * FROM livecomments"); e != nil {
		return e
	}
	for _, c := range cs {
		s.Comments[c.ID] = c
	}
	var rs []ReactionModel
	if e = db.Select(&rs, "SELECT * FROM reactions"); e != nil {
		return e
	}
	for _, r := range rs {
		s.Reactions[r.ID] = r
	}
	var ws []NGWord
	if e = db.Select(&ws, "SELECT * FROM ng_words"); e != nil {
		return e
	}
	for _, w := range ws {
		s.Words[w.ID] = w
	}
	var reps []LivecommentReportModel
	if e = db.Select(&reps, "SELECT * FROM livecomment_reports"); e != nil {
		return e
	}
	for _, r := range reps {
		s.Reports[r.ID] = r
	}
	var vs []struct {
		ID           int64 `db:"id"`
		UserID       int64 `db:"user_id"`
		LivestreamID int64 `db:"livestream_id"`
		CreatedAt    int64 `db:"created_at"`
	}
	if e = db.Select(&vs, "SELECT * FROM livestream_viewers_history"); e != nil {
		return e
	}
	for _, v := range vs {
		if s.Viewers[v.LivestreamID] == nil {
			s.Viewers[v.LivestreamID] = map[int64][]int64{}
		}
		s.Viewers[v.LivestreamID][v.UserID] = append(s.Viewers[v.LivestreamID][v.UserID], v.CreatedAt)
	}
	st = s
	reindex()
	for _, t := range ts {
		maxID("theme", t.ID)
	}
	for _, i := range icons {
		maxID("icon", i.ID)
	}
	return nil
}
func reindex() {
	if st.Tags == nil {
		st.Tags = []Tag{}
	}
	for _, s := range st.Streams {
		if s.Tags == nil {
			s.Tags = []Tag{}
		}
	}
	dnsUsers = map[string]bool{}
	for _, u := range st.Users {
		dnsUsers[strings.ToLower(u.Model.Name)] = true
	}
	commentIDs = map[int64][]int64{}
	reactionIDs = map[int64][]int64{}
	wordIDs = map[int64][]int64{}
	reportIDs = map[int64][]int64{}
	userStreams = map[int64][]int64{}
	tagStreams = map[string][]int64{}
	streamIDs = nil
	streamStats = map[int64]*Aggregate{}
	userStats = map[int64]*Aggregate{}
	totalTips = 0
	for id := range st.Users {
		maxID("user", id)
		agg(userStats, id)
	}
	for id, s := range st.Streams {
		maxID("stream", id)
		indexStream(s)
	}
	sort.Slice(streamIDs, func(i, j int) bool { return streamIDs[i] < streamIDs[j] })
	for _, ids := range userStreams {
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	}
	for _, ids := range tagStreams {
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	}
	for id, c := range st.Comments {
		maxID("comment", id)
		indexComment(c, 1)
		commentIDs[c.LivestreamID] = append(commentIDs[c.LivestreamID], id)
	}
	for _, ids := range commentIDs {
		sort.Slice(ids, func(i, j int) bool {
			a, b := st.Comments[ids[i]], st.Comments[ids[j]]
			if a.CreatedAt == b.CreatedAt {
				return a.ID < b.ID
			}
			return a.CreatedAt < b.CreatedAt
		})
	}
	for id, r := range st.Reactions {
		maxID("reaction", id)
		indexReaction(r)
		reactionIDs[r.LivestreamID] = append(reactionIDs[r.LivestreamID], id)
	}
	for _, ids := range reactionIDs {
		sort.Slice(ids, func(i, j int) bool {
			a, b := st.Reactions[ids[i]], st.Reactions[ids[j]]
			if a.CreatedAt == b.CreatedAt {
				return a.ID < b.ID
			}
			return a.CreatedAt < b.CreatedAt
		})
	}
	for id, w := range st.Words {
		maxID("word", id)
		wordIDs[w.LivestreamID] = append(wordIDs[w.LivestreamID], id)
	}
	for _, ids := range wordIDs {
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	}
	for id, r := range st.Reports {
		maxID("report", id)
		reportIDs[r.LivestreamID] = append(reportIDs[r.LivestreamID], id)
		agg(streamStats, r.LivestreamID).Reports++
	}
	for _, ids := range reportIDs {
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	}
	for lid, vs := range st.Viewers {
		for _, v := range vs {
			agg(streamStats, lid).Viewers += int64(len(v))
			if l := st.Streams[lid]; l != nil {
				agg(userStats, l.Model.UserID).Viewers += int64(len(v))
			}
		}
	}
}
func indexStream(s *StreamData) {
	id := s.Model.ID
	streamIDs = append(streamIDs, id)
	userStreams[s.Model.UserID] = append(userStreams[s.Model.UserID], id)
	for _, t := range s.Tags {
		tagStreams[t.Name] = append(tagStreams[t.Name], id)
	}
	agg(streamStats, id)
}
func indexComment(c LivecommentModel, sign int64) {
	a := agg(streamStats, c.LivestreamID)
	a.Comments += sign
	a.Tips += c.Tip * sign
	if sign > 0 && c.Tip > a.MaxTip {
		a.MaxTip = c.Tip
	}
	if l := st.Streams[c.LivestreamID]; l != nil {
		a = agg(userStats, l.Model.UserID)
		a.Comments += sign
		a.Tips += c.Tip * sign
	}
	totalTips += c.Tip * sign
}
func indexReaction(r ReactionModel) {
	a := agg(streamStats, r.LivestreamID)
	a.Reactions++
	a.Emoji[r.EmojiName]++
	if l := st.Streams[r.LivestreamID]; l != nil {
		a = agg(userStats, l.Model.UserID)
		a.Reactions++
		a.Emoji[r.EmojiName]++
	}
}
func apply(e Event) {
	switch e.Kind {
	case "user":
		dnsUsers[strings.ToLower(e.User.Model.Name)] = true
		st.Users[e.User.Model.ID] = e.User
		st.Names[e.User.Model.Name] = e.User.Model.ID
		maxID("user", e.User.Model.ID)
		maxID("theme", e.User.Theme.ID)
		agg(userStats, e.User.Model.ID)
	case "icon":
		u := st.Users[e.UserID]
		u.Image = e.Image
		u.Hash = hash(e.Image)
		maxID("icon", e.ID)
	case "stream":
		s := e.Stream
		st.Streams[s.Model.ID] = s
		maxID("stream", s.Model.ID)
		for i := range st.Slots {
			v := &st.Slots[i]
			if v.StartAt >= s.Model.StartAt && v.EndAt <= s.Model.EndAt {
				v.Slot--
			}
		}
		indexStream(s)
	case "comment":
		c := *e.Comment
		st.Comments[c.ID] = c
		maxID("comment", c.ID)
		commentIDs[c.LivestreamID] = append(commentIDs[c.LivestreamID], c.ID)
		indexComment(c, 1)
	case "reaction":
		r := *e.Reaction
		st.Reactions[r.ID] = r
		maxID("reaction", r.ID)
		reactionIDs[r.LivestreamID] = append(reactionIDs[r.LivestreamID], r.ID)
		indexReaction(r)
	case "report":
		r := *e.Report
		st.Reports[r.ID] = r
		maxID("report", r.ID)
		reportIDs[r.LivestreamID] = append(reportIDs[r.LivestreamID], r.ID)
		agg(streamStats, r.LivestreamID).Reports++
	case "word":
		w := *e.Word
		st.Words[w.ID] = w
		maxID("word", w.ID)
		wordIDs[w.LivestreamID] = append(wordIDs[w.LivestreamID], w.ID)
		for _, id := range e.Delete {
			if c, ok := st.Comments[id]; ok {
				indexComment(c, -1)
				delete(st.Comments, id)
			}
		}
		a := agg(streamStats, w.LivestreamID)
		a.MaxTip = 0
		for _, id := range commentIDs[w.LivestreamID] {
			c := st.Comments[id]
			if c.Tip > a.MaxTip {
				a.MaxTip = c.Tip
			}
		}
	case "enter", "exit":
		if st.Viewers[e.StreamID] == nil {
			st.Viewers[e.StreamID] = map[int64][]int64{}
		}
		delta := int64(1)
		if e.Kind == "enter" {
			st.Viewers[e.StreamID][e.UserID] = append(st.Viewers[e.StreamID][e.UserID], e.Time)
		} else {
			delta = -int64(len(st.Viewers[e.StreamID][e.UserID]))
			delete(st.Viewers[e.StreamID], e.UserID)
		}
		agg(streamStats, e.StreamID).Viewers += delta
		if s := st.Streams[e.StreamID]; s != nil {
			agg(userStats, s.Model.UserID).Viewers += delta
		}
	}
}
func record(e Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	walMu.RLock()
	_, err = wal.Write(b)
	if err == nil {
		walSeq.Add(1)
	}
	walMu.RUnlock()
	if err != nil {
		return err
	}
	apply(e)
	return nil
}
func syncWAL() error {
	target := walSeq.Load()
	if durableSeq.Load() >= target {
		return nil
	}
	syncMu.Lock()
	defer syncMu.Unlock()
	if durableSeq.Load() >= target {
		return nil
	}
	walMu.RLock()
	defer walMu.RUnlock()
	upto := walSeq.Load()
	if err := wal.Sync(); err != nil {
		return err
	}
	durableSeq.Store(upto)
	return nil
}

func snapshot() error {
	f, e := os.Create("state.gob.tmp")
	if e != nil {
		return e
	}
	if e = gob.NewEncoder(f).Encode(st); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	f.Close()
	return os.Rename("state.gob.tmp", "state.gob")
}
func recoverState() error {
	f, e := os.Open("state.gob")
	if os.IsNotExist(e) {
		if e = loadSQL(); e != nil {
			return e
		}
		if e = snapshot(); e != nil {
			return e
		}
	} else if e != nil {
		return e
	} else {
		e = gob.NewDecoder(f).Decode(&st)
		f.Close()
		if e != nil {
			return e
		}
		reindex()
	}
	f, e = os.Open("state.wal")
	if e == nil {
		r := bufio.NewReader(f)
		var validBytes int64
		for {
			line, err := r.ReadBytes('\n')
			if err == io.EOF {
				if len(line) > 0 {
					if e := os.Truncate("state.wal", validBytes); e != nil {
						return e
					}
				}
				break
			}
			if err != nil {
				return err
			}
			var ev Event
			if err = json.Unmarshal(line, &ev); err != nil {
				return err
			}
			apply(ev)
			validBytes += int64(len(line))
		}
		f.Close()
	} else if !os.IsNotExist(e) {
		return e
	}
	wal, e = os.OpenFile("state.wal", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	return e
}
