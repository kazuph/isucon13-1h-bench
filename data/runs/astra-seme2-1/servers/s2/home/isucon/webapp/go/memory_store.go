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
	"strings"
	"sync"
	"time"
)

// All mutations are serialized and durably logged before being published.
// SQL is used only to import the competition's initial dataset.
type memUser struct {
	Model    UserModel
	Response User
	Image    []byte
	HasIcon  bool
}
type memStream struct {
	Model                                                  LivestreamModel
	Tags                                                   []Tag
	Comments                                               []int64
	Reactions                                              []int64
	Reports                                                []int64
	Words                                                  []NGWord
	Viewers                                                map[int64]int64
	Tips, ReactionCount, CommentCount, ViewerCount, MaxTip int64
	Emoji                                                  map[string]int64
}
type memoryStore struct {
	Users       map[int64]*memUser
	Names       map[string]int64
	DNSNames    map[string]bool
	Streams     map[int64]*memStream
	Comments    map[int64]LivecommentModel
	Reactions   map[int64]ReactionModel
	Reports     map[int64]LivecommentReportModel
	Tags        []Tag
	Slots       []ReservationSlotModel
	Next        map[string]int64
	TotalTip    int64
	StreamIDs   []int64
	UserStreams map[int64][]int64
	TagStreams  map[string][]int64
}
type memEvent struct {
	Kind     string
	User     *memUser                `json:",omitempty"`
	Stream   *LivestreamModel        `json:",omitempty"`
	Tags     []int64                 `json:",omitempty"`
	Comment  *LivecommentModel       `json:",omitempty"`
	Reaction *ReactionModel          `json:",omitempty"`
	Report   *LivecommentReportModel `json:",omitempty"`
	Word     *NGWord                 `json:",omitempty"`
	Viewer   *LivestreamViewerModel  `json:",omitempty"`
	UserID   int64                   `json:",omitempty"`
	ID       int64                   `json:",omitempty"`
	Image    []byte                  `json:",omitempty"`
}

var memMu sync.RWMutex
var mem *memoryStore
var journal *os.File
var defaultImage []byte
var defaultHash string

const dataDir = "../state"

