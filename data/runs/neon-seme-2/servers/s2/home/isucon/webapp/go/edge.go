package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// EdgeCache runs on edge nodes (s2, s3) to serve icons, tags, themes, and login bcrypt locally!
type EdgeCache struct {
	mu           sync.RWMutex
	snap         *InitialSnapshot
	tagsJSON     []byte
	usersByName  map[string]*InternalUserAuth
	iconBlobs    map[string][]byte
	coreBaseURL  string
	httpClient   *http.Client
}

func NewEdgeCache(snap *InitialSnapshot, coreAddr string) *EdgeCache {
	tr := &http.Transport{
		MaxIdleConns:        512,
		MaxIdleConnsPerHost: 512,
		MaxConnsPerHost:     512,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true,
	}
	tagPtrs := make([]*Tag, len(snap.Tags))
	for i := range snap.Tags {
		tagPtrs[i] = &Tag{ID: snap.Tags[i].ID, Name: snap.Tags[i].Name}
	}
	tagsJSON, _ := json.Marshal(&TagsResponse{Tags: tagPtrs})

	ec := &EdgeCache{
		snap:        snap,
		tagsJSON:    tagsJSON,
		coreBaseURL: "http://" + coreAddr,
		httpClient: &http.Client{
			Transport: tr,
			Timeout:   15 * time.Second,
		},
	}
	ec.ResetToSnapshot()
	return ec
}

func (e *EdgeCache) ResetToSnapshot() {
	m := make(map[string]*InternalUserAuth, len(e.snap.Users)+4096)
	for _, u := range e.snap.Users {
		m[u.Name] = &InternalUserAuth{
			ID:             u.ID,
			Name:           u.Name,
			HashedPassword: u.HashedPassword,
			ThemeID:        u.ThemeID,
			DarkMode:       u.DarkMode,
			IconHash:       e.snap.FallbackHash,
		}
	}
	blobs := make(map[string][]byte, 256)
	blobs[e.snap.FallbackHash] = e.snap.FallbackImage

	e.mu.Lock()
	e.usersByName = m
	e.iconBlobs = blobs
	e.mu.Unlock()
}

func (e *EdgeCache) SyncFromCore() {
	for attempt := 0; attempt < 20; attempt++ {
		resp, err := e.httpClient.Get(e.coreBaseURL + "/internal/sync_edge")
		if err != nil {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		var syncData InternalEdgeSync
		err = json.NewDecoder(resp.Body).Decode(&syncData)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			time.Sleep(250 * time.Millisecond)
			continue
		}

		m := make(map[string]*InternalUserAuth, len(syncData.Users)+2048)
		for i := range syncData.Users {
			u := syncData.Users[i]
			uCopy := u
			m[u.Name] = &uCopy
		}
		blobs := make(map[string][]byte, len(syncData.IconBlobs)+64)
		blobs[e.snap.FallbackHash] = e.snap.FallbackImage
		for h, b := range syncData.IconBlobs {
			blobs[h] = b
		}
		e.mu.Lock()
		e.usersByName = m
		e.iconBlobs = blobs
		e.mu.Unlock()
		log.Printf("Edge synced from core: %d users, %d icon blobs", len(m), len(blobs))
		return
	}
	log.Printf("WARNING: Edge failed to sync from core on startup; using snapshot")
}

func (e *EdgeCache) PutUser(u InternalUserAuth) {
	uCopy := u
	e.mu.Lock()
	e.usersByName[u.Name] = &uCopy
	e.mu.Unlock()
}

func (e *EdgeCache) PutIcon(username, hash string, img []byte) {
	e.mu.Lock()
	if len(img) > 0 {
		if _, ok := e.iconBlobs[hash]; !ok {
			imgCopy := make([]byte, len(img))
			copy(imgCopy, img)
			e.iconBlobs[hash] = imgCopy
		}
	}
	if u := e.usersByName[username]; u != nil {
		u.IconHash = hash
	}
	e.mu.Unlock()
}

func (e *EdgeCache) GetUser(username string) (*InternalUserAuth, bool) {
	e.mu.RLock()
	u, ok := e.usersByName[username]
	if ok {
		uCopy := *u
		e.mu.RUnlock()
		return &uCopy, true
	}
	e.mu.RUnlock()

	// Cache miss: fetch from core
	resp, err := e.httpClient.Get(e.coreBaseURL + "/internal/user_auth?username=" + url.QueryEscape(username))
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var ua InternalUserAuth
	if err := json.NewDecoder(resp.Body).Decode(&ua); err != nil {
		return nil, false
	}
	e.PutUser(ua)
	return &ua, true
}

