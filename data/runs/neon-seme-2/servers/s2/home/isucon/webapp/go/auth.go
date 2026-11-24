package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookieName = "SESSIONID"
	sessionShards     = 64
)

var (
	errSessionMissing = errors.New("missing session cookie")
	errSessionInvalid = errors.New("invalid or expired session")
	sessionSecret     = []byte("isucon13_session_cookiestore_defaultsecret")
	sessCache         [sessionShards]struct {
		mu sync.RWMutex
		m  map[string]SessionData
	}
)

func init() {
	for i := range sessCache {
		sessCache[i].m = make(map[string]SessionData, 256)
	}
}

func resetSessionCache() {
	for i := range sessCache {
		sessCache[i].mu.Lock()
		sessCache[i].m = make(map[string]SessionData, 256)
		sessCache[i].mu.Unlock()
	}
}

func sessShard(val string) int {
	var h uint32 = 2166136261
	for i := 0; i < len(val); i++ {
		h ^= uint32(val[i])
		h *= 16777619
	}
	return int(h & (sessionShards - 1))
}

func cacheSession(val string, sd SessionData) {
	sh := &sessCache[sessShard(val)]
	sh.mu.Lock()
	sh.m[val] = sd
	sh.mu.Unlock()
}

func lookupSessionCache(val string) (SessionData, bool) {
	sh := &sessCache[sessShard(val)]
	sh.mu.RLock()
	sd, ok := sh.m[val]
	sh.mu.RUnlock()
	return sd, ok
}

func extractSessionCookieValue(cookieHeader string) string {
	const prefix = "SESSIONID="
	if strings.HasPrefix(cookieHeader, prefix) {
		rest := cookieHeader[len(prefix):]
		if semi := strings.IndexByte(rest, ';'); semi >= 0 {
			return rest[:semi]
		}
		return rest
	}
	idx := strings.Index(cookieHeader, "SESSIONID=")
	if idx < 0 {
		return ""
	}
	if idx > 0 && cookieHeader[idx-1] != ' ' && cookieHeader[idx-1] != ';' {
		return ""
	}
	rest := cookieHeader[idx+len(prefix):]
	if semi := strings.IndexByte(rest, ';'); semi >= 0 {
		return rest[:semi]
	}
	return rest
}

type SessionData struct {
	UserID   int64
	Expires  int64
	Username string
}

// Format: [8B userID][8B expires][2B nameLen][nameBytes][16B hmac-sha256-trunc]
// Encoded with RawURLEncoding.
func encodeSessionCookie(userID int64, username string, expires int64) string {
	n := len(username)
	buf := make([]byte, 18+n+16)
	binary.BigEndian.PutUint64(buf[0:8], uint64(userID))
	binary.BigEndian.PutUint64(buf[8:16], uint64(expires))
	binary.BigEndian.PutUint16(buf[16:18], uint16(n))
	copy(buf[18:18+n], username)

	mac := hmac.New(sha256.New, sessionSecret)
	mac.Write(buf[:18+n])
	var sum [32]byte
	mac.Sum(sum[:0])
	copy(buf[18+n:], sum[:16])

	return base64.RawURLEncoding.EncodeToString(buf)
}

func decodeSessionCookie(val string) (SessionData, error) {
	raw, err := base64.RawURLEncoding.DecodeString(val)
	if err != nil || len(raw) < 34 {
		return SessionData{}, errSessionInvalid
	}
	nameLen := int(binary.BigEndian.Uint16(raw[16:18]))
	if len(raw) != 18+nameLen+16 {
		return SessionData{}, errSessionInvalid
	}
	payload := raw[:18+nameLen]
	sig := raw[18+nameLen:]

	mac := hmac.New(sha256.New, sessionSecret)
	mac.Write(payload)
	var sum [32]byte
	mac.Sum(sum[:0])
	if !hmac.Equal(sig, sum[:16]) {
		return SessionData{}, errSessionInvalid
	}

	userID := int64(binary.BigEndian.Uint64(raw[0:8]))
	expires := int64(binary.BigEndian.Uint64(raw[8:16]))
	if time.Now().Unix() > expires {
		return SessionData{}, errSessionInvalid
	}
	return SessionData{
		UserID:   userID,
		Expires:  expires,
		Username: string(raw[18 : 18+nameLen]),
	}, nil
}

func setSessionCookie(w http.ResponseWriter, userID int64, username string) {
	expires := time.Now().Add(1 * time.Hour).Unix()
	val := encodeSessionCookie(userID, username, expires)
	cacheSession(val, SessionData{UserID: userID, Expires: expires, Username: username})
	w.Header().Add("Set-Cookie", "SESSIONID="+val+"; Path=/; Domain=u.isucon.dev; Max-Age=60000")
}

func verifySession(r *http.Request) (SessionData, int, string) {
	hdr := r.Header.Get("Cookie")
	if hdr == "" {
		return SessionData{}, http.StatusForbidden, "failed to get EXPIRES value from session"
	}
	val := extractSessionCookieValue(hdr)
	if val == "" {
		return SessionData{}, http.StatusForbidden, "failed to get EXPIRES value from session"
	}
	if sd, ok := lookupSessionCache(val); ok {
		if time.Now().Unix() > sd.Expires {
			return SessionData{}, http.StatusUnauthorized, "session has expired"
		}
		return sd, 0, ""
	}
	sd, err := decodeSessionCookie(val)
	if err != nil {
		return SessionData{}, http.StatusUnauthorized, "failed to verify session"
	}
	cacheSession(val, sd)
	return sd, 0, ""
}
