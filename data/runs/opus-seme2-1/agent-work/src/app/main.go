package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/exec"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var secret = []byte("isucon13_session_cookiestore_defaultsecret_v2_inmem")

const cookieName = "SESSIONID"

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	debug.SetGCPercent(400)

	var err error
	fallbackImage, err = os.ReadFile("../img/NoImage.jpg")
	must(err)
	fallbackHash = hashHex(fallbackImage)
	loadZoneNames("../pdns/u.isucon.dev.zone")

	if ip := net.ParseIP(getenv("ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS", "127.0.0.1")).To4(); ip != nil {
		copy(subdomainIP[:], ip)
	}
	for _, x := range strings.Split(os.Getenv("DNS_IPS"), ",") {
		if ip := net.ParseIP(strings.TrimSpace(x)).To4(); ip != nil {
			var a [4]byte
			copy(a[:], ip)
			dnsIPs = append(dnsIPs, a)
		}
	}
	if v := os.Getenv("DNS_TTL"); v != "" {
		n, _ := strconv.Atoi(v)
		dnsTTL = uint32(n)
	}

	go http.ListenAndServe("127.0.0.1:6060", nil)
	for _, n := range zoneNames {
		dnsNames.Store(n, true)
	}
	if os.Getenv("DNS_ADDR") != "" {
		startDNS(os.Getenv("DNS_ADDR"))
	}
	db = connectDB()
	mu.Lock()
	loadAll(db)
	mu.Unlock()
	go writer()


	var h http.Handler = http.HandlerFunc(route)
	if os.Getenv("METRICS") != "" {
		h = instrument(h)
		http.HandleFunc("/stats", statsHandler)
	}
	if cert := os.Getenv("TLS_CERT"); cert != "" {
		go func() {
			c, err := loadCertOpenSSL(cert, os.Getenv("TLS_KEY"))
			must(err)
			srv := &http.Server{Addr: ":443", Handler: h, TLSConfig: &tls.Config{
				Certificates: []tls.Certificate{c},
				MinVersion:   tls.VersionTLS12,
			}, IdleTimeout: 120 * time.Second, ConnState: connState, ConnContext: connContext}
			log.Fatal(srv.ListenAndServeTLS("", ""))
		}()
	}
	srv := &http.Server{Addr: ":8080", Handler: h, IdleTimeout: 120 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

// ---------- helpers ----------

func writeJSON(w http.ResponseWriter, code int, b []byte) {
	hd := w.Header()
	hd["Content-Type"] = jsonCT
	hd["Content-Length"] = []string{strconv.Itoa(len(b))}
	w.WriteHeader(code)
	w.Write(b)
}

var jsonCT = []string{"application/json; charset=UTF-8"}

func httpErr(w http.ResponseWriter, code int, msg string) {
	b := []byte(`{"error":`)
	b = appendStr(b, "code="+strconv.Itoa(code)+", message="+msg)
	b = append(b, '}')
	writeJSON(w, code, b)
}

type sess struct {
	uid int64
	exp int64
}

var sessCache sync.Map
var pwCache sync.Map

func sign(payload string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(payload))
	return hex.EncodeToString(m.Sum(nil))[:40]
}

// returns uid, or writes error and returns 0,false
func auth(w http.ResponseWriter, r *http.Request) (int64, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		httpErr(w, http.StatusForbidden, "failed to get EXPIRES value from session")
		return 0, false
	}
	var s sess
	if v, ok := sessCache.Load(c.Value); ok {
		s = v.(sess)
	} else {
		parts := strings.Split(c.Value, ".")
		if len(parts) != 3 || sign(parts[0]+"."+parts[1]) != parts[2] {
			httpErr(w, http.StatusUnauthorized, "failed to get session")
			return 0, false
		}
		uid, e1 := strconv.ParseInt(parts[0], 10, 64)
		exp, e2 := strconv.ParseInt(parts[1], 10, 64)
		if e1 != nil || e2 != nil {
			httpErr(w, http.StatusUnauthorized, "failed to get session")
			return 0, false
		}
		s = sess{uid, exp}
		sessCache.Store(c.Value, s)
	}
	if time.Now().Unix() > s.exp {
		httpErr(w, http.StatusUnauthorized, "session has expired")
		return 0, false
	}
	return s.uid, true
}

