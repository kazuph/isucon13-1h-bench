package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"github.com/jmoiron/sqlx"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
)

type StreamStat struct {
	Reactions, Tips, Comments, Viewers, Reports, MaxTip int64
	Emoji                                               map[string]int64
}
type State struct {
	indexReady     bool
	AllStreams     []int64
	OwnerStreams   map[int64][]int64
	TaggedStreams  map[string][]int64
	Users          map[int64]UserModel
	Names          map[string]int64
	Themes         map[int64]Theme
	Icons          map[int64][]byte
	Hashes         map[int64]string
	Streams        map[int64]LivestreamModel
	StreamTags     map[int64][]Tag
	Tags           []Tag
	Comments       map[int64]LivecommentModel
	CommentIDs     map[int64][]int64
	Reactions      map[int64]ReactionModel
	ReactionIDs    map[int64][]int64
	Reports        map[int64][]LivecommentReportModel
	ReportComments map[int64]LivecommentModel
	Words          map[int64][]NGWord
	Viewers        map[int64]map[int64]int64
	Slots          []ReservationSlotModel
	Stats          map[int64]*StreamStat
	Next           map[string]int64
	DNS            map[string]bool
}

var mu sync.RWMutex
var st *State
var journal *os.File
var fallback []byte
var fallbackHash string

const dataDir = "../state"

type Event struct {
	Kind                 string
	User                 *UserModel
	Theme                Theme
	Stream               *LivestreamModel
	Tags                 []int64
	Comment              *LivecommentModel
	Reaction             *ReactionModel
	Report               *LivecommentReportModel
	Word                 *NGWord
	UserID, StreamID, ID int64
	Image                []byte
}

