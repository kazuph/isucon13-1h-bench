package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type verifiedKey struct {
	userID int64
	pwHash [32]byte
}

type decodedIcon struct {
	hash string
	img  []byte
}

type Server struct {
	backend Backend

	verMu    sync.RWMutex
	verified map[verifiedKey]struct{}

	iconB64Mu    sync.RWMutex
	iconB64Cache map[[32]byte]decodedIcon

	bodyPool sync.Pool

	// Lightweight atomic metrics
	totalReqs     atomic.Int64
	http1Reqs     atomic.Int64
	http2Reqs     atomic.Int64
	icon304Reqs   atomic.Int64
	icon200Reqs   atomic.Int64
	iconB64Hits   atomic.Int64
	iconB64Misses atomic.Int64
	bcryptGenCnt  atomic.Int64
	bcryptCmpCnt  atomic.Int64
	bcryptHitCnt  atomic.Int64
	statMu        sync.Mutex
	epCount       map[string]*atomic.Int64
	epTotalNanos  map[string]*atomic.Int64
}

func NewServer(backend Backend) *Server {
	return &Server{
		backend:      backend,
		verified:     make(map[verifiedKey]struct{}, 8192),
		iconB64Cache: make(map[[32]byte]decodedIcon, 256),
		bodyPool: sync.Pool{
			New: func() any {
				b := bytes.NewBuffer(make([]byte, 0, 128*1024))
				return b
			},
		},
		epCount:      make(map[string]*atomic.Int64),
		epTotalNanos: make(map[string]*atomic.Int64),
	}
}

func (sv *Server) ClearVerifiedCache() {
	sv.verMu.Lock()
	sv.verified = make(map[verifiedKey]struct{}, 8192)
	sv.verMu.Unlock()
}

func (sv *Server) recordMetric(ep string, dur time.Duration) {
	sv.statMu.Lock()
	c, ok := sv.epCount[ep]
	if !ok {
		c = &atomic.Int64{}
		t := &atomic.Int64{}
		sv.epCount[ep] = c
		sv.epTotalNanos[ep] = t
	}
	t := sv.epTotalNanos[ep]
	sv.statMu.Unlock()
	c.Add(1)
	t.Add(dur.Nanoseconds())
}

