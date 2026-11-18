package main

import (
	"bufio"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type Tag struct {
	ID   int64
	Name string
}

type User struct {
	ID          int64
	Name        string
	DisplayName string
	Description string
	Password    string
	ThemeID     int64
	Dark        bool
	Icon        []byte
	IconHash    string
	json        []byte
	lss         []*Livestream
	reactions   int64
	tips        int64
	comments    int64
	viewers     int64
	emoji       map[string]int64
}

func (u *User) score() int64 { return u.reactions + u.tips }

type Livestream struct {
	ID           int64
	Owner        *User
	Title        string
	Description  string
	PlaylistURL  string
	ThumbnailURL string
	StartAt      int64
	EndAt        int64
	Tags         []*Tag
	json         []byte
	comments     []*Comment
	reactions    []*Reaction
	ngwords      []*NGWord
	reports      []*Report
	viewerHist   map[int64]int64
	viewers      int64
	tipSum       int64
}

func (l *Livestream) score() int64 { return int64(len(l.reactions)) + l.tipSum }

type Comment struct {
	ID        int64
	User      *User
	LS        *Livestream
	Comment   string
	Tip       int64
	CreatedAt int64
	deleted   bool
}

type Reaction struct {
	ID        int64
	User      *User
	LS        *Livestream
	Emoji     string
	CreatedAt int64
}

type NGWord struct {
	ID           int64
	UserID       int64
	LivestreamID int64
	Word         string
	CreatedAt    int64
}

type Report struct {
	ID        int64
	User      *User
	LSID      int64
	Comment   *Comment
	CreatedAt int64
}

type Slot struct {
	ID      int64
	Slot    int64
	StartAt int64
	EndAt   int64
}

var (
	mu sync.RWMutex

	users       map[int64]*User
	usersByName map[string]*User
	userList    []*User

	lss    map[int64]*Livestream
	lsList []*Livestream

	tagByID   map[int64]*Tag
	tagByName map[string]*Tag
	tagsJSON  []byte
	lsByTag   map[int64][]*Livestream // livestream_tags order (livestream_id asc)

	comments map[int64]*Comment
	slots    []*Slot
	totalTip int64

	nextUserID, nextThemeID, nextIconID, nextLSID, nextLSTagID, nextHistID int64
	nextCommentID, nextReportID, nextNGID, nextReactionID                   int64

	initNGMax     int64
	fallbackImage []byte
	fallbackHash  string

	// DNS names (lowercase label under u.isucon.dev). "" means apex.
	dnsNames   sync.Map
	zoneNames  []string
)

func hashHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func loadZoneNames(path string) {
	f, err := os.Open(path)
	if err != nil {
		log.Printf("zone file: %v", err)
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	zoneNames = zoneNames[:0]
	for sc.Scan() {
		line := sc.Text()
		fs := strings.Fields(line)
		if len(fs) >= 4 && fs[len(fs)-2] == "A" {
			name := fs[0]
			if name == "@" {
				name = ""
			}
			zoneNames = append(zoneNames, strings.ToLower(name))
		}
	}
}

func maxID(db *sql.DB, table string) int64 {
	var v sql.NullInt64
	if err := db.QueryRow("SELECT MAX(id) FROM " + table).Scan(&v); err != nil {
		log.Fatalf("max id %s: %v", table, err)
	}
	return v.Int64
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

// loadAll loads every table into memory. Caller must hold mu (write).
func loadAll(db *sql.DB) {
	users = map[int64]*User{}
	usersByName = map[string]*User{}
	userList = nil
	lss = map[int64]*Livestream{}
	lsList = nil
	tagByID = map[int64]*Tag{}
	tagByName = map[string]*Tag{}
	lsByTag = map[int64][]*Livestream{}
	comments = map[int64]*Comment{}
	slots = nil
	totalTip = 0

	// users
	rows, err := db.Query("SELECT id, name, display_name, description, password FROM users ORDER BY id")
	must(err)
	for rows.Next() {
		u := &User{emoji: map[string]int64{}}
		must(rows.Scan(&u.ID, &u.Name, &u.DisplayName, &u.Description, &u.Password))
		u.IconHash = fallbackHash
		users[u.ID] = u
		usersByName[u.Name] = u
		userList = append(userList, u)
	}
	rows.Close()

	rows, err = db.Query("SELECT id, user_id, dark_mode FROM themes ORDER BY id")
	must(err)
	for rows.Next() {
		var id, uid int64
		var dark bool
		must(rows.Scan(&id, &uid, &dark))
		if u := users[uid]; u != nil && u.ThemeID == 0 {
			u.ThemeID = id
			u.Dark = dark
		}
	}
	rows.Close()

	rows, err = db.Query("SELECT user_id, image FROM icons ORDER BY id")
	must(err)
	for rows.Next() {
		var uid int64
		var img []byte
		must(rows.Scan(&uid, &img))
		if u := users[uid]; u != nil {
			u.Icon = img
			u.IconHash = hashHex(img)
		}
	}
	rows.Close()

	rows, err = db.Query("SELECT id, name FROM tags ORDER BY id")
	must(err)
	var tagList []*Tag
	for rows.Next() {
		t := &Tag{}
		must(rows.Scan(&t.ID, &t.Name))
		tagByID[t.ID] = t
		tagByName[t.Name] = t
		tagList = append(tagList, t)
	}
	rows.Close()
	{
		b := []byte(`{"tags":[`)
		for i, t := range tagList {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendTag(b, t)
		}
		b = append(b, "]}"...)
		tagsJSON = b
	}

	rows, err = db.Query("SELECT id, user_id, title, description, playlist_url, thumbnail_url, start_at, end_at FROM livestreams ORDER BY id")
	must(err)
	for rows.Next() {
		l := &Livestream{viewerHist: map[int64]int64{}}
		var uid int64
		must(rows.Scan(&l.ID, &uid, &l.Title, &l.Description, &l.PlaylistURL, &l.ThumbnailURL, &l.StartAt, &l.EndAt))
		l.Owner = users[uid]
		if l.Owner == nil {
			log.Printf("livestream %d has unknown owner %d", l.ID, uid)
			continue
		}
		l.Owner.lss = append(l.Owner.lss, l)
		lss[l.ID] = l
		lsList = append(lsList, l)
	}
	rows.Close()

	rows, err = db.Query("SELECT livestream_id, tag_id FROM livestream_tags ORDER BY id")
	must(err)
	type lt struct{ ls, tag int64 }
	var lts []lt
	for rows.Next() {
		var x lt
		must(rows.Scan(&x.ls, &x.tag))
		lts = append(lts, x)
		if l := lss[x.ls]; l != nil {
			if t := tagByID[x.tag]; t != nil {
				l.Tags = append(l.Tags, t)
			}
		}
	}
	rows.Close()
	sort.SliceStable(lts, func(i, j int) bool { return lts[i].ls < lts[j].ls })
	for _, x := range lts {
		if l := lss[x.ls]; l != nil {
			lsByTag[x.tag] = append(lsByTag[x.tag], l)
		}
	}

	rows, err = db.Query("SELECT user_id, livestream_id FROM livestream_viewers_history ORDER BY id")
	must(err)
	for rows.Next() {
		var uid, lid int64
		must(rows.Scan(&uid, &lid))
		if l := lss[lid]; l != nil {
			l.viewerHist[uid]++
			l.viewers++
			l.Owner.viewers++
		}
	}
	rows.Close()

	rows, err = db.Query("SELECT id, user_id, livestream_id, comment, tip, created_at FROM livecomments ORDER BY id")
	must(err)
	for rows.Next() {
		c := &Comment{}
		var uid, lid int64
		must(rows.Scan(&c.ID, &uid, &lid, &c.Comment, &c.Tip, &c.CreatedAt))
		c.User = users[uid]
		c.LS = lss[lid]
		if c.User == nil || c.LS == nil {
			continue
		}
		comments[c.ID] = c
		addCommentStats(c)
	}
	rows.Close()

	rows, err = db.Query("SELECT id, user_id, livestream_id, emoji_name, created_at FROM reactions ORDER BY id")
	must(err)
	for rows.Next() {
		r := &Reaction{}
		var uid, lid int64
		must(rows.Scan(&r.ID, &uid, &lid, &r.Emoji, &r.CreatedAt))
		r.User = users[uid]
		r.LS = lss[lid]
		if r.User == nil || r.LS == nil {
			continue
		}
		addReactionStats(r)
	}
	rows.Close()

	rows, err = db.Query("SELECT id, user_id, livestream_id, word, created_at FROM ng_words ORDER BY id")
	must(err)
	for rows.Next() {
		n := &NGWord{}
		must(rows.Scan(&n.ID, &n.UserID, &n.LivestreamID, &n.Word, &n.CreatedAt))
		if l := lss[n.LivestreamID]; l != nil {
			l.ngwords = append(l.ngwords, n)
		}
	}
	rows.Close()

	rows, err = db.Query("SELECT id, user_id, livestream_id, livecomment_id, created_at FROM livecomment_reports ORDER BY id")
	must(err)
	for rows.Next() {
		r := &Report{}
		var uid, cid int64
		must(rows.Scan(&r.ID, &uid, &r.LSID, &cid, &r.CreatedAt))
		r.User = users[uid]
		r.Comment = comments[cid]
		l := lss[r.LSID]
		if r.User == nil || l == nil || r.Comment == nil {
			continue
		}
		l.reports = append(l.reports, r)
	}
	rows.Close()

	rows, err = db.Query("SELECT id, slot, start_at, end_at FROM reservation_slots ORDER BY start_at, id")
	must(err)
	for rows.Next() {
		s := &Slot{}
		must(rows.Scan(&s.ID, &s.Slot, &s.StartAt, &s.EndAt))
		slots = append(slots, s)
	}
	rows.Close()

	nextUserID = maxID(db, "users")
	nextThemeID = maxID(db, "themes")
	nextIconID = maxID(db, "icons")
	nextLSID = maxID(db, "livestreams")
	nextLSTagID = maxID(db, "livestream_tags")
	nextHistID = maxID(db, "livestream_viewers_history")
	nextCommentID = maxID(db, "livecomments")
	nextReportID = maxID(db, "livecomment_reports")
	nextNGID = maxID(db, "ng_words")
	initNGMax = nextNGID
	nextReactionID = maxID(db, "reactions")

	for _, u := range userList {
		u.json = buildUserJSON(u)
	}
	for _, l := range lsList {
		l.json = buildLSJSON(l)
	}

	// DNS
	dnsNames.Range(func(k, v any) bool { dnsNames.Delete(k); return true })
	for _, n := range zoneNames {
		dnsNames.Store(n, true)
	}
	for _, u := range userList {
		dnsNames.Store(strings.ToLower(u.Name), true)
	}
	log.Printf("loaded users=%d livestreams=%d comments=%d slots=%d", len(userList), len(lsList), len(comments), len(slots))
}

func addCommentStats(c *Comment) {
	l := c.LS
	l.comments = append(l.comments, c)
	l.tipSum += c.Tip
	l.Owner.tips += c.Tip
	l.Owner.comments++
	totalTip += c.Tip
}

func removeCommentStats(c *Comment) {
	l := c.LS
	l.tipSum -= c.Tip
	l.Owner.tips -= c.Tip
	l.Owner.comments--
	totalTip -= c.Tip
	c.deleted = true
	delete(comments, c.ID)
}

func addReactionStats(r *Reaction) {
	l := r.LS
	l.reactions = append(l.reactions, r)
	l.Owner.reactions++
	l.Owner.emoji[r.Emoji]++
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
