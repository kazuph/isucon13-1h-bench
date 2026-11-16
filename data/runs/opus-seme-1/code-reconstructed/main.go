package main

import (
	"os/signal"
	"syscall"
	"crypto/tls"
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	_ "net/http/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
)

// ---------------- models ----------------

type UserM struct {
	ID          int64
	Name        string
	DisplayName string
	Description string
	Hash        string
	ThemeID     int64
	Dark        bool
	IconHash    string
	Icon        []byte
	Streams     []*LS
	ReactRecv   int64
	TipRecv     int64
	Emoji       map[string]int64
}

type LS struct {
	ID           int64
	UserID       int64
	Title        string
	Description  string
	PlaylistUrl  string
	ThumbnailUrl string
	StartAt      int64
	EndAt        int64
	Tags         []int64
	Comments     []*LC
	Reactions    []*RX
	Reports      []*RP
	NG           []*NGWord
	ReactCount   int64
	TipSum       int64
	Viewers      int64
}

type LC struct {
	ID           int64  `json:"id"`
	UserID       int64  `json:"user_id"`
	LivestreamID int64  `json:"livestream_id"`
	Comment      string `json:"comment"`
	Tip          int64  `json:"tip"`
	CreatedAt    int64  `json:"created_at"`
	Deleted      bool   `json:"-"`
}

type RX struct {
	ID           int64  `json:"id"`
	UserID       int64  `json:"user_id"`
	LivestreamID int64  `json:"livestream_id"`
	EmojiName    string `json:"emoji_name"`
	CreatedAt    int64  `json:"created_at"`
}

type RP struct {
	ID            int64 `json:"id"`
	UserID        int64 `json:"user_id"`
	LivestreamID  int64 `json:"livestream_id"`
	LivecommentID int64 `json:"livecomment_id"`
	CreatedAt     int64 `json:"created_at"`
}

type NGWord struct {
	ID           int64  `json:"id" db:"id"`
	UserID       int64  `json:"user_id" db:"user_id"`
	LivestreamID int64  `json:"livestream_id" db:"livestream_id"`
	Word         string `json:"word" db:"word"`
	CreatedAt    int64  `json:"created_at" db:"created_at"`
}

type Tag struct {
	ID   int64  `json:"id" db:"id"`
	Name string `json:"name" db:"name"`
}

// ---------------- responses ----------------

type User struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
	Theme       Theme  `json:"theme,omitempty"`
	IconHash    string `json:"icon_hash,omitempty"`
}

