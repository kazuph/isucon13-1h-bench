package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"github.com/labstack/echo/v4"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type State struct {
	Users      map[int64]UserModel
	Themes     map[int64]Theme
	Icons      map[int64][]byte
	Streams    map[int64]LivestreamModel
	Tags       map[int64]Tag
	StreamTags map[int64][]int64
	Slots      []ReservationSlotModel
	Comments   map[int64]LivecommentModel
	Reactions  map[int64]ReactionModel
	Reports    map[int64]LivecommentReportModel
	Words      map[int64]NGWord
	Viewers    []LivestreamViewerModel
	Next       map[string]int64
}

var mu sync.RWMutex
var state *State
var journal *os.File
var names map[string]int64
var hashes map[int64]string
var streamComments, streamReactions, streamWords, userStreams map[int64][]int64
var fallback []byte
var dataDir = "../state"

type Event struct {
	Kind     string
	User     UserModel
	Theme    Theme
	Stream   LivestreamModel
	Tags     []int64
	Comment  LivecommentModel
	Reaction ReactionModel
	Report   LivecommentReportModel
	Word     NGWord
	Viewer   LivestreamViewerModel
	ID       int64
	UserID   int64
	Image    []byte
}

func newState() *State {
	return &State{Users: map[int64]UserModel{}, Themes: map[int64]Theme{}, Icons: map[int64][]byte{}, Streams: map[int64]LivestreamModel{}, Tags: map[int64]Tag{}, StreamTags: map[int64][]int64{}, Comments: map[int64]LivecommentModel{}, Reactions: map[int64]ReactionModel{}, Reports: map[int64]LivecommentReportModel{}, Words: map[int64]NGWord{}, Next: map[string]int64{}}
}
func next(kind string) int64 { state.Next[kind]++; return state.Next[kind] }
func bump(kind string, id int64) {
	if id > state.Next[kind] {
		state.Next[kind] = id
	}
}
func rebuild() {
	names = map[string]int64{}
	hashes = map[int64]string{}
	streamComments = map[int64][]int64{}
	streamReactions = map[int64][]int64{}
	streamWords = map[int64][]int64{}
	userStreams = map[int64][]int64{}
	for id, u := range state.Users {
		names[u.Name] = id
		hashes[id] = hash(icon(id))
	}
	for id, s := range state.Streams {
		userStreams[s.UserID] = append(userStreams[s.UserID], id)
	}
	for id, c := range state.Comments {
		streamComments[c.LivestreamID] = append(streamComments[c.LivestreamID], id)
	}
	for id, r := range state.Reactions {
		streamReactions[r.LivestreamID] = append(streamReactions[r.LivestreamID], id)
	}
	for id, w := range state.Words {
		streamWords[w.LivestreamID] = append(streamWords[w.LivestreamID], id)
	}
	for _, m := range []map[int64][]int64{streamComments, streamReactions, streamWords, userStreams} {
		for _, ids := range m {
			sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		}
	}
}
func hash(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
func icon(id int64) []byte {
	if b, ok := state.Icons[id]; ok {
		return b
	}
	return fallback
}
func apply(e Event) {
	switch e.Kind {
	case "user":
		state.Users[e.User.ID] = e.User
		state.Themes[e.User.ID] = e.Theme
		names[e.User.Name] = e.User.ID
		hashes[e.User.ID] = hash(fallback)
		bump("user", e.User.ID)
		bump("theme", e.Theme.ID)
	case "icon":
		state.Icons[e.UserID] = e.Image
		hashes[e.UserID] = hash(e.Image)
		bump("icon", e.ID)
	case "stream":
		state.Streams[e.Stream.ID] = e.Stream
		state.StreamTags[e.Stream.ID] = e.Tags
		userStreams[e.Stream.UserID] = append(userStreams[e.Stream.UserID], e.Stream.ID)
		bump("stream", e.Stream.ID)
		for i, s := range state.Slots {
			if s.StartAt >= e.Stream.StartAt && s.EndAt <= e.Stream.EndAt {
				state.Slots[i].Slot--
			}
		}
	case "comment":
		state.Comments[e.Comment.ID] = e.Comment
		streamComments[e.Comment.LivestreamID] = append(streamComments[e.Comment.LivestreamID], e.Comment.ID)
		bump("comment", e.Comment.ID)
	case "reaction":
		state.Reactions[e.Reaction.ID] = e.Reaction
		streamReactions[e.Reaction.LivestreamID] = append(streamReactions[e.Reaction.LivestreamID], e.Reaction.ID)
		bump("reaction", e.Reaction.ID)
	case "report":
		state.Reports[e.Report.ID] = e.Report
		bump("report", e.Report.ID)
	case "word":
		state.Words[e.Word.ID] = e.Word
		streamWords[e.Word.LivestreamID] = append(streamWords[e.Word.LivestreamID], e.Word.ID)
		bump("word", e.Word.ID)
		for _, id := range streamComments[e.Word.LivestreamID] {
			if c, ok := state.Comments[id]; ok && matches(c.Comment, e.Word.Word) {
				delete(state.Comments, id)
			}
		}
	case "enter":
		state.Viewers = append(state.Viewers, e.Viewer)
	case "exit":
		out := state.Viewers[:0]
		for _, v := range state.Viewers {
			if v.UserID != e.Viewer.UserID || v.LivestreamID != e.Viewer.LivestreamID {
				out = append(out, v)
			}
		}
		state.Viewers = out
	}
}
func commit(e Event) error {
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
	apply(e)
	return nil
}
func matches(s, w string) bool { return strings.Contains(s, w) }
func openStore() error {
	if d := os.Getenv("STATE_DIR"); d != "" {
		dataDir = d
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return err
	}
	var err error
	fallback, err = os.ReadFile("../img/NoImage.jpg")
	if err != nil {
		return err
	}
	f, err := os.Open(filepath.Join(dataDir, "snapshot.gob"))
	if os.IsNotExist(err) {
		return loadInitial()
	}
	if err != nil {
		return err
	}
	state = newState()
	err = gob.NewDecoder(f).Decode(state)
	f.Close()
	if err != nil {
		return err
	}
	rebuild()
	f, err = os.Open(filepath.Join(dataDir, "journal.jsonl"))
	if err == nil {
		dec := json.NewDecoder(bufio.NewReader(f))
		for {
			var e Event
			err = dec.Decode(&e)
			if err == io.EOF {
				break
			}
			if err != nil {
				f.Close()
				return err
			}
			apply(e)
		}
		f.Close()
	} else if !os.IsNotExist(err) {
		return err
	}
	journal, err = os.OpenFile(filepath.Join(dataDir, "journal.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	return err
}
func snapshot() error {
	path := filepath.Join(dataDir, "snapshot.gob")
	f, err := os.Create(path + ".tmp")
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
	return os.Rename(path+".tmp", path)
}
func loadInitial() error {
	state = newState()
	var users []UserModel
	var themes []ThemeModel
	var streams []LivestreamModel
	var tags []TagModel
	var st []LivestreamTagModel
	var comments []LivecommentModel
	var reactions []ReactionModel
	var words []NGWord
	for _, q := range []struct {
		dest  interface{}
		table string
	}{{&users, "users"}, {&themes, "themes"}, {&streams, "livestreams"}, {&tags, "tags"}, {&st, "livestream_tags"}, {&comments, "livecomments"}, {&reactions, "reactions"}, {&words, "ng_words"}, {&state.Slots, "reservation_slots"}} {
		if err := dbConn.Select(q.dest, "SELECT * FROM "+q.table+" ORDER BY id"); err != nil {
			return err
		}
	}
	for _, u := range users {
		state.Users[u.ID] = u
		bump("user", u.ID)
	}
	for _, t := range themes {
		state.Themes[t.UserID] = Theme{ID: t.ID, DarkMode: t.DarkMode}
		bump("theme", t.ID)
	}
	for _, s := range streams {
		state.Streams[s.ID] = s
		bump("stream", s.ID)
	}
	for _, t := range tags {
		state.Tags[t.ID] = Tag{ID: t.ID, Name: t.Name}
	}
	for _, t := range st {
		state.StreamTags[t.LivestreamID] = append(state.StreamTags[t.LivestreamID], t.TagID)
	}
	for _, c := range comments {
		state.Comments[c.ID] = c
		bump("comment", c.ID)
	}
	for _, r := range reactions {
		state.Reactions[r.ID] = r
		bump("reaction", r.ID)
	}
	for _, w := range words {
		state.Words[w.ID] = w
		bump("word", w.ID)
	}
	rebuild()
	if err := snapshot(); err != nil {
		return err
	}
	if journal != nil {
		journal.Close()
	}
	var err error
	journal, err = os.OpenFile(filepath.Join(dataDir, "journal.jsonl"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	return err
}
func initializeHandler(c echo.Context) error {
	mu.Lock()
	defer mu.Unlock()
	if out, err := exec.Command("../sql/init.sh").CombinedOutput(); err != nil {
		return fmt.Errorf("initialize: %s: %w", out, err)
	}
	if err := loadInitial(); err != nil {
		return err
	}
	if err := syncDNS(); err != nil {
		return err
	}
	return c.JSON(200, map[string]string{"language": "golang", "lang": "golang"})
}
func userResponse(id int64) User {
	u := state.Users[id]
	return User{ID: id, Name: u.Name, DisplayName: u.DisplayName, Description: u.Description, Theme: state.Themes[id], IconHash: hashes[id]}
}
func streamResponse(id int64) Livestream {
	s := state.Streams[id]
	tags := make([]Tag, 0, len(state.StreamTags[id]))
	for _, id := range state.StreamTags[id] {
		tags = append(tags, state.Tags[id])
	}
	return Livestream{ID: s.ID, Owner: userResponse(s.UserID), Title: s.Title, Description: s.Description, PlaylistUrl: s.PlaylistUrl, ThumbnailUrl: s.ThumbnailUrl, Tags: tags, StartAt: s.StartAt, EndAt: s.EndAt}
}
func commentResponse(id int64) Livecomment {
	v := state.Comments[id]
	return Livecomment{ID: v.ID, User: userResponse(v.UserID), Livestream: streamResponse(v.LivestreamID), Comment: v.Comment, Tip: v.Tip, CreatedAt: v.CreatedAt}
}
func reactionResponse(id int64) Reaction {
	v := state.Reactions[id]
	return Reaction{ID: v.ID, User: userResponse(v.UserID), Livestream: streamResponse(v.LivestreamID), EmojiName: v.EmojiName, CreatedAt: v.CreatedAt}
}
func reportResponse(id int64) LivecommentReport {
	v := state.Reports[id]
	return LivecommentReport{ID: v.ID, Reporter: userResponse(v.UserID), Livecomment: commentResponse(v.LivecommentID), CreatedAt: v.CreatedAt}
}

var dnsClient = &http.Client{Timeout: 5 * time.Second}

func updateDNS(names []string, replace bool) error {
	b, e := json.Marshal(names)
	if e != nil {
		return e
	}
	method := "POST"
	if replace {
		method = "PUT"
	}
	r, e := http.NewRequest(method, "http://10.99.0.11:8053/names", bytes.NewReader(b))
	if e != nil {
		return e
	}
	res, e := dnsClient.Do(r)
	if e != nil {
		return e
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("DNS update status %d", res.StatusCode)
	}
	return nil
}
func syncDNS() error {
	list := make([]string, 0, len(names))
	for name := range names {
		list = append(list, name)
	}
	zone, err := os.ReadFile("../pdns/u.isucon.dev.zone")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(zone), "\n") {
		f := strings.Fields(line)
		if len(f) >= 5 && f[2] == "IN" && f[3] == "A" && f[0] != "@" {
			list = append(list, f[0])
		}
	}
	return updateDNS(list, true)
}
