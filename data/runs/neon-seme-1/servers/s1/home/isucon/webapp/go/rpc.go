package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	rpcInitialize        byte = 1
	rpcGetIcon           byte = 2
	rpcPostIcon          byte = 3
	rpcGetUserAuth       byte = 4
	rpcRegister          byte = 5
	rpcGetMe             byte = 6
	rpcGetUser           byte = 7
	rpcGetStreamerTheme  byte = 8
	rpcReserveLivestream byte = 9
	rpcSearchLivestreams byte = 10
	rpcGetMyLivestreams  byte = 11
	rpcGetUserStreams    byte = 12
	rpcGetLivestream     byte = 13
	rpcEnterLivestream   byte = 14
	rpcExitLivestream    byte = 15
	rpcGetLivecomments   byte = 16
	rpcPostLivecomment   byte = 17
	rpcGetNgwords        byte = 18
	rpcModerate          byte = 19
	rpcReportLivecomment byte = 20
	rpcGetReports        byte = 21
	rpcGetReactions      byte = 22
	rpcPostReaction      byte = 23
	rpcGetUserStats      byte = 24
	rpcGetStreamStats    byte = 25
	rpcGetPayment        byte = 26

	// Push ops (Leader -> Worker)
	pushOpIcon  byte = 101
	pushOpReset byte = 102
)

// Backend abstracts local in-memory Store vs remote TCP RPC to Leader.
type Backend interface {
	Initialize() (int, []byte)
	GetTagsJSON() []byte
	GetIcon(username string) (hash string, img []byte, found bool)
	PostIcon(userID int64, hash string, img []byte) (int, []byte)
	GetUserAuth(username string) (userID int64, hashedPassword string, found bool)
	Register(name, displayName, description, hashedPassword string, darkMode bool) (int, int64, []byte)
	GetMe(userID int64) (int, []byte)
	GetUser(username string) (int, []byte)
	GetStreamerTheme(username string) (int, []byte)
	ReserveLivestream(userID int64, req *ReserveLivestreamRequest) (int, []byte)
	SearchLivestreams(tagName string, limit int) (int, []byte)
	GetMyLivestreams(userID int64) (int, []byte)
	GetUserLivestreams(username string) (int, []byte)
	GetLivestream(livestreamID int64) (int, []byte)
	EnterLivestream(userID, livestreamID int64) (int, []byte)
	ExitLivestream(userID, livestreamID int64) (int, []byte)
	GetLivecomments(livestreamID int64, limit int) (int, []byte)
	PostLivecomment(userID, livestreamID int64, comment string, tip int64) (int, []byte)
	GetNgwords(userID, livestreamID int64) (int, []byte)
	Moderate(userID, livestreamID int64, ngWord string) (int, []byte)
	ReportLivecomment(userID, livestreamID, livecommentID int64) (int, []byte)
	GetLivecommentReports(userID, livestreamID int64) (int, []byte)
	GetReactions(livestreamID int64, limit int) (int, []byte)
	PostReaction(userID, livestreamID int64, emojiName string) (int, []byte)
	GetUserStatistics(username string) (int, []byte)
	GetLivestreamStatistics(livestreamID int64) (int, []byte)
	GetPaymentResult() (int, []byte)
}

// --- Leader RPC Server ---

type RPCServer struct {
	store *Store
	ln    net.Listener
}

func StartRPCServer(addr string, store *Store) (*RPCServer, error) {
	ln, err := net.Listen("tcp4", addr)
	if err != nil {
		return nil, err
	}
	s := &RPCServer{store: store, ln: ln}
	go s.acceptLoop()
	log.Printf("RPC server listening on %s", addr)
	return s, nil
}

func (s *RPCServer) Close() {
	if s.ln != nil {
		_ = s.ln.Close()
	}
}

func (s *RPCServer) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if strings.Contains(err.Error(), "closed") {
				return
			}
			continue
		}
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetNoDelay(true)
		}
		go s.handleConn(conn)
	}
}