func writeJSONBytes(w http.ResponseWriter, status int, body []byte) {
	if len(body) == 0 {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	b, _ := json.Marshal(&ErrorResponse{Error: msg})
	writeJSONBytes(w, status, b)
}

func parseLimitQuery(r *http.Request) (int, bool) {
	q := r.URL.Query().Get("limit")
	if q == "" {
		return -1, true
	}
	v, err := strconv.Atoi(q)
	if err != nil {
		return 0, false
	}
	return v, true
}

func (sv *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	sv.totalReqs.Add(1)
	if r.ProtoMajor == 2 {
		sv.http2Reqs.Add(1)
	} else {
		sv.http1Reqs.Add(1)
	}
	path := r.URL.Path
	method := r.Method

	if path == "/_debug/stats" {
		sv.handleDebugStats(w)
		return
	}

	if !strings.HasPrefix(path, "/api/") {
		http.NotFound(w, r)
		return
	}
	rest := path[len("/api/"):]

	var ep string
	switch {
	case rest == "tag" && method == http.MethodGet:
		ep = "GET /api/tag"
		writeJSONBytes(w, http.StatusOK, sv.backend.GetTagsJSON())

	case rest == "initialize" && method == http.MethodPost:
		ep = "POST /api/initialize"
		sv.ClearVerifiedCache()
		status, body := sv.backend.Initialize()
		writeJSONBytes(w, status, body)

	case rest == "payment" && method == http.MethodGet:
		ep = "GET /api/payment"
		status, body := sv.backend.GetPaymentResult()
		writeJSONBytes(w, status, body)

	case rest == "register" && method == http.MethodPost:
		ep = "POST /api/register"
		sv.handleRegister(w, r)

	case rest == "login" && method == http.MethodPost:
		ep = "POST /api/login"
		sv.handleLogin(w, r)

	case rest == "icon" && method == http.MethodPost:
		ep = "POST /api/icon"
		sv.handlePostIcon(w, r)

	case strings.HasPrefix(rest, "user/"):
		ep = sv.routeUser(w, r, method, rest[len("user/"):])

	case rest == "livestream" && method == http.MethodGet:
		ep = "GET /api/livestream"
		uid, st, msg := verifySession(r)
		if st != 0 {
			writeErr(w, st, msg)
		} else {
			status, body := sv.backend.GetMyLivestreams(uid)
			writeJSONBytes(w, status, body)
		}

	case strings.HasPrefix(rest, "livestream/"):
		ep = sv.routeLivestream(w, r, method, rest[len("livestream/"):])

	default:
		ep = method + " 404"
		writeErr(w, http.StatusNotFound, "Not Found")
	}

	sv.recordMetric(ep, time.Since(start))
}

func (sv *Server) routeUser(w http.ResponseWriter, r *http.Request, method, sub string) string {
	if sub == "me" && method == http.MethodGet {
		uid, st, msg := verifySession(r)
		if st != 0 {
			writeErr(w, st, msg)
			return "GET /api/user/me"
		}
		status, body := sv.backend.GetMe(uid)
		writeJSONBytes(w, status, body)
		return "GET /api/user/me"
	}

	slash := strings.IndexByte(sub, '/')
	if slash < 0 {
		if method == http.MethodGet {
			username, err := url.PathUnescape(sub)
			if err != nil {
				username = sub
			}
			if _, st, msg := verifySession(r); st != 0 {
				writeErr(w, st, msg)
				return "GET /api/user/:username"
			}
			status, body := sv.backend.GetUser(username)
			writeJSONBytes(w, status, body)
			return "GET /api/user/:username"
		}
		writeErr(w, http.StatusMethodNotAllowed, "Method Not Allowed")
		return method + " /api/user/:username"
	}

	rawUser := sub[:slash]
	username, err := url.PathUnescape(rawUser)
	if err != nil {
		username = rawUser
	}
	action := sub[slash+1:]

	switch action {
	case "icon":
		if method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "Method Not Allowed")
			return method + " /api/user/:username/icon"
		}
		sv.handleGetIcon(w, r, username)
		return "GET /api/user/:username/icon"

	case "theme":
		if method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "Method Not Allowed")
			return method + " /api/user/:username/theme"
		}
		if _, st, msg := verifySession(r); st != 0 {
			writeErr(w, st, msg)
			return "GET /api/user/:username/theme"
		}
		status, body := sv.backend.GetStreamerTheme(username)
		writeJSONBytes(w, status, body)
		return "GET /api/user/:username/theme"

	case "statistics":
		if method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "Method Not Allowed")
			return method + " /api/user/:username/statistics"
		}
		if _, st, msg := verifySession(r); st != 0 {
			writeErr(w, st, msg)
			return "GET /api/user/:username/statistics"
		}
		status, body := sv.backend.GetUserStatistics(username)
		writeJSONBytes(w, status, body)
		return "GET /api/user/:username/statistics"

	case "livestream":
		if method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "Method Not Allowed")
			return method + " /api/user/:username/livestream"
		}
		if _, st, msg := verifySession(r); st != 0 {
			writeErr(w, st, msg)
			return "GET /api/user/:username/livestream"
		}
		status, body := sv.backend.GetUserLivestreams(username)
		writeJSONBytes(w, status, body)
		return "GET /api/user/:username/livestream"

	default:
		writeErr(w, http.StatusNotFound, "Not Found")
		return method + " /api/user/404"
	}
}