type Theme struct {
	ID       int64 `json:"id"`
	DarkMode bool  `json:"dark_mode"`
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

type LivecommentReport struct {
	ID          int64       `json:"id"`
	Reporter    User        `json:"reporter"`
	Livecomment Livecomment `json:"livecomment"`
	CreatedAt   int64       `json:"created_at"`
}

type Reaction struct {
	ID         int64      `json:"id"`
	EmojiName  string     `json:"emoji_name"`
	User       User       `json:"user"`
	Livestream Livestream `json:"livestream"`
	CreatedAt  int64      `json:"created_at"`
}

// ---------------- state ----------------

var (
	mu          sync.RWMutex
	users       []*UserM // index = id
	userByName  map[string]*UserM
	streams     []*LS // index = id
	tags        []Tag
	tagByName   map[string]int64
	tagIndex    map[int64][]int64
	comments    map[int64]*LC
	slots       []int64
	viewerHist  map[[2]int64]int64
	totalTip    int64
	nextUserID  int64
	nextThemeID int64
	nextIconID  int64
	nextLSID    int64
	nextLCID    int64
	nextRXID    int64
	nextRPID    int64
	nextNGID    int64

	fallbackHash string
	db           *sqlx.DB
)

const slotBase = 1700874000

func getUser(id int64) *UserM {
	if id <= 0 || id >= int64(len(users)) {
		return nil
	}
	return users[id]
}

func getLS(id int64) *LS {
	if id <= 0 || id >= int64(len(streams)) {
		return nil
	}
	return streams[id]
}

func putUser(u *UserM) {
	for int64(len(users)) <= u.ID {
		users = append(users, nil)
	}
	users[u.ID] = u
	userByName[u.Name] = u
	dnsAdd(u.Name)
}

func putLS(l *LS) {
	for int64(len(streams)) <= l.ID {
		streams = append(streams, nil)
	}
	streams[l.ID] = l
	if o := getUser(l.UserID); o != nil {
		o.Streams = append(o.Streams, l)
	}
	for _, t := range l.Tags {
		tagIndex[t] = append(tagIndex[t], l.ID)
	}
}

// ---------------- load ----------------

func loadAll() error {
	type urow struct {
		ID          int64  `db:"id"`
		Name        string `db:"name"`
		DisplayName string `db:"display_name"`
		Password    string `db:"password"`
		Description string `db:"description"`
	}
	var urows []urow
	if err := db.Select(&urows, "SELECT id,name,display_name,password,description FROM users ORDER BY id"); err != nil {
		return err
	}
	type trow struct {
		ID     int64 `db:"id"`
		UserID int64 `db:"user_id"`
		Dark   bool  `db:"dark_mode"`
	}
	var trows []trow
	if err := db.Select(&trows, "SELECT id,user_id,dark_mode FROM themes ORDER BY id"); err != nil {
		return err
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
	if err := db.Select(&lrows, "SELECT id,user_id,title,description,playlist_url,thumbnail_url,start_at,end_at FROM livestreams ORDER BY id"); err != nil {
		return err
	}
	type ltrow struct {
		LivestreamID int64 `db:"livestream_id"`
		TagID        int64 `db:"tag_id"`
	}
	var ltrows []ltrow
	if err := db.Select(&ltrows, "SELECT livestream_id, tag_id FROM livestream_tags ORDER BY id"); err != nil {
		return err
	}
	var tagRows []Tag
	if err := db.Select(&tagRows, "SELECT id,name FROM tags ORDER BY id"); err != nil {
		return err
	}
	type srow struct {
		Slot    int64 `db:"slot"`
		StartAt int64 `db:"start_at"`
	}
	var srows []srow
	if err := db.Select(&srows, "SELECT slot,start_at FROM reservation_slots ORDER BY start_at"); err != nil {
		return err
	}
	type crow struct {
		ID           int64  `db:"id"`
		UserID       int64  `db:"user_id"`
		LivestreamID int64  `db:"livestream_id"`
		Comment      string `db:"comment"`
		Tip          int64  `db:"tip"`
		CreatedAt    int64  `db:"created_at"`
	}
	var crows []crow
	if err := db.Select(&crows, "SELECT id,user_id,livestream_id,comment,tip,created_at FROM livecomments ORDER BY id"); err != nil {
		return err
	}
	type rrow struct {
		ID           int64  `db:"id"`
		UserID       int64  `db:"user_id"`
		LivestreamID int64  `db:"livestream_id"`
		EmojiName    string `db:"emoji_name"`
		CreatedAt    int64  `db:"created_at"`
	}
	var rrows []rrow
	if err := db.Select(&rrows, "SELECT id,user_id,livestream_id,emoji_name,created_at FROM reactions ORDER BY id"); err != nil {
		return err
	}
	var ngrows []NGWord
	if err := db.Select(&ngrows, "SELECT id,user_id,livestream_id,word,created_at FROM ng_words ORDER BY id"); err != nil {
		return err
	}

	// reset
	users = make([]*UserM, 0, 4096)
	userByName = make(map[string]*UserM, 4096)
	streams = make([]*LS, 0, 16384)
	tags = nil
	tagByName = map[string]int64{}
	tagIndex = map[int64][]int64{}
	comments = make(map[int64]*LC, 8192)
	viewerHist = map[[2]int64]int64{}
	totalTip = 0
	nextUserID, nextThemeID, nextIconID, nextLSID, nextLCID, nextRXID, nextRPID, nextNGID = 1, 1, 1, 1, 1, 1, 1, 1
	dnsReset()

	themeBy := map[int64]trow{}
	for _, t := range trows {
		themeBy[t.UserID] = t
		if t.ID >= nextThemeID {
			nextThemeID = t.ID + 1
		}
	}
	for _, r := range urows {
		t := themeBy[r.ID]
		u := &UserM{ID: r.ID, Name: r.Name, DisplayName: r.DisplayName, Description: r.Description, Hash: r.Password, ThemeID: t.ID, Dark: t.Dark, IconHash: fallbackHash, Emoji: map[string]int64{}}
		putUser(u)
		if r.ID >= nextUserID {
			nextUserID = r.ID + 1
		}
	}
	for _, t := range tagRows {
		tags = append(tags, t)
		tagByName[t.Name] = t.ID
	}
	lsTags := map[int64][]int64{}
	for _, r := range ltrows {
		lsTags[r.LivestreamID] = append(lsTags[r.LivestreamID], r.TagID)
	}
	for _, r := range lrows {
		l := &LS{ID: r.ID, UserID: r.UserID, Title: r.Title, Description: r.Description, PlaylistUrl: r.PlaylistUrl, ThumbnailUrl: r.ThumbnailUrl, StartAt: r.StartAt, EndAt: r.EndAt, Tags: lsTags[r.ID]}
		putLS(l)
		if r.ID >= nextLSID {
			nextLSID = r.ID + 1
		}
	}
	slots = make([]int64, len(srows))
	for i, s := range srows {
		slots[i] = s.Slot
	}
	for _, r := range crows {
		applyLC(&LC{ID: r.ID, UserID: r.UserID, LivestreamID: r.LivestreamID, Comment: r.Comment, Tip: r.Tip, CreatedAt: r.CreatedAt})
	}
	for _, r := range rrows {
		applyRX(&RX{ID: r.ID, UserID: r.UserID, LivestreamID: r.LivestreamID, EmojiName: r.EmojiName, CreatedAt: r.CreatedAt})
	}
	for i := range ngrows {
		n := ngrows[i]
		if l := getLS(n.LivestreamID); l != nil {
			l.NG = append(l.NG, &n)
		}
		if n.ID >= nextNGID {
			nextNGID = n.ID + 1
		}
	}
	return nil
}

// ---------------- apply (shared by handlers and replay) ----------------

func applyUser(r *RecUser) {
	u := &UserM{ID: r.ID, Name: r.Name, DisplayName: r.DisplayName, Description: r.Description, Hash: r.Hash, ThemeID: r.ThemeID, Dark: r.Dark, IconHash: fallbackHash, Emoji: map[string]int64{}}
	putUser(u)
	if r.ID >= nextUserID {
		nextUserID = r.ID + 1
	}
	if r.ThemeID >= nextThemeID {
		nextThemeID = r.ThemeID + 1
	}
}

func applyIcon(r *RecIcon) {
	u := getUser(r.UserID)
	if u != nil {
		u.Icon = r.Image
		h := sha256.Sum256(r.Image)
		u.IconHash = hex.EncodeToString(h[:])
	}
	if r.IconID >= nextIconID {
		nextIconID = r.IconID + 1
	}
}

func slotRange(start, end int64) (int, int) {
	// slot i covers [base+3600i, base+3600(i+1)); include if slotStart>=start && slotEnd<=end
	var lo int64
	if start <= slotBase {
		lo = 0
	} else {
		lo = (start - slotBase + 3599) / 3600
	}
	hi := (end-slotBase)/3600 - 1
	if end < slotBase {
		hi = -1
	}
	if hi >= int64(len(slots)) {
		hi = int64(len(slots)) - 1
	}
	return int(lo), int(hi)
}

func applyLS(r *LS) {
	lo, hi := slotRange(r.StartAt, r.EndAt)
	for i := lo; i <= hi; i++ {
		slots[i]--
	}
	putLS(r)
	if r.ID >= nextLSID {
		nextLSID = r.ID + 1
	}
}

func applyLC(c *LC) {
	comments[c.ID] = c
	if c.ID >= nextLCID {
		nextLCID = c.ID + 1
	}
	l := getLS(c.LivestreamID)
	if l == nil {
		return
	}
	l.Comments = append(l.Comments, c)
	l.TipSum += c.Tip
	totalTip += c.Tip
	if o := getUser(l.UserID); o != nil {
		o.TipRecv += c.Tip
	}
}

func applyRX(r *RX) {
	if r.ID >= nextRXID {
		nextRXID = r.ID + 1
	}
	l := getLS(r.LivestreamID)
	if l == nil {
		return
	}
	l.Reactions = append(l.Reactions, r)
	l.ReactCount++
	if o := getUser(l.UserID); o != nil {
		o.ReactRecv++
		o.Emoji[r.EmojiName]++
	}
}

func applyRP(r *RP) {
	if r.ID >= nextRPID {
		nextRPID = r.ID + 1
	}
	if l := getLS(r.LivestreamID); l != nil {
		l.Reports = append(l.Reports, r)
	}
}

func applyNG(n *NGWord) {
	if n.ID >= nextNGID {
		nextNGID = n.ID + 1
	}
	l := getLS(n.LivestreamID)
	if l == nil {
		return
	}
	l.NG = append(l.NG, n)
	kept := l.Comments[:0:0]
	for _, c := range l.Comments {
		hit := false
		for _, w := range l.NG {
			if ciContains(c.Comment, w.Word) {
				hit = true
				break
			}
		}
		if hit {
			c.Deleted = true
			delete(comments, c.ID)
			l.TipSum -= c.Tip
			totalTip -= c.Tip
			if o := getUser(l.UserID); o != nil {
				o.TipRecv -= c.Tip
			}
		} else {
			kept = append(kept, c)
		}
	}
	l.Comments = kept
}

func applyEnter(uid, lid int64) {
	viewerHist[[2]int64{uid, lid}]++
	if l := getLS(lid); l != nil {
		l.Viewers++
	}
}

func applyExit(uid, lid int64) {
	k := [2]int64{uid, lid}
	n := viewerHist[k]
	if n == 0 {
		return
	}
	delete(viewerHist, k)
	if l := getLS(lid); l != nil {
		l.Viewers -= n
	}
}

// ---------------- op log ----------------

type RecUser struct {
	ID          int64  `json:"id"`
	Name        string `json:"n"`
	DisplayName string `json:"dn"`
	Description string `json:"d"`
	Hash        string `json:"h"`
	ThemeID     int64  `json:"t"`
	Dark        bool   `json:"k"`
}

type RecIcon struct {
	UserID int64  `json:"u"`
	IconID int64  `json:"i"`
	Image  []byte `json:"b"`
}

type RecLS struct {
	ID           int64   `json:"id"`
	UserID       int64   `json:"u"`
	Title        string  `json:"t"`
	Description  string  `json:"d"`
	PlaylistUrl  string  `json:"p"`
	ThumbnailUrl string  `json:"th"`
	StartAt      int64   `json:"s"`
	EndAt        int64   `json:"e"`
	Tags         []int64 `json:"g"`
}

type Op struct {
	T  string   `json:"t"`
	U  *RecUser `json:"u,omitempty"`
	I  *RecIcon `json:"i,omitempty"`
	L  *RecLS   `json:"l,omitempty"`
	C  *LC      `json:"c,omitempty"`
	R  *RX      `json:"r,omitempty"`
	P  *RP      `json:"p,omitempty"`
	N  *NGWord  `json:"n,omitempty"`
	UI int64    `json:"ui,omitempty"`
	LI int64    `json:"li,omitempty"`
}

const logPath = "/home/isucon/data/oplog.jsonl"

var (
	logMu   sync.Mutex
	logFile *os.File
)

func openLog(truncate bool) {
	logMu.Lock()
	defer logMu.Unlock()
	if logFile != nil {
		logFile.Close()
	}
	logQ = nil
	flag := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if truncate {
		flag |= os.O_TRUNC
	}
	f, err := os.OpenFile(logPath, flag, 0644)
	if err != nil {
		log.Fatal(err)
	}
	logFile = f
}

// writeOp must be called while holding mu (write) so that log order == apply order.
func writeOp(op *Op) {
	logMu.Lock()
	logQ = append(logQ, op)
	logMu.Unlock()
}

var logQ []*Op

func flushLog() {
	logMu.Lock()
	q := logQ
	logQ = nil
	f := logFile
	if len(q) == 0 {
		logMu.Unlock()
		return
	}
	buf := make([]byte, 0, 256*len(q))
	for _, op := range q {
		b, _ := json.Marshal(op)
		buf = append(buf, b...)
		buf = append(buf, '\n')
	}
	f.Write(buf)
	logMu.Unlock()
}

func logFlusher() {
	for {
		time.Sleep(20 * time.Millisecond)
		flushLog()
	}
}

func replayLog() {
	f, err := os.Open(logPath)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	n := 0
	for sc.Scan() {
		var op Op
		if err := json.Unmarshal(sc.Bytes(), &op); err != nil {
			log.Printf("bad log line: %v", err)
			continue
		}
		applyOp(&op)
		n++
	}
	log.Printf("replayed %d ops", n)
}

func applyOp(op *Op) {
	switch op.T {
	case "u":
		applyUser(op.U)
	case "i":
		applyIcon(op.I)
	case "l":
		r := op.L
		applyLS(&LS{ID: r.ID, UserID: r.UserID, Title: r.Title, Description: r.Description, PlaylistUrl: r.PlaylistUrl, ThumbnailUrl: r.ThumbnailUrl, StartAt: r.StartAt, EndAt: r.EndAt, Tags: r.Tags})
	case "c":
		applyLC(op.C)
	case "r":
		applyRX(op.R)
	case "p":
		applyRP(op.P)
	case "n":
		applyNG(op.N)
	case "en":
		applyEnter(op.UI, op.LI)
	case "ex":
		applyExit(op.UI, op.LI)
	}
}

// ---------------- response builders (must hold mu.RLock) ----------------

func userResp(u *UserM) User {
	return User{
		ID:          u.ID,
		Name:        u.Name,
		DisplayName: u.DisplayName,
		Description: u.Description,
		Theme:       Theme{ID: u.ThemeID, DarkMode: u.Dark},
		IconHash:    u.IconHash,
	}
}

func lsResp(l *LS) Livestream {
	ts := make([]Tag, 0, len(l.Tags))
	for _, t := range l.Tags {
		if t >= 1 && t <= int64(len(tags)) {
			ts = append(ts, tags[t-1])
		}
	}
	var owner User
	if o := getUser(l.UserID); o != nil {
		owner = userResp(o)
	}
	return Livestream{
		ID:           l.ID,
		Owner:        owner,
		Title:        l.Title,
		Description:  l.Description,
		PlaylistUrl:  l.PlaylistUrl,
		ThumbnailUrl: l.ThumbnailUrl,
		Tags:         ts,
		StartAt:      l.StartAt,
		EndAt:        l.EndAt,
	}
}

func lcResp(c *LC) Livecomment {
	var u User
	if cu := getUser(c.UserID); cu != nil {
		u = userResp(cu)
	}
	var ls Livestream
	if l := getLS(c.LivestreamID); l != nil {
		ls = lsResp(l)
	}
	return Livecomment{ID: c.ID, User: u, Livestream: ls, Comment: c.Comment, Tip: c.Tip, CreatedAt: c.CreatedAt}
}

func rxResp(r *RX) Reaction {
	var u User
	if cu := getUser(r.UserID); cu != nil {
		u = userResp(cu)
	}
	var ls Livestream
	if l := getLS(r.LivestreamID); l != nil {
		ls = lsResp(l)
	}
	return Reaction{ID: r.ID, EmojiName: r.EmojiName, User: u, Livestream: ls, CreatedAt: r.CreatedAt}
}

// ---------------- session ----------------

var secret = []byte("isucon13-inmem-secret-key")

const cookieName = "SESSIONID"

var sessCache sync.Map

func makeToken(uid int64, exp int64) string {
	payload := strconv.FormatInt(uid, 10) + "." + strconv.FormatInt(exp, 10)
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(payload))
	return payload + "." + hex.EncodeToString(m.Sum(nil)[:12])
}

// returns userID, httpStatus(0 if ok)
func auth(c echo.Context) (int64, int) {
	ck, err := c.Cookie(cookieName)
	if err != nil || ck.Value == "" {
		return 0, http.StatusForbidden
	}
	tok := ck.Value
	if v, ok := sessCache.Load(tok); ok {
		s := v.([2]int64)
		if time.Now().Unix() > s[1] {
			return 0, http.StatusUnauthorized
		}
		return s[0], 0
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return 0, http.StatusUnauthorized
	}
	uid, err1 := strconv.ParseInt(parts[0], 10, 64)
	exp, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil || makeToken(uid, exp) != tok {
		return 0, http.StatusUnauthorized
	}
	sessCache.Store(tok, [2]int64{uid, exp})
	if time.Now().Unix() > exp {
		return 0, http.StatusUnauthorized
	}
	return uid, 0
}

// ---------------- helpers ----------------

type ErrorResponse struct {
	Error string `json:"error"`
}

func errJSON(c echo.Context, code int, msg string) error {
	return c.JSON(code, &ErrorResponse{Error: msg})
}

func writeJSON(c echo.Context, code int, v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return errJSON(c, 500, err.Error())
	}
	return c.Blob(code, "application/json", b)
}