func freshStore() *memoryStore {
	return &memoryStore{
		Users: map[int64]*memUser{}, Names: map[string]int64{}, DNSNames: map[string]bool{}, Streams: map[int64]*memStream{},
		Comments: map[int64]LivecommentModel{}, Reactions: map[int64]ReactionModel{}, Reports: map[int64]LivecommentReportModel{}, Next: map[string]int64{},
		Tags: []Tag{}, Slots: []ReservationSlotModel{},
	}
}
func (s *memoryStore) bump(kind string, id int64) {
	if s.Next[kind] < id {
		s.Next[kind] = id
	}
}
func (s *memoryStore) next(kind string) int64 { return s.Next[kind] + 1 }
func newMemStream(m LivestreamModel) *memStream {
	return &memStream{Model: m, Tags: []Tag{}, Viewers: map[int64]int64{}, Emoji: map[string]int64{}}
}
func (s *memoryStore) apply(e memEvent) {
	switch e.Kind {
	case "user":
		if s.DNSNames == nil {
			s.DNSNames = map[string]bool{}
		}
		s.DNSNames[strings.ToLower(e.User.Model.Name)] = true
		s.Users[e.User.Model.ID] = e.User
		s.Names[e.User.Model.Name] = e.User.Model.ID
		s.bump("user", e.User.Model.ID)
		s.bump("theme", e.User.Response.Theme.ID)
	case "icon":
		u := s.Users[e.UserID]
		u.Image = e.Image
		u.HasIcon = true
		u.Response.IconHash = fmt.Sprintf("%x", sha256.Sum256(e.Image))
		s.bump("icon", e.ID)
	case "stream":
		l := newMemStream(*e.Stream)
		for _, id := range e.Tags {
			for _, t := range s.Tags {
				if t.ID == id {
					l.Tags = append(l.Tags, t)
					break
				}
			}
		}
		s.Streams[l.Model.ID] = l
		s.bump("stream", l.Model.ID)
		s.StreamIDs = append(s.StreamIDs, l.Model.ID)
		if s.UserStreams == nil {
			s.UserStreams = map[int64][]int64{}
		}
		if s.TagStreams == nil {
			s.TagStreams = map[string][]int64{}
		}
		s.UserStreams[l.Model.UserID] = append(s.UserStreams[l.Model.UserID], l.Model.ID)
		for _, t := range l.Tags {
			s.TagStreams[t.Name] = append(s.TagStreams[t.Name], l.Model.ID)
		}
		for i := range s.Slots {
			v := &s.Slots[i]
			if v.StartAt >= l.Model.StartAt && v.EndAt <= l.Model.EndAt {
				v.Slot--
			}
		}
	case "comment":
		c := *e.Comment
		s.Comments[c.ID] = c
		s.bump("comment", c.ID)
		if l := s.Streams[c.LivestreamID]; l != nil {
			l.Comments = append(l.Comments, c.ID)
			l.Tips += c.Tip
			l.CommentCount++
			if l.CommentCount == 1 || c.Tip > l.MaxTip {
				l.MaxTip = c.Tip
			}
		}
		s.TotalTip += c.Tip
	case "reaction":
		r := *e.Reaction
		s.Reactions[r.ID] = r
		s.bump("reaction", r.ID)
		if l := s.Streams[r.LivestreamID]; l != nil {
			l.Reactions = append(l.Reactions, r.ID)
			l.ReactionCount++
			l.Emoji[r.EmojiName]++
		}
	case "report":
		r := *e.Report
		s.Reports[r.ID] = r
		s.bump("report", r.ID)
		s.Streams[r.LivestreamID].Reports = append(s.Streams[r.LivestreamID].Reports, r.ID)
	case "word":
		w := *e.Word
		l := s.Streams[w.LivestreamID]
		l.Words = append(l.Words, w)
		s.bump("word", w.ID)
		ids := make([]int64, 0, len(l.Comments))
		l.MaxTip = 0
		for _, id := range l.Comments {
			c := s.Comments[id]
			hit := false
			for _, word := range l.Words {
				if likeContains(c.Comment, word.Word) {
					hit = true
					break
				}
			}
			if hit {
				delete(s.Comments, id)
				l.Tips -= c.Tip
				s.TotalTip -= c.Tip
				l.CommentCount--
			} else {
				ids = append(ids, id)
				if len(ids) == 1 || c.Tip > l.MaxTip {
					l.MaxTip = c.Tip
				}
			}
		}
		l.Comments = ids
	case "enter":
		v := e.Viewer
		if l := s.Streams[v.LivestreamID]; l != nil {
			l.Viewers[v.UserID]++
			l.ViewerCount++
		}
	case "exit":
		v := e.Viewer
		if l := s.Streams[v.LivestreamID]; l != nil {
			l.ViewerCount -= l.Viewers[v.UserID]
			delete(l.Viewers, v.UserID)
		}
	}
}

// MySQL LIKE, including its wildcard and escape semantics, on UTF-8 characters.
func likeContains(text, word string) bool {
	if !strings.ContainsAny(word, "%_\\") {
		return strings.Contains(sqlFold(text), sqlFold(word))
	}
	type token struct {
		c    rune
		kind byte
	}
	ts := []token{{kind: '%'}}
	escaped := false
	for _, r := range word {
		if escaped {
			ts = append(ts, token{c: sqlWeight(r)})
			escaped = false
		} else if r == '\\' {
			escaped = true
		} else if r == '%' || r == '_' {
			ts = append(ts, token{kind: byte(r)})
		} else {
			ts = append(ts, token{c: sqlWeight(r)})
		}
	}
	if escaped {
		ts = append(ts, token{c: '%'})
	} else {
		ts = append(ts, token{kind: '%'})
	}
	prev := make([]bool, len(ts)+1)
	prev[0] = true
	for j, t := range ts {
		if t.kind == '%' {
			prev[j+1] = prev[j]
		}
	}
	for _, r := range text {
		r = sqlWeight(r)
		cur := make([]bool, len(ts)+1)
		for j, t := range ts {
			if t.kind == '%' {
				cur[j+1] = cur[j] || prev[j+1]
			} else {
				cur[j+1] = prev[j] && (t.kind == '_' || t.c == r)
			}
		}
		prev = cur
	}
	return prev[len(ts)]
}

// Group commit: make all queued mutations durable with one fsync. The
// application mutex is released while waiting, so unrelated reads and writes
// are not serialized behind storage latency. No write response precedes fsync.
type syncRequest struct {
	file *os.File
	done chan error
}

var syncQueue = make(chan syncRequest, 4096)