func (sv *Server) routeLivestream(w http.ResponseWriter, r *http.Request, method, sub string) string {
	if sub == "reservation" && method == http.MethodPost {
		uid, st, msg := verifySession(r)
		if st != 0 {
			writeErr(w, st, msg)
			return "POST /api/livestream/reservation"
		}
		var req ReserveLivestreamRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "failed to decode the request body as json")
			return "POST /api/livestream/reservation"
		}
		status, body := sv.backend.ReserveLivestream(uid, &req)
		writeJSONBytes(w, status, body)
		return "POST /api/livestream/reservation"
	}

	if sub == "search" && method == http.MethodGet {
		tagName := r.URL.Query().Get("tag")
		limit := -1
		if tagName == "" {
			var ok bool
			limit, ok = parseLimitQuery(r)
			if !ok {
				writeErr(w, http.StatusBadRequest, "limit query parameter must be integer")
				return "GET /api/livestream/search"
			}
		}
		status, body := sv.backend.SearchLivestreams(tagName, limit)
		writeJSONBytes(w, status, body)
		return "GET /api/livestream/search"
	}

	slash := strings.IndexByte(sub, '/')
	if slash < 0 {
		if method == http.MethodGet {
			if _, st, msg := verifySession(r); st != 0 {
				writeErr(w, st, msg)
				return "GET /api/livestream/:id"
			}
			lid, err := strconv.ParseInt(sub, 10, 64)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "livestream_id in path must be integer")
				return "GET /api/livestream/:id"
			}
			status, body := sv.backend.GetLivestream(lid)
			writeJSONBytes(w, status, body)
			return "GET /api/livestream/:id"
		}
		writeErr(w, http.StatusNotFound, "Not Found")
		return method + " /api/livestream/404"
	}

	lidStr := sub[:slash]
	action := sub[slash+1:]

	lid, err := strconv.ParseInt(lidStr, 10, 64)
	if err != nil {
		if _, st, msg := verifySession(r); st != 0 {
			writeErr(w, st, msg)
			return method + " /api/livestream/:id/" + action
		}
		writeErr(w, http.StatusBadRequest, "livestream_id in path must be integer")
		return method + " /api/livestream/:id/" + action
	}

	switch action {
	case "livecomment":
		uid, st, msg := verifySession(r)
		if st != 0 {
			writeErr(w, st, msg)
			return method + " /api/livestream/:id/livecomment"
		}
		if method == http.MethodGet {
			limit, ok := parseLimitQuery(r)
			if !ok {
				writeErr(w, http.StatusBadRequest, "limit query parameter must be integer")
				return "GET /api/livestream/:id/livecomment"
			}
			status, body := sv.backend.GetLivecomments(lid, limit)
			writeJSONBytes(w, status, body)
			return "GET /api/livestream/:id/livecomment"
		}
		if method == http.MethodPost {
			var req PostLivecommentRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeErr(w, http.StatusBadRequest, "failed to decode the request body as json")
				return "POST /api/livestream/:id/livecomment"
			}
			status, body := sv.backend.PostLivecomment(uid, lid, req.Comment, req.Tip)
			writeJSONBytes(w, status, body)
			return "POST /api/livestream/:id/livecomment"
		}
		writeErr(w, http.StatusMethodNotAllowed, "Method Not Allowed")
		return method + " /api/livestream/:id/livecomment"

	case "reaction":
		uid, st, msg := verifySession(r)
		if st != 0 {
			writeErr(w, st, msg)
			return method + " /api/livestream/:id/reaction"
		}
		if method == http.MethodGet {
			limit, ok := parseLimitQuery(r)
			if !ok {
				writeErr(w, http.StatusBadRequest, "limit query parameter must be integer")
				return "GET /api/livestream/:id/reaction"
			}
			status, body := sv.backend.GetReactions(lid, limit)
			writeJSONBytes(w, status, body)
			return "GET /api/livestream/:id/reaction"
		}
		if method == http.MethodPost {
			var req PostReactionRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeErr(w, http.StatusBadRequest, "failed to decode the request body as json")
				return "POST /api/livestream/:id/reaction"
			}
			status, body := sv.backend.PostReaction(uid, lid, req.EmojiName)
			writeJSONBytes(w, status, body)
			return "POST /api/livestream/:id/reaction"
		}
		writeErr(w, http.StatusMethodNotAllowed, "Method Not Allowed")
		return method + " /api/livestream/:id/reaction"

	case "report":
		if method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "Method Not Allowed")
			return method + " /api/livestream/:id/report"
		}
		uid, st, msg := verifySession(r)
		if st != 0 {
			writeErr(w, st, msg)
			return "GET /api/livestream/:id/report"
		}
		status, body := sv.backend.GetLivecommentReports(uid, lid)
		writeJSONBytes(w, status, body)
		return "GET /api/livestream/:id/report"

	case "ngwords":
		if method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "Method Not Allowed")
			return method + " /api/livestream/:id/ngwords"
		}
		uid, st, msg := verifySession(r)
		if st != 0 {
			writeErr(w, st, msg)
			return "GET /api/livestream/:id/ngwords"
		}
		status, body := sv.backend.GetNgwords(uid, lid)
		writeJSONBytes(w, status, body)
		return "GET /api/livestream/:id/ngwords"

	case "moderate":
		if method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "Method Not Allowed")
			return method + " /api/livestream/:id/moderate"
		}
		uid, st, msg := verifySession(r)
		if st != 0 {
			writeErr(w, st, msg)
			return "POST /api/livestream/:id/moderate"
		}
		var req ModerateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "failed to decode the request body as json")
			return "POST /api/livestream/:id/moderate"
		}
		status, body := sv.backend.Moderate(uid, lid, req.NGWord)
		writeJSONBytes(w, status, body)
		return "POST /api/livestream/:id/moderate"

	case "enter":
		if method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "Method Not Allowed")
			return method + " /api/livestream/:id/enter"
		}
		uid, st, msg := verifySession(r)
		if st != 0 {
			writeErr(w, st, msg)
			return "POST /api/livestream/:id/enter"
		}
		status, body := sv.backend.EnterLivestream(uid, lid)
		writeJSONBytes(w, status, body)
		return "POST /api/livestream/:id/enter"

	case "exit":
		if method != http.MethodDelete {
			writeErr(w, http.StatusMethodNotAllowed, "Method Not Allowed")
			return method + " /api/livestream/:id/exit"
		}
		uid, st, msg := verifySession(r)
		if st != 0 {
			writeErr(w, st, msg)
			return "DELETE /api/livestream/:id/exit"
		}
		status, body := sv.backend.ExitLivestream(uid, lid)
		writeJSONBytes(w, status, body)
		return "DELETE /api/livestream/:id/exit"

	case "statistics":
		if method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "Method Not Allowed")
			return method + " /api/livestream/:id/statistics"
		}
		if _, st, msg := verifySession(r); st != 0 {
			writeErr(w, st, msg)
			return "GET /api/livestream/:id/statistics"
		}
		status, body := sv.backend.GetLivestreamStatistics(lid)
		writeJSONBytes(w, status, body)
		return "GET /api/livestream/:id/statistics"

	default:
		if method == http.MethodPost && strings.HasPrefix(action, "livecomment/") && strings.HasSuffix(action, "/report") {
			uid, st, msg := verifySession(r)
			if st != 0 {
				writeErr(w, st, msg)
				return "POST /api/livestream/:id/livecomment/:cid/report"
			}
			cidStr := action[len("livecomment/") : len(action)-len("/report")]
			cid, err := strconv.ParseInt(cidStr, 10, 64)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "livecomment_id in path must be integer")
				return "POST /api/livestream/:id/livecomment/:cid/report"
			}
			status, body := sv.backend.ReportLivecomment(uid, lid, cid)
			writeJSONBytes(w, status, body)
			return "POST /api/livestream/:id/livecomment/:cid/report"
		}
		writeErr(w, http.StatusNotFound, "Not Found")
		return method + " /api/livestream/404"
	}
}

