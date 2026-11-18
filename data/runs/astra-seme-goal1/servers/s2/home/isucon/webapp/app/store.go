package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
)

type StoredUser struct {
	Model UserModel
	Theme Theme
	Image []byte
	Hash  string
}
type StreamData struct {
	Moderated                                 bool
	Model                                     LivestreamModel
	Tags                                      []int64
	Comments                                  []int64
	Reactions                                 []int64
	Reports                                   []int64
	NG                                        []NGWord
	Viewers                                   map[int64]int64
	Tips, ReactionCount, CommentCount, MaxTip int64
	Emoji                                     map[string]int64
}
type State struct {
	Archived                                                                                 map[int64]LivecommentModel
	Users                                                                                    map[int64]*StoredUser
	Names                                                                                    map[string]int64
	Streams                                                                                  map[int64]*StreamData
	StreamIDs                                                                                []int64
	ByUser                                                                                   map[int64][]int64
	ByTag                                                                                    map[int64][]int64
	Tags                                                                                     map[int64]Tag
	TagIDs                                                                                   []int64
	Comments                                                                                 map[int64]LivecommentModel
	Reactions                                                                                map[int64]ReactionModel
	Reports                                                                                  map[int64]LivecommentReportModel
	Slots                                                                                    []ReservationSlotModel
	NextUser, NextTheme, NextStream, NextComment, NextReaction, NextReport, NextNG, NextIcon int64
	TotalTip                                                                                 int64
}
type Event struct {
	Moderation   bool `json:",omitempty"`
	Kind         string
	User         *StoredUser             `json:",omitempty"`
	Stream       *StreamData             `json:",omitempty"`
	Comment      *LivecommentModel       `json:",omitempty"`
	Reaction     *ReactionModel          `json:",omitempty"`
	Report       *LivecommentReportModel `json:",omitempty"`
	NG           *NGWord                 `json:",omitempty"`
	Deleted      []int64                 `json:",omitempty"`
	UID, SID, ID int64                   `json:",omitempty"`
	Image        []byte                  `json:",omitempty"`
}

var mu sync.RWMutex
var state *State
var wal *os.File
var dataDir = "../data"
var defaultImage []byte
var defaultHash string