func init() {
	go func() {
		for first := range syncQueue {
			batch := []syncRequest{first}
		drain:
			for len(batch) < 4096 {
				select {
				case r := <-syncQueue:
					batch = append(batch, r)
				default:
					break drain
				}
			}
			results := map[*os.File]error{}
			for _, r := range batch {
				if _, ok := results[r.file]; !ok {
					results[r.file] = r.file.Sync()
				}
			}
			for _, r := range batch {
				r.done <- results[r.file]
			}
		}
	}()
}
func persistEvent(e memEvent) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err = journal.Write(b); err != nil {
		return err
	}
	state := mem
	mem.apply(e)
	if e.Kind == "user" {
		publishDNSUser(e.User.Model.Name)
	}
	r := syncRequest{file: journal, done: make(chan error, 1)}
	syncQueue <- r
	memMu.Unlock()
	err = <-r.done
	memMu.Lock()
	if mem != state {
		return fmt.Errorf("state reinitialized during mutation")
	}
	return err
}

func installStore(s *memoryStore) error {
	s.reindex()
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return err
	}
	gen := fmt.Sprintf("%d", time.Now().UnixNano())
	base := filepath.Join(dataDir, gen)
	f, err := os.OpenFile(base+".gob", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = gob.NewEncoder(f).Encode(s)
	if err == nil {
		err = f.Sync()
	}
	f.Close()
	if err != nil {
		return err
	}
	w, err := os.OpenFile(base+".wal", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err = w.Sync(); err != nil {
		w.Close()
		return err
	}
	if err = os.WriteFile(filepath.Join(dataDir, "CURRENT.tmp"), []byte(gen), 0600); err != nil {
		return err
	}
	mf, err := os.OpenFile(filepath.Join(dataDir, "CURRENT.tmp"), os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	err = mf.Sync()
	mf.Close()
	if err != nil {
		return err
	}
	if err = os.Rename(filepath.Join(dataDir, "CURRENT.tmp"), filepath.Join(dataDir, "CURRENT")); err != nil {
		return err
	}
	dir, err := os.Open(dataDir)
	if err != nil {
		return err
	}
	err = dir.Sync()
	dir.Close()
	if err != nil {
		return err
	}
	if journal != nil {
		old := journal
		r := syncRequest{file: old, done: make(chan error, 1)}
		syncQueue <- r
		<-r.done
		old.Close()
	}
	journal = w
	mem = s
	publishDNSNames(s)
	return nil
}
func restoreStore() error {
	var err error
	defaultImage, err = os.ReadFile(fallbackImage)
	if err != nil {
		return err
	}
	defaultHash = fmt.Sprintf("%x", sha256.Sum256(defaultImage))
	gen, err := os.ReadFile(filepath.Join(dataDir, "CURRENT"))
	if os.IsNotExist(err) {
		s, err := importStore()
		if err != nil {
			return err
		}
		return installStore(s)
	}
	if err != nil {
		return err
	}
	f, err := os.Open(filepath.Join(dataDir, string(gen)+".gob"))
	if err != nil {
		return err
	}
	s := freshStore()
	err = gob.NewDecoder(f).Decode(s)
	f.Close()
	if err != nil {
		return err
	}
	s.reindex()
	w, err := os.OpenFile(filepath.Join(dataDir, string(gen)+".wal"), os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	r := bufio.NewReader(w)
	var offset int64
	for {
		b, e := r.ReadBytes('\n')
		if e == io.EOF {
			if len(b) > 0 {
				if err = w.Truncate(offset); err != nil {
					return err
				}
			}
			break
		}
		if e != nil {
			return e
		}
		var event memEvent
		if err = json.Unmarshal(b, &event); err != nil {
			return err
		}
		s.apply(event)
		offset += int64(len(b))
	}
	if _, err = w.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	journal = w
	mem = s
	publishDNSNames(s)
	return nil
}
func importStore() (*memoryStore, error) {
	s := freshStore()
	var users []UserModel
	var themes []ThemeModel
	var ls []LivestreamModel
	var lt []LivestreamTagModel
	var cs []LivecommentModel
	var rs []ReactionModel
	var ws []NGWord
	var reports []LivecommentReportModel
	var viewers []struct {
		ID int64 `db:"id"`
		LivestreamViewerModel
	}
	var icons []struct {
		ID     int64  `db:"id"`
		UserID int64  `db:"user_id"`
		Image  []byte `db:"image"`
	}
	for _, q := range []struct {
		dest  interface{}
		table string
	}{
		{&users, "users"}, {&themes, "themes"}, {&s.Tags, "tags"}, {&s.Slots, "reservation_slots"}, {&ls, "livestreams"}, {&lt, "livestream_tags"}, {&cs, "livecomments"}, {&rs, "reactions"}, {&ws, "ng_words"}, {&reports, "livecomment_reports"}, {&viewers, "livestream_viewers_history"}, {&icons, "icons"},
	} {
		if err := dbConn.Select(q.dest, "SELECT * FROM "+q.table+" ORDER BY id"); err != nil {
			return nil, err
		}
	}
	for _, u := range users {
		s.apply(memEvent{Kind: "user", User: &memUser{Model: u, Response: User{ID: u.ID, Name: u.Name, DisplayName: u.DisplayName, Description: u.Description, IconHash: defaultHash}}})
	}
	for _, t := range themes {
		s.Users[t.UserID].Response.Theme = Theme{ID: t.ID, DarkMode: t.DarkMode}
		s.bump("theme", t.ID)
	}
	for _, i := range icons {
		s.apply(memEvent{Kind: "icon", UserID: i.UserID, ID: i.ID, Image: i.Image})
	}
	for _, l := range ls {
		s.Streams[l.ID] = newMemStream(l)
		s.bump("stream", l.ID)
	}
	for _, t := range lt {
		for _, tag := range s.Tags {
			if tag.ID == t.TagID {
				s.Streams[t.LivestreamID].Tags = append(s.Streams[t.LivestreamID].Tags, tag)
				break
			}
		}
	}
	for _, c := range cs {
		c := c
		s.apply(memEvent{Kind: "comment", Comment: &c})
	}
	for _, r := range rs {
		r := r
		s.apply(memEvent{Kind: "reaction", Reaction: &r})
	}
	// Initial NG words do not retroactively remove the supplied initial comments.
	for _, w := range ws {
		s.Streams[w.LivestreamID].Words = append(s.Streams[w.LivestreamID].Words, w)
		s.bump("word", w.ID)
	}
	for _, r := range reports {
		r := r
		s.apply(memEvent{Kind: "report", Report: &r})
	}
	for _, v := range viewers {
		v := v
		s.apply(memEvent{Kind: "enter", Viewer: &v.LivestreamViewerModel})
	}
	s.DNSNames = map[string]bool{}
	return s, nil
}
func (s *memoryStore) streamResponse(l *memStream) Livestream {
	m := l.Model
	return Livestream{ID: m.ID, Owner: s.Users[m.UserID].Response, Title: m.Title, Description: m.Description, PlaylistUrl: m.PlaylistUrl, ThumbnailUrl: m.ThumbnailUrl, StartAt: m.StartAt, EndAt: m.EndAt, Tags: l.Tags}
}
func (s *memoryStore) commentResponse(c LivecommentModel) Livecomment {
	return Livecomment{ID: c.ID, User: s.Users[c.UserID].Response, Livestream: s.streamResponse(s.Streams[c.LivestreamID]), Comment: c.Comment, Tip: c.Tip, CreatedAt: c.CreatedAt}
}
func (s *memoryStore) reactionResponse(r ReactionModel) Reaction {
	return Reaction{ID: r.ID, User: s.Users[r.UserID].Response, Livestream: s.streamResponse(s.Streams[r.LivestreamID]), EmojiName: r.EmojiName, CreatedAt: r.CreatedAt}
}
func (s *memoryStore) reportResponse(r LivecommentReportModel) LivecommentReport {
	return LivecommentReport{ID: r.ID, Reporter: s.Users[r.UserID].Response, Livecomment: s.commentResponse(s.Comments[r.LivecommentID]), CreatedAt: r.CreatedAt}
}
func sortedStreamIDs(s *memoryStore) []int64 {
	ids := make([]int64, 0, len(s.Streams))
	for id := range s.Streams {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	return ids
}

func (s *memoryStore) reindex() {
	for _, u := range s.Users {
		if u.Image != nil {
			u.HasIcon = true
		}
	}
	s.StreamIDs = make([]int64, 0, len(s.Streams))
	s.UserStreams = map[int64][]int64{}
	s.TagStreams = map[string][]int64{}
	for id := range s.Streams {
		s.StreamIDs = append(s.StreamIDs, id)
	}
	sort.Slice(s.StreamIDs, func(i, j int) bool { return s.StreamIDs[i] < s.StreamIDs[j] })
	for _, id := range s.StreamIDs {
		l := s.Streams[id]
		s.UserStreams[l.Model.UserID] = append(s.UserStreams[l.Model.UserID], id)
		for _, t := range l.Tags {
			s.TagStreams[t.Name] = append(s.TagStreams[t.Name], id)
		}
	}
}