func (sv *Server) handleGetIcon(w http.ResponseWriter, r *http.Request, username string) {
	hash, img, found := sv.backend.GetIcon(username)
	if !found {
		writeErr(w, http.StatusNotFound, "not found user that has the given username")
		return
	}
	etag := `"` + hash + `"`
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		clean := strings.TrimSpace(inm)
		clean = strings.TrimPrefix(clean, "W/")
		clean = strings.Trim(clean, `"`)
		if clean == hash {
			sv.icon304Reqs.Add(1)
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	sv.icon200Reqs.Add(1)
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Length", strconv.Itoa(len(img)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(img)
}

var imageKeyPattern = []byte(`"image"`)

// extractFastBase64 extracts the raw base64 slice from {"image":"<b64>"} without reflection.
func extractFastBase64(body []byte) ([]byte, bool) {
	idx := bytes.Index(body, imageKeyPattern)
	if idx < 0 {
		return nil, false
	}
	i := idx + len(imageKeyPattern)
	for i < len(body) && (body[i] == ' ' || body[i] == '\t' || body[i] == '\n' || body[i] == '\r') {
		i++
	}
	if i >= len(body) || body[i] != ':' {
		return nil, false
	}
	i++
	for i < len(body) && (body[i] == ' ' || body[i] == '\t' || body[i] == '\n' || body[i] == '\r') {
		i++
	}
	if i >= len(body) || body[i] != '"' {
		return nil, false
	}
	start := i + 1
	end := bytes.IndexByte(body[start:], '"')
	if end < 0 {
		return nil, false
	}
	raw := body[start : start+end]
	if bytes.IndexByte(raw, '\\') >= 0 {
		return nil, false
	}
	return raw, true
}

func (sv *Server) handlePostIcon(w http.ResponseWriter, r *http.Request) {
	uid, st, msg := verifySession(r)
	if st != 0 {
		writeErr(w, st, msg)
		return
	}
	buf := sv.bodyPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer sv.bodyPool.Put(buf)

	if _, err := io.Copy(buf, io.LimitReader(r.Body, 10*1024*1024)); err != nil {
		writeErr(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	bodyBytes := buf.Bytes()

	var hashHex string
	var img []byte

	if b64, ok := extractFastBase64(bodyBytes); ok {
		key := sha256.Sum256(b64)
		sv.iconB64Mu.RLock()
		cached, hit := sv.iconB64Cache[key]
		sv.iconB64Mu.RUnlock()
		if hit {
			sv.iconB64Hits.Add(1)
			hashHex = cached.hash
			img = cached.img
		} else {
			sv.iconB64Misses.Add(1)
			dec := make([]byte, base64.StdEncoding.DecodedLen(len(b64)))
			n, err := base64.StdEncoding.Decode(dec, b64)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "failed to decode base64 image")
				return
			}
			img = dec[:n]
			sum := sha256.Sum256(img)
			hashHex = hex.EncodeToString(sum[:])
			sv.iconB64Mu.Lock()
			sv.iconB64Cache[key] = decodedIcon{hash: hashHex, img: img}
			sv.iconB64Mu.Unlock()
		}
	} else {
		var req PostIconRequest
		if err := json.Unmarshal(bodyBytes, &req); err != nil {
			writeErr(w, http.StatusBadRequest, "failed to decode the request body as json")
			return
		}
		img = req.Image
		sum := sha256.Sum256(img)
		hashHex = hex.EncodeToString(sum[:])
	}

	status, body := sv.backend.PostIcon(uid, hashHex, img)
	writeJSONBytes(w, status, body)
}

func (sv *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req PostUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "failed to decode the request body as json")
		return
	}
	if req.Name == "pipe" {
		writeErr(w, http.StatusBadRequest, "the username 'pipe' is reserved")
		return
	}
	sv.bcryptGenCnt.Add(1)
	hashed, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.MinCost)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to generate hashed password: "+err.Error())
		return
	}
	status, uid, body := sv.backend.Register(req.Name, req.DisplayName, req.Description, string(hashed), req.Theme.DarkMode)
	if status == http.StatusCreated && uid > 0 {
		vk := verifiedKey{userID: uid, pwHash: sha256.Sum256([]byte(req.Password))}
		sv.verMu.Lock()
		sv.verified[vk] = struct{}{}
		sv.verMu.Unlock()
	}
	writeJSONBytes(w, status, body)
}