func (e *EdgeCache) GetIcon(username string) (hash string, img []byte, ok bool) {
	e.mu.RLock()
	u := e.usersByName[username]
	if u != nil {
		hash = u.IconHash
		img = e.iconBlobs[hash]
		if img != nil {
			e.mu.RUnlock()
			return hash, img, true
		}
	}
	e.mu.RUnlock()

	// Fallback to core
	resp, err := e.httpClient.Get(e.coreBaseURL + "/api/user/" + url.PathEscape(username) + "/icon")
	if err != nil {
		return "", nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, false
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, false
	}
	etag := strings.Trim(resp.Header.Get("ETag"), `"`)
	if etag == "" {
		etag = sha256Hex(data)
	}
	e.PutIcon(username, etag, data)
	return etag, data, true
}

// Forward an arbitrary HTTP request directly to core :8081
func (e *EdgeCache) ProxyToCore(w http.ResponseWriter, r *http.Request) {
	targetURL := e.coreBaseURL + r.URL.RequestURI()
	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, r.Body)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if ck := r.Header.Get("Cookie"); ck != "" {
		req.Header.Set("Cookie", ck)
	}
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		req.Header.Set("If-None-Match", inm)
	}
	resp, err := e.httpClient.Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()

	for k, vals := range resp.Header {
		if k == "Content-Length" || k == "Connection" || k == "Keep-Alive" {
			continue
		}
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (e *EdgeCache) PostJSONToCore(path string, payload any) (*http.Response, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return e.httpClient.Post(e.coreBaseURL+path, "application/json", bytes.NewReader(b))
}

// --- Core -> Edge Peer Broadcaster ---

type PeerBroadcaster struct {
	peers  []string
	client *http.Client

	mu          sync.Mutex
	sentBlobs   map[string]bool // hash already broadcast to peers
}

func NewPeerBroadcaster(peersCSV string) *PeerBroadcaster {
	var peers []string
	for _, p := range strings.Split(peersCSV, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			if !strings.HasPrefix(p, "http://") {
				p = "http://" + p
			}
			peers = append(peers, p)
		}
	}
	tr := &http.Transport{
		MaxIdleConns:        128,
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true,
	}
	return &PeerBroadcaster{
		peers: peers,
		client: &http.Client{
			Transport: tr,
			Timeout:   3 * time.Second,
		},
		sentBlobs: make(map[string]bool, 256),
	}
}

func (pb *PeerBroadcaster) ResetPeers() {
	pb.mu.Lock()
	pb.sentBlobs = make(map[string]bool, 256)
	pb.mu.Unlock()

	if len(pb.peers) == 0 {
		return
	}
	var wg sync.WaitGroup
	for _, peer := range pb.peers {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			resp, err := pb.client.Post(p+"/internal/reset", "application/json", nil)
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}(peer)
	}
	wg.Wait()
}

func (pb *PeerBroadcaster) BroadcastUser(u InternalUserAuth) {
	if len(pb.peers) == 0 {
		return
	}
	body, _ := json.Marshal(u)
	var wg sync.WaitGroup
	for _, peer := range pb.peers {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			resp, err := pb.client.Post(p+"/internal/push_user", "application/json", bytes.NewReader(body))
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}(peer)
	}
	wg.Wait()
}

func (pb *PeerBroadcaster) BroadcastIcon(username, hash string, img []byte) {
	if len(pb.peers) == 0 {
		return
	}
	pb.mu.Lock()
	alreadySent := pb.sentBlobs[hash]
	if !alreadySent {
		pb.sentBlobs[hash] = true
	}
	pb.mu.Unlock()

	msg := InternalPushIcon{
		Username: username,
		Hash:     hash,
	}
	if !alreadySent {
		msg.Image = img
	}
	body, _ := json.Marshal(msg)
	var wg sync.WaitGroup
	for _, peer := range pb.peers {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			resp, err := pb.client.Post(p+"/internal/push_icon", "application/json", bytes.NewReader(body))
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			}
		}(peer)
	}
	wg.Wait()
}
