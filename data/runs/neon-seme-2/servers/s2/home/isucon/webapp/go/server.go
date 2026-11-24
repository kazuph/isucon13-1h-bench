package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const bcryptCost = bcrypt.MinCost

type AppServer struct {
	isCore  bool
	store   *Store
	peers   *PeerBroadcaster
	edge    *EdgeCache
	metrics *Metrics
}

type statusCaptureWriter struct {
	http.ResponseWriter
	status int
}

func (scw *statusCaptureWriter) WriteHeader(code int) {
	scw.status = code
	scw.ResponseWriter.WriteHeader(code)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	writeJSONFast(w, status, v)
}

func writeRawJSON(w http.ResponseWriter, status int, b []byte) {
	writeBufJSON(w, status, b)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	bp := getRespBuf()
	b := append(*bp, `{"error":`...)
	b = appendJSONString(b, msg)
	b = append(b, '}')
	writeBufJSON(w, status, b)
	*bp = b
	putRespBuf(bp)
}

func (a *AppServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	if strings.HasPrefix(path, "/internal/") {
		a.serveInternal(w, r)
		return
	}

	if a.metrics == nil {
		a.routePublic(w, r, path)
		return
	}
	t0 := time.Now()
	scw := &statusCaptureWriter{ResponseWriter: w, status: 200}
	ep := a.routePublic(scw, r, path)
	a.metrics.Record(ep, time.Since(t0), scw.status)
}

