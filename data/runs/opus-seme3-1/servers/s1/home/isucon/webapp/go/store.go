package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"log"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/jmoiron/sqlx"
)

type User struct {
	ID          int64
	Name        string
	DisplayName string
	Description string
	Password    string
	ThemeID     int64
	DarkMode    bool
	Icon        []byte
	IconHash    string

	Streams []*Livestream
	// aggregates over own livestreams
	Reactions int64
	Tips      int64
	Comments  int64
	Viewers   int64
	Emoji     map[string]int64

	pwOK string // cached verified plaintext
	JS   []byte
}

type Tag struct {
	ID   int64  `json:"id" db:"id"`
	Name string `json:"name" db:"name"`
}

type Livestream struct {
	ID           int64
	Owner        *User
	Title        string
	Description  string
	PlaylistUrl  string
	ThumbnailUrl string
	StartAt      int64
	EndAt        int64
	Tags         []Tag

	Comments  []*Comment
	Reactions []*Reaction
	Reports   []*Report
	NGWords   []*NGWord
	ViewerMap map[int64]int64
	Viewers   int64
	Tips      int64
	ReactCnt  int64
	JS        []byte
}

type Comment struct {
	ID           int64  `db:"id"`
	UserID       int64  `db:"user_id"`
	LivestreamID int64  `db:"livestream_id"`
	Comment      string `db:"comment"`
	Tip          int64  `db:"tip"`
	CreatedAt    int64  `db:"created_at"`
}

type Reaction struct {
	ID           int64  `db:"id"`
	UserID       int64  `db:"user_id"`
	LivestreamID int64  `db:"livestream_id"`
	EmojiName    string `db:"emoji_name"`
	CreatedAt    int64  `db:"created_at"`
}

type Report struct {
	ID            int64 `db:"id"`
	UserID        int64 `db:"user_id"`
	LivestreamID  int64 `db:"livestream_id"`
	LivecommentID int64 `db:"livecomment_id"`
	CreatedAt     int64 `db:"created_at"`
}

type NGWord struct {
	ID           int64  `json:"id" db:"id"`
	UserID       int64  `json:"user_id" db:"user_id"`
	LivestreamID int64  `json:"livestream_id" db:"livestream_id"`
	Word         string `json:"word" db:"word"`
	CreatedAt    int64  `json:"created_at" db:"created_at"`
}

type Slot struct {
	ID      int64 `db:"id"`
	Slot    int64 `db:"slot"`
	StartAt int64 `db:"start_at"`
	EndAt   int64 `db:"end_at"`
}

var (
	mu sync.RWMutex

	usersByID   map[int64]*User
	usersByName map[string]*User
	lowerNames  map[string]bool
	streams     map[int64]*Livestream
	streamList  []*Livestream // id asc
	commentsAll map[int64]*Comment
	tagsByID    map[int64]*Tag
	tagsByName  map[string]*Tag
	tagList     []*Tag
	tagStreams  map[int64][]int64 // tag id -> livestream ids asc
	slots       []*Slot            // sorted by start_at
	totalTips   int64

	nextUserID, nextThemeID, nextStreamID, nextCommentID, nextReactionID, nextReportID, nextNGID, nextIconID int64

	fallbackImage     []byte
	fallbackImageHash string
)

func loadFallback() {
	b, err := os.ReadFile("../img/NoImage.jpg")
	if err != nil {
		log.Fatal(err)
	}
	fallbackImage = b
	h := sha256.Sum256(b)
	fallbackImageHash = hex.EncodeToString(h[:])
}

func maxID(db *sqlx.DB, table string) int64 {
	var v sql.NullInt64
	if err := db.Get(&v, "SELECT MAX(id) FROM "+table); err != nil {
		log.Fatal(err)
	}
	return v.Int64
}

