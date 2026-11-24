package main

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"log"
	"net"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-sql-driver/mysql"
)

type Theme struct {
	ID       int64 `json:"id"`
	DarkMode bool  `json:"dark_mode"`
}

type User struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
	Theme       Theme  `json:"theme,omitempty"`
	IconHash    string `json:"icon_hash,omitempty"`
}

type Tag struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type Livestream struct {
	ID           int64  `json:"id"`
	Owner        User   `json:"owner"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	PlaylistUrl  string `json:"playlist_url"`
	ThumbnailUrl string `json:"thumbnail_url"`
	Tags         []Tag  `json:"tags"`
	StartAt      int64  `json:"start_at"`
	EndAt        int64  `json:"end_at"`
}

type Livecomment struct {
	ID         int64      `json:"id"`
	User       User       `json:"user"`
	Livestream Livestream `json:"livestream"`
	Comment    string     `json:"comment"`
	Tip        int64      `json:"tip"`
	CreatedAt  int64      `json:"created_at"`
}

type Reaction struct {
	ID         int64      `json:"id"`
	EmojiName  string     `json:"emoji_name"`
	User       User       `json:"user"`
	Livestream Livestream `json:"livestream"`
	CreatedAt  int64      `json:"created_at"`
}

type LivecommentReport struct {
	ID          int64       `json:"id"`
	Reporter    User        `json:"reporter"`
	Livecomment Livecomment `json:"livecomment"`
	CreatedAt   int64       `json:"created_at"`
}

type NGWord struct {
	ID           int64  `json:"id"`
	UserID       int64  `json:"user_id"`
	LivestreamID int64  `json:"livestream_id"`
	Word         string `json:"word"`
	CreatedAt    int64  `json:"created_at"`
}

// ---- in-memory models ----

type UserM struct {
	ID          int64
	Name        string
	DisplayName string
	Description string
	Password    string
	verifiedPw  string
	ThemeID     int64
	Dark        bool
	Icon        []byte
	IconHash    string
	resp        User

	livestreams []*LivestreamM
	// aggregates over owned livestreams
	recvReactions int64
	recvTips      int64
	recvComments  int64
	emoji         map[string]int64
}

func (u *UserM) rebuild() {
	h := u.IconHash
	if h == "" {
		h = fallbackHash
	}
	u.resp = User{ID: u.ID, Name: u.Name, DisplayName: u.DisplayName, Description: u.Description,
		Theme: Theme{ID: u.ThemeID, DarkMode: u.Dark}, IconHash: h}
}

type LivestreamM struct {
	ID           int64
	UserID       int64
	owner        *UserM
	Title        string
	Description  string
	PlaylistUrl  string
	ThumbnailUrl string
	StartAt      int64
	EndAt        int64
	tags         []Tag

	comments  []*CommentM
	reactions []*ReactionM
	reports   []*ReportM
	ngwords   []*NGWord
	viewers   map[int64]int64
	viewerCnt int64
	tipSum    int64
	reactCnt  int64
}

func (l *LivestreamM) resp() Livestream {
	return Livestream{ID: l.ID, Owner: l.owner.resp, Title: l.Title, Description: l.Description,
		PlaylistUrl: l.PlaylistUrl, ThumbnailUrl: l.ThumbnailUrl, Tags: l.tags, StartAt: l.StartAt, EndAt: l.EndAt}
}

func (l *LivestreamM) maxTip() int64 {
	var m int64
	for _, c := range l.comments {
		if c.Tip > m {
			m = c.Tip
		}
	}
	return m
}

type CommentM struct {
	ID        int64
	user      *UserM
	ls        *LivestreamM
	Comment   string
	Tip       int64
	CreatedAt int64
}

func (c *CommentM) resp() Livecomment {
	return Livecomment{ID: c.ID, User: c.user.resp, Livestream: c.ls.resp(), Comment: c.Comment, Tip: c.Tip, CreatedAt: c.CreatedAt}
}

type ReactionM struct {
	ID        int64
	user      *UserM
	ls        *LivestreamM
	Emoji     string
	CreatedAt int64
}

func (c *ReactionM) resp() Reaction {
	return Reaction{ID: c.ID, EmojiName: c.Emoji, User: c.user.resp, Livestream: c.ls.resp(), CreatedAt: c.CreatedAt}
}

type ReportM struct {
	ID        int64
	user      *UserM
	comment   *CommentM
	CreatedAt int64
}

type SlotM struct {
	Start, End, Count int64
}

var (
	mu sync.RWMutex

	usersByID   map[int64]*UserM
	usersByName map[string]*UserM
	livestreams map[int64]*LivestreamM
	lsOrder     []*LivestreamM // ascending id
	comments    map[int64]*CommentM
	tagsAll     []Tag
	tagByID     map[int64]Tag
	tagByName   map[string]int64
	tagIndex    map[int64][]*LivestreamM // ascending by livestream id
	slots       []SlotM
	totalTip    int64

	nextUserID, nextThemeID, nextLsID, nextCommentID, nextReactionID, nextReportID, nextNgID, nextIconID int64

	fallbackImage []byte
	fallbackHash  string

	dnsNames atomic.Pointer[map[string]struct{}]
	dnsMu    sync.Mutex
)

func loadFallback() {
	b, err := os.ReadFile("../img/NoImage.jpg")
	if err != nil {
		log.Fatal(err)
	}
	fallbackImage = b
	fallbackHash = fmt.Sprintf("%x", sha256.Sum256(b))
}

var db *sql.DB

func initDB() {
	conf := mysql.NewConfig()
	conf.Net = "tcp"
	host := os.Getenv("ISUCON13_MYSQL_DIALCONFIG_ADDRESS")
	if host == "" {
		host = "127.0.0.1"
	}
	conf.Addr = net.JoinHostPort(host, "3306")
	conf.User = "isucon"
	conf.Passwd = "isucon"
	conf.DBName = "isupipe"
	conf.InterpolateParams = true
	var err error
	db, err = sql.Open("mysql", conf.FormatDSN())
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	for {
		if err := db.Ping(); err == nil {
			break
		} else {
			log.Printf("db ping: %v", err)
		}
		time.Sleep(time.Second)
	}
}

func loadAll() error {
	mu.Lock()
	defer mu.Unlock()
	return loadAllLocked()
}

func loadAllLocked() error {
	uByID := map[int64]*UserM{}
	uByName := map[string]*UserM{}
	var maxU, maxT, maxL, maxC, maxR, maxRep, maxNg int64

	rows, err := db.Query("SELECT id, name, display_name, description, password FROM users")
	if err != nil {
		return err
	}
	for rows.Next() {
		u := &UserM{emoji: map[string]int64{}}
		if err := rows.Scan(&u.ID, &u.Name, &u.DisplayName, &u.Description, &u.Password); err != nil {
			return err
		}
		uByID[u.ID] = u
		uByName[u.Name] = u
		if u.ID > maxU {
			maxU = u.ID
		}
	}
	rows.Close()

	rows, err = db.Query("SELECT id, user_id, dark_mode FROM themes")
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, uid int64
		var dark bool
		if err := rows.Scan(&id, &uid, &dark); err != nil {
			return err
		}
		if u := uByID[uid]; u != nil {
			u.ThemeID = id
			u.Dark = dark
		}
		if id > maxT {
			maxT = id
		}
	}
	rows.Close()

	var maxIcon int64
	rows, err = db.Query("SELECT id, user_id, image FROM icons ORDER BY id")
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, uid int64
		var img []byte
		if err := rows.Scan(&id, &uid, &img); err != nil {
			return err
		}
		if u := uByID[uid]; u != nil {
			u.Icon = img
			u.IconHash = fmt.Sprintf("%x", sha256.Sum256(img))
		}
		if id > maxIcon {
			maxIcon = id
		}
	}
	rows.Close()
	for _, u := range uByID {
		u.rebuild()
	}

	tAll := []Tag{}
	tByID := map[int64]Tag{}
	tByName := map[string]int64{}
	rows, err = db.Query("SELECT id, name FROM tags ORDER BY id")
	if err != nil {
		return err
	}
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Name); err != nil {
			return err
		}
		tAll = append(tAll, t)
		tByID[t.ID] = t
		tByName[t.Name] = t.ID
	}
	rows.Close()

	lsMap := map[int64]*LivestreamM{}
	var order []*LivestreamM
	rows, err = db.Query("SELECT id, user_id, title, description, playlist_url, thumbnail_url, start_at, end_at FROM livestreams ORDER BY id")
	if err != nil {
		return err
	}
	for rows.Next() {
		l := &LivestreamM{viewers: map[int64]int64{}, tags: []Tag{}}
		if err := rows.Scan(&l.ID, &l.UserID, &l.Title, &l.Description, &l.PlaylistUrl, &l.ThumbnailUrl, &l.StartAt, &l.EndAt); err != nil {
			return err
		}
		l.owner = uByID[l.UserID]
		if l.owner == nil {
			return fmt.Errorf("livestream %d owner missing", l.ID)
		}
		l.owner.livestreams = append(l.owner.livestreams, l)
		lsMap[l.ID] = l
		order = append(order, l)
		if l.ID > maxL {
			maxL = l.ID
		}
	}
	rows.Close()

	tIdx := map[int64][]*LivestreamM{}
	rows, err = db.Query("SELECT livestream_id, tag_id FROM livestream_tags ORDER BY id")
	if err != nil {
		return err
	}
	type lt struct{ l, t int64 }
	var lts []lt
	for rows.Next() {
		var x lt
		if err := rows.Scan(&x.l, &x.t); err != nil {
			return err
		}
		lts = append(lts, x)
	}
	rows.Close()
	for _, x := range lts {
		if l := lsMap[x.l]; l != nil {
			l.tags = append(l.tags, tByID[x.t])
		}
	}
	sort.SliceStable(lts, func(i, j int) bool { return lts[i].l < lts[j].l })
	for _, x := range lts {
		if l := lsMap[x.l]; l != nil {
			tIdx[x.t] = append(tIdx[x.t], l)
		}
	}

	var sl []SlotM
	rows, err = db.Query("SELECT slot, start_at, end_at FROM reservation_slots ORDER BY start_at, id")
	if err != nil {
		return err
	}
	for rows.Next() {
		var s SlotM
		if err := rows.Scan(&s.Count, &s.Start, &s.End); err != nil {
			return err
		}
		sl = append(sl, s)
	}
	rows.Close()

	cMap := map[int64]*CommentM{}
	var tt int64
	rows, err = db.Query("SELECT id, user_id, livestream_id, comment, tip, created_at FROM livecomments ORDER BY id")
	if err != nil {
		return err
	}
	for rows.Next() {
		c := &CommentM{}
		var uid, lid int64
		if err := rows.Scan(&c.ID, &uid, &lid, &c.Comment, &c.Tip, &c.CreatedAt); err != nil {
			return err
		}
		if c.ID > maxC {
			maxC = c.ID
		}
		c.user = uByID[uid]
		c.ls = lsMap[lid]
		if c.user == nil || c.ls == nil {
			continue
		}
		cMap[c.ID] = c
		c.ls.comments = append(c.ls.comments, c)
		c.ls.tipSum += c.Tip
		c.ls.owner.recvTips += c.Tip
		c.ls.owner.recvComments++
		tt += c.Tip
	}
	rows.Close()

	rows, err = db.Query("SELECT id, user_id, livestream_id, emoji_name, created_at FROM reactions ORDER BY id")
	if err != nil {
		return err
	}
	for rows.Next() {
		c := &ReactionM{}
		var uid, lid int64
		if err := rows.Scan(&c.ID, &uid, &lid, &c.Emoji, &c.CreatedAt); err != nil {
			return err
		}
		if c.ID > maxR {
			maxR = c.ID
		}
		c.user = uByID[uid]
		c.ls = lsMap[lid]
		if c.user == nil || c.ls == nil {
			continue
		}
		c.ls.reactions = append(c.ls.reactions, c)
		c.ls.reactCnt++
		c.ls.owner.recvReactions++
		c.ls.owner.emoji[c.Emoji]++
	}
	rows.Close()

	rows, err = db.Query("SELECT id, user_id, livestream_id, livecomment_id, created_at FROM livecomment_reports ORDER BY id")
	if err != nil {
		return err
	}
	for rows.Next() {
		c := &ReportM{}
		var uid, lid, cid int64
		if err := rows.Scan(&c.ID, &uid, &lid, &cid, &c.CreatedAt); err != nil {
			return err
		}
		if c.ID > maxRep {
			maxRep = c.ID
		}
		c.user = uByID[uid]
		c.comment = cMap[cid]
		l := lsMap[lid]
		if c.user == nil || c.comment == nil || l == nil {
			continue
		}
		l.reports = append(l.reports, c)
	}
	rows.Close()

	rows, err = db.Query("SELECT id, user_id, livestream_id, word, created_at FROM ng_words ORDER BY id")
	if err != nil {
		return err
	}
	for rows.Next() {
		n := &NGWord{}
		if err := rows.Scan(&n.ID, &n.UserID, &n.LivestreamID, &n.Word, &n.CreatedAt); err != nil {
			return err
		}
		if n.ID > maxNg {
			maxNg = n.ID
		}
		if l := lsMap[n.LivestreamID]; l != nil {
			l.ngwords = append(l.ngwords, n)
		}
	}
	rows.Close()

	rows, err = db.Query("SELECT user_id, livestream_id FROM livestream_viewers_history")
	if err != nil {
		return err
	}
	for rows.Next() {
		var uid, lid int64
		if err := rows.Scan(&uid, &lid); err != nil {
			return err
		}
		if l := lsMap[lid]; l != nil {
			l.viewers[uid]++
			l.viewerCnt++
		}
	}
	rows.Close()

	usersByID, usersByName = uByID, uByName
	livestreams, lsOrder = lsMap, order
	comments = cMap
	tagsAll, tagByID, tagByName, tagIndex = tAll, tByID, tByName, tIdx
	slots = sl
	totalTip = tt
	nextUserID, nextThemeID, nextLsID, nextCommentID = maxU+1, maxT+1, maxL+1, maxC+1
	nextReactionID, nextReportID, nextNgID, nextIconID = maxR+1, maxRep+1, maxNg+1, maxIcon+1
	rebuildDNS()
	log.Printf("loaded users=%d livestreams=%d comments=%d", len(uByID), len(lsMap), len(cMap))
	return nil
}

// ---------- persistence ----------

type pItem struct {
	q    string
	args []any
}

var (
	pCh      = make(chan pItem, 1<<20)
	pPending atomic.Int64
)

func persist(q string, args ...any) {
	pPending.Add(1)
	pCh <- pItem{q, args}
}

func flushPersist() {
	for pPending.Load() > 0 {
		time.Sleep(5 * time.Millisecond)
	}
}

func persistWorker() {
	batch := make([]pItem, 0, 256)
	for {
		it := <-pCh
		batch = append(batch[:0], it)
	drain:
		for len(batch) < 256 {
			select {
			case it := <-pCh:
				batch = append(batch, it)
			default:
				break drain
			}
		}
		for {
			err := execBatch(batch)
			if err == nil {
				break
			}
			log.Printf("persist error: %v", err)
			if isFatalSQL(err) {
				// execute individually, skipping bad ones
				for _, b := range batch {
					if _, err := db.Exec(b.q, b.args...); err != nil {
						log.Printf("persist drop: %v %s", err, b.q)
					}
				}
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		pPending.Add(-int64(len(batch)))
	}
}

func isFatalSQL(err error) bool {
	_, ok := err.(*mysql.MySQLError)
	return ok
}

func execBatch(batch []pItem) error {
	if len(batch) == 1 {
		_, err := db.Exec(batch[0].q, batch[0].args...)
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	for _, b := range batch {
		if _, err := tx.Exec(b.q, b.args...); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}
