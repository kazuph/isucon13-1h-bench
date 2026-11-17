package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type memoryState struct {
	Archived   map[int64]LivecommentModel
	Users      map[int64]UserModel
	Themes     map[int64]ThemeModel
	Icons      map[int64][]byte
	Streams    map[int64]LivestreamModel
	Tags       map[int64]TagModel
	StreamTags map[int64][]int64
	Comments   map[int64]LivecommentModel
	Reactions  map[int64]ReactionModel
	Reports    map[int64]LivecommentReportModel
	Words      map[int64]NGWord
	Viewers    []LivestreamViewerModel
	Slots      []ReservationSlotModel
	Next       map[string]int64
}
type mutation struct {
	Kind     string
	User     UserModel
	Theme    ThemeModel
	Image    []byte
	Stream   LivestreamModel
	Tags     []int64
	Comment  LivecommentModel
	Reaction ReactionModel
	Report   LivecommentReportModel
	Word     NGWord
	Viewer   LivestreamViewerModel
	ID       int64
	Deleted  []int64
}
type streamAggregate struct {
	Reactions, Comments, Tips, Viewers, Reports, MaxTip int64
	Emoji                                               map[string]int64
}
type memoryEngine struct {
	AllStreams []int64
	TagStreams map[string][]int64
	sync.RWMutex
	S               memoryState
	Names           map[string]int64
	UserStreams     map[int64][]int64
	StreamComments  map[int64][]int64
	StreamReactions map[int64][]int64
	StreamReports   map[int64][]int64
	StreamWords     map[int64][]int64
	Agg             map[int64]*streamAggregate
	Hashes          map[int64]string
	Fallback        []byte
	generation      string
	log             *os.File
	writerStopped   chan struct{}
	queue           chan durableItem
}
type durableItem struct {
	Data []byte
	Done chan error
}

var walSyncCount atomic.Uint64
var walSyncNanos atomic.Uint64
var walItemCount atomic.Uint64
var mem = &memoryEngine{}