func paramID(c echo.Context, name string) (int64, bool) {
	v, err := strconv.ParseInt(c.Param(name), 10, 64)
	return v, err == nil
}

// ---------------- handlers ----------------

func initializeHandler(c echo.Context) error {
	mu.Lock()
	defer mu.Unlock()
	if err := loadAll(); err != nil {
		return errJSON(c, 500, err.Error())
	}
	openLog(true)
	return c.JSON(http.StatusOK, map[string]string{"language": "golang"})
}

func getTagHandler(c echo.Context) error {
	mu.RLock()
	ts := make([]Tag, len(tags))
	copy(ts, tags)
	mu.RUnlock()
	sort.Slice(ts, func(i, j int) bool { return ts[i].Name < ts[j].Name })
	return writeJSON(c, 200, map[string]interface{}{"tags": ts})
}

func getStreamerThemeHandler(c echo.Context) error {
	if _, st := auth(c); st != 0 {
		return errJSON(c, st, "unauthorized")
	}
	mu.RLock()
	u := userByName[c.Param("username")]
	if u == nil {
		mu.RUnlock()
		return errJSON(c, 404, "not found user that has the given username")
	}
	t := Theme{ID: u.ThemeID, DarkMode: u.Dark}
	mu.RUnlock()
	return writeJSON(c, 200, t)
}