func decodeBody(r *http.Request, v any) bool {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v) == nil
}

// ---------- router ----------

func route(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if !strings.HasPrefix(p, "/api/") {
		http.ServeFile(w, r, "../public/index.html")
		return
	}
	segs := strings.Split(p[5:], "/")
	m := r.Method
	switch segs[0] {
	case "initialize":
		if m == "POST" && len(segs) == 1 {
			initializeHandler(w, r)
			return
		}
	case "tag":
		if m == "GET" && len(segs) == 1 {
			writeJSON(w, 200, tagsJSONSnapshot())
			return
		}
	case "payment":
		if m == "GET" && len(segs) == 1 {
			mu.RLock()
			t := totalTip
			mu.RUnlock()
			writeJSON(w, 200, append(appendInt([]byte(`{"total_tip":`), t), '}'))
			return
		}
	case "register":
		if m == "POST" && len(segs) == 1 {
			registerHandler(w, r)
			return
		}
	case "login":
		if m == "POST" && len(segs) == 1 {
			loginHandler(w, r)
			return
		}
	case "icon":
		if m == "POST" && len(segs) == 1 {
			postIconHandler(w, r)
			return
		}
	case "user":
		if len(segs) == 2 && m == "GET" {
			if segs[1] == "me" {
				getMeHandler(w, r)
			} else {
				getUserHandler(w, r, segs[1])
			}
			return
		}
		if len(segs) == 3 && m == "GET" {
			switch segs[2] {
			case "icon":
				getIconHandler(w, r, segs[1])
				return
			case "theme":
				getThemeHandler(w, r, segs[1])
				return
			case "livestream":
				getUserLivestreamsHandler(w, r, segs[1])
				return
			case "statistics":
				getUserStatsHandler(w, r, segs[1])
				return
			}
		}
	case "livestream":
		if len(segs) == 1 {
			if m == "GET" {
				getMyLivestreamsHandler(w, r)
				return
			}
			break
		}
		if len(segs) == 2 {
			if segs[1] == "reservation" {
				if m == "POST" {
					reserveHandler(w, r)
					return
				}
				break
			}
			if segs[1] == "search" {
				if m == "GET" {
					searchHandler(w, r)
					return
				}
				break
			}
		}
		lid, err := strconv.ParseInt(segs[1], 10, 64)
		if err != nil {
			if !validRoute(segs, m) {
				break
			}
			httpErr(w, 400, "livestream_id in path must be integer")
			return
		}
		if len(segs) == 2 && m == "GET" {
			getLivestreamHandler(w, r, lid)
			return
		}
		if len(segs) == 3 {
			switch segs[2] {
			case "livecomment":
				if m == "GET" {
					getLivecommentsHandler(w, r, lid)
					return
				} else if m == "POST" {
					postLivecommentHandler(w, r, lid)
					return
				}
			case "reaction":
				if m == "GET" {
					getReactionsHandler(w, r, lid)
					return
				} else if m == "POST" {
					postReactionHandler(w, r, lid)
					return
				}
			case "report":
				if m == "GET" {
					getReportsHandler(w, r, lid)
					return
				}
			case "ngwords":
				if m == "GET" {
					getNgwordsHandler(w, r, lid)
					return
				}
			case "moderate":
				if m == "POST" {
					moderateHandler(w, r, lid)
					return
				}
			case "enter":
				if m == "POST" {
					enterHandler(w, r, lid)
					return
				}
			case "exit":
				if m == "DELETE" {
					exitHandler(w, r, lid)
					return
				}
			case "statistics":
				if m == "GET" {
					getLivestreamStatsHandler(w, r, lid)
					return
				}
			}
		}
		if len(segs) == 5 && segs[2] == "livecomment" && segs[4] == "report" && m == "POST" {
			cid, err := strconv.ParseInt(segs[3], 10, 64)
			if err != nil {
				httpErr(w, 400, "livecomment_id in path must be integer")
				return
			}
			reportHandler(w, r, lid, cid)
			return
		}
	}
	httpErr(w, 404, "Not Found")
}

func validRoute(segs []string, m string) bool {
	return len(segs) >= 2 && len(segs) <= 5
}