func (s *RPCServer) handleConn(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReaderSize(conn, 64*1024)
	bw := bufio.NewWriterSize(conn, 64*1024)
	var reqHdr [5]byte
	var respHdr [6]byte
	for {
		if _, err := io.ReadFull(br, reqHdr[:]); err != nil {
			return
		}
		op := reqHdr[0]
		sz := binary.BigEndian.Uint32(reqHdr[1:5])
		req := make([]byte, sz)
		if sz > 0 {
			if _, err := io.ReadFull(br, req); err != nil {
				return
			}
		}
		status, resp := s.dispatch(op, req)
		binary.BigEndian.PutUint16(respHdr[0:2], uint16(status))
		binary.BigEndian.PutUint32(respHdr[2:6], uint32(len(resp)))
		if _, err := bw.Write(respHdr[:]); err != nil {
			return
		}
		if len(resp) > 0 {
			if _, err := bw.Write(resp); err != nil {
				return
			}
		}
		if err := bw.Flush(); err != nil {
			return
		}
	}
}

func (s *RPCServer) dispatch(op byte, p []byte) (int, []byte) {
	off := 0
	switch op {
	case rpcInitialize:
		return s.store.Initialize()
	case rpcGetIcon:
		username, _ := readStr(p, &off)
		hash, img, found := s.store.GetIcon(username)
		if !found {
			return http.StatusNotFound, nil
		}
		out := make([]byte, 0, 4+len(hash)+4+len(img))
		out = appendStr(out, hash)
		out = appendBytes(out, img)
		return http.StatusOK, out
	case rpcPostIcon:
		uid, _ := readInt64(p, &off)
		hash, _ := readStr(p, &off)
		img, _ := readBytes(p, &off)
		return s.store.PostIcon(uid, hash, img)
	case rpcGetUserAuth:
		username, _ := readStr(p, &off)
		uid, pw, found := s.store.GetUserAuth(username)
		if !found {
			return http.StatusNotFound, nil
		}
		out := make([]byte, 0, 12+len(pw))
		out = appendInt64(out, uid)
		out = appendStr(out, pw)
		return http.StatusOK, out
	case rpcRegister:
		name, _ := readStr(p, &off)
		disp, _ := readStr(p, &off)
		desc, _ := readStr(p, &off)
		pw, _ := readStr(p, &off)
		dark := off < len(p) && p[off] == 1
		st, uid, body := s.store.Register(name, disp, desc, pw, dark)
		out := make([]byte, 0, 8+len(body))
		out = appendInt64(out, uid)
		out = append(out, body...)
		return st, out
	case rpcGetMe:
		uid, _ := readInt64(p, &off)
		return s.store.GetMe(uid)
	case rpcGetUser:
		username, _ := readStr(p, &off)
		return s.store.GetUser(username)
	case rpcGetStreamerTheme:
		username, _ := readStr(p, &off)
		return s.store.GetStreamerTheme(username)
	case rpcReserveLivestream:
		uid, _ := readInt64(p, &off)
		body, _ := readBytes(p, &off)
		var req ReserveLivestreamRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return errJSON(http.StatusBadRequest, "failed to decode the request body as json")
		}
		return s.store.ReserveLivestream(uid, &req)
	case rpcSearchLivestreams:
		tag, _ := readStr(p, &off)
		limit, _ := readInt64(p, &off)
		return s.store.SearchLivestreams(tag, int(limit))
	case rpcGetMyLivestreams:
		uid, _ := readInt64(p, &off)
		return s.store.GetMyLivestreams(uid)
	case rpcGetUserStreams:
		username, _ := readStr(p, &off)
		return s.store.GetUserLivestreams(username)
	case rpcGetLivestream:
		lid, _ := readInt64(p, &off)
		return s.store.GetLivestream(lid)
	case rpcEnterLivestream:
		uid, _ := readInt64(p, &off)
		lid, _ := readInt64(p, &off)
		return s.store.EnterLivestream(uid, lid)
	case rpcExitLivestream:
		uid, _ := readInt64(p, &off)
		lid, _ := readInt64(p, &off)
		return s.store.ExitLivestream(uid, lid)
	case rpcGetLivecomments:
		lid, _ := readInt64(p, &off)
		limit, _ := readInt64(p, &off)
		return s.store.GetLivecomments(lid, int(limit))
	case rpcPostLivecomment:
		uid, _ := readInt64(p, &off)
		lid, _ := readInt64(p, &off)
		comment, _ := readStr(p, &off)
		tip, _ := readInt64(p, &off)
		return s.store.PostLivecomment(uid, lid, comment, tip)
	case rpcGetNgwords:
		uid, _ := readInt64(p, &off)
		lid, _ := readInt64(p, &off)
		return s.store.GetNgwords(uid, lid)
	case rpcModerate:
		uid, _ := readInt64(p, &off)
		lid, _ := readInt64(p, &off)
		word, _ := readStr(p, &off)
		return s.store.Moderate(uid, lid, word)
	case rpcReportLivecomment:
		uid, _ := readInt64(p, &off)
		lid, _ := readInt64(p, &off)
		cid, _ := readInt64(p, &off)
		return s.store.ReportLivecomment(uid, lid, cid)
	case rpcGetReports:
		uid, _ := readInt64(p, &off)
		lid, _ := readInt64(p, &off)
		return s.store.GetLivecommentReports(uid, lid)
	case rpcGetReactions:
		lid, _ := readInt64(p, &off)
		limit, _ := readInt64(p, &off)
		return s.store.GetReactions(lid, int(limit))
	case rpcPostReaction:
		uid, _ := readInt64(p, &off)
		lid, _ := readInt64(p, &off)
		emoji, _ := readStr(p, &off)
		return s.store.PostReaction(uid, lid, emoji)
	case rpcGetUserStats:
		username, _ := readStr(p, &off)
		return s.store.GetUserStatistics(username)
	case rpcGetStreamStats:
		lid, _ := readInt64(p, &off)
		return s.store.GetLivestreamStatistics(lid)
	case rpcGetPayment:
		return s.store.GetPaymentResult()
	default:
		return errJSON(http.StatusInternalServerError, "unknown rpc op")
	}
}