type ReserveLivestreamRequest struct {
	Tags         []int64 `json:"tags"`
	Title        string  `json:"title"`
	Description  string  `json:"description"`
	PlaylistUrl  string  `json:"playlist_url"`
	ThumbnailUrl string  `json:"thumbnail_url"`
	StartAt      int64   `json:"start_at"`
	EndAt        int64   `json:"end_at"`
}

var (
	termStartAt = time.Date(2023, 11, 25, 1, 0, 0, 0, time.UTC).Unix()
	termEndAt   = time.Date(2024, 11, 25, 1, 0, 0, 0, time.UTC).Unix()
)

func reserveLivestreamHandler(c echo.Context) error {
	uid, st := auth(c)
	if st != 0 {
		return errJSON(c, st, "unauthorized")
	}
	var req ReserveLivestreamRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return errJSON(c, 400, "failed to decode the request body as json")
	}
	if req.StartAt >= termEndAt || req.EndAt <= termStartAt {
		return errJSON(c, 400, "bad reservation time range")
	}
	mu.Lock()
	lo, hi := slotRange(req.StartAt, req.EndAt)
	for i := lo; i <= hi; i++ {
		if slots[i] < 1 {
			mu.Unlock()
			return errJSON(c, 400, fmt.Sprintf("予約期間 %d ~ %dに対して、予約区間 %d ~ %dが予約できません", termStartAt, termEndAt, req.StartAt, req.EndAt))
		}
	}
	tg := req.Tags
	if tg == nil {
		tg = []int64{}
	}
	l := &LS{ID: nextLSID, UserID: uid, Title: req.Title, Description: req.Description, PlaylistUrl: req.PlaylistUrl, ThumbnailUrl: req.ThumbnailUrl, StartAt: req.StartAt, EndAt: req.EndAt, Tags: tg}
	applyLS(l)
	writeOp(&Op{T: "l", L: &RecLS{ID: l.ID, UserID: l.UserID, Title: l.Title, Description: l.Description, PlaylistUrl: l.PlaylistUrl, ThumbnailUrl: l.ThumbnailUrl, StartAt: l.StartAt, EndAt: l.EndAt, Tags: l.Tags}})
	resp := lsResp(l)
	mu.Unlock()
	return writeJSON(c, 201, resp)
}