func (a *AppServer) routePublic(w http.ResponseWriter, r *http.Request, path string) string {
	if !strings.HasPrefix(path, "/api/") {
		http.NotFound(w, r)
		return "NON_API"
	}

	sub := path[5:] // after "/api/"

	switch sub {
	case "initialize":
		if r.Method == http.MethodPost {
			a.handleInitialize(w, r)
			return "POST /api/initialize"
		}
	case "tag":
		if r.Method == http.MethodGet {
			a.handleGetTags(w, r)
			return "GET /api/tag"
		}
	case "register":
		if r.Method == http.MethodPost {
			a.handleRegister(w, r)
			return "POST /api/register"
		}
	case "login":
		if r.Method == http.MethodPost {
			a.handleLogin(w, r)
			return "POST /api/login"
		}
	case "icon":
		if r.Method == http.MethodPost {
			a.handlePostIcon(w, r)
			return "POST /api/icon"
		}
	case "payment":
		if r.Method == http.MethodGet {
			a.handlePayment(w, r)
			return "GET /api/payment"
		}
	case "livestream":
		if r.Method == http.MethodGet {
			a.handleGetMyLivestreams(w, r)
			return "GET /api/livestream"
		}
	case "livestream/reservation":
		if r.Method == http.MethodPost {
			a.handleReserveLivestream(w, r)
			return "POST /api/livestream/reservation"
		}
	case "livestream/search":
		if r.Method == http.MethodGet {
			a.handleSearchLivestreams(w, r)
			return "GET /api/livestream/search"
		}
	case "user/me":
		if r.Method == http.MethodGet {
			a.handleGetMe(w, r)
			return "GET /api/user/me"
		}
	}

	if strings.HasPrefix(sub, "user/") {
		rest := sub[5:]
		slash := strings.IndexByte(rest, '/')
		if slash < 0 {
			if r.Method == http.MethodGet {
				a.handleGetUser(w, r, rest)
				return "GET /api/user/:u"
			}
		} else {
			username := rest[:slash]
			tail := rest[slash+1:]
			if r.Method == http.MethodGet {
				switch tail {
				case "icon":
					a.handleGetIcon(w, r, username)
					return "GET /api/user/:u/icon"
				case "theme":
					a.handleGetStreamerTheme(w, r, username)
					return "GET /api/user/:u/theme"
				case "statistics":
					a.handleGetUserStatistics(w, r, username)
					return "GET /api/user/:u/statistics"
				case "livestream":
					a.handleGetUserLivestreams(w, r, username)
					return "GET /api/user/:u/livestream"
				}
			}
		}
	}

	if strings.HasPrefix(sub, "livestream/") {
		rest := sub[11:]
		slash := strings.IndexByte(rest, '/')
		if slash < 0 {
			if r.Method == http.MethodGet {
				a.handleGetLivestream(w, r, rest)
				return "GET /api/livestream/:id"
			}
		} else {
			idStr := rest[:slash]
			tail := rest[slash+1:]
			switch tail {
			case "livecomment":
				if r.Method == http.MethodGet {
					a.handleGetLivecomments(w, r, idStr)
					return "GET /api/livestream/:id/livecomment"
				}
				if r.Method == http.MethodPost {
					a.handlePostLivecomment(w, r, idStr)
					return "POST /api/livestream/:id/livecomment"
				}
			case "reaction":
				if r.Method == http.MethodGet {
					a.handleGetReactions(w, r, idStr)
					return "GET /api/livestream/:id/reaction"
				}
				if r.Method == http.MethodPost {
					a.handlePostReaction(w, r, idStr)
					return "POST /api/livestream/:id/reaction"
				}
			case "report":
				if r.Method == http.MethodGet {
					a.handleGetReports(w, r, idStr)
					return "GET /api/livestream/:id/report"
				}
			case "ngwords":
				if r.Method == http.MethodGet {
					a.handleGetNGWords(w, r, idStr)
					return "GET /api/livestream/:id/ngwords"
				}
			case "moderate":
				if r.Method == http.MethodPost {
					a.handleModerate(w, r, idStr)
					return "POST /api/livestream/:id/moderate"
				}
			case "enter":
				if r.Method == http.MethodPost {
					a.handleEnter(w, r, idStr)
					return "POST /api/livestream/:id/enter"
				}
			case "exit":
				if r.Method == http.MethodDelete {
					a.handleExit(w, r, idStr)
					return "DELETE /api/livestream/:id/exit"
				}
			case "statistics":
				if r.Method == http.MethodGet {
					a.handleGetLivestreamStatistics(w, r, idStr)
					return "GET /api/livestream/:id/statistics"
				}
			default:
				if r.Method == http.MethodPost && strings.HasPrefix(tail, "livecomment/") && strings.HasSuffix(tail, "/report") {
					cidStr := tail[len("livecomment/") : len(tail)-len("/report")]
					a.handleReportLivecomment(w, r, idStr, cidStr)
					return "POST /api/livestream/:id/livecomment/:cid/report"
				}
			}
		}
	}

	http.NotFound(w, r)
	return "404"
}

// --- Internal RPC Handlers ---

func (a *AppServer) serveInternal(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/internal/reset":
		if a.edge != nil {
			a.edge.ResetToSnapshot()
		}
		w.WriteHeader(http.StatusOK)
	case "/internal/push_user":
		if a.edge != nil {
			var u InternalUserAuth
			if err := json.NewDecoder(r.Body).Decode(&u); err == nil {
				a.edge.PutUser(u)
			}
		}
		w.WriteHeader(http.StatusOK)
	case "/internal/push_icon":
		if a.edge != nil {
			var msg InternalPushIcon
			if err := json.NewDecoder(r.Body).Decode(&msg); err == nil {
				a.edge.PutIcon(msg.Username, msg.Hash, msg.Image)
			}
		}
		w.WriteHeader(http.StatusOK)
	case "/internal/sync_edge":
		if !a.isCore {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, a.store.GetEdgeSync())
	case "/internal/user_auth":
		if !a.isCore {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		username := r.URL.Query().Get("username")
		ua, ok := a.store.GetUserAuth(username)
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, ua)
	case "/internal/register":
		if !a.isCore {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req InternalRegisterReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "bad json")
			return
		}
		u, err := a.store.Register(req)
		if err != nil {
			writeErr(w, mapStoreErr(err), err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, u)
	case "/internal/icon":
		if !a.isCore {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req InternalSetIconReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "bad json")
			return
		}
		id, err := a.store.SetIcon(req.UserID, req.Hash, req.Image)
		if err != nil {
			writeErr(w, mapStoreErr(err), err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, PostIconResponse{ID: id})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// --- Public Handlers ---

var initRespBytes = []byte(`{"language":"golang"}`)

func (a *AppServer) handleInitialize(w http.ResponseWriter, r *http.Request) {
	if !a.isCore {
		a.edge.ProxyToCore(w, r)
		return
	}
	resetSessionCache()
	if a.metrics != nil {
		a.metrics.Reset()
	}
	if err := a.store.Initialize(); err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to initialize: "+err.Error())
		return
	}
	if a.peers != nil {
		a.peers.ResetPeers()
	}
	writeBufJSON(w, http.StatusOK, initRespBytes)
}