func newState() *State {
	return &State{Users: map[int64]*StoredUser{}, Names: map[string]int64{}, Streams: map[int64]*StreamData{}, ByUser: map[int64][]int64{}, ByTag: map[int64][]int64{}, Tags: map[int64]Tag{}, Comments: map[int64]LivecommentModel{}, Reactions: map[int64]ReactionModel{}, Reports: map[int64]LivecommentReportModel{}}
}
func hashImage(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
func (s *State) apply(e Event) {
	switch e.Kind {
	case "user":
		u := e.User
		s.Users[u.Model.ID] = u
		s.Names[u.Model.Name] = u.Model.ID
		s.NextUser = max(s.NextUser, u.Model.ID)
		s.NextTheme = max(s.NextTheme, u.Theme.ID)
	case "icon":
		u := s.Users[e.UID]
		u.Image = e.Image
		u.Hash = hashImage(e.Image)
		s.NextIcon = max(s.NextIcon, e.ID)
	case "stream":
		d := e.Stream
		id := d.Model.ID
		if d.Viewers == nil {
			d.Viewers = map[int64]int64{}
		}
		if d.Emoji == nil {
			d.Emoji = map[string]int64{}
		}
		s.Streams[id] = d
		s.StreamIDs = append(s.StreamIDs, id)
		s.ByUser[d.Model.UserID] = append(s.ByUser[d.Model.UserID], id)
		for _, t := range d.Tags {
			s.ByTag[t] = append(s.ByTag[t], id)
		}
		s.NextStream = max(s.NextStream, id)
		for i := range s.Slots {
			v := &s.Slots[i]
			if v.StartAt >= d.Model.StartAt && v.EndAt <= d.Model.EndAt {
				v.Slot--
			}
		}
	case "comment":
		c := *e.Comment
		s.Comments[c.ID] = c
		d := s.Streams[c.LivestreamID]
		d.Comments = append(d.Comments, c.ID)
		d.Tips += c.Tip
		d.CommentCount++
		d.MaxTip = max(d.MaxTip, c.Tip)
		s.TotalTip += c.Tip
		s.NextComment = max(s.NextComment, c.ID)
	case "reaction":
		r := *e.Reaction
		s.Reactions[r.ID] = r
		d := s.Streams[r.LivestreamID]
		d.Reactions = append(d.Reactions, r.ID)
		d.ReactionCount++
		d.Emoji[r.EmojiName]++
		s.NextReaction = max(s.NextReaction, r.ID)
	case "report":
		r := *e.Report
		s.Reports[r.ID] = r
		s.Streams[r.LivestreamID].Reports = append(s.Streams[r.LivestreamID].Reports, r.ID)
		s.NextReport = max(s.NextReport, r.ID)
	case "ng":
		n := *e.NG
		d := s.Streams[n.LivestreamID]
		d.NG = append(d.NG, n)
		if e.Moderation {
			d.Moderated = true
		}
		s.NextNG = max(s.NextNG, n.ID)
		for _, id := range e.Deleted {
			if c, ok := s.Comments[id]; ok {
				d.Tips -= c.Tip
				d.CommentCount--
				s.TotalTip -= c.Tip
				if s.Archived == nil {
					s.Archived = map[int64]LivecommentModel{}
				}
				s.Archived[id] = c
				delete(s.Comments, id)
			}
		}
		d.MaxTip = 0
		for _, id := range d.Comments {
			if c, ok := s.Comments[id]; ok {
				d.MaxTip = max(d.MaxTip, c.Tip)
			}
		}
	case "enter":
		s.Streams[e.SID].Viewers[e.UID]++
	case "exit":
		delete(s.Streams[e.SID].Viewers, e.UID)
	}
}
func commit(e Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err = wal.Write(b); err != nil {
		return err
	}
	state.apply(e)
	return nil
}
func loadStore() error {
	var err error
	defaultImage, err = os.ReadFile("../img/NoImage.jpg")
	if err != nil {
		return err
	}
	defaultHash = hashImage(defaultImage)
	if err = os.MkdirAll(dataDir, 0755); err != nil {
		return err
	}
	f, err := os.Open(filepath.Join(dataDir, "snapshot.gob"))
	if err != nil {
		return err
	}
	state = newState()
	err = gob.NewDecoder(f).Decode(state)
	f.Close()
	if err != nil {
		return err
	}
	wal, err = os.OpenFile(filepath.Join(dataDir, "wal.jsonl"), os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	r := bufio.NewReader(wal)
	var offset int64
	for {
		b, er := r.ReadBytes('\n')
		if er == io.EOF {
			if len(b) > 0 {
				if err = wal.Truncate(offset); err != nil {
					return err
				}
			}
			break
		}
		if er != nil {
			return er
		}
		var e Event
		if err = json.Unmarshal(b, &e); err != nil {
			return err
		}
		state.apply(e)
		offset += int64(len(b))
	}
	_, err = wal.Seek(0, io.SeekEnd)
	if err == nil {
		durability = newDurableWAL(wal)
	}
	return err
}
func saveSnapshot(s *State) error {
	p := filepath.Join(dataDir, "snapshot.gob")
	f, err := os.Create(p + ".tmp")
	if err != nil {
		return err
	}
	if err = gob.NewEncoder(f).Encode(s); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(p+".tmp", p); err != nil {
		return err
	}
	d, err := os.Open(dataDir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func importDB() (*State, error) {
	db, err := sqlx.Connect("mysql", "isucon:isucon@tcp(127.0.0.1:3306)/isupipe?parseTime=true")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	s := newState()
	var users []UserModel
	if err = db.Select(&users, "SELECT * FROM users ORDER BY id"); err != nil {
		return nil, err
	}
	var themes []ThemeModel
	if err = db.Select(&themes, "SELECT * FROM themes"); err != nil {
		return nil, err
	}
	tm := map[int64]Theme{}
	for _, t := range themes {
		tm[t.UserID] = Theme{t.ID, t.DarkMode}
	}
	for _, u := range users {
		s.apply(Event{Kind: "user", User: &StoredUser{Model: u, Theme: tm[u.ID], Hash: defaultHash}})
	}
	var tags []TagModel
	if err = db.Select(&tags, "SELECT * FROM tags ORDER BY id"); err != nil {
		return nil, err
	}
	for _, t := range tags {
		s.Tags[t.ID] = Tag{t.ID, t.Name}
		s.TagIDs = append(s.TagIDs, t.ID)
	}
	var ls []LivestreamModel
	if err = db.Select(&ls, "SELECT * FROM livestreams ORDER BY id"); err != nil {
		return nil, err
	}
	for _, l := range ls {
		s.apply(Event{Kind: "stream", Stream: &StreamData{Model: l, Tags: []int64{}}})
	}
	var lt []LivestreamTagModel
	if err = db.Select(&lt, "SELECT * FROM livestream_tags ORDER BY id"); err != nil {
		return nil, err
	}
	for _, t := range lt {
		s.Streams[t.LivestreamID].Tags = append(s.Streams[t.LivestreamID].Tags, t.TagID)
		s.ByTag[t.TagID] = append(s.ByTag[t.TagID], t.LivestreamID)
	}
	for t := range s.ByTag {
		sort.Slice(s.ByTag[t], func(i, j int) bool { return s.ByTag[t][i] < s.ByTag[t][j] })
	}
	if err = db.Select(&s.Slots, "SELECT * FROM reservation_slots ORDER BY id"); err != nil {
		return nil, err
	}
	var cs []LivecommentModel
	if err = db.Select(&cs, "SELECT * FROM livecomments ORDER BY created_at,id"); err != nil {
		return nil, err
	}
	for _, c := range cs {
		c := c
		s.apply(Event{Kind: "comment", Comment: &c})
	}
	var rs []ReactionModel
	if err = db.Select(&rs, "SELECT * FROM reactions ORDER BY created_at,id"); err != nil {
		return nil, err
	}
	for _, r := range rs {
		r := r
		s.apply(Event{Kind: "reaction", Reaction: &r})
	}
	var ns []NGWord
	if err = db.Select(&ns, "SELECT * FROM ng_words ORDER BY created_at,id"); err != nil {
		return nil, err
	}
	for _, n := range ns {
		n := n
		s.apply(Event{Kind: "ng", NG: &n})
	}
	return s, nil
}
func (s *State) user(id int64) User {
	u := s.Users[id]
	if u == nil {
		return User{}
	}
	return User{ID: id, Name: u.Model.Name, DisplayName: u.Model.DisplayName, Description: u.Model.Description, Theme: u.Theme, IconHash: u.Hash}
}
func (s *State) stream(id int64) Livestream {
	d := s.Streams[id]
	if d == nil {
		return Livestream{Tags: []Tag{}}
	}
	l := d.Model
	t := make([]Tag, 0, len(d.Tags))
	for _, id := range d.Tags {
		t = append(t, s.Tags[id])
	}
	return Livestream{ID: l.ID, Owner: s.user(l.UserID), Title: l.Title, Description: l.Description, PlaylistUrl: l.PlaylistUrl, ThumbnailUrl: l.ThumbnailUrl, Tags: t, StartAt: l.StartAt, EndAt: l.EndAt}
}
func (s *State) comment(id int64) Livecomment {
	c := s.Comments[id]
	return Livecomment{ID: c.ID, User: s.user(c.UserID), Livestream: s.stream(c.LivestreamID), Comment: c.Comment, Tip: c.Tip, CreatedAt: c.CreatedAt}
}
func (s *State) reaction(id int64) Reaction {
	r := s.Reactions[id]
	return Reaction{ID: r.ID, EmojiName: r.EmojiName, User: s.user(r.UserID), Livestream: s.stream(r.LivestreamID), CreatedAt: r.CreatedAt}
}
func (s *State) report(id int64) LivecommentReport {
	r := s.Reports[id]
	return LivecommentReport{ID: r.ID, Reporter: s.user(r.UserID), Livecomment: s.reportComment(r.LivecommentID), CreatedAt: r.CreatedAt}
}

func (s *State) reportComment(id int64) Livecomment {
	if _, ok := s.Comments[id]; ok {
		return s.comment(id)
	}
	c := s.Archived[id]
	return Livecomment{ID: c.ID, User: s.user(c.UserID), Livestream: s.stream(c.LivestreamID), Comment: c.Comment, Tip: c.Tip, CreatedAt: c.CreatedAt}
}
