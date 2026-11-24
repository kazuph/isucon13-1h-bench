package main

import (
	"log"
	"os"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
)

// ---------- response types ----------

type UserResp struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	DisplayName string    `json:"display_name,omitempty"`
	Description string    `json:"description,omitempty"`
	Theme       ThemeResp `json:"theme,omitempty"`
	IconHash    string    `json:"icon_hash,omitempty"`
}

type ThemeResp struct {
	ID       int64 `json:"id"`
	DarkMode bool  `json:"dark_mode"`
}

type LivestreamResp struct {
	ID           int64    `json:"id"`
	Owner        UserResp `json:"owner"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	PlaylistUrl  string   `json:"playlist_url"`
	ThumbnailUrl string   `json:"thumbnail_url"`
	Tags         []Tag    `json:"tags"`
	StartAt      int64    `json:"start_at"`
	EndAt        int64    `json:"end_at"`
}

type LivecommentResp struct {
	ID         int64          `json:"id"`
	User       UserResp       `json:"user"`
	Livestream LivestreamResp `json:"livestream"`
	Comment    string         `json:"comment"`
	Tip        int64          `json:"tip"`
	CreatedAt  int64          `json:"created_at"`
}

type ReactionResp struct {
	ID         int64          `json:"id"`
	EmojiName  string         `json:"emoji_name"`
	User       UserResp       `json:"user"`
	Livestream LivestreamResp `json:"livestream"`
	CreatedAt  int64          `json:"created_at"`
}

type ReportResp struct {
	ID          int64           `json:"id"`
	Reporter    UserResp        `json:"reporter"`
	Livecomment LivecommentResp `json:"livecomment"`
	CreatedAt   int64           `json:"created_at"`
}

func userResp(u *User) UserResp {
	if u == nil {
		return UserResp{}
	}
	return UserResp{ID: u.ID, Name: u.Name, DisplayName: u.DisplayName, Description: u.Description, Theme: ThemeResp{ID: u.ThemeID, DarkMode: u.DarkMode}, IconHash: u.IconHash}
}

func streamResp(ls *Livestream) LivestreamResp {
	if ls == nil {
		return LivestreamResp{Tags: []Tag{}}
	}
	return LivestreamResp{ID: ls.ID, Owner: userResp(ls.Owner), Title: ls.Title, Description: ls.Description, PlaylistUrl: ls.PlaylistUrl, ThumbnailUrl: ls.ThumbnailUrl, Tags: ls.Tags, StartAt: ls.StartAt, EndAt: ls.EndAt}
}

func commentResp(cm *Comment) LivecommentResp {
	return LivecommentResp{ID: cm.ID, User: userResp(usersByID[cm.UserID]), Livestream: streamResp(streams[cm.LivestreamID]), Comment: cm.Comment, Tip: cm.Tip, CreatedAt: cm.CreatedAt}
}

func reactionResp(r *Reaction) ReactionResp {
	return ReactionResp{ID: r.ID, EmojiName: r.EmojiName, User: userResp(usersByID[r.UserID]), Livestream: streamResp(streams[r.LivestreamID]), CreatedAt: r.CreatedAt}
}

func reportResp(r *Report) ReportResp {
	rr := ReportResp{ID: r.ID, Reporter: userResp(usersByID[r.UserID]), CreatedAt: r.CreatedAt}
	if cm := commentsAll[r.LivecommentID]; cm != nil {
		rr.Livecomment = commentResp(cm)
	}
	return rr
}

func bad(code int, msg string) error { return echo.NewHTTPError(code, msg) }

func paramID(c echo.Context, name string) (int64, error) {
	v, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil {
		return 0, bad(http.StatusBadRequest, name+" in path must be integer")
	}
	return v, nil
}

func limitParam(c echo.Context) (int, error) {
	s := c.QueryParam("limit")
	if s == "" {
		return -1, nil
	}
	l, err := strconv.Atoi(s)
	if err != nil {
		return 0, bad(http.StatusBadRequest, "limit query parameter must be integer")
	}
	return l, nil
}

// ---------- top ----------

func getTagHandler(c echo.Context) error {
	mu.RLock()
	tags := make([]*Tag, len(tagList))
	copy(tags, tagList)
	mu.RUnlock()
	return c.JSON(http.StatusOK, map[string]any{"tags": tags})
}

func getStreamerThemeHandler(c echo.Context) error {
	if _, err := getSession(c); err != nil {
		return err
	}
	mu.RLock()
	u := usersByName[c.Param("username")]
	if u == nil {
		mu.RUnlock()
		return bad(http.StatusNotFound, "not found user that has the given username")
	}
	t := ThemeResp{ID: u.ThemeID, DarkMode: u.DarkMode}
	mu.RUnlock()
	return c.JSON(http.StatusOK, t)
}

// ---------- users ----------

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
		return bad(http.StatusBadRequest, "failed to decode the request body as json")
	}
	if req.Name == "pipe" {
		return bad(http.StatusBadRequest, "the username 'pipe' is reserved")
	}
	hashed, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.MinCost)
	if err != nil {
		return bad(http.StatusInternalServerError, "failed to generate hashed password")
	}
	mu.Lock()
	if usersByName[req.Name] != nil {
		mu.Unlock()
		return bad(http.StatusInternalServerError, "failed to insert user: duplicate")
	}
	nextUserID++
	nextThemeID++
	u := &User{ID: nextUserID, Name: req.Name, DisplayName: req.DisplayName, Description: req.Description, Password: string(hashed), ThemeID: nextThemeID, DarkMode: req.Theme.DarkMode, IconHash: fallbackImageHash, Emoji: map[string]int64{}, pwOK: req.Password}
	usersByID[u.ID] = u
	usersByName[u.Name] = u
	lowerNames[strings.ToLower(u.Name)] = true
	renderUser(u)
	resp := u.JS
	persist("INSERT INTO users (id, name, display_name, description, password) VALUES (?,?,?,?,?)", u.ID, u.Name, u.DisplayName, u.Description, u.Password)
	persist("INSERT INTO themes (id, user_id, dark_mode) VALUES (?,?,?)", u.ThemeID, u.ID, u.DarkMode)
	mu.Unlock()
	return jsonBytes(c, http.StatusCreated, resp)
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func loginHandler(c echo.Context) error {
	var req LoginRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return bad(http.StatusBadRequest, "failed to decode the request body as json")
	}
	mu.RLock()
	u := usersByName[req.Username]
	var hash, ok string
	var uid int64
	if u != nil {
		hash, ok, uid = u.Password, u.pwOK, u.ID
	}
	mu.RUnlock()
	if u == nil {
		return bad(http.StatusUnauthorized, "invalid username or password")
	}
	if ok == "" || ok != req.Password {
		if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
			return bad(http.StatusUnauthorized, "invalid username or password")
		}
		mu.Lock()
		u.pwOK = req.Password
		mu.Unlock()
	}
	exp := time.Now().Add(time.Hour).Unix()
	c.SetCookie(&http.Cookie{Name: cookieName, Value: signSession(uid, exp), Domain: "u.isucon.dev", Path: "/", MaxAge: 60000, Expires: time.Now().Add(60000 * time.Second)})
	return c.NoContent(http.StatusOK)
}

func getMeHandler(c echo.Context) error {
	uid, err := getSession(c)
	if err != nil {
		return err
	}
	mu.RLock()
	u := usersByID[uid]
	if u == nil {
		mu.RUnlock()
		return bad(http.StatusNotFound, "not found user that has the userid in session")
	}
	r := u.JS
	mu.RUnlock()
	return jsonBytes(c, http.StatusOK, r)
}

func getUserHandler(c echo.Context) error {
	if _, err := getSession(c); err != nil {
		return err
	}
	mu.RLock()
	u := usersByName[c.Param("username")]
	if u == nil {
		mu.RUnlock()
		return bad(http.StatusNotFound, "not found user that has the given username")
	}
	r := u.JS
	mu.RUnlock()
	return jsonBytes(c, http.StatusOK, r)
}

func getIconHandler(c echo.Context) error {
	mu.RLock()
	u := usersByName[c.Param("username")]
	if u == nil {
		mu.RUnlock()
		return bad(http.StatusNotFound, "not found user that has the given username")
	}
	img, hash := u.Icon, u.IconHash
	mu.RUnlock()
	if iconCacheControl != "" {
		hd := c.Response().Header()
		hd.Set("Cache-Control", iconCacheControl)
		hd.Set("ETag", `"`+hash+`"`)
	}
	if inm := c.Request().Header.Get("If-None-Match"); inm != "" {
		if strings.Trim(inm, "\" ") == hash {
			return c.NoContent(http.StatusNotModified)
		}
	}
	if img == nil {
		img = fallbackImage
	}
	return c.Blob(http.StatusOK, "image/jpeg", img)
}

type PostIconRequest struct {
	Image []byte `json:"image"`
}

func postIconHandler(c echo.Context) error {
	uid, err := getSession(c)
	if err != nil {
		return err
	}
	var req PostIconRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return bad(http.StatusBadRequest, "failed to decode the request body as json")
	}
	h := sha256.Sum256(req.Image)
	hs := hex.EncodeToString(h[:])
	mu.Lock()
	u := usersByID[uid]
	if u == nil {
		mu.Unlock()
		return bad(http.StatusNotFound, "user not found")
	}
	nextIconID++
	id := nextIconID
	u.Icon = req.Image
	u.IconHash = hs
	renderUser(u)
	for _, ls := range u.Streams {
		renderStream(ls)
	}
	persist("DELETE FROM icons WHERE user_id = ?", uid)
	persist("INSERT INTO icons (id, user_id, image) VALUES (?,?,?)", id, uid, req.Image)
	mu.Unlock()
	return c.JSON(http.StatusCreated, map[string]int64{"id": id})
}

// ---------- livestreams ----------

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
	uid, err := getSession(c)
	if err != nil {
		return err
	}
	var req ReserveLivestreamRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return bad(http.StatusBadRequest, "failed to decode the request body as json")
	}
	if req.StartAt >= termEndAt || req.EndAt <= termStartAt {
		return bad(http.StatusBadRequest, "bad reservation time range")
	}
	mu.Lock()
	// slots with start_at >= S and end_at <= E
	i0 := sort.Search(len(slots), func(i int) bool { return slots[i].StartAt >= req.StartAt })
	var hit []*Slot
	for i := i0; i < len(slots); i++ {
		s := slots[i]
		if s.EndAt <= req.EndAt {
			hit = append(hit, s)
		}
		if s.StartAt > req.EndAt {
			break
		}
	}
	for _, s := range hit {
		if s.Slot < 1 {
			mu.Unlock()
			return bad(http.StatusBadRequest, "予約期間に対して、予約区間が予約できません")
		}
	}
	for _, s := range hit {
		s.Slot--
	}
	persist("UPDATE reservation_slots SET slot = slot - 1 WHERE start_at >= ? AND end_at <= ?", req.StartAt, req.EndAt)
	owner := usersByID[uid]
	nextStreamID++
	ls := &Livestream{ID: nextStreamID, Owner: owner, Title: req.Title, Description: req.Description, PlaylistUrl: req.PlaylistUrl, ThumbnailUrl: req.ThumbnailUrl, StartAt: req.StartAt, EndAt: req.EndAt, Tags: []Tag{}, ViewerMap: map[int64]int64{}}
	streams[ls.ID] = ls
	streamList = append(streamList, ls)
	if owner != nil {
		owner.Streams = append(owner.Streams, ls)
	}
	persist("INSERT INTO livestreams (id, user_id, title, description, playlist_url, thumbnail_url, start_at, end_at) VALUES (?,?,?,?,?,?,?,?)", ls.ID, uid, ls.Title, ls.Description, ls.PlaylistUrl, ls.ThumbnailUrl, ls.StartAt, ls.EndAt)
	for _, tid := range req.Tags {
		if t := tagsByID[tid]; t != nil {
			ls.Tags = append(ls.Tags, *t)
		}
		tagStreams[tid] = append(tagStreams[tid], ls.ID)
		persist("INSERT INTO livestream_tags (livestream_id, tag_id) VALUES (?,?)", ls.ID, tid)
	}
	renderStream(ls)
	resp := ls.JS
	mu.Unlock()
	return jsonBytes(c, http.StatusCreated, resp)
}

func searchLivestreamsHandler(c echo.Context) error {
	tag := c.QueryParam("tag")
	b := make([]byte, 0, 64*1024)
	b = append(b, '[')
	first := true
	add := func(ls *Livestream) {
		if !first {
			b = append(b, ',')
		}
		first = false
		if ls != nil && ls.JS != nil {
			b = append(b, ls.JS...)
		} else {
			b = append(b, emptyStreamJS...)
		}
	}
	if tag != "" {
		mu.RLock()
		if t := tagsByName[tag]; t != nil {
			ids := tagStreams[t.ID]
			for i := len(ids) - 1; i >= 0; i-- {
				add(streams[ids[i]])
			}
		}
		mu.RUnlock()
	} else {
		limit, err := limitParam(c)
		if err != nil {
			return err
		}
		mu.RLock()
		n := len(streamList)
		if limit >= 0 && limit < n {
			n = limit
		}
		for i := len(streamList) - 1; i >= len(streamList)-n; i-- {
			add(streamList[i])
		}
		mu.RUnlock()
	}
	b = append(b, ']')
	return jsonBytes(c, http.StatusOK, b)
}

func userStreams(u *User) []byte {
	b := make([]byte, 0, 4096)
	b = append(b, '[')
	for i, ls := range u.Streams {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, ls.JS...)
	}
	return append(b, ']')
}

func getMyLivestreamsHandler(c echo.Context) error {
	uid, err := getSession(c)
	if err != nil {
		return err
	}
	mu.RLock()
	out := []byte("[]")
	if u := usersByID[uid]; u != nil {
		out = userStreams(u)
	}
	mu.RUnlock()
	return jsonBytes(c, http.StatusOK, out)
}

func getUserLivestreamsHandler(c echo.Context) error {
	if _, err := getSession(c); err != nil {
		return err
	}
	mu.RLock()
	u := usersByName[c.Param("username")]
	if u == nil {
		mu.RUnlock()
		return bad(http.StatusNotFound, "user not found")
	}
	out := userStreams(u)
	mu.RUnlock()
	return jsonBytes(c, http.StatusOK, out)
}

func enterLivestreamHandler(c echo.Context) error {
	uid, err := getSession(c)
	if err != nil {
		return err
	}
	lid, err := paramID(c, "livestream_id")
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	mu.Lock()
	if ls := streams[lid]; ls != nil {
		ls.ViewerMap[uid]++
		ls.Viewers++
		if ls.Owner != nil {
			ls.Owner.Viewers++
		}
	}
	persist("INSERT INTO livestream_viewers_history (user_id, livestream_id, created_at) VALUES (?,?,?)", uid, lid, now)
	mu.Unlock()
	return c.NoContent(http.StatusOK)
}

func exitLivestreamHandler(c echo.Context) error {
	uid, err := getSession(c)
	if err != nil {
		return err
	}
	lid, err := paramID(c, "livestream_id")
	if err != nil {
		return err
	}
	mu.Lock()
	if ls := streams[lid]; ls != nil {
		n := ls.ViewerMap[uid]
		if n > 0 {
			delete(ls.ViewerMap, uid)
			ls.Viewers -= n
			if ls.Owner != nil {
				ls.Owner.Viewers -= n
			}
		}
	}
	persist("DELETE FROM livestream_viewers_history WHERE user_id = ? AND livestream_id = ?", uid, lid)
	mu.Unlock()
	return c.NoContent(http.StatusOK)
}

func getLivestreamHandler(c echo.Context) error {
	if _, err := getSession(c); err != nil {
		return err
	}
	lid, err := paramID(c, "livestream_id")
	if err != nil {
		return err
	}
	mu.RLock()
	ls := streams[lid]
	if ls == nil {
		mu.RUnlock()
		return bad(http.StatusNotFound, "not found livestream that has the given id")
	}
	r := ls.JS
	mu.RUnlock()
	return jsonBytes(c, http.StatusOK, r)
}

func getLivecommentReportsHandler(c echo.Context) error {
	uid, err := getSession(c)
	if err != nil {
		return err
	}
	lid, err := paramID(c, "livestream_id")
	if err != nil {
		return err
	}
	mu.RLock()
	defer mu.RUnlock()
	ls := streams[lid]
	if ls == nil {
		return bad(http.StatusInternalServerError, "failed to get livestream")
	}
	if ls.Owner == nil || ls.Owner.ID != uid {
		return bad(http.StatusForbidden, "can't get other streamer's livecomment reports")
	}
	b := make([]byte, 0, 4096)
	b = append(b, '[')
	for i, r := range ls.Reports {
		if i > 0 {
			b = append(b, ',')
		}
		b = appendReport(b, r)
	}
	b = append(b, ']')
	return jsonBytes(c, http.StatusOK, b)
}

// ---------- livecomments ----------

func getLivecommentsHandler(c echo.Context) error {
	if _, err := getSession(c); err != nil {
		return err
	}
	lid, err := paramID(c, "livestream_id")
	if err != nil {
		return err
	}
	limit, err := limitParam(c)
	if err != nil {
		return err
	}
	b := make([]byte, 0, 32*1024)
	b = append(b, '[')
	mu.RLock()
	if ls := streams[lid]; ls != nil {
		cs := ls.Comments
		n := len(cs)
		if limit >= 0 && limit < n {
			n = limit
		}
		for i := len(cs) - 1; i >= len(cs)-n; i-- {
			if i != len(cs)-1 {
				b = append(b, ',')
			}
			b = appendComment(b, cs[i])
		}
	}
	mu.RUnlock()
	b = append(b, ']')
	return jsonBytes(c, http.StatusOK, b)
}

func getNgwords(c echo.Context) error {
	uid, err := getSession(c)
	if err != nil {
		return err
	}
	lid, err := paramID(c, "livestream_id")
	if err != nil {
		return err
	}
	mu.RLock()
	out := []NGWord{}
	if ls := streams[lid]; ls != nil {
		for i := len(ls.NGWords) - 1; i >= 0; i-- {
			if w := ls.NGWords[i]; w.UserID == uid {
				out = append(out, *w)
			}
		}
	}
	mu.RUnlock()
	return c.JSON(http.StatusOK, out)
}

type PostLivecommentRequest struct {
	Comment string `json:"comment"`
	Tip     int64  `json:"tip"`
}

func postLivecommentHandler(c echo.Context) error {
	uid, err := getSession(c)
	if err != nil {
		return err
	}
	lid, err := paramID(c, "livestream_id")
	if err != nil {
		return err
	}
	var req PostLivecommentRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return bad(http.StatusBadRequest, "failed to decode the request body as json")
	}
	now := time.Now().Unix()
	mu.Lock()
	ls := streams[lid]
	if ls == nil {
		mu.Unlock()
		return bad(http.StatusNotFound, "livestream not found")
	}
	ownerID := int64(0)
	if ls.Owner != nil {
		ownerID = ls.Owner.ID
	}
	for _, w := range ls.NGWords {
		if w.UserID == ownerID && strings.Contains(req.Comment, w.Word) {
			mu.Unlock()
			return bad(http.StatusBadRequest, "このコメントがスパム判定されました")
		}
	}
	if debugSpam {
		var ws []string
		for _, w := range ls.NGWords {
			if w.ID > initNGMax {
				ws = append(ws, w.Word)
			}
		}
		if len(ws) > 0 {
			bb, _ := json.Marshal(map[string]any{"c": req.Comment, "w": ws})
			log.Printf("SPAMDBG %s", bb)
		}
	}
	nextCommentID++
	cm := &Comment{ID: nextCommentID, UserID: uid, LivestreamID: lid, Comment: req.Comment, Tip: req.Tip, CreatedAt: now}
	addCommentMem(cm)
	persist("INSERT INTO livecomments (id, user_id, livestream_id, comment, tip, created_at) VALUES (?,?,?,?,?,?)", cm.ID, uid, lid, cm.Comment, cm.Tip, now)
	resp := appendComment(make([]byte, 0, 2048), cm)
	mu.Unlock()
	return jsonBytes(c, http.StatusCreated, resp)
}

func reportLivecommentHandler(c echo.Context) error {
	uid, err := getSession(c)
	if err != nil {
		return err
	}
	lid, err := paramID(c, "livestream_id")
	if err != nil {
		return err
	}
	cid, err := paramID(c, "livecomment_id")
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	mu.Lock()
	ls := streams[lid]
	if ls == nil {
		mu.Unlock()
		return bad(http.StatusNotFound, "livestream not found")
	}
	if commentsAll[cid] == nil {
		mu.Unlock()
		return bad(http.StatusNotFound, "livecomment not found")
	}
	nextReportID++
	r := &Report{ID: nextReportID, UserID: uid, LivestreamID: lid, LivecommentID: cid, CreatedAt: now}
	ls.Reports = append(ls.Reports, r)
	persist("INSERT INTO livecomment_reports (id, user_id, livestream_id, livecomment_id, created_at) VALUES (?,?,?,?,?)", r.ID, uid, lid, cid, now)
	resp := appendReport(make([]byte, 0, 4096), r)
	mu.Unlock()
	return jsonBytes(c, http.StatusCreated, resp)
}

type ModerateRequest struct {
	NGWord string `json:"ng_word"`
}

func moderateHandler(c echo.Context) error {
	uid, err := getSession(c)
	if err != nil {
		return err
	}
	lid, err := paramID(c, "livestream_id")
	if err != nil {
		return err
	}
	var req ModerateRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return bad(http.StatusBadRequest, "failed to decode the request body as json")
	}
	now := time.Now().Unix()
	mu.Lock()
	defer mu.Unlock()
	ls := streams[lid]
	if ls == nil || ls.Owner == nil || ls.Owner.ID != uid {
		return bad(http.StatusBadRequest, "A streamer can't moderate livestreams that other streamers own")
	}
	nextNGID++
	w := &NGWord{ID: nextNGID, UserID: uid, LivestreamID: lid, Word: req.NGWord, CreatedAt: now}
	ls.NGWords = append(ls.NGWords, w)
	persist("INSERT INTO ng_words (id, user_id, livestream_id, word, created_at) VALUES (?,?,?,?,?)", w.ID, uid, lid, w.Word, now)

	// delete existing comments that hit any ng word of this livestream
	kept := ls.Comments[:0]
	var delIDs []any
	for _, cm := range ls.Comments {
		hit := false
		for _, nw := range ls.NGWords {
			if strings.Contains(cm.Comment, nw.Word) {
				hit = true
				break
			}
		}
		if hit {
			delIDs = append(delIDs, cm.ID)
			ls.Tips -= cm.Tip
			totalTips -= cm.Tip
			ls.Owner.Tips -= cm.Tip
			ls.Owner.Comments--
		} else {
			kept = append(kept, cm)
		}
	}
	for i := len(kept); i < len(ls.Comments); i++ {
		ls.Comments[i] = nil
	}
	ls.Comments = kept
	if len(delIDs) > 0 {
		persist("DELETE FROM livecomments WHERE id IN (?"+strings.Repeat(",?", len(delIDs)-1)+")", delIDs...)
	}
	return c.JSON(http.StatusCreated, map[string]int64{"word_id": w.ID})
}

// ---------- reactions ----------

type PostReactionRequest struct {
	EmojiName string `json:"emoji_name"`
}

func getReactionsHandler(c echo.Context) error {
	if _, err := getSession(c); err != nil {
		return err
	}
	lid, err := paramID(c, "livestream_id")
	if err != nil {
		return err
	}
	limit, err := limitParam(c)
	if err != nil {
		return err
	}
	b := make([]byte, 0, 32*1024)
	b = append(b, '[')
	mu.RLock()
	if ls := streams[lid]; ls != nil {
		rs := ls.Reactions
		n := len(rs)
		if limit >= 0 && limit < n {
			n = limit
		}
		for i := len(rs) - 1; i >= len(rs)-n; i-- {
			if i != len(rs)-1 {
				b = append(b, ',')
			}
			b = appendReaction(b, rs[i])
		}
	}
	mu.RUnlock()
	b = append(b, ']')
	return jsonBytes(c, http.StatusOK, b)
}

func postReactionHandler(c echo.Context) error {
	lid, err := paramID(c, "livestream_id")
	if err != nil {
		return err
	}
	uid, err := getSession(c)
	if err != nil {
		return err
	}
	var req PostReactionRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return bad(http.StatusBadRequest, "failed to decode the request body as json")
	}
	now := time.Now().Unix()
	mu.Lock()
	if streams[lid] == nil {
		mu.Unlock()
		return bad(http.StatusInternalServerError, "failed to fill reaction")
	}
	nextReactionID++
	r := &Reaction{ID: nextReactionID, UserID: uid, LivestreamID: lid, EmojiName: req.EmojiName, CreatedAt: now}
	addReactionMem(r)
	persist("INSERT INTO reactions (id, user_id, livestream_id, emoji_name, created_at) VALUES (?,?,?,?,?)", r.ID, uid, lid, r.EmojiName, now)
	resp := appendReaction(make([]byte, 0, 2048), r)
	mu.Unlock()
	return jsonBytes(c, http.StatusCreated, resp)
}

// ---------- stats ----------

type LivestreamStatistics struct {
	Rank           int64 `json:"rank"`
	ViewersCount   int64 `json:"viewers_count"`
	TotalReactions int64 `json:"total_reactions"`
	TotalReports   int64 `json:"total_reports"`
	MaxTip         int64 `json:"max_tip"`
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
	if _, err := getSession(c); err != nil {
		return err
	}
	name := c.Param("username")
	mu.RLock()
	u := usersByName[name]
	if u == nil {
		mu.RUnlock()
		return bad(http.StatusBadRequest, "not found user that has the given username")
	}
	my := u.Reactions + u.Tips
	var rank int64 = 1
	for _, o := range usersByID {
		s := o.Reactions + o.Tips
		if s > my || (s == my && o.Name > name) {
			rank++
		}
	}
	fav := ""
	var favN int64 = -1
	for e, n := range u.Emoji {
		if n > favN || (n == favN && e > fav) {
			fav, favN = e, n
		}
	}
	st := UserStatistics{Rank: rank, ViewersCount: u.Viewers, TotalReactions: u.Reactions, TotalLivecomments: u.Comments, TotalTip: u.Tips, FavoriteEmoji: fav}
	mu.RUnlock()
	return c.JSON(http.StatusOK, st)
}

func getLivestreamStatisticsHandler(c echo.Context) error {
	if _, err := getSession(c); err != nil {
		return err
	}
	lid, err := paramID(c, "livestream_id")
	if err != nil {
		return err
	}
	mu.RLock()
	ls := streams[lid]
	if ls == nil {
		mu.RUnlock()
		return bad(http.StatusBadRequest, "cannot get stats of not found livestream")
	}
	my := ls.ReactCnt + ls.Tips
	var rank int64 = 1
	for _, o := range streamList {
		s := o.ReactCnt + o.Tips
		if s > my || (s == my && o.ID > lid) {
			rank++
		}
	}
	var maxTip int64
	for _, cm := range ls.Comments {
		if cm.Tip > maxTip {
			maxTip = cm.Tip
		}
	}
	st := LivestreamStatistics{Rank: rank, ViewersCount: ls.Viewers, TotalReactions: ls.ReactCnt, TotalReports: int64(len(ls.Reports)), MaxTip: maxTip}
	mu.RUnlock()
	return c.JSON(http.StatusOK, st)
}

func getPaymentHandler(c echo.Context) error {
	mu.RLock()
	t := totalTips
	mu.RUnlock()
	return c.JSON(http.StatusOK, map[string]int64{"total_tip": t})
}

var debugSpam = os.Getenv("DEBUG_SPAM") == "1"
var initNGMax int64

var iconCacheControl = os.Getenv("ICON_CACHE_CONTROL")