func loadAll(db *sqlx.DB) {
	mu.Lock()
	defer mu.Unlock()

	usersByID = map[int64]*User{}
	usersByName = map[string]*User{}
	lowerNames = map[string]bool{}
	streams = map[int64]*Livestream{}
	streamList = nil
	commentsAll = map[int64]*Comment{}
	tagsByID = map[int64]*Tag{}
	tagsByName = map[string]*Tag{}
	tagList = nil
	tagStreams = map[int64][]int64{}
	slots = nil
	totalTips = 0

	type urow struct {
		ID          int64  `db:"id"`
		Name        string `db:"name"`
		DisplayName string `db:"display_name"`
		Password    string `db:"password"`
		Description string `db:"description"`
	}
	var urows []urow
	must(db.Select(&urows, "SELECT id,name,display_name,password,description FROM users ORDER BY id"))
	for _, r := range urows {
		u := &User{ID: r.ID, Name: r.Name, DisplayName: r.DisplayName, Description: r.Description, Password: r.Password, IconHash: fallbackImageHash, Emoji: map[string]int64{}}
		usersByID[u.ID] = u
		usersByName[u.Name] = u
		lowerNames[strings.ToLower(u.Name)] = true
	}
	type trow struct {
		ID     int64 `db:"id"`
		UserID int64 `db:"user_id"`
		Dark   bool  `db:"dark_mode"`
	}
	var trows []trow
	must(db.Select(&trows, "SELECT id,user_id,dark_mode FROM themes ORDER BY id"))
	for _, r := range trows {
		if u := usersByID[r.UserID]; u != nil {
			u.ThemeID = r.ID
			u.DarkMode = r.Dark
		}
	}
	type irow struct {
		ID     int64  `db:"id"`
		UserID int64  `db:"user_id"`
		Image  []byte `db:"image"`
	}
	var irows []irow
	must(db.Select(&irows, "SELECT id,user_id,image FROM icons ORDER BY id"))
	for _, r := range irows {
		if u := usersByID[r.UserID]; u != nil {
			u.Icon = r.Image
			h := sha256.Sum256(r.Image)
			u.IconHash = hex.EncodeToString(h[:])
		}
	}

	var tagrows []*Tag
	must(db.Select(&tagrows, "SELECT id,name FROM tags ORDER BY id"))
	for _, t := range tagrows {
		tagsByID[t.ID] = t
		tagsByName[t.Name] = t
		tagList = append(tagList, t)
	}

	type lrow struct {
		ID           int64  `db:"id"`
		UserID       int64  `db:"user_id"`
		Title        string `db:"title"`
		Description  string `db:"description"`
		PlaylistUrl  string `db:"playlist_url"`
		ThumbnailUrl string `db:"thumbnail_url"`
		StartAt      int64  `db:"start_at"`
		EndAt        int64  `db:"end_at"`
	}
	var lrows []lrow
	must(db.Select(&lrows, "SELECT * FROM livestreams ORDER BY id"))
	for _, r := range lrows {
		ls := &Livestream{ID: r.ID, Owner: usersByID[r.UserID], Title: r.Title, Description: r.Description, PlaylistUrl: r.PlaylistUrl, ThumbnailUrl: r.ThumbnailUrl, StartAt: r.StartAt, EndAt: r.EndAt, Tags: []Tag{}, ViewerMap: map[int64]int64{}}
		streams[ls.ID] = ls
		streamList = append(streamList, ls)
		if ls.Owner != nil {
			ls.Owner.Streams = append(ls.Owner.Streams, ls)
		}
	}

	type ltrow struct {
		ID           int64 `db:"id"`
		LivestreamID int64 `db:"livestream_id"`
		TagID        int64 `db:"tag_id"`
	}
	var ltrows []ltrow
	must(db.Select(&ltrows, "SELECT id,livestream_id,tag_id FROM livestream_tags ORDER BY id"))
	for _, r := range ltrows {
		ls := streams[r.LivestreamID]
		t := tagsByID[r.TagID]
		if ls != nil && t != nil {
			ls.Tags = append(ls.Tags, *t)
		}
		tagStreams[r.TagID] = append(tagStreams[r.TagID], r.LivestreamID)
	}
	for k := range tagStreams {
		s := tagStreams[k]
		sort.SliceStable(s, func(i, j int) bool { return s[i] < s[j] })
	}

	var crows []*Comment
	must(db.Select(&crows, "SELECT id,user_id,livestream_id,comment,tip,created_at FROM livecomments ORDER BY id"))
	for _, c := range crows {
		addCommentMem(c)
	}
	var rrows []*Reaction
	must(db.Select(&rrows, "SELECT id,user_id,livestream_id,emoji_name,created_at FROM reactions ORDER BY id"))
	for _, r := range rrows {
		addReactionMem(r)
	}
	var reprows []*Report
	must(db.Select(&reprows, "SELECT id,user_id,livestream_id,livecomment_id,created_at FROM livecomment_reports ORDER BY id"))
	for _, r := range reprows {
		if ls := streams[r.LivestreamID]; ls != nil {
			ls.Reports = append(ls.Reports, r)
		}
	}
	var ngrows []*NGWord
	must(db.Select(&ngrows, "SELECT id,user_id,livestream_id,word,created_at FROM ng_words ORDER BY id"))
	for _, n := range ngrows {
		if ls := streams[n.LivestreamID]; ls != nil {
			ls.NGWords = append(ls.NGWords, n)
		}
	}
	type vrow struct {
		UserID       int64 `db:"user_id"`
		LivestreamID int64 `db:"livestream_id"`
	}
	var vrows []vrow
	must(db.Select(&vrows, "SELECT user_id,livestream_id FROM livestream_viewers_history"))
	for _, v := range vrows {
		if ls := streams[v.LivestreamID]; ls != nil {
			ls.ViewerMap[v.UserID]++
			ls.Viewers++
			if ls.Owner != nil {
				ls.Owner.Viewers++
			}
		}
	}
	must(db.Select(&slots, "SELECT id,slot,start_at,end_at FROM reservation_slots ORDER BY start_at, id"))

	nextUserID = maxID(db, "users")
	nextThemeID = maxID(db, "themes")
	nextStreamID = maxID(db, "livestreams")
	nextCommentID = maxID(db, "livecomments")
	nextReactionID = maxID(db, "reactions")
	nextReportID = maxID(db, "livecomment_reports")
	nextNGID = maxID(db, "ng_words")
	initNGMax = nextNGID
	nextIconID = maxID(db, "icons")
	for _, u := range usersByID {
		renderUser(u)
	}
	for _, ls := range streamList {
		renderStream(ls)
	}
	log.Printf("loaded users=%d streams=%d comments=%d reactions=%d", len(usersByID), len(streams), len(crows), len(rrows))
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func addCommentMem(c *Comment) {
	commentsAll[c.ID] = c
	ls := streams[c.LivestreamID]
	if ls == nil {
		return
	}
	ls.Comments = append(ls.Comments, c)
	ls.Tips += c.Tip
	totalTips += c.Tip
	if o := ls.Owner; o != nil {
		o.Tips += c.Tip
		o.Comments++
	}
}

func addReactionMem(r *Reaction) {
	ls := streams[r.LivestreamID]
	if ls == nil {
		return
	}
	ls.Reactions = append(ls.Reactions, r)
	ls.ReactCnt++
	if o := ls.Owner; o != nil {
		o.Reactions++
		o.Emoji[r.EmojiName]++
	}
}
