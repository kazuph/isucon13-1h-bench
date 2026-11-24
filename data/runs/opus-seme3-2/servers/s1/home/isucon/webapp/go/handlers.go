package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	json "github.com/goccy/go-json"
	"golang.org/x/crypto/bcrypt"
)

func decode(r *http.Request, v any) error {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func parseID(s string) (int64, bool) {
	v, err := strconv.ParseInt(s, 10, 64)
	return v, err == nil
}

// ---------- top ----------

func getTagHandler(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	t := tagsAll
	mu.RUnlock()
	writeJSON(w, 200, map[string]any{"tags": t})
}

func getThemeHandler(w http.ResponseWriter, r *http.Request, name string) {
	if _, ok := auth(w, r); !ok {
		return
	}
	mu.RLock()
	u := usersByName[name]
	var t Theme
	if u != nil {
		t = Theme{ID: u.ThemeID, DarkMode: u.Dark}
	}
	mu.RUnlock()
	if u == nil {
		writeErr(w, 404, "not found user that has the given username")
		return
	}
	writeJSON(w, 200, t)
}

// ---------- user ----------

func getIconHandler(w http.ResponseWriter, r *http.Request, name string) {
	mu.RLock()
	u := usersByName[name]
	var img []byte
	var hash string
	if u != nil {
		img, hash = u.Icon, u.IconHash
	}
	mu.RUnlock()
	if u == nil {
		writeErr(w, 404, "not found user that has the given username")
		return
	}
	if img == nil {
		img, hash = fallbackImage, fallbackHash
	}
	h := w.Header()
	h["Etag"] = []string{"\"" + hash + "\""}
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		if strings.Trim(inm, "\"") == hash {
			w.WriteHeader(304)
			return
		}
	}
	h["Content-Type"] = []string{"image/jpeg"}
	w.WriteHeader(200)
	w.Write(img)
}

func postIconHandler(w http.ResponseWriter, r *http.Request) {
	uid, ok := auth(w, r)
	if !ok {
		return
	}
	var req struct {
		Image []byte `json:"image"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "failed to decode the request body as json")
		return
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(req.Image))
	mu.Lock()
	u := usersByID[uid]
	if u == nil {
		mu.Unlock()
		writeErr(w, 404, "user not found")
		return
	}
	id := nextIconID
	nextIconID++
	u.Icon = req.Image
	u.IconHash = hash
	u.rebuild()
	persist("DELETE FROM icons WHERE user_id = ?", uid)
	persist("INSERT INTO icons (id, user_id, image) VALUES (?, ?, ?)", id, uid, req.Image)
	mu.Unlock()
	writeJSON(w, 201, map[string]int64{"id": id})
}

func getMeHandler(w http.ResponseWriter, r *http.Request) {
	uid, ok := auth(w, r)
	if !ok {
		return
	}
	mu.RLock()
	u := usersByID[uid]
	var resp User
	if u != nil {
		resp = u.resp
	}
	mu.RUnlock()
	if u == nil {
		writeErr(w, 404, "not found user that has the userid in session")
		return
	}
	writeJSON(w, 200, resp)
}

func getUserHandler(w http.ResponseWriter, r *http.Request, name string) {
	if _, ok := auth(w, r); !ok {
		return
	}
	mu.RLock()
	u := usersByName[name]
	var resp User
	if u != nil {
		resp = u.resp
	}
	mu.RUnlock()
	if u == nil {
		writeErr(w, 404, "not found user that has the given username")
		return
	}
	writeJSON(w, 200, resp)
}

func registerHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Description string `json:"description"`
		Password    string `json:"password"`
		Theme       struct {
			DarkMode bool `json:"dark_mode"`
		} `json:"theme"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "failed to decode the request body as json")
		return
	}
	if req.Name == "pipe" {
		writeErr(w, 400, "the username 'pipe' is reserved")
		return
	}
	mu.Lock()
	if usersByName[req.Name] != nil {
		mu.Unlock()
		writeErr(w, 500, "failed to insert user: duplicate")
		return
	}
	u := &UserM{ID: nextUserID, Name: req.Name, DisplayName: req.DisplayName, Description: req.Description,
		verifiedPw: req.Password, ThemeID: nextThemeID, Dark: req.Theme.DarkMode,
		emoji: map[string]int64{}}
	nextUserID++
	nextThemeID++
	u.rebuild()
	usersByID[u.ID] = u
	usersByName[u.Name] = u
	addDNSName(u.Name)
	persist("INSERT INTO themes (id, user_id, dark_mode) VALUES (?, ?, ?)", u.ThemeID, u.ID, u.Dark)
	resp := u.resp
	mu.Unlock()
	pPending.Add(1) // keep initialize from racing with the pending user insert
	go func() {
		hashed, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.MinCost)
		if err != nil {
			log.Printf("bcrypt: %v", err)
		}
		mu.Lock()
		if usersByID[u.ID] == u {
			u.Password = string(hashed)
			persist("INSERT INTO users (id, name, display_name, description, password) VALUES (?, ?, ?, ?, ?)", u.ID, u.Name, u.DisplayName, u.Description, u.Password)
		}
		mu.Unlock()
		pPending.Add(-1)
	}()
	writeJSON(w, 201, resp)
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "failed to decode the request body as json")
		return
	}
	mu.RLock()
	u := usersByName[req.Username]
	var hashed, verified string
	var uid int64
	if u != nil {
		hashed, verified, uid = u.Password, u.verifiedPw, u.ID
	}
	mu.RUnlock()
	if u == nil {
		writeErr(w, 401, "invalid username or password")
		return
	}
	if verified == "" || verified != req.Password {
		if err := bcrypt.CompareHashAndPassword([]byte(hashed), []byte(req.Password)); err != nil {
			writeErr(w, 401, "invalid username or password")
			return
		}
		mu.Lock()
		u.verifiedPw = req.Password
		mu.Unlock()
	}
	exp := time.Now().Add(time.Hour)
	tok := makeSession(uid, exp.Unix())
	http.SetCookie(w, &http.Cookie{Name: "SESSIONID", Value: tok, Path: "/", Domain: "u.isucon.dev",
		MaxAge: 60000, Expires: time.Now().Add(60000 * time.Second)})
	w.WriteHeader(200)
}