func searchLivestreamsHandler(c echo.Context) error {
	tagName := c.QueryParam("tag")
	if dbgQ {
		pstMu.Lock()
		searchQ[c.QueryString()]++
		pstMu.Unlock()
	}
	limit := -1
	if tagName == "" && c.QueryParam("limit") != "" {
		v, err := strconv.Atoi(c.QueryParam("limit"))
		if err != nil {
			return errJSON(c, 400, "limit query parameter must be integer")
		}
		limit = v
	}
	mu.RLock()
	out := make([]Livestream, 0, 64)
	if tagName != "" {
		if tid, ok := tagByName[tagName]; ok {
			ids := tagIndex[tid]
			for i := len(ids) - 1; i >= 0; i-- {
				if l := getLS(ids[i]); l != nil {
					out = append(out, lsResp(l))
				}
			}
		}
	} else {
		for i := len(streams) - 1; i >= 1; i-- {
			if limit >= 0 && len(out) >= limit {
				break
			}
			if l := streams[i]; l != nil {
				out = append(out, lsResp(l))
			}
		}
	}
	mu.RUnlock()
	return writeJSON(c, 200, out)
}

func getMyLivestreamsHandler(c echo.Context) error {
	uid, st := auth(c)
	if st != 0 {
		return errJSON(c, st, "unauthorized")
	}
	mu.RLock()
	out := make([]Livestream, 0, 8)
	if u := getUser(uid); u != nil {
		for _, l := range u.Streams {
			out = append(out, lsResp(l))
		}
	}
	mu.RUnlock()
	return writeJSON(c, 200, out)
}

func getUserLivestreamsHandler(c echo.Context) error {
	if _, st := auth(c); st != 0 {
		return errJSON(c, st, "unauthorized")
	}
	mu.RLock()
	u := userByName[c.Param("username")]
	if u == nil {
		mu.RUnlock()
		return errJSON(c, 404, "user not found")
	}
	out := make([]Livestream, 0, len(u.Streams))
	for _, l := range u.Streams {
		out = append(out, lsResp(l))
	}
	mu.RUnlock()
	return writeJSON(c, 200, out)
}

func enterLivestreamHandler(c echo.Context) error {
	uid, st := auth(c)
	if st != 0 {
		return errJSON(c, st, "unauthorized")
	}
	lid, ok := paramID(c, "livestream_id")
	if !ok {
		return errJSON(c, 400, "livestream_id must be integer")
	}
	mu.Lock()
	applyEnter(uid, lid)
	writeOp(&Op{T: "en", UI: uid, LI: lid})
	mu.Unlock()
	return c.NoContent(200)
}

func exitLivestreamHandler(c echo.Context) error {
	uid, st := auth(c)
	if st != 0 {
		return errJSON(c, st, "unauthorized")
	}
	lid, ok := paramID(c, "livestream_id")
	if !ok {
		return errJSON(c, 400, "livestream_id in path must be integer")
	}
	mu.Lock()
	applyExit(uid, lid)
	writeOp(&Op{T: "ex", UI: uid, LI: lid})
	mu.Unlock()
	return c.NoContent(200)
}

func getLivestreamHandler(c echo.Context) error {
	if _, st := auth(c); st != 0 {
		return errJSON(c, st, "unauthorized")
	}
	lid, ok := paramID(c, "livestream_id")
	if !ok {
		return errJSON(c, 400, "livestream_id in path must be integer")
	}
	mu.RLock()
	l := getLS(lid)
	if l == nil {
		mu.RUnlock()
		return errJSON(c, 404, "not found livestream that has the given id")
	}
	resp := lsResp(l)
	mu.RUnlock()
	return writeJSON(c, 200, resp)
}

func getLivecommentReportsHandler(c echo.Context) error {
	uid, st := auth(c)
	if st != 0 {
		return errJSON(c, st, "unauthorized")
	}
	lid, ok := paramID(c, "livestream_id")
	if !ok {
		return errJSON(c, 400, "livestream_id in path must be integer")
	}
	mu.RLock()
	l := getLS(lid)
	if l == nil {
		mu.RUnlock()
		return errJSON(c, 404, "livestream not found")
	}
	if l.UserID != uid {
		mu.RUnlock()
		return errJSON(c, 403, "can't get other streamer's livecomment reports")
	}
	out := make([]LivecommentReport, 0, len(l.Reports))
	for _, r := range l.Reports {
		lc := comments[r.LivecommentID]
		if lc == nil {
			continue
		}
		var rep User
		if ru := getUser(r.UserID); ru != nil {
			rep = userResp(ru)
		}
		out = append(out, LivecommentReport{ID: r.ID, Reporter: rep, Livecomment: lcResp(lc), CreatedAt: r.CreatedAt})
	}
	mu.RUnlock()
	return writeJSON(c, 200, out)
}

func getLivecommentsHandler(c echo.Context) error {
	if _, st := auth(c); st != 0 {
		return errJSON(c, st, "unauthorized")
	}
	lid, ok := paramID(c, "livestream_id")
	if !ok {
		return errJSON(c, 400, "livestream_id in path must be integer")
	}
	limit := -1
	if c.QueryParam("limit") != "" {
		v, err := strconv.Atoi(c.QueryParam("limit"))
		if err != nil {
			return errJSON(c, 400, "limit query parameter must be integer")
		}
		limit = v
	}
	mu.RLock()
	out := make([]Livecomment, 0, 16)
	if l := getLS(lid); l != nil {
		for i := len(l.Comments) - 1; i >= 0; i-- {
			if limit >= 0 && len(out) >= limit {
				break
			}
			out = append(out, lcResp(l.Comments[i]))
		}
	}
	mu.RUnlock()
	return writeJSON(c, 200, out)
}

func getNgwords(c echo.Context) error {
	uid, st := auth(c)
	if st != 0 {
		return errJSON(c, st, "unauthorized")
	}
	lid, ok := paramID(c, "livestream_id")
	if !ok {
		return errJSON(c, 400, "livestream_id in path must be integer")
	}
	mu.RLock()
	out := make([]NGWord, 0, 8)
	if l := getLS(lid); l != nil {
		for _, n := range l.NG {
			if n.UserID == uid {
				out = append(out, *n)
			}
		}
	}
	mu.RUnlock()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt > out[j].CreatedAt
		}
		return out[i].ID > out[j].ID
	})
	return writeJSON(c, 200, out)
}