// --- Leader -> Worker Push Client (for Icon cache & Initialize reset) ---

type PeerHub struct {
	peers []string
	chs   []chan []byte
}

func NewPeerHub(peerAddrs []string) *PeerHub {
	var clean []string
	for _, p := range peerAddrs {
		p = strings.TrimSpace(p)
		if p != "" {
			clean = append(clean, p)
		}
	}
	if len(clean) == 0 {
		return nil
	}
	h := &PeerHub{
		peers: clean,
		chs:   make([]chan []byte, len(clean)),
	}
	for i, peer := range clean {
		ch := make(chan []byte, 2048)
		h.chs[i] = ch
		go runPeerSender(peer, ch)
	}
	return h
}

func runPeerSender(addr string, ch chan []byte) {
	var conn net.Conn
	var bw *bufio.Writer
	for frame := range ch {
		for attempt := 0; attempt < 2; attempt++ {
			if conn == nil {
				c, err := net.DialTimeout("tcp4", addr, 500*time.Millisecond)
				if err != nil {
					break
				}
				if tc, ok := c.(*net.TCPConn); ok {
					_ = tc.SetNoDelay(true)
				}
				conn = c
				bw = bufio.NewWriterSize(c, 64*1024)
			}
			_, err := bw.Write(frame)
			if err == nil {
				err = bw.Flush()
			}
			if err == nil {
				break
			}
			_ = conn.Close()
			conn = nil
			bw = nil
		}
	}
}

func (h *PeerHub) PushIconAsync(username, hash string, img []byte) {
	if h == nil {
		return
	}
	p := make([]byte, 0, 8+len(username)+len(hash)+len(img))
	p = appendStr(p, username)
	p = appendStr(p, hash)
	p = appendBytes(p, img)
	frame := make([]byte, 5+len(p))
	frame[0] = pushOpIcon
	binary.BigEndian.PutUint32(frame[1:5], uint32(len(p)))
	copy(frame[5:], p)
	for _, ch := range h.chs {
		select {
		case ch <- frame:
		default:
		}
	}
}

func (h *PeerHub) PushResetSync() {
	if h == nil {
		return
	}
	frame := []byte{pushOpReset, 0, 0, 0, 0}
	var wg sync.WaitGroup
	for _, addr := range h.peers {
		wg.Add(1)
		go func(a string) {
			defer wg.Done()
			c, err := net.DialTimeout("tcp4", a, 500*time.Millisecond)
			if err != nil {
				return
			}
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(500 * time.Millisecond))
			_, _ = c.Write(frame)
			var ack [1]byte
			_, _ = io.ReadFull(c, ack[:])
		}(addr)
	}
	wg.Wait()
}

// --- Worker RemoteBackend (with connection pool + local icon/auth caches) ---

type iconCacheEntry struct {
	hash string
	img  []byte
}

type authCacheEntry struct {
	userID int64
	hash   string
}