func (a *AppServer) handleGetTags(w http.ResponseWriter, r *http.Request) {
	if a.isCore {
		writeBufJSON(w, http.StatusOK, a.store.GetTagsJSON())
	} else {
		writeBufJSON(w, http.StatusOK, a.edge.tagsJSON)
	}
}

func (a *AppServer) handleGetIcon(w http.ResponseWriter, r *http.Request, rawUsername string) {
	username := rawUsername
	if strings.IndexByte(rawUsername, '%') >= 0 {
		if u, err := url.PathUnescape(rawUsername); err == nil {
			username = u
		}
	}
	if a.isCore {
		hash, err := a.store.GetIconHash(username)
		if err != nil {
			writeErr(w, mapStoreErr(err), "not found user that has the given username")
			return
		}
		etag := `"` + hash + `"`
		w.Header().Set("ETag", etag)
		if inm := r.Header.Get("If-None-Match"); inm != "" {
			if inm == etag || strings.Trim(strings.TrimPrefix(inm, "W/"), `"`) == hash {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		img := a.store.GetIconBlob(hash)
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Length", strconv.Itoa(len(img)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(img)
		return
	}

	hash, img, ok := a.edge.GetIcon(username)
	if !ok {
		writeErr(w, http.StatusNotFound, "not found user that has the given username")
		return
	}
	etag := `"` + hash + `"`
	w.Header().Set("ETag", etag)
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		if inm == etag || strings.Trim(strings.TrimPrefix(inm, "W/"), `"`) == hash {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(img)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(img)
}

func (a *AppServer) handlePostIcon(w http.ResponseWriter, r *http.Request) {
	sd, code, msg := verifySession(r)
	if code != 0 {
		writeErr(w, code, msg)
		return
	}
	body, release, err := readBodyPooled(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer release()

	if a.isCore {
		if b64, ok := extractIconBase64Fast(body); ok {
			id, err := a.store.SetIconFromBase64(sd.UserID, b64)
			if err != nil {
				writeErr(w, mapStoreErr(err), err.Error())
				return
			}
			var respBuf [32]byte
			b := append(respBuf[:0], `{"id":`...)
			b = strconv.AppendInt(b, id, 10)
			b = append(b, '}')
			writeBufJSON(w, http.StatusCreated, b)
			return
		}
		var req PostIconRequest
		if err := json.Unmarshal(body, &req); err != nil {
			writeErr(w, http.StatusBadRequest, "failed to decode the request body as json")
			return
		}
		id, err := a.store.SetIcon(sd.UserID, "", req.Image)
		if err != nil {
			writeErr(w, mapStoreErr(err), err.Error())
			return
		}
		var respBuf [32]byte
		b := append(respBuf[:0], `{"id":`...)
		b = strconv.AppendInt(b, id, 10)
		b = append(b, '}')
		writeBufJSON(w, http.StatusCreated, b)
		return
	}

	var req PostIconRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "failed to decode the request body as json")
		return
	}
	hash := sha256Hex(req.Image)
	e := a.edge
	e.mu.RLock()
	_, localHas := e.iconBlobs[hash]
	e.mu.RUnlock()
	rpcReq := InternalSetIconReq{UserID: sd.UserID, Hash: hash}
	if !localHas {
		rpcReq.Image = req.Image
	}
	resp, err := e.PostJSONToCore("/internal/icon", rpcReq)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	rbody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusCreated {
		e.PutIcon(sd.Username, hash, req.Image)
	}
	writeBufJSON(w, resp.StatusCode, rbody)
}

func (a *AppServer) handleRegister(w http.ResponseWriter, r *http.Request) {
	body, release, err := readBodyPooled(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "failed to decode the request body as json")
		return
	}
	var req PostUserRequest
	err = json.Unmarshal(body, &req)
	release()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "failed to decode the request body as json")
		return
	}
	if req.Name == "pipe" {
		writeErr(w, http.StatusBadRequest, "the username 'pipe' is reserved")
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcryptCost)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to generate hashed password: "+err.Error())
		return
	}

	ireq := InternalRegisterReq{
		Name:           req.Name,
		DisplayName:    req.DisplayName,
		Description:    req.Description,
		HashedPassword: string(hashedPassword),
		DarkMode:       req.Theme.DarkMode,
	}

	if a.isCore {
		u, err := a.store.Register(ireq)
		if err != nil {
			writeErr(w, mapStoreErr(err), err.Error())
			return
		}
		writeJSONFast(w, http.StatusCreated, u)
		return
	}

	resp, err := a.edge.PostJSONToCore("/internal/register", ireq)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	rbody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusCreated {
		var u User
		if json.Unmarshal(rbody, &u) == nil {
			a.edge.PutUser(InternalUserAuth{
				ID:             u.ID,
				Name:           u.Name,
				HashedPassword: ireq.HashedPassword,
				ThemeID:        u.Theme.ID,
				DarkMode:       u.Theme.DarkMode,
				IconHash:       u.IconHash,
			})
		}
	}
	writeBufJSON(w, resp.StatusCode, rbody)
}