func tagsJSONSnapshot() []byte {
	mu.RLock()
	b := tagsJSON
	mu.RUnlock()
	return b
}

// ---------- initialize ----------

func initializeHandler(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()
	drainQueue()
	writerMu.Lock()
	out, err := exec.Command("../sql/init.sh").CombinedOutput()
	if err != nil {
		writerMu.Unlock()
		log.Printf("init.sh failed: %s", out)
		httpErr(w, 500, "failed to initialize: "+err.Error())
		return
	}
	loadAll(db)
	writerMu.Unlock()
	sessCache.Range(func(k, v any) bool { sessCache.Delete(k); return true })
	pwCache.Range(func(k, v any) bool { pwCache.Delete(k); return true })
	writeJSON(w, 200, []byte(`{"language":"golang"}`))
}

// ---------- users ----------

func getIconHandler(w http.ResponseWriter, r *http.Request, name string) {
	mu.RLock()
	u := usersByName[name]
	if u == nil {
		mu.RUnlock()
		httpErr(w, 404, "not found user that has the given username")
		return
	}
	img, h := u.Icon, u.IconHash
	mu.RUnlock()
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		if strings.Trim(inm, `"`) == h {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	if img == nil {
		img = fallbackImage
	}
	hd := w.Header()
	hd["Content-Type"] = []string{"image/jpeg"}
	hd["Content-Length"] = []string{strconv.Itoa(len(img))}
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
	if !decodeBody(r, &req) {
		httpErr(w, 400, "failed to decode the request body as json")
		return
	}
	h := hashHex(req.Image)
	mu.Lock()
	u := users[uid]
	if u == nil {
		mu.Unlock()
		httpErr(w, 500, "user not found")
		return
	}
	nextIconID++
	id := nextIconID
	u.Icon = req.Image
	u.IconHash = h
	u.json = buildUserJSON(u)
	for _, l := range u.lss {
		l.json = buildLSJSON(l)
	}
	enqueue("DELETE FROM icons WHERE user_id = ?", uid)
	enqueue("INSERT INTO icons (id, user_id, image) VALUES (?, ?, ?)", id, uid, req.Image)
	mu.Unlock()
	writeJSON(w, 201, append(appendInt([]byte(`{"id":`), id), '}'))
}

func getMeHandler(w http.ResponseWriter, r *http.Request) {
	uid, ok := auth(w, r)
	if !ok {
		return
	}
	mu.RLock()
	u := users[uid]
	var b []byte
	if u != nil {
		b = u.json
	}
	mu.RUnlock()
	if u == nil {
		httpErr(w, 404, "not found user that has the userid in session")
		return
	}
	writeJSON(w, 200, b)
}

func getUserHandler(w http.ResponseWriter, r *http.Request, name string) {
	if _, ok := auth(w, r); !ok {
		return
	}
	mu.RLock()
	u := usersByName[name]
	var b []byte
	if u != nil {
		b = u.json
	}
	mu.RUnlock()
	if u == nil {
		httpErr(w, 404, "not found user that has the given username")
		return
	}
	writeJSON(w, 200, b)
}

func getThemeHandler(w http.ResponseWriter, r *http.Request, name string) {
	if _, ok := auth(w, r); !ok {
		return
	}
	mu.RLock()
	u := usersByName[name]
	var b []byte
	if u != nil {
		b = append(b, `{"id":`...)
		b = appendInt(b, u.ThemeID)
		if u.Dark {
			b = append(b, `,"dark_mode":true}`...)
		} else {
			b = append(b, `,"dark_mode":false}`...)
		}
	}
	mu.RUnlock()
	if u == nil {
		httpErr(w, 404, "not found user that has the given username")
		return
	}
	writeJSON(w, 200, b)
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
	if !decodeBody(r, &req) {
		httpErr(w, 400, "failed to decode the request body as json")
		return
	}
	if req.Name == "pipe" {
		httpErr(w, 400, "the username 'pipe' is reserved")
		return
	}
	pwsum := sha256.Sum256([]byte(req.Password))
	mu.Lock()
	if usersByName[req.Name] != nil {
		mu.Unlock()
		httpErr(w, 500, "failed to insert user: duplicate")
		return
	}
	nextUserID++
	nextThemeID++
	u := &User{ID: nextUserID, Name: req.Name, DisplayName: req.DisplayName, Description: req.Description,
		ThemeID: nextThemeID, Dark: req.Theme.DarkMode, IconHash: fallbackHash,
		emoji: map[string]int64{}}
	u.json = buildUserJSON(u)
	users[u.ID] = u
	usersByName[u.Name] = u
	userList = append(userList, u)
	dnsNames.Store(strings.ToLower(u.Name), true)
	pwCache.Store(u.ID, pwsum)
	pw := req.Password
	wq <- op{q: "INSERT INTO users (id, name, display_name, description, password) VALUES (?, ?, ?, ?, ?)",
		args: []any{u.ID, u.Name, u.DisplayName, u.Description, ""},
		prep: func(a []any) {
			h, _ := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
			a[4] = string(h)
		}}
	enqueue("INSERT INTO themes (id, user_id, dark_mode) VALUES (?, ?, ?)", u.ThemeID, u.ID, u.Dark)
	b := u.json
	mu.Unlock()
	writeJSON(w, 201, b)
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeBody(r, &req) {
		httpErr(w, 400, "failed to decode the request body as json")
		return
	}
	mu.RLock()
	u := usersByName[req.Username]
	var hashed string
	var uid int64
	if u != nil {
		hashed = u.Password
		uid = u.ID
	}
	mu.RUnlock()
	if u == nil {
		httpErr(w, 401, "invalid username or password")
		return
	}
	pwsum := sha256.Sum256([]byte(req.Password))
	if v, ok := pwCache.Load(uid); ok {
		if v.([32]byte) != pwsum {
			httpErr(w, 401, "invalid username or password")
			return
		}
	} else {
		if err := bcrypt.CompareHashAndPassword([]byte(hashed), []byte(req.Password)); err != nil {
			httpErr(w, 401, "invalid username or password")
			return
		}
		pwCache.Store(uid, pwsum)
	}
	exp := time.Now().Add(time.Hour).Unix()
	payload := itoa(uid) + "." + itoa(exp)
	val := payload + "." + sign(payload)
	sessCache.Store(val, sess{uid, exp})
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: val, Path: "/", Domain: "u.isucon.dev",
		MaxAge: 60000, Expires: time.Now().Add(60000 * time.Second)})
	w.WriteHeader(200)
}