func (sv *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "failed to decode the request body as json")
		return
	}
	uid, hashed, found := sv.backend.GetUserAuth(req.Username)
	if !found {
		writeErr(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	vk := verifiedKey{userID: uid, pwHash: sha256.Sum256([]byte(req.Password))}
	sv.verMu.RLock()
	_, ok := sv.verified[vk]
	sv.verMu.RUnlock()
	if ok {
		sv.bcryptHitCnt.Add(1)
	} else {
		sv.bcryptCmpCnt.Add(1)
		if err := bcrypt.CompareHashAndPassword([]byte(hashed), []byte(req.Password)); err != nil {
			if err == bcrypt.ErrMismatchedHashAndPassword {
				writeErr(w, http.StatusUnauthorized, "invalid username or password")
				return
			}
			writeErr(w, http.StatusInternalServerError, "failed to compare hash and password: "+err.Error())
			return
		}
		sv.verMu.Lock()
		sv.verified[vk] = struct{}{}
		sv.verMu.Unlock()
	}

	setSessionCookie(w, uid)
	w.WriteHeader(http.StatusOK)
}

func (sv *Server) handleDebugStats(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "total_reqs=%d http1=%d http2=%d icon_304=%d icon_200=%d icon_b64_hit=%d icon_b64_miss=%d bcrypt_gen=%d bcrypt_cmp=%d bcrypt_cache_hit=%d\n",
		sv.totalReqs.Load(), sv.http1Reqs.Load(), sv.http2Reqs.Load(),
		sv.icon304Reqs.Load(), sv.icon200Reqs.Load(),
		sv.iconB64Hits.Load(), sv.iconB64Misses.Load(),
		sv.bcryptGenCnt.Load(), sv.bcryptCmpCnt.Load(), sv.bcryptHitCnt.Load())
	sv.statMu.Lock()
	defer sv.statMu.Unlock()
	for ep, c := range sv.epCount {
		cnt := c.Load()
		totMs := float64(sv.epTotalNanos[ep].Load()) / 1e6
		avgMs := 0.0
		if cnt > 0 {
			avgMs = totMs / float64(cnt)
		}
		fmt.Fprintf(w, "%6d  tot=%9.2fms  avg=%7.3fms  %s\n", cnt, totMs, avgMs, ep)
	}
}