func (a *AppServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	body, release, err := readBodyPooled(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "failed to decode the request body as json")
		return
	}
	var req LoginRequest
	err = json.Unmarshal(body, &req)
	release()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "failed to decode the request body as json")
		return
	}

	var ua InternalUserAuth
	var ok bool
	if a.isCore {
		ua, ok = a.store.GetUserAuth(req.Username)
	} else {
		var ptr *InternalUserAuth
		ptr, ok = a.edge.GetUser(req.Username)
		if ok {
			ua = *ptr
		}
	}
	if !ok {
		writeErr(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(ua.HashedPassword), []byte(req.Password)); err != nil {
		if err == bcrypt.ErrMismatchedHashAndPassword {
			writeErr(w, http.StatusUnauthorized, "invalid username or password")
			return
		}
		writeErr(w, http.StatusInternalServerError, "failed to compare hash and password: "+err.Error())
		return
	}

	setSessionCookie(w, ua.ID, ua.Name)
	w.WriteHeader(http.StatusOK)
}

func (a *AppServer) handleGetStreamerTheme(w http.ResponseWriter, r *http.Request, rawUsername string) {
	if _, code, msg := verifySession(r); code != 0 {
		writeErr(w, code, msg)
		return
	}
	username := rawUsername
	if strings.IndexByte(rawUsername, '%') >= 0 {
		if u, err := url.PathUnescape(rawUsername); err == nil {
			username = u
		}
	}
	var th Theme
	if a.isCore {
		var err error
		th, err = a.store.GetUserTheme(username)
		if err != nil {
			writeErr(w, mapStoreErr(err), "not found user that has the given username")
			return
		}
	} else {
		ua, ok := a.edge.GetUser(username)
		if !ok {
			writeErr(w, http.StatusNotFound, "not found user that has the given username")
			return
		}
		th = Theme{ID: ua.ThemeID, DarkMode: ua.DarkMode}
	}
	var buf [64]byte
	b := append(buf[:0], `{"id":`...)
	b = strconv.AppendInt(b, th.ID, 10)
	if th.DarkMode {
		b = append(b, `,"dark_mode":true}`...)
	} else {
		b = append(b, `,"dark_mode":false}`...)
	}
	writeBufJSON(w, http.StatusOK, b)
}