func paymentHandler(w http.ResponseWriter, r *http.Request) {
	mu.RLock()
	t := totalTip
	mu.RUnlock()
	writeJSON(w, 200, map[string]int64{"total_tip": t})
}

// ---------- livestream ----------

var (
	termStart = time.Date(2023, 11, 25, 1, 0, 0, 0, time.UTC).Unix()
	termEnd   = time.Date(2024, 11, 25, 1, 0, 0, 0, time.UTC).Unix()
)

func reserveHandler(w http.ResponseWriter, r *http.Request) {
	uid, ok := auth(w, r)
	if !ok {
		return
	}
	var req struct {
		Tags         []int64 `json:"tags"`
		Title        string  `json:"title"`
		Description  string  `json:"description"`
		PlaylistUrl  string  `json:"playlist_url"`
		ThumbnailUrl string  `json:"thumbnail_url"`
		StartAt      int64   `json:"start_at"`
		EndAt        int64   `json:"end_at"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "failed to decode the request body as json")
		return
	}
	if req.StartAt >= termEnd || req.EndAt <= termStart {
		writeErr(w, 400, "bad reservation time range")
		return
	}
	mu.Lock()
	owner := usersByID[uid]
	if owner == nil {
		mu.Unlock()
		writeErr(w, 500, "user not found")
		return
	}
	i0 := sort.Search(len(slots), func(i int) bool { return slots[i].Start >= req.StartAt })
	var idx []int
	for i := i0; i < len(slots); i++ {
		if slots[i].Start > req.EndAt {
			break
		}
		if slots[i].End <= req.EndAt {
			if slots[i].Count < 1 {
				mu.Unlock()
				writeErr(w, 400, fmt.Sprintf("予約期間 %d ~ %dに対して、予約区間 %d ~ %dが予約できません", termStart, termEnd, req.StartAt, req.EndAt))
				return
			}
			idx = append(idx, i)
		}
	}
	for _, i := range idx {
		slots[i].Count--
	}
	l := &LivestreamM{ID: nextLsID, UserID: uid, owner: owner, Title: req.Title, Description: req.Description,
		PlaylistUrl: req.PlaylistUrl, ThumbnailUrl: req.ThumbnailUrl, StartAt: req.StartAt, EndAt: req.EndAt,
		viewers: map[int64]int64{}, tags: make([]Tag, 0, len(req.Tags))}
	nextLsID++
	for _, t := range req.Tags {
		tg, ok := tagByID[t]
		if !ok {
			tg = Tag{ID: t}
		}
		l.tags = append(l.tags, tg)
		tagIndex[t] = append(tagIndex[t], l)
	}
	livestreams[l.ID] = l
	lsOrder = append(lsOrder, l)
	owner.livestreams = append(owner.livestreams, l)
	persist("UPDATE reservation_slots SET slot = slot - 1 WHERE start_at >= ? AND end_at <= ?", req.StartAt, req.EndAt)
	persist("INSERT INTO livestreams (id, user_id, title, description, playlist_url, thumbnail_url, start_at, end_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		l.ID, l.UserID, l.Title, l.Description, l.PlaylistUrl, l.ThumbnailUrl, l.StartAt, l.EndAt)
	if len(req.Tags) > 0 {
		var sb strings.Builder
		sb.WriteString("INSERT INTO livestream_tags (livestream_id, tag_id) VALUES ")
		args := make([]any, 0, len(req.Tags)*2)
		for i, t := range req.Tags {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("(?, ?)")
			args = append(args, l.ID, t)
		}
		persist(sb.String(), args...)
	}
	resp := l.resp()
	mu.Unlock()
	writeJSON(w, 201, resp)
}

func searchHandler(w http.ResponseWriter, r *http.Request) {
	tag := r.URL.Query().Get("tag")
	limitS := r.URL.Query().Get("limit")
	limit := -1
	if limitS != "" && tag == "" {
		v, err := strconv.Atoi(limitS)
		if err != nil {
			writeErr(w, 400, "limit query parameter must be integer")
			return
		}
		limit = v
	}
	mu.RLock()
	var res []Livestream
	if tag != "" {
		tid, ok := tagByName[tag]
		res = []Livestream{}
		if ok {
			lst := tagIndex[tid]
			res = make([]Livestream, 0, len(lst))
			for i := len(lst) - 1; i >= 0; i-- {
				res = append(res, lst[i].resp())
			}
		}
	} else {
		n := len(lsOrder)
		if limit >= 0 && limit < n {
			n = limit
		}
		res = make([]Livestream, 0, n)
		for i := len(lsOrder) - 1; i >= 0 && len(res) < n; i-- {
			res = append(res, lsOrder[i].resp())
		}
	}
	mu.RUnlock()
	writeJSON(w, 200, res)
}

func getMyLivestreamsHandler(w http.ResponseWriter, r *http.Request) {
	uid, ok := auth(w, r)
	if !ok {
		return
	}
	mu.RLock()
	res := []Livestream{}
	if u := usersByID[uid]; u != nil {
		res = make([]Livestream, 0, len(u.livestreams))
		for _, l := range u.livestreams {
			res = append(res, l.resp())
		}
	}
	mu.RUnlock()
	writeJSON(w, 200, res)
}

func getUserLivestreamsHandler(w http.ResponseWriter, r *http.Request, name string) {
	if _, ok := auth(w, r); !ok {
		return
	}
	mu.RLock()
	u := usersByName[name]
	if u == nil {
		mu.RUnlock()
		writeErr(w, 404, "user not found")
		return
	}
	res := make([]Livestream, 0, len(u.livestreams))
	for _, l := range u.livestreams {
		res = append(res, l.resp())
	}
	mu.RUnlock()
	writeJSON(w, 200, res)
}

func enterHandler(w http.ResponseWriter, r *http.Request, ids string) {
	uid, ok := auth(w, r)
	if !ok {
		return
	}
	lid, ok := parseID(ids)
	if !ok {
		writeErr(w, 400, "livestream_id must be integer")
		return
	}
	now := time.Now().Unix()
	mu.Lock()
	if l := livestreams[lid]; l != nil {
		l.viewers[uid]++
		l.viewerCnt++
	}
	persist("INSERT INTO livestream_viewers_history (user_id, livestream_id, created_at) VALUES (?, ?, ?)", uid, lid, now)
	mu.Unlock()
	w.WriteHeader(200)
}

func exitHandler(w http.ResponseWriter, r *http.Request, ids string) {
	uid, ok := auth(w, r)
	if !ok {
		return
	}
	lid, ok := parseID(ids)
	if !ok {
		writeErr(w, 400, "livestream_id in path must be integer")
		return
	}
	mu.Lock()
	if l := livestreams[lid]; l != nil {
		if n := l.viewers[uid]; n > 0 {
			l.viewerCnt -= n
			delete(l.viewers, uid)
		}
	}
	persist("DELETE FROM livestream_viewers_history WHERE user_id = ? AND livestream_id = ?", uid, lid)
	mu.Unlock()
	w.WriteHeader(200)
}

func getLivestreamHandler(w http.ResponseWriter, r *http.Request, ids string) {
	if _, ok := auth(w, r); !ok {
		return
	}
	lid, ok := parseID(ids)
	if !ok {
		writeErr(w, 400, "livestream_id in path must be integer")
		return
	}
	mu.RLock()
	l := livestreams[lid]
	var resp Livestream
	if l != nil {
		resp = l.resp()
	}
	mu.RUnlock()
	if l == nil {
		writeErr(w, 404, "not found livestream that has the given id")
		return
	}
	writeJSON(w, 200, resp)
}

func getReportsHandler(w http.ResponseWriter, r *http.Request, ids string) {
	uid, ok := auth(w, r)
	if !ok {
		return
	}
	lid, ok := parseID(ids)
	if !ok {
		writeErr(w, 400, "livestream_id in path must be integer")
		return
	}
	mu.RLock()
	l := livestreams[lid]
	if l == nil {
		mu.RUnlock()
		writeErr(w, 500, "failed to get livestream")
		return
	}
	if l.UserID != uid {
		mu.RUnlock()
		writeErr(w, 403, "can't get other streamer's livecomment reports")
		return
	}
	res := make([]LivecommentReport, 0, len(l.reports))
	for _, rp := range l.reports {
		res = append(res, LivecommentReport{ID: rp.ID, Reporter: rp.user.resp, Livecomment: rp.comment.resp(), CreatedAt: rp.CreatedAt})
	}
	mu.RUnlock()
	writeJSON(w, 200, res)
}

// ---------- livecomment ----------

func getLivecommentsHandler(w http.ResponseWriter, r *http.Request, ids string) {
	if _, ok := auth(w, r); !ok {
		return
	}
	lid, ok := parseID(ids)
	if !ok {
		writeErr(w, 400, "livestream_id in path must be integer")
		return
	}
	limit := -1
	if s := r.URL.Query().Get("limit"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil {
			writeErr(w, 400, "limit query parameter must be integer")
			return
		}
		limit = v
	}
	mu.RLock()
	res := []Livecomment{}
	if l := livestreams[lid]; l != nil {
		n := len(l.comments)
		if limit >= 0 && limit < n {
			n = limit
		}
		res = make([]Livecomment, 0, n)
		for i := len(l.comments) - 1; i >= 0 && len(res) < n; i-- {
			res = append(res, l.comments[i].resp())
		}
	}
	mu.RUnlock()
	writeJSON(w, 200, res)
}

func getNgwordsHandler(w http.ResponseWriter, r *http.Request, ids string) {
	uid, ok := auth(w, r)
	if !ok {
		return
	}
	lid, ok := parseID(ids)
	if !ok {
		writeErr(w, 400, "livestream_id in path must be integer")
		return
	}
	mu.RLock()
	var res []*NGWord
	if l := livestreams[lid]; l != nil {
		for i := len(l.ngwords) - 1; i >= 0; i-- {
			n := l.ngwords[i]
			if n.UserID == uid {
				c := *n
				res = append(res, &c)
			}
		}
	}
	mu.RUnlock()
	writeJSON(w, 200, res)
}

func postLivecommentHandler(w http.ResponseWriter, r *http.Request, ids string) {
	uid, ok := auth(w, r)
	if !ok {
		return
	}
	lid, ok := parseID(ids)
	if !ok {
		writeErr(w, 400, "livestream_id in path must be integer")
		return
	}
	var req struct {
		Comment string `json:"comment"`
		Tip     int64  `json:"tip"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "failed to decode the request body as json")
		return
	}
	mu.RLock()
	l := livestreams[lid]
	if l == nil {
		mu.RUnlock()
		writeErr(w, 404, "livestream not found")
		return
	}
	for _, ng := range l.ngwords {
		if ng.UserID == l.UserID && strings.Contains(req.Comment, ng.Word) {
			mu.RUnlock()
			writeErr(w, 400, "このコメントがスパム判定されました")
			return
		}
	}
	mu.RUnlock()

	now := time.Now().Unix()
	mu.Lock()
	u := usersByID[uid]
	if u == nil {
		mu.Unlock()
		writeErr(w, 500, "user not found")
		return
	}
	c := &CommentM{ID: nextCommentID, user: u, ls: l, Comment: req.Comment, Tip: req.Tip, CreatedAt: now}
	nextCommentID++
	comments[c.ID] = c
	l.comments = append(l.comments, c)
	l.tipSum += c.Tip
	l.owner.recvTips += c.Tip
	l.owner.recvComments++
	totalTip += c.Tip
	persist("INSERT INTO livecomments (id, user_id, livestream_id, comment, tip, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		c.ID, uid, lid, c.Comment, c.Tip, now)
	resp := c.resp()
	mu.Unlock()
	writeJSON(w, 201, resp)
}