type rpcConn struct {
	conn net.Conn
	br   *bufio.Reader
	bw   *bufio.Writer
}

type RemoteBackend struct {
	leaderAddr string
	tagsJSON   []byte
	pool       chan *rpcConn

	iconMu    sync.RWMutex
	iconCache map[string]iconCacheEntry

	authMu    sync.RWMutex
	authCache map[string]authCacheEntry

	onResetCache func()
}

func NewRemoteBackend(leaderAddr string, tagsJSON []byte, pushListenAddr string, onResetCache func()) (*RemoteBackend, error) {
	rb := &RemoteBackend{
		leaderAddr:   leaderAddr,
		tagsJSON:     tagsJSON,
		pool:         make(chan *rpcConn, 64),
		iconCache:    make(map[string]iconCacheEntry, 2048),
		authCache:    make(map[string]authCacheEntry, 2048),
		onResetCache: onResetCache,
	}
	if pushListenAddr != "" {
		ln, err := net.Listen("tcp4", pushListenAddr)
		if err != nil {
			return nil, err
		}
		go rb.acceptPushLoop(ln)
		log.Printf("Worker push listener on %s", pushListenAddr)
	}
	return rb, nil
}

func (rb *RemoteBackend) acceptPushLoop(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			if strings.Contains(err.Error(), "closed") {
				return
			}
			continue
		}
		go rb.handlePushConn(c)
	}
}

func (rb *RemoteBackend) handlePushConn(c net.Conn) {
	defer c.Close()
	br := bufio.NewReaderSize(c, 64*1024)
	var hdr [5]byte
	for {
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			return
		}
		op := hdr[0]
		sz := binary.BigEndian.Uint32(hdr[1:5])
		p := make([]byte, sz)
		if sz > 0 {
			if _, err := io.ReadFull(br, p); err != nil {
				return
			}
		}
		switch op {
		case pushOpIcon:
			off := 0
			username, ok1 := readStr(p, &off)
			hash, ok2 := readStr(p, &off)
			img, ok3 := readBytes(p, &off)
			if ok1 && ok2 && ok3 {
				rb.iconMu.Lock()
				rb.iconCache[username] = iconCacheEntry{hash: hash, img: img}
				rb.iconMu.Unlock()
			}
		case pushOpReset:
			rb.clearCaches()
			_, _ = c.Write([]byte{1})
		}
	}
}

func (rb *RemoteBackend) clearCaches() {
	rb.iconMu.Lock()
	rb.iconCache = make(map[string]iconCacheEntry, 2048)
	rb.iconMu.Unlock()
	rb.authMu.Lock()
	rb.authCache = make(map[string]authCacheEntry, 2048)
	rb.authMu.Unlock()
	if rb.onResetCache != nil {
		rb.onResetCache()
	}
}

func (rb *RemoteBackend) getConn() (*rpcConn, error) {
	select {
	case rc := <-rb.pool:
		return rc, nil
	default:
		c, err := net.DialTimeout("tcp4", rb.leaderAddr, 2*time.Second)
		if err != nil {
			return nil, err
		}
		if tc, ok := c.(*net.TCPConn); ok {
			_ = tc.SetNoDelay(true)
		}
		return &rpcConn{
			conn: c,
			br:   bufio.NewReaderSize(c, 64*1024),
			bw:   bufio.NewWriterSize(c, 64*1024),
		}, nil
	}
}

func (rb *RemoteBackend) putConn(rc *rpcConn) {
	select {
	case rb.pool <- rc:
	default:
		_ = rc.conn.Close()
	}
}

func (rb *RemoteBackend) call(op byte, payload []byte) (int, []byte) {
	for attempt := 0; attempt < 2; attempt++ {
		rc, err := rb.getConn()
		if err != nil {
			return errJSON(http.StatusInternalServerError, "rpc dial failed: "+err.Error())
		}
		var reqHdr [5]byte
		reqHdr[0] = op
		binary.BigEndian.PutUint32(reqHdr[1:5], uint32(len(payload)))
		_, err = rc.bw.Write(reqHdr[:])
		if err == nil && len(payload) > 0 {
			_, err = rc.bw.Write(payload)
		}
		if err == nil {
			err = rc.bw.Flush()
		}
		var respHdr [6]byte
		if err == nil {
			_, err = io.ReadFull(rc.br, respHdr[:])
		}
		if err != nil {
			_ = rc.conn.Close()
			continue
		}
		status := int(binary.BigEndian.Uint16(respHdr[0:2]))
		sz := binary.BigEndian.Uint32(respHdr[2:6])
		var resp []byte
		if sz > 0 {
			resp = make([]byte, sz)
			if _, err = io.ReadFull(rc.br, resp); err != nil {
				_ = rc.conn.Close()
				continue
			}
		}
		rb.putConn(rc)
		return status, resp
	}
	return errJSON(http.StatusInternalServerError, "rpc failed after retry")
}