func (a *AppServer) handleGetMe(w http.ResponseWriter, r *http.Request) {
	sd, code, msg := verifySession(r)
	if code != 0 {
		writeErr(w, code, msg)
		return
	}
	if !a.isCore {
		a.edge.ProxyToCore(w, r)
		return
	}
	bp := getRespBuf()
	b, err := a.store.AppendUserByIDJSON(*bp, sd.UserID)
	if err != nil {
		*bp = b
		putRespBuf(bp)
		writeErr(w, mapStoreErr(err), "not found user that has the userid in session")
		return
	}
	writeBufJSON(w, http.StatusOK, b)
	*bp = b
	putRespBuf(bp)
}

func (a *AppServer) handleGetUser(w http.ResponseWriter, r *http.Request, rawUsername string) {
	if _, code, msg := verifySession(r); code != 0 {
		writeErr(w, code, msg)
		return
	}
	if !a.isCore {
		a.edge.ProxyToCore(w, r)
		return
	}
	username := rawUsername
	if strings.IndexByte(rawUsername, '%') >= 0 {
		if u, err := url.PathUnescape(rawUsername); err == nil {
			username = u
		}
	}
	bp := getRespBuf()
	b, err := a.store.AppendUserByNameJSON(*bp, username)
	if err != nil {
		*bp = b
		putRespBuf(bp)
		writeErr(w, mapStoreErr(err), "not found user that has the given username")
		return
	}
	writeBufJSON(w, http.StatusOK, b)
	*bp = b
	putRespBuf(bp)
}

func (a *AppServer) handleReserveLivestream(w http.ResponseWriter, r *http.Request) {
	sd, code, msg := verifySession(r)
	if code != 0 {
		writeErr(w, code, msg)
		return
	}
	if !a.isCore {
		a.edge.ProxyToCore(w, r)
		return
	}
	body, release, err := readBodyPooled(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "failed to decode the request body as json")
		return
	}
	var req ReserveLivestreamRequest
	err = json.Unmarshal(body, &req)
	release()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "failed to decode the request body as json")
		return
	}
	bp := getRespBuf()
	b, err := a.store.ReserveLivestreamJSON(*bp, sd.UserID, req)
	if err != nil {
		*bp = b
		putRespBuf(bp)
		writeErr(w, mapStoreErr(err), err.Error())
		return
	}
	writeBufJSON(w, http.StatusCreated, b)
	*bp = b
	putRespBuf(bp)
}

func (a *AppServer) handleSearchLivestreams(w http.ResponseWriter, r *http.Request) {
	if !a.isCore {
		a.edge.ProxyToCore(w, r)
		return
	}
	q := r.URL.Query()
	tagName := q.Get("tag")
	limitStr := q.Get("limit")
	hasLimit := false
	limit := 0
	if tagName == "" && limitStr != "" {
		var err error
		limit, err = strconv.Atoi(limitStr)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "limit query parameter must be integer")
			return
		}
		hasLimit = true
	}
	if tagName == "" && hasLimit && limit == 50 {
		writeBufJSON(w, http.StatusOK, a.store.SearchLivestreamsLimit50Cached())
		return
	}
	bp := getRespBuf()
	b := a.store.SearchLivestreamsJSON(*bp, tagName, hasLimit, limit)
	writeBufJSON(w, http.StatusOK, b)
	*bp = b
	putRespBuf(bp)
}