func reportHandler(w http.ResponseWriter, r *http.Request, ids, cids string) {
	uid, ok := auth(w, r)
	if !ok {
		return
	}
	lid, ok := parseID(ids)
	if !ok {
		writeErr(w, 400, "livestream_id in path must be integer")
		return
	}
	cid, ok := parseID(cids)
	if !ok {
		writeErr(w, 400, "livecomment_id in path must be integer")
		return
	}
	now := time.Now().Unix()
	mu.Lock()
	l := livestreams[lid]
	if l == nil {
		mu.Unlock()
		writeErr(w, 404, "livestream not found")
		return
	}
	c := comments[cid]
	if c == nil {
		mu.Unlock()
		writeErr(w, 404, "livecomment not found")
		return
	}
	u := usersByID[uid]
	if u == nil {
		mu.Unlock()
		writeErr(w, 500, "user not found")
		return
	}
	rp := &ReportM{ID: nextReportID, user: u, comment: c, CreatedAt: now}
	nextReportID++
	l.reports = append(l.reports, rp)
	persist("INSERT INTO livecomment_reports (id, user_id, livestream_id, livecomment_id, created_at) VALUES (?, ?, ?, ?, ?)",
		rp.ID, uid, lid, cid, now)
	resp := LivecommentReport{ID: rp.ID, Reporter: u.resp, Livecomment: c.resp(), CreatedAt: now}
	mu.Unlock()
	writeJSON(w, 201, resp)
}