type PostLivecommentRequest struct {
	Comment string `json:"comment"`
	Tip     int64  `json:"tip"`
}

func postLivecommentHandler(c echo.Context) error {
	uid, st := auth(c)
	if st != 0 {
		return errJSON(c, st, "unauthorized")
	}
	lid, ok := paramID(c, "livestream_id")
	if !ok {
		return errJSON(c, 400, "livestream_id in path must be integer")
	}
	var req PostLivecommentRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return errJSON(c, 400, "failed to decode the request body as json")
	}
	mu.Lock()
	l := getLS(lid)
	if l == nil {
		mu.Unlock()
		return errJSON(c, 404, "livestream not found")
	}
	for _, n := range l.NG {
		if n.UserID == l.UserID && ciContains(req.Comment, n.Word) {
			mu.Unlock()
			return errJSON(c, 400, "このコメントがスパム判定されました")
		}
	}
	lc := &LC{ID: nextLCID, UserID: uid, LivestreamID: lid, Comment: req.Comment, Tip: req.Tip, CreatedAt: time.Now().Unix()}
	applyLC(lc)
	writeOp(&Op{T: "c", C: lc})
	resp := lcResp(lc)
	mu.Unlock()
	return writeJSON(c, 201, resp)
}

func reportLivecommentHandler(c echo.Context) error {
	uid, st := auth(c)
	if st != 0 {
		return errJSON(c, st, "unauthorized")
	}
	lid, ok := paramID(c, "livestream_id")
	if !ok {
		return errJSON(c, 400, "livestream_id in path must be integer")
	}
	cid, ok := paramID(c, "livecomment_id")
	if !ok {
		return errJSON(c, 400, "livecomment_id in path must be integer")
	}
	mu.Lock()
	if getLS(lid) == nil {
		mu.Unlock()
		return errJSON(c, 404, "livestream not found")
	}
	lc := comments[cid]
	if lc == nil {
		mu.Unlock()
		return errJSON(c, 404, "livecomment not found")
	}
	r := &RP{ID: nextRPID, UserID: uid, LivestreamID: lid, LivecommentID: cid, CreatedAt: time.Now().Unix()}
	applyRP(r)
	writeOp(&Op{T: "p", P: r})
	var rep User
	if ru := getUser(uid); ru != nil {
		rep = userResp(ru)
	}
	resp := LivecommentReport{ID: r.ID, Reporter: rep, Livecomment: lcResp(lc), CreatedAt: r.CreatedAt}
	mu.Unlock()
	return writeJSON(c, 201, resp)
}

type ModerateRequest struct {
	NGWord string `json:"ng_word"`
}

func moderateHandler(c echo.Context) error {
	uid, st := auth(c)
	if st != 0 {
		return errJSON(c, st, "unauthorized")
	}
	lid, ok := paramID(c, "livestream_id")
	if !ok {
		return errJSON(c, 400, "livestream_id in path must be integer")
	}
	var req ModerateRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return errJSON(c, 400, "failed to decode the request body as json")
	}
	mu.Lock()
	l := getLS(lid)
	if l == nil || l.UserID != uid {
		mu.Unlock()
		return errJSON(c, 400, "A streamer can't moderate livestreams that other streamers own")
	}
	n := &NGWord{ID: nextNGID, UserID: uid, LivestreamID: lid, Word: req.NGWord, CreatedAt: time.Now().Unix()}
	applyNG(n)
	writeOp(&Op{T: "n", N: n})
	mu.Unlock()
	return writeJSON(c, 201, map[string]interface{}{"word_id": n.ID})
}

func getReactionsHandler(c echo.Context) error {
	if _, st := auth(c); st != 0 {
		return errJSON(c, st, "unauthorized")
	}
	lid, ok := paramID(c, "livestream_id")
	if !ok {
		return errJSON(c, 400, "livestream_id in path must be integer")
	}
	limit := -1
	if c.QueryParam("limit") != "" {
		v, err := strconv.Atoi(c.QueryParam("limit"))
		if err != nil {
			return errJSON(c, 400, "limit query parameter must be integer")
		}
		limit = v
	}
	mu.RLock()
	out := make([]Reaction, 0, 16)
	if l := getLS(lid); l != nil {
		for i := len(l.Reactions) - 1; i >= 0; i-- {
			if limit >= 0 && len(out) >= limit {
				break
			}
			out = append(out, rxResp(l.Reactions[i]))
		}
	}
	mu.RUnlock()
	return writeJSON(c, 200, out)
}

type PostReactionRequest struct {
	EmojiName string `json:"emoji_name"`
}

func postReactionHandler(c echo.Context) error {
	lid, ok := paramID(c, "livestream_id")
	if !ok {
		return errJSON(c, 400, "livestream_id in path must be integer")
	}
	uid, st := auth(c)
	if st != 0 {
		return errJSON(c, st, "unauthorized")
	}
	var req PostReactionRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return errJSON(c, 400, "failed to decode the request body as json")
	}
	mu.Lock()
	if getLS(lid) == nil {
		mu.Unlock()
		return errJSON(c, 404, "livestream not found")
	}
	r := &RX{ID: nextRXID, UserID: uid, LivestreamID: lid, EmojiName: req.EmojiName, CreatedAt: time.Now().Unix()}
	applyRX(r)
	writeOp(&Op{T: "r", R: r})
	resp := rxResp(r)
	mu.Unlock()
	return writeJSON(c, 201, resp)
}

type PostUserRequest struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Password    string `json:"password"`
	Theme       struct {
		DarkMode bool `json:"dark_mode"`
	} `json:"theme"`
}