func (a *AppServer) handleGetMyLivestreams(w http.ResponseWriter, r *http.Request) {
	sd, code, msg := verifySession(r)
	if code != 0 {
		writeErr(w, code, msg)
		return
	}
	if !a.isCore {
		a.edge.ProxyToCore(w, r)
		return
	}
	bp := getRespBuf()
	b := a.store.AppendUserLivestreamsByIDJSON(*bp, sd.UserID)
	writeBufJSON(w, http.StatusOK, b)
	*bp = b
	putRespBuf(bp)
}

func (a *AppServer) handleGetUserLivestreams(w http.ResponseWriter, r *http.Request, rawUsername string) {
	if _, code, msg := verifySession(r); code != 0 {
		writeErr(w, code, msg)
		return
	}
	if !a.isCore {
		a.edge.ProxyToCore(w, r)
		return
	}
	username := rawUsername
	if strings.IndexByte(rawUsername, '%') >= 0 {
		if u, err := url.PathUnescape(rawUsername); err == nil {
			username = u
		}
	}
	bp := getRespBuf()
	b, err := a.store.AppendUserLivestreamsByNameJSON(*bp, username)
	if err != nil {
		*bp = b
		putRespBuf(bp)
		writeErr(w, mapStoreErr(err), "user not found")
		return
	}
	writeBufJSON(w, http.StatusOK, b)
	*bp = b
	putRespBuf(bp)
}

func (a *AppServer) handleGetLivestream(w http.ResponseWriter, r *http.Request, idStr string) {
	if _, code, msg := verifySession(r); code != 0 {
		writeErr(w, code, msg)
		return
	}
	if !a.isCore {
		a.edge.ProxyToCore(w, r)
		return
	}
	lsID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "livestream_id in path must be integer")
		return
	}
	bp := getRespBuf()
	b, err := a.store.AppendLivestreamJSON(*bp, lsID)
	if err != nil {
		*bp = b
		putRespBuf(bp)
		writeErr(w, mapStoreErr(err), "not found livestream that has the given id")
		return
	}
	writeBufJSON(w, http.StatusOK, b)
	*bp = b
	putRespBuf(bp)
}

func (a *AppServer) handleEnter(w http.ResponseWriter, r *http.Request, idStr string) {
	sd, code, msg := verifySession(r)
	if code != 0 {
		writeErr(w, code, msg)
		return
	}
	if !a.isCore {
		a.edge.ProxyToCore(w, r)
		return
	}
	lsID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "livestream_id must be integer")
		return
	}
	a.store.EnterLivestream(sd.UserID, lsID)
	w.WriteHeader(http.StatusOK)
}

func (a *AppServer) handleExit(w http.ResponseWriter, r *http.Request, idStr string) {
	sd, code, msg := verifySession(r)
	if code != 0 {
		writeErr(w, code, msg)
		return
	}
	if !a.isCore {
		a.edge.ProxyToCore(w, r)
		return
	}
	lsID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "livestream_id in path must be integer")
		return
	}
	a.store.ExitLivestream(sd.UserID, lsID)
	w.WriteHeader(http.StatusOK)
}

func (a *AppServer) handleGetLivecomments(w http.ResponseWriter, r *http.Request, idStr string) {
	if _, code, msg := verifySession(r); code != 0 {
		writeErr(w, code, msg)
		return
	}
	if !a.isCore {
		a.edge.ProxyToCore(w, r)
		return
	}
	lsID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "livestream_id in path must be integer")
		return
	}
	hasLimit := false
	limit := 0
	if ls := r.URL.Query().Get("limit"); ls != "" {
		limit, err = strconv.Atoi(ls)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "limit query parameter must be integer")
			return
		}
		hasLimit = true
	}
	bp := getRespBuf()
	b := a.store.AppendLivecommentsJSON(*bp, lsID, hasLimit, limit)
	writeBufJSON(w, http.StatusOK, b)
	*bp = b
	putRespBuf(bp)
}