func moderateHandler(w http.ResponseWriter, r *http.Request, ids string) {
	uid, ok := auth(w, r)
	if !ok {
		return
	}
	lid, ok := parseID(ids)
	if !ok {
		writeErr(w, 400, "livestream_id in path must be integer")
		return
	}
	var req struct {
		NGWord string `json:"ng_word"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "failed to decode the request body as json")
		return
	}
	now := time.Now().Unix()
	mu.Lock()
	l := livestreams[lid]
	if l == nil || l.UserID != uid {
		mu.Unlock()
		writeErr(w, 400, "A streamer can't moderate livestreams that other streamers own")
		return
	}
	ng := &NGWord{ID: nextNgID, UserID: uid, LivestreamID: lid, Word: req.NGWord, CreatedAt: now}
	nextNgID++
	l.ngwords = append(l.ngwords, ng)
	persist("INSERT INTO ng_words (id, user_id, livestream_id, word, created_at) VALUES (?, ?, ?, ?, ?)", ng.ID, uid, lid, ng.Word, now)

	kept := l.comments[:0]
	var deleted []any
	for _, c := range l.comments {
		hit := false
		for _, n := range l.ngwords {
			if strings.Contains(c.Comment, n.Word) {
				hit = true
				break
			}
		}
		if hit {
			deleted = append(deleted, c.ID)
			delete(comments, c.ID)
			l.tipSum -= c.Tip
			l.owner.recvTips -= c.Tip
			l.owner.recvComments--
			totalTip -= c.Tip
		} else {
			kept = append(kept, c)
		}
	}
	for i := len(kept); i < len(l.comments); i++ {
		l.comments[i] = nil
	}
	l.comments = kept
	if len(deleted) > 0 {
		q := "DELETE FROM livecomments WHERE id IN (?" + strings.Repeat(",?", len(deleted)-1) + ")"
		persist(q, deleted...)
	}
	mu.Unlock()
	writeJSON(w, 201, map[string]int64{"word_id": ng.ID})
}

// ---------- reaction ----------

func getReactionsHandler(w http.ResponseWriter, r *http.Request, ids string) {
	if _, ok := auth(w, r); !ok {
		return
	}
	lid, ok := parseID(ids)
	if !ok {
		writeErr(w, 400, "livestream_id in path must be integer")
		return
	}
	limit := -1
	if s := r.URL.Query().Get("limit"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil {
			writeErr(w, 400, "limit query parameter must be integer")
			return
		}
		limit = v
	}
	mu.RLock()
	res := []Reaction{}
	if l := livestreams[lid]; l != nil {
		n := len(l.reactions)
		if limit >= 0 && limit < n {
			n = limit
		}
		res = make([]Reaction, 0, n)
		for i := len(l.reactions) - 1; i >= 0 && len(res) < n; i-- {
			res = append(res, l.reactions[i].resp())
		}
	}
	mu.RUnlock()
	writeJSON(w, 200, res)
}

func postReactionHandler(w http.ResponseWriter, r *http.Request, ids string) {
	lid, ok := parseID(ids)
	if !ok {
		writeErr(w, 400, "livestream_id in path must be integer")
		return
	}
	uid, ok := auth(w, r)
	if !ok {
		return
	}
	var req struct {
		EmojiName string `json:"emoji_name"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "failed to decode the request body as json")
		return
	}
	now := time.Now().Unix()
	mu.Lock()
	l := livestreams[lid]
	u := usersByID[uid]
	if l == nil || u == nil {
		mu.Unlock()
		writeErr(w, 500, "failed to fill reaction")
		return
	}
	rc := &ReactionM{ID: nextReactionID, user: u, ls: l, Emoji: req.EmojiName, CreatedAt: now}
	nextReactionID++
	l.reactions = append(l.reactions, rc)
	l.reactCnt++
	l.owner.recvReactions++
	l.owner.emoji[rc.Emoji]++
	persist("INSERT INTO reactions (id, user_id, livestream_id, emoji_name, created_at) VALUES (?, ?, ?, ?, ?)", rc.ID, uid, lid, rc.Emoji, now)
	resp := rc.resp()
	mu.Unlock()
	writeJSON(w, 201, resp)
}