func (rb *RemoteBackend) GetTagsJSON() []byte { return rb.tagsJSON }

func (rb *RemoteBackend) Initialize() (int, []byte) {
	status, resp := rb.call(rpcInitialize, nil)
	rb.clearCaches()
	return status, resp
}

func (rb *RemoteBackend) GetIcon(username string) (string, []byte, bool) {
	rb.iconMu.RLock()
	e, ok := rb.iconCache[username]
	rb.iconMu.RUnlock()
	if ok {
		return e.hash, e.img, true
	}
	p := appendStr(nil, username)
	status, resp := rb.call(rpcGetIcon, p)
	if status != http.StatusOK {
		return "", nil, false
	}
	off := 0
	hash, ok1 := readStr(resp, &off)
	img, ok2 := readBytes(resp, &off)
	if !ok1 || !ok2 {
		return "", nil, false
	}
	rb.iconMu.Lock()
	rb.iconCache[username] = iconCacheEntry{hash: hash, img: img}
	rb.iconMu.Unlock()
	return hash, img, true
}

func (rb *RemoteBackend) PostIcon(userID int64, hash string, img []byte) (int, []byte) {
	p := make([]byte, 0, 16+len(hash)+len(img))
	p = appendInt64(p, userID)
	p = appendStr(p, hash)
	p = appendBytes(p, img)
	return rb.call(rpcPostIcon, p)
}

func (rb *RemoteBackend) GetUserAuth(username string) (int64, string, bool) {
	rb.authMu.RLock()
	e, ok := rb.authCache[username]
	rb.authMu.RUnlock()
	if ok {
		return e.userID, e.hash, true
	}
	p := appendStr(nil, username)
	status, resp := rb.call(rpcGetUserAuth, p)
	if status != http.StatusOK {
		return 0, "", false
	}
	off := 0
	uid, ok1 := readInt64(resp, &off)
	pw, ok2 := readStr(resp, &off)
	if !ok1 || !ok2 {
		return 0, "", false
	}
	rb.authMu.Lock()
	rb.authCache[username] = authCacheEntry{userID: uid, hash: pw}
	rb.authMu.Unlock()
	return uid, pw, true
}

func (rb *RemoteBackend) Register(name, displayName, description, hashedPassword string, darkMode bool) (int, int64, []byte) {
	p := make([]byte, 0, 32+len(name)+len(displayName)+len(description)+len(hashedPassword))
	p = appendStr(p, name)
	p = appendStr(p, displayName)
	p = appendStr(p, description)
	p = appendStr(p, hashedPassword)
	if darkMode {
		p = append(p, 1)
	} else {
		p = append(p, 0)
	}
	st, resp := rb.call(rpcRegister, p)
	if len(resp) < 8 {
		return st, 0, resp
	}
	off := 0
	uid, _ := readInt64(resp, &off)
	return st, uid, resp[8:]
}

func (rb *RemoteBackend) GetMe(userID int64) (int, []byte) {
	return rb.call(rpcGetMe, appendInt64(nil, userID))
}

func (rb *RemoteBackend) GetUser(username string) (int, []byte) {
	return rb.call(rpcGetUser, appendStr(nil, username))
}

func (rb *RemoteBackend) GetStreamerTheme(username string) (int, []byte) {
	return rb.call(rpcGetStreamerTheme, appendStr(nil, username))
}

func (rb *RemoteBackend) ReserveLivestream(userID int64, req *ReserveLivestreamRequest) (int, []byte) {
	body, _ := json.Marshal(req)
	p := make([]byte, 0, 12+len(body))
	p = appendInt64(p, userID)
	p = appendBytes(p, body)
	return rb.call(rpcReserveLivestream, p)
}