func emptyState() memoryState {
	return memoryState{Users: map[int64]UserModel{}, Themes: map[int64]ThemeModel{}, Icons: map[int64][]byte{}, Streams: map[int64]LivestreamModel{}, Tags: map[int64]TagModel{}, StreamTags: map[int64][]int64{}, Comments: map[int64]LivecommentModel{}, Reactions: map[int64]ReactionModel{}, Reports: map[int64]LivecommentReportModel{}, Words: map[int64]NGWord{}, Next: map[string]int64{}}
}
func (m *memoryEngine) next(k string) int64 { m.S.Next[k]++; return m.S.Next[k] }
func (m *memoryEngine) aggregate(id int64) *streamAggregate {
	a := m.Agg[id]
	if a == nil {
		a = &streamAggregate{Emoji: map[string]int64{}}
		m.Agg[id] = a
	}
	return a
}
func (m *memoryEngine) rebuild() {
	if m.S.Archived == nil {
		m.S.Archived = map[int64]LivecommentModel{}
	}
	m.Names = map[string]int64{}
	m.UserStreams = map[int64][]int64{}
	m.AllStreams = nil
	m.TagStreams = map[string][]int64{}
	m.StreamComments = map[int64][]int64{}
	m.StreamReactions = map[int64][]int64{}
	m.StreamReports = map[int64][]int64{}
	m.StreamWords = map[int64][]int64{}
	m.Agg = map[int64]*streamAggregate{}
	m.Hashes = map[int64]string{}
	for id, u := range m.S.Users {
		m.Names[u.Name] = id
	}
	for id := range m.S.Streams {
		m.AllStreams = append(m.AllStreams, id)
	}
	sort.Slice(m.AllStreams, func(i, j int) bool { return m.AllStreams[i] < m.AllStreams[j] })
	for _, id := range m.AllStreams {
		s := m.S.Streams[id]
		m.UserStreams[s.UserID] = append(m.UserStreams[s.UserID], id)
		for _, tid := range m.S.StreamTags[id] {
			name := m.S.Tags[tid].Name
			m.TagStreams[name] = append(m.TagStreams[name], id)
		}
	}
	for id, c := range m.S.Comments {
		m.StreamComments[c.LivestreamID] = append(m.StreamComments[c.LivestreamID], id)
		a := m.aggregate(c.LivestreamID)
		a.Comments++
		a.Tips += c.Tip
		if c.Tip > a.MaxTip {
			a.MaxTip = c.Tip
		}
	}
	for id, r := range m.S.Reactions {
		m.StreamReactions[r.LivestreamID] = append(m.StreamReactions[r.LivestreamID], id)
		a := m.aggregate(r.LivestreamID)
		a.Reactions++
		a.Emoji[r.EmojiName]++
	}
	for id, r := range m.S.Reports {
		m.StreamReports[r.LivestreamID] = append(m.StreamReports[r.LivestreamID], id)
		m.aggregate(r.LivestreamID).Reports++
	}
	for id, w := range m.S.Words {
		m.StreamWords[w.LivestreamID] = append(m.StreamWords[w.LivestreamID], id)
	}
	for _, v := range m.S.Viewers {
		m.aggregate(v.LivestreamID).Viewers++
	}
	for id := range m.S.Users {
		m.hash(id)
	}
}
func (m *memoryEngine) hash(id int64) {
	image, ok := m.S.Icons[id]
	if !ok {
		image = m.Fallback
	}
	m.Hashes[id] = fmt.Sprintf("%x", sha256.Sum256(image))
}
func (m *memoryEngine) user(id int64) User {
	u := m.S.Users[id]
	t := m.S.Themes[id]
	return User{ID: u.ID, Name: u.Name, DisplayName: u.DisplayName, Description: u.Description, Theme: Theme{ID: t.ID, DarkMode: t.DarkMode}, IconHash: m.Hashes[id]}
}
func (m *memoryEngine) stream(id int64) Livestream {
	s := m.S.Streams[id]
	tags := make([]Tag, 0, len(m.S.StreamTags[id]))
	for _, tid := range m.S.StreamTags[id] {
		t := m.S.Tags[tid]
		tags = append(tags, Tag{ID: t.ID, Name: t.Name})
	}
	return Livestream{ID: s.ID, Owner: m.user(s.UserID), Title: s.Title, Description: s.Description, PlaylistUrl: s.PlaylistUrl, ThumbnailUrl: s.ThumbnailUrl, StartAt: s.StartAt, EndAt: s.EndAt, Tags: tags}
}
func (m *memoryEngine) comment(id int64) Livecomment {
	c, ok := m.S.Comments[id]
	if !ok {
		c = m.S.Archived[id]
	}
	return Livecomment{ID: c.ID, User: m.user(c.UserID), Livestream: m.stream(c.LivestreamID), Comment: c.Comment, Tip: c.Tip, CreatedAt: c.CreatedAt}
}
func (m *memoryEngine) reaction(id int64) Reaction {
	r := m.S.Reactions[id]
	return Reaction{ID: r.ID, User: m.user(r.UserID), Livestream: m.stream(r.LivestreamID), EmojiName: r.EmojiName, CreatedAt: r.CreatedAt}
}
func (m *memoryEngine) report(id int64) LivecommentReport {
	r := m.S.Reports[id]
	return LivecommentReport{ID: r.ID, Reporter: m.user(r.UserID), Livecomment: m.comment(r.LivecommentID), CreatedAt: r.CreatedAt}
}
func (m *memoryEngine) apply(e mutation) {
	switch e.Kind {
	case "user":
		m.S.Users[e.User.ID] = e.User
		m.S.Themes[e.User.ID] = e.Theme
		m.Names[e.User.Name] = e.User.ID
		m.hash(e.User.ID)
		m.S.Next["user"] = e.User.ID
		m.S.Next["theme"] = e.Theme.ID
	case "icon":
		m.S.Icons[e.ID] = e.Image
		m.hash(e.ID)
		m.S.Next["icon"] = e.Theme.ID
	case "stream":
		s := e.Stream
		m.S.Streams[s.ID] = s
		m.S.StreamTags[s.ID] = e.Tags
		m.UserStreams[s.UserID] = append(m.UserStreams[s.UserID], s.ID)
		m.AllStreams = append(m.AllStreams, s.ID)
		for _, tid := range e.Tags {
			name := m.S.Tags[tid].Name
			m.TagStreams[name] = append(m.TagStreams[name], s.ID)
		}
		for i := range m.S.Slots {
			v := &m.S.Slots[i]
			if v.StartAt >= s.StartAt && v.EndAt <= s.EndAt {
				v.Slot--
			}
		}
		m.S.Next["stream"] = s.ID
	case "comment":
		c := e.Comment
		m.S.Comments[c.ID] = c
		m.StreamComments[c.LivestreamID] = append(m.StreamComments[c.LivestreamID], c.ID)
		a := m.aggregate(c.LivestreamID)
		a.Comments++
		a.Tips += c.Tip
		if c.Tip > a.MaxTip {
			a.MaxTip = c.Tip
		}
		m.S.Next["comment"] = c.ID
	case "reaction":
		r := e.Reaction
		m.S.Reactions[r.ID] = r
		m.StreamReactions[r.LivestreamID] = append(m.StreamReactions[r.LivestreamID], r.ID)
		a := m.aggregate(r.LivestreamID)
		a.Reactions++
		a.Emoji[r.EmojiName]++
		m.S.Next["reaction"] = r.ID
	case "report":
		r := e.Report
		m.S.Reports[r.ID] = r
		m.StreamReports[r.LivestreamID] = append(m.StreamReports[r.LivestreamID], r.ID)
		m.aggregate(r.LivestreamID).Reports++
		m.S.Next["report"] = r.ID
	case "word":
		w := e.Word
		m.S.Words[w.ID] = w
		m.StreamWords[w.LivestreamID] = append(m.StreamWords[w.LivestreamID], w.ID)
		a := m.aggregate(w.LivestreamID)
		for _, id := range e.Deleted {
			c, ok := m.S.Comments[id]
			if ok {
				a.Comments--
				a.Tips -= c.Tip
				m.S.Archived[id] = c
				delete(m.S.Comments, id)
			}
		}
		a.MaxTip = 0
		for _, id := range m.StreamComments[w.LivestreamID] {
			if c, ok := m.S.Comments[id]; ok && c.Tip > a.MaxTip {
				a.MaxTip = c.Tip
			}
		}
		m.S.Next["word"] = w.ID
	case "enter":
		m.S.Viewers = append(m.S.Viewers, e.Viewer)
		m.aggregate(e.Viewer.LivestreamID).Viewers++
	case "exit":
		vv := m.S.Viewers[:0]
		for _, v := range m.S.Viewers {
			if v.UserID == e.Viewer.UserID && v.LivestreamID == e.Viewer.LivestreamID {
				m.aggregate(v.LivestreamID).Viewers--
			} else {
				vv = append(vv, v)
			}
		}
		m.S.Viewers = vv
	}
}
func dataDir() string {
	if s := os.Getenv("ISUPIPE_DATA"); s != "" {
		return s
	}
	return "../state"
}
func (m *memoryEngine) open() error {
	var err error
	m.Fallback, err = os.ReadFile(fallbackImage)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dataDir(), 0755); err != nil {
		return err
	}
	if current, readErr := os.ReadFile(filepath.Join(dataDir(), "CURRENT")); readErr == nil {
		m.generation = strings.TrimSpace(string(current))
	} else if !os.IsNotExist(readErr) {
		return readErr
	}
	f, err := os.Open(m.statePath("snapshot.json"))
	if err == nil {
		err = json.NewDecoder(f).Decode(&m.S)
		f.Close()
		if err != nil {
			return err
		}
	} else if os.IsNotExist(err) {
		if err = m.loadInitial(); err != nil {
			return err
		}
		if err = m.snapshot(); err != nil {
			return err
		}
	} else {
		return err
	}
	m.rebuild()
	f, err = os.Open(m.statePath("events.jsonl"))
	if err == nil {
		r := bufio.NewReader(f)
		var validBytes int64
		for {
			line, e := r.ReadBytes('\n')
			if e == io.EOF {
				break
			}
			if e != nil {
				f.Close()
				return e
			}
			var event mutation
			if e = json.Unmarshal(line, &event); e != nil {
				f.Close()
				return e
			}
			m.apply(event)
			validBytes += int64(len(line))
		}
		f.Close()
		if err := os.Truncate(m.statePath("events.jsonl"), validBytes); err != nil {
			return err
		}
	}
	m.log, err = os.OpenFile(m.statePath("events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	m.queue = make(chan durableItem, 8192)
	m.writerStopped = make(chan struct{})
	go m.writer()
	return nil
}
func (m *memoryEngine) statePath(name string) string {
	if m.generation != "" {
		name = m.generation + "-" + name
	}
	return filepath.Join(dataDir(), name)
}

// A generation manifest atomically selects a matched snapshot and empty log.
// The writer must be drained and state locked by the caller.
func (m *memoryEngine) newGeneration() error {
	oldGeneration := m.generation
	oldLog := m.log
	m.generation = fmt.Sprintf("%d", time.Now().UnixNano())
	if err := m.snapshot(); err != nil {
		m.generation = oldGeneration
		return err
	}
	fresh, err := os.OpenFile(m.statePath("events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	if err = fresh.Sync(); err != nil {
		fresh.Close()
		return err
	}
	manifest := filepath.Join(dataDir(), "CURRENT")
	f, err := os.Create(manifest + ".tmp")
	if err != nil {
		fresh.Close()
		return err
	}
	_, err = f.WriteString(m.generation + "\n")
	if err == nil {
		err = f.Sync()
	}
	f.Close()
	if err != nil {
		fresh.Close()
		return err
	}
	if err = os.Rename(manifest+".tmp", manifest); err != nil {
		fresh.Close()
		return err
	}
	dir, err := os.Open(dataDir())
	if err != nil {
		fresh.Close()
		return err
	}
	err = dir.Sync()
	dir.Close()
	if err != nil {
		fresh.Close()
		return err
	}
	m.log = fresh
	if oldLog != nil {
		oldLog.Close()
	}
	oldPrefix := ""
	if oldGeneration != "" {
		oldPrefix = oldGeneration + "-"
	}
	os.Remove(filepath.Join(dataDir(), oldPrefix+"snapshot.json"))
	os.Remove(filepath.Join(dataDir(), oldPrefix+"events.jsonl"))
	return nil
}
func (m *memoryEngine) snapshot() error {
	p := m.statePath("snapshot.json")
	f, err := os.Create(p + ".tmp")
	if err != nil {
		return err
	}
	if err = json.NewEncoder(f).Encode(m.S); err == nil {
		err = f.Sync()
	}
	f.Close()
	if err != nil {
		return err
	}
	if err := os.Rename(p+".tmp", p); err != nil {
		return err
	}
	dir, err := os.Open(dataDir())
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (m *memoryEngine) writer() {
	defer close(m.writerStopped)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	dirty := false
	lastSync := time.Now()
	flush := func() error {
		if !dirty {
			return nil
		}
		started := time.Now()
		err := m.log.Sync()
		walSyncNanos.Add(uint64(time.Since(started)))
		walSyncCount.Add(1)
		dirty = false
		lastSync = time.Now()
		return err
	}
	for {
		select {
		case <-ticker.C:
			if err := flush(); err != nil {
				panic(err)
			}
		case first, ok := <-m.queue:
			if !ok {
				return
			}
			batch := []durableItem{first}
			for len(batch) < 256 {
				select {
				case item, open := <-m.queue:
					if !open {
						goto write
					}
					batch = append(batch, item)
				default:
					goto write
				}
			}
		write:
			var err error
			barrier := false
			for _, item := range batch {
				if item.Data == nil {
					barrier = true
					continue
				}
				if err == nil {
					_, err = m.log.Write(item.Data)
					dirty = true
				}
				walItemCount.Add(1)
			}
			// ACK only after the entire record has reached the kernel file cache.
			// The OS flushes this cache on reboot; fsync is batched every 20ms.
			if err == nil && (barrier || time.Since(lastSync) >= 20*time.Millisecond) {
				err = flush()
			}
			for _, item := range batch {
				item.Done <- err
			}
			if err != nil {
				panic(err)
			}
		}
	}
}

// Called under the state lock. Returning channel is awaited after unlocking.
func (m *memoryEngine) persist(e mutation) chan error {
	b, err := json.Marshal(e)
	if err != nil {
		panic(err)
	}
	done := make(chan error, 1)
	m.queue <- durableItem{append(b, '\n'), done}
	return done
}
func (m *memoryEngine) loadInitial() error {
	ctx := context.Background()
	m.S = emptyState()
	var users []UserModel
	if err := dbConn.SelectContext(ctx, &users, "SELECT * FROM users"); err != nil {
		return err
	}
	for _, u := range users {
		m.S.Users[u.ID] = u
		if u.ID > m.S.Next["user"] {
			m.S.Next["user"] = u.ID
		}
	}
	var themes []ThemeModel
	if err := dbConn.SelectContext(ctx, &themes, "SELECT * FROM themes"); err != nil {
		return err
	}
	for _, t := range themes {
		m.S.Themes[t.UserID] = t
		if t.ID > m.S.Next["theme"] {
			m.S.Next["theme"] = t.ID
		}
	}
	var streams []LivestreamModel
	if err := dbConn.SelectContext(ctx, &streams, "SELECT * FROM livestreams"); err != nil {
		return err
	}
	for _, v := range streams {
		m.S.Streams[v.ID] = v
		if v.ID > m.S.Next["stream"] {
			m.S.Next["stream"] = v.ID
		}
	}
	var tags []TagModel
	if err := dbConn.SelectContext(ctx, &tags, "SELECT * FROM tags"); err != nil {
		return err
	}
	for _, v := range tags {
		m.S.Tags[v.ID] = v
	}
	var st []LivestreamTagModel
	if err := dbConn.SelectContext(ctx, &st, "SELECT * FROM livestream_tags ORDER BY id"); err != nil {
		return err
	}
	for _, v := range st {
		m.S.StreamTags[v.LivestreamID] = append(m.S.StreamTags[v.LivestreamID], v.TagID)
	}
	var comments []LivecommentModel
	if err := dbConn.SelectContext(ctx, &comments, "SELECT * FROM livecomments"); err != nil {
		return err
	}
	for _, v := range comments {
		m.S.Comments[v.ID] = v
		if v.ID > m.S.Next["comment"] {
			m.S.Next["comment"] = v.ID
		}
	}
	var reactions []ReactionModel
	if err := dbConn.SelectContext(ctx, &reactions, "SELECT * FROM reactions"); err != nil {
		return err
	}
	for _, v := range reactions {
		m.S.Reactions[v.ID] = v
		if v.ID > m.S.Next["reaction"] {
			m.S.Next["reaction"] = v.ID
		}
	}
	var words []NGWord
	if err := dbConn.SelectContext(ctx, &words, "SELECT * FROM ng_words"); err != nil {
		return err
	}
	for _, v := range words {
		m.S.Words[v.ID] = v
		if v.ID > m.S.Next["word"] {
			m.S.Next["word"] = v.ID
		}
	}
	if err := dbConn.SelectContext(ctx, &m.S.Slots, "SELECT * FROM reservation_slots ORDER BY id"); err != nil {
		return err
	}
	return nil
}