// ---------- livestreams ----------

var (
	termStartAt = time.Date(2023, 11, 25, 1, 0, 0, 0, time.UTC).Unix()
	termEndAt   = time.Date(2024, 11, 25, 1, 0, 0, 0, time.UTC).Unix()
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
	if !decodeBody(r, &req) {
		httpErr(w, 400, "failed to decode the request body as json")
		return
	}
	if req.StartAt >= termEndAt || req.EndAt <= termStartAt {
		httpErr(w, 400, "bad reservation time range")
		return
	}
	mu.Lock()
	owner := users[uid]
	if owner == nil {
		mu.Unlock()
		httpErr(w, 500, "user not found")
		return
	}
	tags := make([]*Tag, 0, len(req.Tags))
	for _, tid := range req.Tags {
		t := tagByID[tid]
		if t == nil {
			mu.Unlock()
			httpErr(w, 500, "failed to fill livestream: tag not found")
			return
		}
		tags = append(tags, t)
	}
	lo := sort.Search(len(slots), func(i int) bool { return slots[i].StartAt >= req.StartAt })
	hi := lo
	for i := lo; i < len(slots) && slots[i].StartAt < req.EndAt; i++ {
		s := slots[i]
		if s.EndAt <= req.EndAt {
			if s.Slot < 1 {
				mu.Unlock()
				httpErr(w, 400, "予約期間 "+itoa(termStartAt)+" ~ "+itoa(termEndAt)+"に対して、予約区間 "+itoa(req.StartAt)+" ~ "+itoa(req.EndAt)+"が予約できません")
				return
			}
		}
		hi = i + 1
	}
	for i := lo; i < hi; i++ {
		if slots[i].EndAt <= req.EndAt {
			slots[i].Slot--
		}
	}
	nextLSID++
	l := &Livestream{ID: nextLSID, Owner: owner, Title: req.Title, Description: req.Description,
		PlaylistURL: req.PlaylistUrl, ThumbnailURL: req.ThumbnailUrl, StartAt: req.StartAt, EndAt: req.EndAt,
		Tags: tags, viewerHist: map[int64]int64{}}
	l.json = buildLSJSON(l)
	lss[l.ID] = l
	lsList = append(lsList, l)
	owner.lss = append(owner.lss, l)
	enqueue("UPDATE reservation_slots SET slot = slot - 1 WHERE start_at >= ? AND end_at <= ?", req.StartAt, req.EndAt)
	enqueue("INSERT INTO livestreams (id, user_id, title, description, playlist_url, thumbnail_url, start_at, end_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		l.ID, uid, l.Title, l.Description, l.PlaylistURL, l.ThumbnailURL, l.StartAt, l.EndAt)
	for _, t := range tags {
		nextLSTagID++
		lsByTag[t.ID] = append(lsByTag[t.ID], l)
		enqueue("INSERT INTO livestream_tags (id, livestream_id, tag_id) VALUES (?, ?, ?)", nextLSTagID, l.ID, t.ID)
	}
	b := l.json
	mu.Unlock()
	writeJSON(w, 201, b)
}

func appendLSList(b []byte, list []*Livestream, reverse bool, limit int) []byte {
	b = append(b, '[')
	n := 0
	if reverse {
		for i := len(list) - 1; i >= 0; i-- {
			if limit >= 0 && n >= limit {
				break
			}
			if n > 0 {
				b = append(b, ',')
			}
			b = append(b, list[i].json...)
			n++
		}
	} else {
		for _, l := range list {
			if limit >= 0 && n >= limit {
				break
			}
			if n > 0 {
				b = append(b, ',')
			}
			b = append(b, l.json...)
			n++
		}
	}
	return append(b, ']')
}

func parseLimit(r *http.Request) (int, bool) {
	q := r.URL.Query().Get("limit")
	if q == "" {
		return -1, true
	}
	n, err := strconv.Atoi(q)
	if err != nil {
		return 0, false
	}
	if n < 0 {
		n = 0
	}
	return n, true
}

func searchHandler(w http.ResponseWriter, r *http.Request) {
	qs := r.URL.Query()
	tag := qs.Get("tag")
	var b []byte
	if tag != "" {
		mu.RLock()
		var list []*Livestream
		if t := tagByName[tag]; t != nil {
			list = lsByTag[t.ID]
		}
		b = appendLSList(make([]byte, 0, 4096), list, true, -1)
		mu.RUnlock()
	} else {
		limit, ok := parseLimit(r)
		if !ok {
			httpErr(w, 400, "limit query parameter must be integer")
			return
		}
		mu.RLock()
		b = appendLSList(make([]byte, 0, 4096), lsList, true, limit)
		mu.RUnlock()
	}
	writeJSON(w, 200, b)
}

func getMyLivestreamsHandler(w http.ResponseWriter, r *http.Request) {
	uid, ok := auth(w, r)
	if !ok {
		return
	}
	mu.RLock()
	var list []*Livestream
	if u := users[uid]; u != nil {
		list = u.lss
	}
	b := appendLSList(make([]byte, 0, 2048), list, false, -1)
	mu.RUnlock()
	writeJSON(w, 200, b)
}

func getUserLivestreamsHandler(w http.ResponseWriter, r *http.Request, name string) {
	if _, ok := auth(w, r); !ok {
		return
	}
	mu.RLock()
	u := usersByName[name]
	if u == nil {
		mu.RUnlock()
		httpErr(w, 404, "user not found")
		return
	}
	b := appendLSList(make([]byte, 0, 2048), u.lss, false, -1)
	mu.RUnlock()
	writeJSON(w, 200, b)
}

func enterHandler(w http.ResponseWriter, r *http.Request, lid int64) {
	uid, ok := auth(w, r)
	if !ok {
		return
	}
	now := time.Now().Unix()
	mu.Lock()
	nextHistID++
	if l := lss[lid]; l != nil {
		l.viewerHist[uid]++
		l.viewers++
		l.Owner.viewers++
	}
	enqueue("INSERT INTO livestream_viewers_history (id, user_id, livestream_id, created_at) VALUES (?, ?, ?, ?)", nextHistID, uid, lid, now)
	mu.Unlock()
	w.WriteHeader(200)
}

func exitHandler(w http.ResponseWriter, r *http.Request, lid int64) {
	uid, ok := auth(w, r)
	if !ok {
		return
	}
	mu.Lock()
	if l := lss[lid]; l != nil {
		if n := l.viewerHist[uid]; n > 0 {
			delete(l.viewerHist, uid)
			l.viewers -= n
			l.Owner.viewers -= n
		}
	}
	enqueue("DELETE FROM livestream_viewers_history WHERE user_id = ? AND livestream_id = ?", uid, lid)
	mu.Unlock()
	w.WriteHeader(200)
}

func getLivestreamHandler(w http.ResponseWriter, r *http.Request, lid int64) {
	if _, ok := auth(w, r); !ok {
		return
	}
	mu.RLock()
	l := lss[lid]
	var b []byte
	if l != nil {
		b = l.json
	}
	mu.RUnlock()
	if l == nil {
		httpErr(w, 404, "not found livestream that has the given id")
		return
	}
	writeJSON(w, 200, b)
}

func getReportsHandler(w http.ResponseWriter, r *http.Request, lid int64) {
	uid, ok := auth(w, r)
	if !ok {
		return
	}
	mu.RLock()
	l := lss[lid]
	if l == nil {
		mu.RUnlock()
		httpErr(w, 500, "failed to get livestream")
		return
	}
	if l.Owner.ID != uid {
		mu.RUnlock()
		httpErr(w, 403, "can't get other streamer's livecomment reports")
		return
	}
	b := make([]byte, 0, 4096)
	b = append(b, '[')
	for i, rp := range l.reports {
		if i > 0 {
			b = append(b, ',')
		}
		b = appendReport(b, rp)
	}
	b = append(b, ']')
	mu.RUnlock()
	writeJSON(w, 200, b)
}

// ---------- livecomments ----------

func getLivecommentsHandler(w http.ResponseWriter, r *http.Request, lid int64) {
	if _, ok := auth(w, r); !ok {
		return
	}
	limit, ok := parseLimit(r)
	if !ok {
		httpErr(w, 400, "limit query parameter must be integer")
		return
	}
	b := make([]byte, 0, 8192)
	b = append(b, '[')
	mu.RLock()
	if l := lss[lid]; l != nil {
		n := 0
		for i := len(l.comments) - 1; i >= 0; i-- {
			if limit >= 0 && n >= limit {
				break
			}
			if n > 0 {
				b = append(b, ',')
			}
			b = appendComment(b, l.comments[i])
			n++
		}
	}
	mu.RUnlock()
	b = append(b, ']')
	writeJSON(w, 200, b)
}

func getNgwordsHandler(w http.ResponseWriter, r *http.Request, lid int64) {
	uid, ok := auth(w, r)
	if !ok {
		return
	}
	b := make([]byte, 0, 1024)
	b = append(b, '[')
	n := 0
	mu.RLock()
	if l := lss[lid]; l != nil {
		for i := len(l.ngwords) - 1; i >= 0; i-- {
			ng := l.ngwords[i]
			if ng.UserID != uid {
				continue
			}
			if n > 0 {
				b = append(b, ',')
			}
			b = appendNG(b, ng)
			n++
		}
	}
	mu.RUnlock()
	if n == 0 {
		writeJSON(w, 200, []byte("null"))
		return
	}
	b = append(b, ']')
	writeJSON(w, 200, b)
}

func postLivecommentHandler(w http.ResponseWriter, r *http.Request, lid int64) {
	uid, ok := auth(w, r)
	if !ok {
		return
	}
	var req struct {
		Comment string `json:"comment"`
		Tip     int64  `json:"tip"`
	}
	if !decodeBody(r, &req) {
		httpErr(w, 400, "failed to decode the request body as json")
		return
	}
	now := time.Now().Unix()
	mu.Lock()
	l := lss[lid]
	if l == nil {
		mu.Unlock()
		httpErr(w, 404, "livestream not found")
		return
	}
	for _, ng := range l.ngwords {
		if ng.UserID == l.Owner.ID && strings.Contains(req.Comment, ng.Word) {
			mu.Unlock()
			httpErr(w, 400, "このコメントがスパム判定されました")
			return
		}
	}
	u := users[uid]
	if u == nil {
		mu.Unlock()
		httpErr(w, 500, "user not found")
		return
	}
	if os.Getenv("SPAMLOG") != "" {
		for _, ng := range l.ngwords {
			if ng.ID > initNGMax {
				log.Printf("SPAMLOG ls=%d word=%q comment=%q", lid, ng.Word, req.Comment)
			}
		}
	}
	nextCommentID++
	c := &Comment{ID: nextCommentID, User: u, LS: l, Comment: req.Comment, Tip: req.Tip, CreatedAt: now}
	comments[c.ID] = c
	addCommentStats(c)
	enqueue("INSERT INTO livecomments (id, user_id, livestream_id, comment, tip, created_at) VALUES (?, ?, ?, ?, ?, ?)", c.ID, uid, lid, c.Comment, c.Tip, now)
	b := appendComment(make([]byte, 0, 2048), c)
	mu.Unlock()
	writeJSON(w, 201, b)
}

func reportHandler(w http.ResponseWriter, r *http.Request, lid, cid int64) {
	uid, ok := auth(w, r)
	if !ok {
		return
	}
	now := time.Now().Unix()
	mu.Lock()
	if lss[lid] == nil {
		mu.Unlock()
		httpErr(w, 404, "livestream not found")
		return
	}
	c := comments[cid]
	if c == nil {
		mu.Unlock()
		httpErr(w, 404, "livecomment not found")
		return
	}
	u := users[uid]
	if u == nil {
		mu.Unlock()
		httpErr(w, 500, "user not found")
		return
	}
	nextReportID++
	rp := &Report{ID: nextReportID, User: u, LSID: lid, Comment: c, CreatedAt: now}
	lss[lid].reports = append(lss[lid].reports, rp)
	enqueue("INSERT INTO livecomment_reports (id, user_id, livestream_id, livecomment_id, created_at) VALUES (?, ?, ?, ?, ?)", rp.ID, uid, lid, cid, now)
	b := appendReport(make([]byte, 0, 4096), rp)
	mu.Unlock()
	writeJSON(w, 201, b)
}

func moderateHandler(w http.ResponseWriter, r *http.Request, lid int64) {
	uid, ok := auth(w, r)
	if !ok {
		return
	}
	var req struct {
		NGWord string `json:"ng_word"`
	}
	if !decodeBody(r, &req) {
		httpErr(w, 400, "failed to decode the request body as json")
		return
	}
	now := time.Now().Unix()
	mu.Lock()
	l := lss[lid]
	if l == nil || l.Owner.ID != uid {
		mu.Unlock()
		httpErr(w, 400, "A streamer can't moderate livestreams that other streamers own")
		return
	}
	nextNGID++
	ng := &NGWord{ID: nextNGID, UserID: uid, LivestreamID: lid, Word: req.NGWord, CreatedAt: now}
	l.ngwords = append(l.ngwords, ng)
	enqueue("INSERT INTO ng_words (id, user_id, livestream_id, word, created_at) VALUES (?, ?, ?, ?, ?)", ng.ID, uid, lid, ng.Word, now)
	kept := l.comments[:0]
	var removed []*Comment
	for _, c := range l.comments {
		hit := false
		for _, w := range l.ngwords {
			if strings.Contains(c.Comment, w.Word) {
				hit = true
				break
			}
		}
		if hit {
			removed = append(removed, c)
		} else {
			kept = append(kept, c)
		}
	}
	for i := len(kept); i < len(l.comments); i++ {
		l.comments[i] = nil
	}
	l.comments = kept
	for _, c := range removed {
		removeCommentStats(c)
		enqueue("DELETE FROM livecomments WHERE id = ?", c.ID)
	}
	mu.Unlock()
	writeJSON(w, 201, append(appendInt([]byte(`{"word_id":`), ng.ID), '}'))
}

// ---------- reactions ----------

func getReactionsHandler(w http.ResponseWriter, r *http.Request, lid int64) {
	if _, ok := auth(w, r); !ok {
		return
	}
	limit, ok := parseLimit(r)
	if !ok {
		httpErr(w, 400, "limit query parameter must be integer")
		return
	}
	b := make([]byte, 0, 8192)
	b = append(b, '[')
	mu.RLock()
	if l := lss[lid]; l != nil {
		n := 0
		for i := len(l.reactions) - 1; i >= 0; i-- {
			if limit >= 0 && n >= limit {
				break
			}
			if n > 0 {
				b = append(b, ',')
			}
			b = appendReaction(b, l.reactions[i])
			n++
		}
	}
	mu.RUnlock()
	b = append(b, ']')
	writeJSON(w, 200, b)
}

func postReactionHandler(w http.ResponseWriter, r *http.Request, lid int64) {
	uid, ok := auth(w, r)
	if !ok {
		return
	}
	var req struct {
		EmojiName string `json:"emoji_name"`
	}
	if !decodeBody(r, &req) {
		httpErr(w, 400, "failed to decode the request body as json")
		return
	}
	now := time.Now().Unix()
	mu.Lock()
	l := lss[lid]
	u := users[uid]
	if l == nil || u == nil {
		mu.Unlock()
		httpErr(w, 500, "failed to fill reaction")
		return
	}
	nextReactionID++
	rc := &Reaction{ID: nextReactionID, User: u, LS: l, Emoji: req.EmojiName, CreatedAt: now}
	addReactionStats(rc)
	enqueue("INSERT INTO reactions (id, user_id, livestream_id, emoji_name, created_at) VALUES (?, ?, ?, ?, ?)", rc.ID, uid, lid, rc.Emoji, now)
	b := appendReaction(make([]byte, 0, 2048), rc)
	mu.Unlock()
	writeJSON(w, 201, b)
}

// ---------- stats ----------

func getUserStatsHandler(w http.ResponseWriter, r *http.Request, name string) {
	if _, ok := auth(w, r); !ok {
		return
	}
	mu.RLock()
	u := usersByName[name]
	if u == nil {
		mu.RUnlock()
		httpErr(w, 400, "not found user that has the given username")
		return
	}
	s := u.score()
	var rank int64 = 1
	for _, v := range userList {
		vs := v.score()
		if vs > s || (vs == s && v.Name > u.Name) {
			rank++
		}
	}
	fav := ""
	var favN int64
	for e, n := range u.emoji {
		if n <= 0 {
			continue
		}
		if n > favN || (n == favN && e > fav) {
			fav, favN = e, n
		}
	}
	b := []byte(`{"rank":`)
	b = appendInt(b, rank)
	b = append(b, `,"viewers_count":`...)
	b = appendInt(b, u.viewers)
	b = append(b, `,"total_reactions":`...)
	b = appendInt(b, u.reactions)
	b = append(b, `,"total_livecomments":`...)
	b = appendInt(b, u.comments)
	b = append(b, `,"total_tip":`...)
	b = appendInt(b, u.tips)
	b = append(b, `,"favorite_emoji":`...)
	b = appendStr(b, fav)
	b = append(b, '}')
	mu.RUnlock()
	writeJSON(w, 200, b)
}

func getLivestreamStatsHandler(w http.ResponseWriter, r *http.Request, lid int64) {
	if _, ok := auth(w, r); !ok {
		return
	}
	mu.RLock()
	l := lss[lid]
	if l == nil {
		mu.RUnlock()
		httpErr(w, 400, "cannot get stats of not found livestream")
		return
	}
	s := l.score()
	var rank int64 = 1
	for _, v := range lsList {
		vs := v.score()
		if vs > s || (vs == s && v.ID > l.ID) {
			rank++
		}
	}
	var maxTip int64
	for _, c := range l.comments {
		if c.Tip > maxTip {
			maxTip = c.Tip
		}
	}
	b := []byte(`{"rank":`)
	b = appendInt(b, rank)
	b = append(b, `,"viewers_count":`...)
	b = appendInt(b, l.viewers)
	b = append(b, `,"total_reactions":`...)
	b = appendInt(b, int64(len(l.reactions)))
	b = append(b, `,"total_reports":`...)
	b = appendInt(b, int64(len(l.reports)))
	b = append(b, `,"max_tip":`...)
	b = appendInt(b, maxTip)
	b = append(b, '}')
	mu.RUnlock()
	writeJSON(w, 200, b)
}