func (rb *RemoteBackend) SearchLivestreams(tagName string, limit int) (int, []byte) {
	p := make([]byte, 0, 12+len(tagName))
	p = appendStr(p, tagName)
	p = appendInt64(p, int64(limit))
	return rb.call(rpcSearchLivestreams, p)
}

func (rb *RemoteBackend) GetMyLivestreams(userID int64) (int, []byte) {
	return rb.call(rpcGetMyLivestreams, appendInt64(nil, userID))
}

func (rb *RemoteBackend) GetUserLivestreams(username string) (int, []byte) {
	return rb.call(rpcGetUserStreams, appendStr(nil, username))
}

func (rb *RemoteBackend) GetLivestream(livestreamID int64) (int, []byte) {
	return rb.call(rpcGetLivestream, appendInt64(nil, livestreamID))
}

func (rb *RemoteBackend) EnterLivestream(userID, livestreamID int64) (int, []byte) {
	p := make([]byte, 0, 16)
	p = appendInt64(p, userID)
	p = appendInt64(p, livestreamID)
	return rb.call(rpcEnterLivestream, p)
}

func (rb *RemoteBackend) ExitLivestream(userID, livestreamID int64) (int, []byte) {
	p := make([]byte, 0, 16)
	p = appendInt64(p, userID)
	p = appendInt64(p, livestreamID)
	return rb.call(rpcExitLivestream, p)
}

func (rb *RemoteBackend) GetLivecomments(livestreamID int64, limit int) (int, []byte) {
	p := make([]byte, 0, 16)
	p = appendInt64(p, livestreamID)
	p = appendInt64(p, int64(limit))
	return rb.call(rpcGetLivecomments, p)
}

func (rb *RemoteBackend) PostLivecomment(userID, livestreamID int64, comment string, tip int64) (int, []byte) {
	p := make([]byte, 0, 32+len(comment))
	p = appendInt64(p, userID)
	p = appendInt64(p, livestreamID)
	p = appendStr(p, comment)
	p = appendInt64(p, tip)
	return rb.call(rpcPostLivecomment, p)
}

func (rb *RemoteBackend) GetNgwords(userID, livestreamID int64) (int, []byte) {
	p := make([]byte, 0, 16)
	p = appendInt64(p, userID)
	p = appendInt64(p, livestreamID)
	return rb.call(rpcGetNgwords, p)
}

func (rb *RemoteBackend) Moderate(userID, livestreamID int64, ngWord string) (int, []byte) {
	p := make([]byte, 0, 24+len(ngWord))
	p = appendInt64(p, userID)
	p = appendInt64(p, livestreamID)
	p = appendStr(p, ngWord)
	return rb.call(rpcModerate, p)
}

func (rb *RemoteBackend) ReportLivecomment(userID, livestreamID, livecommentID int64) (int, []byte) {
	p := make([]byte, 0, 24)
	p = appendInt64(p, userID)
	p = appendInt64(p, livestreamID)
	p = appendInt64(p, livecommentID)
	return rb.call(rpcReportLivecomment, p)
}

func (rb *RemoteBackend) GetLivecommentReports(userID, livestreamID int64) (int, []byte) {
	p := make([]byte, 0, 16)
	p = appendInt64(p, userID)
	p = appendInt64(p, livestreamID)
	return rb.call(rpcGetReports, p)
}

func (rb *RemoteBackend) GetReactions(livestreamID int64, limit int) (int, []byte) {
	p := make([]byte, 0, 16)
	p = appendInt64(p, livestreamID)
	p = appendInt64(p, int64(limit))
	return rb.call(rpcGetReactions, p)
}

func (rb *RemoteBackend) PostReaction(userID, livestreamID int64, emojiName string) (int, []byte) {
	p := make([]byte, 0, 24+len(emojiName))
	p = appendInt64(p, userID)
	p = appendInt64(p, livestreamID)
	p = appendStr(p, emojiName)
	return rb.call(rpcPostReaction, p)
}

func (rb *RemoteBackend) GetUserStatistics(username string) (int, []byte) {
	return rb.call(rpcGetUserStats, appendStr(nil, username))
}

func (rb *RemoteBackend) GetLivestreamStatistics(livestreamID int64) (int, []byte) {
	return rb.call(rpcGetStreamStats, appendInt64(nil, livestreamID))
}

func (rb *RemoteBackend) GetPaymentResult() (int, []byte) {
	return rb.call(rpcGetPayment, nil)
}