func (a *AppServer) handlePostLivecomment(w http.ResponseWriter, r *http.Request, idStr string) {
	sd, code, msg := verifySession(r)
	if code != 0 {
		writeErr(w, code, msg)
		return
	}
	if !a.isCore {
		a.edge.ProxyToCore(w, r)
		return
	}
	lsID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "livestream_id in path must be integer")
		return
	}
	body, release, err := readBodyPooled(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "failed to decode the request body as json")
		return
	}
	var req PostLivecommentRequest
	err = json.Unmarshal(body, &req)
	release()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "failed to decode the request body as json")
		return
	}
	bp := getRespBuf()
	b, err := a.store.PostLivecommentJSON(*bp, sd.UserID, lsID, req)
	if err != nil {
		*bp = b
		putRespBuf(bp)
		writeErr(w, mapStoreErr(err), err.Error())
		return
	}
	writeBufJSON(w, http.StatusCreated, b)
	*bp = b
	putRespBuf(bp)
}

func (a *AppServer) handleGetNGWords(w http.ResponseWriter, r *http.Request, idStr string) {
	sd, code, msg := verifySession(r)
	if code != 0 {
		writeErr(w, code, msg)
		return
	}
	if !a.isCore {
		a.edge.ProxyToCore(w, r)
		return
	}
	lsID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "livestream_id in path must be integer")
		return
	}
	bp := getRespBuf()
	b := a.store.AppendNGWordsJSON(*bp, sd.UserID, lsID)
	writeBufJSON(w, http.StatusOK, b)
	*bp = b
	putRespBuf(bp)
}

func (a *AppServer) handleModerate(w http.ResponseWriter, r *http.Request, idStr string) {
	sd, code, msg := verifySession(r)
	if code != 0 {
		writeErr(w, code, msg)
		return
	}
	if !a.isCore {
		a.edge.ProxyToCore(w, r)
		return
	}
	lsID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "livestream_id in path must be integer")
		return
	}
	body, release, err := readBodyPooled(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "failed to decode the request body as json")
		return
	}
	var req ModerateRequest
	err = json.Unmarshal(body, &req)
	release()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "failed to decode the request body as json")
		return
	}
	wordID, err := a.store.Moderate(sd.UserID, lsID, req.NGWord)
	if err != nil {
		writeErr(w, mapStoreErr(err), err.Error())
		return
	}
	var buf [32]byte
	b := append(buf[:0], `{"word_id":`...)
	b = strconv.AppendInt(b, wordID, 10)
	b = append(b, '}')
	writeBufJSON(w, http.StatusCreated, b)
}

func (a *AppServer) handleReportLivecomment(w http.ResponseWriter, r *http.Request, idStr, cidStr string) {
	sd, code, msg := verifySession(r)
	if code != 0 {
		writeErr(w, code, msg)
		return
	}
	if !a.isCore {
		a.edge.ProxyToCore(w, r)
		return
	}
	lsID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "livestream_id in path must be integer")
		return
	}
	lcID, err := strconv.ParseInt(cidStr, 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "livecomment_id in path must be integer")
		return
	}
	bp := getRespBuf()
	b, err := a.store.ReportLivecommentJSON(*bp, sd.UserID, lsID, lcID)
	if err != nil {
		*bp = b
		putRespBuf(bp)
		writeErr(w, mapStoreErr(err), err.Error())
		return
	}
	writeBufJSON(w, http.StatusCreated, b)
	*bp = b
	putRespBuf(bp)
}