func registerHandler(c echo.Context) error {
	var req PostUserRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return errJSON(c, 400, "failed to decode the request body as json")
	}
	if req.Name == "pipe" {
		return errJSON(c, 400, "the username 'pipe' is reserved")
	}
	hashed, err := bcryptHash(req.Password)
	if err != nil {
		return errJSON(c, 500, err.Error())
	}
	loginCache.Store(hashed+"\x00"+req.Password, true)
	mu.Lock()
	if userByName[req.Name] != nil {
		mu.Unlock()
		return errJSON(c, 500, "failed to insert user: duplicate entry")
	}
	r := &RecUser{ID: nextUserID, Name: req.Name, DisplayName: req.DisplayName, Description: req.Description, Hash: string(hashed), ThemeID: nextThemeID, Dark: req.Theme.DarkMode}
	applyUser(r)
	writeOp(&Op{T: "u", U: r})
	resp := userResp(getUser(r.ID))
	mu.Unlock()
	return writeJSON(c, 201, resp)
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

var loginCache sync.Map // hash+"\x00"+password -> true

func loginHandler(c echo.Context) error {
	var req LoginRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return errJSON(c, 400, "failed to decode the request body as json")
	}
	mu.RLock()
	u := userByName[req.Username]
	var hash string
	var uid int64
	if u != nil {
		hash = u.Hash
		uid = u.ID
	}
	mu.RUnlock()
	if u == nil {
		return errJSON(c, 401, "invalid username or password")
	}
	key := hash + "\x00" + req.Password
	if _, ok := loginCache.Load(key); !ok {
		if !bcryptCompare(hash, req.Password) {
			return errJSON(c, 401, "invalid username or password")
		}
		loginCache.Store(key, true)
	}
	exp := time.Now().Add(time.Hour).Unix()
	tok := makeToken(uid, exp)
	c.SetCookie(&http.Cookie{
		Name:    cookieName,
		Value:   tok,
		Path:    "/",
		Domain:  "u.isucon.dev",
		MaxAge:  60000,
		Expires: time.Now().Add(60000 * time.Second),
	})
	return c.NoContent(200)
}

func getMeHandler(c echo.Context) error {
	uid, st := auth(c)
	if st != 0 {
		return errJSON(c, st, "unauthorized")
	}
	mu.RLock()
	u := getUser(uid)
	if u == nil {
		mu.RUnlock()
		return errJSON(c, 404, "not found user that has the userid in session")
	}
	resp := userResp(u)
	mu.RUnlock()
	return writeJSON(c, 200, resp)
}

func getUserHandler(c echo.Context) error {
	if _, st := auth(c); st != 0 {
		return errJSON(c, st, "unauthorized")
	}
	mu.RLock()
	u := userByName[c.Param("username")]
	if u == nil {
		mu.RUnlock()
		return errJSON(c, 404, "not found user that has the given username")
	}
	resp := userResp(u)
	mu.RUnlock()
	return writeJSON(c, 200, resp)
}

var fallbackImage []byte
var dbgQ = true
var searchQ = map[string]int{}
var iconMaxAge = os.Getenv("ICON_CACHE_CONTROL")

func getIconHandler(c echo.Context) error {
	mu.RLock()
	u := userByName[c.Param("username")]
	if u == nil {
		mu.RUnlock()
		return errJSON(c, 404, "not found user that has the given username")
	}
	img, h := u.Icon, u.IconHash
	mu.RUnlock()
	hd := c.Response().Header()
	hd.Set("ETag", "\""+h+"\"")
	if iconMaxAge != "" {
		hd.Set("Cache-Control", iconMaxAge)
	}
	if inm := c.Request().Header.Get("If-None-Match"); inm != "" {
		if strings.Trim(inm, "\"") == h {
			return c.NoContent(http.StatusNotModified)
		}
	}
	if img == nil {
		img = fallbackImage
	}
	return c.Blob(200, "image/jpeg", img)
}

type PostIconRequest struct {
	Image []byte `json:"image"`
}

func postIconHandler(c echo.Context) error {
	uid, st := auth(c)
	if st != 0 {
		return errJSON(c, st, "unauthorized")
	}
	var req PostIconRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return errJSON(c, 400, "failed to decode the request body as json")
	}
	mu.Lock()
	r := &RecIcon{UserID: uid, IconID: nextIconID, Image: req.Image}
	applyIcon(r)
	writeOp(&Op{T: "i", I: r})
	mu.Unlock()
	return writeJSON(c, 201, map[string]int64{"id": r.IconID})
}

type UserStatistics struct {
	Rank              int64  `json:"rank"`
	ViewersCount      int64  `json:"viewers_count"`
	TotalReactions    int64  `json:"total_reactions"`
	TotalLivecomments int64  `json:"total_livecomments"`
	TotalTip          int64  `json:"total_tip"`
	FavoriteEmoji     string `json:"favorite_emoji"`
}

func getUserStatisticsHandler(c echo.Context) error {
	if _, st := auth(c); st != 0 {
		return errJSON(c, st, "unauthorized")
	}
	name := c.Param("username")
	mu.RLock()
	u := userByName[name]
	if u == nil {
		mu.RUnlock()
		return errJSON(c, 400, "not found user that has the given username")
	}
	score := u.ReactRecv + u.TipRecv
	var rank int64 = 1
	for _, o := range users {
		if o == nil || o == u {
			continue
		}
		s := o.ReactRecv + o.TipRecv
		if s > score || (s == score && o.Name > name) {
			rank++
		}
	}
	var viewers, lcs int64
	for _, l := range u.Streams {
		viewers += l.Viewers
		lcs += int64(len(l.Comments))
	}
	fav := ""
	var favN int64
	for e, n := range u.Emoji {
		if n > favN || (n == favN && e > fav) {
			fav, favN = e, n
		}
	}
	resp := UserStatistics{Rank: rank, ViewersCount: viewers, TotalReactions: u.ReactRecv, TotalLivecomments: lcs, TotalTip: u.TipRecv, FavoriteEmoji: fav}
	mu.RUnlock()
	return writeJSON(c, 200, resp)
}

type LivestreamStatistics struct {
	Rank           int64 `json:"rank"`
	ViewersCount   int64 `json:"viewers_count"`
	TotalReactions int64 `json:"total_reactions"`
	TotalReports   int64 `json:"total_reports"`
	MaxTip         int64 `json:"max_tip"`
}