func fresh() *State {
	return &State{
		Users: map[int64]UserModel{}, Names: map[string]int64{}, Themes: map[int64]Theme{}, Icons: map[int64][]byte{}, Hashes: map[int64]string{}, Streams: map[int64]LivestreamModel{}, StreamTags: map[int64][]Tag{}, Tags: []Tag{}, Comments: map[int64]LivecommentModel{}, CommentIDs: map[int64][]int64{}, Reactions: map[int64]ReactionModel{}, ReactionIDs: map[int64][]int64{}, Reports: map[int64][]LivecommentReportModel{}, ReportComments: map[int64]LivecommentModel{}, Words: map[int64][]NGWord{}, Viewers: map[int64]map[int64]int64{}, Stats: map[int64]*StreamStat{}, Next: map[string]int64{}, DNS: map[string]bool{},
	}
}
func hashImage(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
func (s *State) stat(id int64) *StreamStat {
	v := s.Stats[id]
	if v == nil {
		v = &StreamStat{Emoji: map[string]int64{}}
		s.Stats[id] = v
	}
	return v
}
func (s *State) next(k string) int64 { return s.Next[k] + 1 }
func (s *State) high(k string, id int64) {
	if id > s.Next[k] {
		s.Next[k] = id
	}
}
func like(text, word string) bool {
	if !strings.ContainsAny(word, "%_\\") {
		return strings.Contains(text, word)
	}
	var b strings.Builder
	b.WriteString("(?s)")
	escape := false
	for _, r := range word {
		if escape {
			b.WriteString(regexp.QuoteMeta(string(r)))
			escape = false
			continue
		}
		switch r {
		case '\\':
			escape = true
		case '%':
			b.WriteString(".*")
		case '_':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	if escape {
		b.WriteString(`\\`)
	}
	ok, _ := regexp.MatchString(b.String(), text)
	return ok
}
func (s *State) apply(e Event) {
	switch e.Kind {
	case "user":
		u := *e.User
		s.Users[u.ID] = u
		s.Names[u.Name] = u.ID
		s.Themes[u.ID] = e.Theme
		s.Hashes[u.ID] = fallbackHash
		s.DNS[strings.ToLower(u.Name)+".u.isucon.dev."] = true
		s.high("user", u.ID)
		s.high("theme", e.Theme.ID)
	case "icon":
		s.Icons[e.UserID] = e.Image
		s.Hashes[e.UserID] = hashImage(e.Image)
		s.high("icon", e.ID)
	case "stream":
		v := *e.Stream
		s.Streams[v.ID] = v
		s.StreamTags[v.ID] = []Tag{}
		for _, id := range e.Tags {
			for _, t := range s.Tags {
				if t.ID == id {
					s.StreamTags[v.ID] = append(s.StreamTags[v.ID], t)
					break
				}
			}
		}
		for i := range s.Slots {
			v2 := &s.Slots[i]
			if v2.StartAt >= v.StartAt && v2.EndAt <= v.EndAt {
				v2.Slot--
			}
		}
		s.high("stream", v.ID)
		if s.indexReady {
			s.AllStreams = append(s.AllStreams, v.ID)
			s.OwnerStreams[v.UserID] = append(s.OwnerStreams[v.UserID], v.ID)
			for _, tag := range s.StreamTags[v.ID] {
				s.TaggedStreams[tag.Name] = append(s.TaggedStreams[tag.Name], v.ID)
			}
		}
		s.stat(v.ID)
	case "comment":
		v := *e.Comment
		s.Comments[v.ID] = v
		s.CommentIDs[v.LivestreamID] = append(s.CommentIDs[v.LivestreamID], v.ID)
		z := s.stat(v.LivestreamID)
		z.Comments++
		z.Tips += v.Tip
		if v.Tip > z.MaxTip {
			z.MaxTip = v.Tip
		}
		s.high("comment", v.ID)
	case "reaction":
		v := *e.Reaction
		s.Reactions[v.ID] = v
		s.ReactionIDs[v.LivestreamID] = append(s.ReactionIDs[v.LivestreamID], v.ID)
		z := s.stat(v.LivestreamID)
		z.Reactions++
		z.Emoji[v.EmojiName]++
		s.high("reaction", v.ID)
	case "report":
		v := *e.Report
		s.Reports[v.LivestreamID] = append(s.Reports[v.LivestreamID], v)
		s.ReportComments[v.LivecommentID] = s.Comments[v.LivecommentID]
		s.stat(v.LivestreamID).Reports++
		s.high("report", v.ID)
	case "word":
		v := *e.Word
		s.Words[v.LivestreamID] = append(s.Words[v.LivestreamID], v)
		s.high("word", v.ID)
		ids := s.CommentIDs[v.LivestreamID]
		kept := make([]int64, 0, len(ids))
		z := s.stat(v.LivestreamID)
		z.MaxTip = 0
		for _, id := range ids {
			c := s.Comments[id]
			hit := false
			for _, w := range s.Words[v.LivestreamID] {
				if like(c.Comment, w.Word) {
					hit = true
					break
				}
			}
			if hit {
				delete(s.Comments, id)
				z.Comments--
				z.Tips -= c.Tip
			} else {
				kept = append(kept, id)
				if c.Tip > z.MaxTip {
					z.MaxTip = c.Tip
				}
			}
		}
		s.CommentIDs[v.LivestreamID] = kept
	case "enter":
		if s.Viewers[e.StreamID] == nil {
			s.Viewers[e.StreamID] = map[int64]int64{}
		}
		s.Viewers[e.StreamID][e.UserID]++
		s.stat(e.StreamID).Viewers++
	case "exit":
		n := s.Viewers[e.StreamID][e.UserID]
		delete(s.Viewers[e.StreamID], e.UserID)
		s.stat(e.StreamID).Viewers -= n
	}
}
func persist(e Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err = journal.Write(b); err != nil {
		return err
	}
	if err = journal.Sync(); err != nil {
		return err
	}
	st.apply(e)
	return nil
}
func saveSnapshot(s *State) error {
	f, err := os.Create(dataDir + "/snapshot.tmp")
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
	f.Close()
	if err = os.Rename(dataDir+"/snapshot.tmp", dataDir+"/snapshot.gob"); err != nil {
		return err
	}
	return nil
}
func loadState() error {
	f, err := os.Open(dataDir + "/snapshot.gob")
	if err != nil {
		return err
	}
	s := fresh()
	err = gob.NewDecoder(f).Decode(s)
	f.Close()
	if err != nil {
		return err
	}
	f, err = os.OpenFile(dataDir+"/journal.jsonl", os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	s.buildIndexes()
	r := bufio.NewReader(f)
	var offset int64
	for {
		b, e := r.ReadBytes('\n')
		if e == io.EOF {
			if len(b) > 0 {
				if err = f.Truncate(offset); err != nil {
					f.Close()
					return err
				}
			}
			break
		}
		if e != nil {
			f.Close()
			return e
		}
		var ev Event
		if err = json.Unmarshal(b, &ev); err != nil {
			f.Close()
			return err
		}
		s.apply(ev)
		offset += int64(len(b))
	}
	f.Seek(0, io.SeekEnd)
	st = s
	journal = f
	return nil
}
func loadDB(db *sqlx.DB) (*State, error) {
	s := fresh()
	var users []UserModel
	var themes []ThemeModel
	var streams []LivestreamModel
	var tags []TagModel
	var links []LivestreamTagModel
	var comments []LivecommentModel
	var reactions []ReactionModel
	var words []NGWord
	var reports []LivecommentReportModel
	var icons []struct {
		ID     int64  `db:"id"`
		UserID int64  `db:"user_id"`
		Image  []byte `db:"image"`
	}
	var viewers []struct {
		ID           int64 `db:"id"`
		UserID       int64 `db:"user_id"`
		LivestreamID int64 `db:"livestream_id"`
		CreatedAt    int64 `db:"created_at"`
	}
	for _, q := range []struct {
		dst   interface{}
		table string
	}{{&users, "users"}, {&themes, "themes"}, {&streams, "livestreams"}, {&tags, "tags"}, {&links, "livestream_tags"}, {&comments, "livecomments"}, {&reactions, "reactions"}, {&words, "ng_words"}, {&reports, "livecomment_reports"}, {&icons, "icons"}, {&viewers, "livestream_viewers_history"}, {&s.Slots, "reservation_slots"}} {
		if err := db.Select(q.dst, "SELECT * FROM "+q.table+" ORDER BY id"); err != nil {
			return nil, err
		}
	}
	for _, u := range users {
		s.Users[u.ID] = u
		s.Names[u.Name] = u.ID
		s.Hashes[u.ID] = fallbackHash
		s.high("user", u.ID)
		s.DNS[strings.ToLower(u.Name)+".u.isucon.dev."] = true
	}
	for _, t := range themes {
		s.Themes[t.UserID] = Theme{ID: t.ID, DarkMode: t.DarkMode}
		s.high("theme", t.ID)
	}
	for _, t := range tags {
		s.Tags = append(s.Tags, Tag{ID: t.ID, Name: t.Name})
	}
	for _, v := range streams {
		s.Streams[v.ID] = v
		s.StreamTags[v.ID] = []Tag{}
		s.stat(v.ID)
		s.high("stream", v.ID)
	}
	for _, l := range links {
		for _, t := range s.Tags {
			if t.ID == l.TagID {
				s.StreamTags[l.LivestreamID] = append(s.StreamTags[l.LivestreamID], t)
			}
		}
	}
	for _, v := range comments {
		s.apply(Event{Kind: "comment", Comment: &v})
	}
	for _, v := range reactions {
		s.apply(Event{Kind: "reaction", Reaction: &v})
	}
	for _, v := range reports {
		s.apply(Event{Kind: "report", Report: &v})
	}
	for _, v := range icons {
		s.apply(Event{Kind: "icon", UserID: v.UserID, ID: v.ID, Image: v.Image})
	}
	for _, v := range words {
		s.Words[v.LivestreamID] = append(s.Words[v.LivestreamID], v)
		s.high("word", v.ID)
	}
	for _, v := range viewers {
		s.apply(Event{Kind: "enter", StreamID: v.LivestreamID, UserID: v.UserID})
	}
	b, err := os.ReadFile("../pdns/u.isucon.dev.zone")
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		v := strings.Fields(line)
		if len(v) >= 5 && v[2] == "IN" && v[3] == "A" {
			name := strings.ToLower(v[0])
			if name == "@" {
				name = "u.isucon.dev."
			} else if !strings.HasSuffix(name, ".") {
				name += ".u.isucon.dev."
			}
			s.DNS[name] = true
		}
	}
	s.buildIndexes()
	return s, nil
}
func (s *State) user(id int64) User {
	u := s.Users[id]
	return User{ID: u.ID, Name: u.Name, DisplayName: u.DisplayName, Description: u.Description, Theme: s.Themes[id], IconHash: s.Hashes[id]}
}
func (s *State) stream(id int64) Livestream {
	v := s.Streams[id]
	return Livestream{ID: v.ID, Owner: s.user(v.UserID), Title: v.Title, Description: v.Description, PlaylistUrl: v.PlaylistUrl, ThumbnailUrl: v.ThumbnailUrl, Tags: s.StreamTags[id], StartAt: v.StartAt, EndAt: v.EndAt}
}
func (s *State) comment(v LivecommentModel) Livecomment {
	return Livecomment{ID: v.ID, User: s.user(v.UserID), Livestream: s.stream(v.LivestreamID), Comment: v.Comment, Tip: v.Tip, CreatedAt: v.CreatedAt}
}
func (s *State) reaction(v ReactionModel) Reaction {
	return Reaction{ID: v.ID, User: s.user(v.UserID), Livestream: s.stream(v.LivestreamID), EmojiName: v.EmojiName, CreatedAt: v.CreatedAt}
}
func (s *State) report(v LivecommentReportModel) LivecommentReport {
	c, ok := s.Comments[v.LivecommentID]
	if !ok {
		c = s.ReportComments[v.LivecommentID]
	}
	return LivecommentReport{ID: v.ID, Reporter: s.user(v.UserID), Livecomment: s.comment(c), CreatedAt: v.CreatedAt}
}
func descIDs(m map[int64]LivestreamModel) []int64 {
	ids := make([]int64, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	return ids
}

func (s *State) buildIndexes() {
	s.AllStreams = make([]int64, 0, len(s.Streams))
	s.OwnerStreams = map[int64][]int64{}
	s.TaggedStreams = map[string][]int64{}
	for id := range s.Streams {
		s.AllStreams = append(s.AllStreams, id)
	}
	sort.Slice(s.AllStreams, func(i, j int) bool { return s.AllStreams[i] < s.AllStreams[j] })
	for _, id := range s.AllStreams {
		v := s.Streams[id]
		s.OwnerStreams[v.UserID] = append(s.OwnerStreams[v.UserID], id)
		for _, tag := range s.StreamTags[id] {
			s.TaggedStreams[tag.Name] = append(s.TaggedStreams[tag.Name], id)
		}
	}
	s.indexReady = true
}