func (a *AppServer) handleGetReports(w http.ResponseWriter, r *http.Request, idStr string) {
	sd, code, msg := verifySession(r)
	if code != 0 {
		writeErr(w, code, msg)
		return
	}
	if !a.isCore {
		a.edge.ProxyToCore(w, r)
		return
	}
	lsID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "livestream_id in path must be integer")
		return
	}
	bp := getRespBuf()
	b, err := a.store.AppendReportsJSON(*bp, sd.UserID, lsID)
	if err != nil {
		*bp = b
		putRespBuf(bp)
		writeErr(w, mapStoreErr(err), err.Error())
		return
	}
	writeBufJSON(w, http.StatusOK, b)
	*bp = b
	putRespBuf(bp)
}

func (a *AppServer) handleGetReactions(w http.ResponseWriter, r *http.Request, idStr string) {
	if _, code, msg := verifySession(r); code != 0 {
		writeErr(w, code, msg)
		return
	}
	if !a.isCore {
		a.edge.ProxyToCore(w, r)
		return
	}
	lsID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "livestream_id in path must be integer")
		return
	}
	hasLimit := false
	limit := 0
	if ls := r.URL.Query().Get("limit"); ls != "" {
		limit, err = strconv.Atoi(ls)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "limit query parameter must be integer")
			return
		}
		hasLimit = true
	}
	bp := getRespBuf()
	b := a.store.AppendReactionsJSON(*bp, lsID, hasLimit, limit)
	writeBufJSON(w, http.StatusOK, b)
	*bp = b
	putRespBuf(bp)
}

func (a *AppServer) handlePostReaction(w http.ResponseWriter, r *http.Request, idStr string) {
	sd, code, msg := verifySession(r)
	if code != 0 {
		writeErr(w, code, msg)
		return
	}
	if !a.isCore {
		a.edge.ProxyToCore(w, r)
		return
	}
	lsID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "livestream_id in path must be integer")
		return
	}
	body, release, err := readBodyPooled(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "failed to decode the request body as json")
		return
	}
	var req PostReactionRequest
	err = json.Unmarshal(body, &req)
	release()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "failed to decode the request body as json")
		return
	}
	bp := getRespBuf()
	b, err := a.store.PostReactionJSON(*bp, sd.UserID, lsID, req.EmojiName)
	if err != nil {
		*bp = b
		putRespBuf(bp)
		writeErr(w, mapStoreErr(err), err.Error())
		return
	}
	writeBufJSON(w, http.StatusCreated, b)
	*bp = b
	putRespBuf(bp)
}

func (a *AppServer) handleGetUserStatistics(w http.ResponseWriter, r *http.Request, rawUsername string) {
	if _, code, msg := verifySession(r); code != 0 {
		writeErr(w, code, msg)
		return
	}
	if !a.isCore {
		a.edge.ProxyToCore(w, r)
		return
	}
	username := rawUsername
	if strings.IndexByte(rawUsername, '%') >= 0 {
		if u, err := url.PathUnescape(rawUsername); err == nil {
			username = u
		}
	}
	st, err := a.store.GetUserStatistics(username)
	if err != nil {
		writeErr(w, mapStoreErr(err), err.Error())
		return
	}
	writeJSONFast(w, http.StatusOK, st)
}

func (a *AppServer) handleGetLivestreamStatistics(w http.ResponseWriter, r *http.Request, idStr string) {
	if _, code, msg := verifySession(r); code != 0 {
		writeErr(w, code, msg)
		return
	}
	if !a.isCore {
		a.edge.ProxyToCore(w, r)
		return
	}
	lsID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "livestream_id in path must be integer")
		return
	}
	st, err := a.store.GetLivestreamStatistics(lsID)
	if err != nil {
		writeErr(w, mapStoreErr(err), err.Error())
		return
	}
	writeJSONFast(w, http.StatusOK, st)
}

func (a *AppServer) handlePayment(w http.ResponseWriter, r *http.Request) {
	if !a.isCore {
		a.edge.ProxyToCore(w, r)
		return
	}
	writeJSONFast(w, http.StatusOK, a.store.GetPaymentResult())
}