// ---------- stats ----------

func getUserStatisticsHandler(w http.ResponseWriter, r *http.Request, name string) {
	if _, ok := auth(w, r); !ok {
		return
	}
	mu.RLock()
	u := usersByName[name]
	if u == nil {
		mu.RUnlock()
		writeErr(w, 400, "not found user that has the given username")
		return
	}
	my := u.recvReactions + u.recvTips
	var rank int64 = 1
	for _, o := range usersByID {
		s := o.recvReactions + o.recvTips
		if s > my || (s == my && o.Name > u.Name) {
			rank++
		}
	}
	var viewers int64
	for _, l := range u.livestreams {
		viewers += l.viewerCnt
	}
	fav := ""
	var best int64
	for e, n := range u.emoji {
		if n > best || (n == best && e > fav) {
			if n > 0 {
				best, fav = n, e
			}
		}
	}
	resp := map[string]any{
		"rank":               rank,
		"viewers_count":      viewers,
		"total_reactions":    u.recvReactions,
		"total_livecomments": u.recvComments,
		"total_tip":          u.recvTips,
		"favorite_emoji":     fav,
	}
	mu.RUnlock()
	writeJSON(w, 200, resp)
}

func getLivestreamStatisticsHandler(w http.ResponseWriter, r *http.Request, ids string) {
	if _, ok := auth(w, r); !ok {
		return
	}
	lid, ok := parseID(ids)
	if !ok {
		writeErr(w, 400, "livestream_id in path must be integer")
		return
	}
	mu.RLock()
	l := livestreams[lid]
	if l == nil {
		mu.RUnlock()
		writeErr(w, 400, "cannot get stats of not found livestream")
		return
	}
	my := l.reactCnt + l.tipSum
	var rank int64 = 1
	for _, o := range lsOrder {
		s := o.reactCnt + o.tipSum
		if s > my || (s == my && o.ID > l.ID) {
			rank++
		}
	}
	resp := map[string]any{
		"rank":            rank,
		"viewers_count":   l.viewerCnt,
		"total_reactions": l.reactCnt,
		"total_reports":   int64(len(l.reports)),
		"max_tip":         l.maxTip(),
	}
	mu.RUnlock()
	writeJSON(w, 200, resp)
}