func getLivestreamStatisticsHandler(c echo.Context) error {
	if _, st := auth(c); st != 0 {
		return errJSON(c, st, "unauthorized")
	}
	lid, ok := paramID(c, "livestream_id")
	if !ok {
		return errJSON(c, 400, "livestream_id in path must be integer")
	}
	mu.RLock()
	l := getLS(lid)
	if l == nil {
		mu.RUnlock()
		return errJSON(c, 400, "cannot get stats of not found livestream")
	}
	score := l.ReactCount + l.TipSum
	var rank int64 = 1
	for _, o := range streams {
		if o == nil || o == l {
			continue
		}
		s := o.ReactCount + o.TipSum
		if s > score || (s == score && o.ID > l.ID) {
			rank++
		}
	}
	var maxTip int64
	for _, cm := range l.Comments {
		if cm.Tip > maxTip {
			maxTip = cm.Tip
		}
	}
	resp := LivestreamStatistics{Rank: rank, ViewersCount: l.Viewers, TotalReactions: l.ReactCount, TotalReports: int64(len(l.Reports)), MaxTip: maxTip}
	mu.RUnlock()
	return writeJSON(c, 200, resp)
}

func getPaymentHandler(c echo.Context) error {
	mu.RLock()
	t := totalTip
	mu.RUnlock()
	return writeJSON(c, 200, map[string]int64{"total_tip": t})
}

// ---------------- main ----------------

func connectDB() *sqlx.DB {
	get := func(k, d string) string {
		if v, ok := os.LookupEnv(k); ok {
			return v
		}
		return d
	}
	dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s?parseTime=true&interpolateParams=true",
		get("ISUCON13_MYSQL_DIALCONFIG_USER", "isucon"), get("ISUCON13_MYSQL_DIALCONFIG_PASSWORD", "isucon"),
		net.JoinHostPort(get("ISUCON13_MYSQL_DIALCONFIG_ADDRESS", "127.0.0.1"), get("ISUCON13_MYSQL_DIALCONFIG_PORT", "3306")),
		get("ISUCON13_MYSQL_DIALCONFIG_DATABASE", "isupipe"))
	for i := 0; ; i++ {
		d, err := sqlx.Open("mysql", dsn)
		if err == nil {
			if err = d.Ping(); err == nil {
				return d
			}
		}
		log.Printf("db connect retry: %v", err)
		time.Sleep(time.Second)
	}
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	if wa := os.Getenv("WORKER_LISTEN"); wa != "" {
		runWorker(wa)
		return
	}
	initWorkers()
	var err error
	fallbackImage, err = os.ReadFile("/home/isucon/webapp/img/NoImage.jpg")
	if err != nil {
		log.Fatal(err)
	}
	h := sha256.Sum256(fallbackImage)
	fallbackHash = hex.EncodeToString(h[:])

	db = connectDB()
	os.MkdirAll("/home/isucon/data", 0755)
	mu.Lock()
	if err := loadAll(); err != nil {
		log.Fatal(err)
	}
	replayLog()
	mu.Unlock()
	openLog(false)
	go logFlusher()
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
		<-ch
		flushLog()
		os.Exit(0)
	}()

	startDNS()
	go func() { log.Println(http.ListenAndServe("127.0.0.1:6060", nil)) }()

	e := echo.New()
	e.HideBanner = true
	e.Use(statsMW)
	e.GET("/debug/stats", statsHandler)
	e.POST("/api/initialize", initializeHandler)
	e.GET("/api/tag", getTagHandler)
	e.GET("/api/user/:username/theme", getStreamerThemeHandler)
	e.POST("/api/livestream/reservation", reserveLivestreamHandler)
	e.GET("/api/livestream/search", searchLivestreamsHandler)
	e.GET("/api/livestream", getMyLivestreamsHandler)
	e.GET("/api/user/:username/livestream", getUserLivestreamsHandler)
	e.GET("/api/livestream/:livestream_id", getLivestreamHandler)
	e.GET("/api/livestream/:livestream_id/livecomment", getLivecommentsHandler)
	e.POST("/api/livestream/:livestream_id/livecomment", postLivecommentHandler)
	e.POST("/api/livestream/:livestream_id/reaction", postReactionHandler)
	e.GET("/api/livestream/:livestream_id/reaction", getReactionsHandler)
	e.GET("/api/livestream/:livestream_id/report", getLivecommentReportsHandler)
	e.GET("/api/livestream/:livestream_id/ngwords", getNgwords)
	e.POST("/api/livestream/:livestream_id/livecomment/:livecomment_id/report", reportLivecommentHandler)
	e.POST("/api/livestream/:livestream_id/moderate", moderateHandler)
	e.POST("/api/livestream/:livestream_id/enter", enterLivestreamHandler)
	e.DELETE("/api/livestream/:livestream_id/exit", exitLivestreamHandler)
	e.POST("/api/register", registerHandler)
	e.POST("/api/login", loginHandler)
	e.GET("/api/user/me", getMeHandler)
	e.GET("/api/user/:username", getUserHandler)
	e.GET("/api/user/:username/statistics", getUserStatisticsHandler)
	e.GET("/api/user/:username/icon", getIconHandler)
	e.POST("/api/icon", postIconHandler)
	e.GET("/api/livestream/:livestream_id/statistics", getLivestreamStatisticsHandler)
	e.GET("/api/payment", getPaymentHandler)

	if tlsAddr := os.Getenv("TLS_ADDR"); tlsAddr != "" {
		pub := "/home/isucon/webapp/public"
		e.GET("/*", func(c echo.Context) error {
			p := filepath.Clean("/" + c.Param("*"))
			fp := filepath.Join(pub, p)
			if st, err := os.Stat(fp); err == nil && !st.IsDir() {
				return c.File(fp)
			}
			return c.File(filepath.Join(pub, "index.html"))
		})
		go func() {
			srv := &http.Server{Addr: tlsAddr, Handler: e, IdleTimeout: 120 * time.Second, TLSConfig: tlsConfig("/home/isucon/app/tls/cert.pem", "/home/isucon/app/tls/key.pem")}
			if os.Getenv("NO_H2") == "1" {
				srv.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){}
			}
			log.Fatal(srv.ListenAndServeTLS("", ""))
		}()
	}
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	log.Fatal(e.Start(addr))
}

func ciContains(s, sub string) bool {
	if strings.Contains(s, sub) {
		return true
	}
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}
