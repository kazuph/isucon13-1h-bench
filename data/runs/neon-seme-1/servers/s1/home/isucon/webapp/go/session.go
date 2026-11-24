package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"os"
	"time"
)

const (
	sessionCookieName = "SESSIONID"
	sessionDuration   = 1 * time.Hour
	sessionCookieMax  = 60000
	sessionTokenBytes = 8 + 8 + 16 // userID + expires + truncated HMAC-SHA256
)

var sessionSecret = []byte("isucon13_session_cookiestore_defaultsecret")

func initSessionSecret() {
	if s, ok := os.LookupEnv("ISUCON13_SESSION_SECRETKEY"); ok && s != "" {
		sessionSecret = []byte(s)
	}
}

func makeSessionToken(userID int64, expires int64) string {
	var raw [sessionTokenBytes]byte
	binary.BigEndian.PutUint64(raw[0:8], uint64(userID))
	binary.BigEndian.PutUint64(raw[8:16], uint64(expires))
	mac := hmac.New(sha256.New, sessionSecret)
	mac.Write(raw[0:16])
	sum := mac.Sum(nil)
	copy(raw[16:32], sum[:16])
	return base64.RawURLEncoding.EncodeToString(raw[:])
}

func setSessionCookie(w http.ResponseWriter, userID int64) {
	expires := time.Now().Add(sessionDuration).Unix()
	token := makeSessionToken(userID, expires)
	http.SetCookie(w, &http.Cookie{
		Name:   sessionCookieName,
		Value:  token,
		Path:   "/",
		Domain: "u.isucon.dev",
		MaxAge: sessionCookieMax,
	})
}

// verifySession returns (userID, httpStatusError, errMsg).
// Matches initial implementation: missing cookie -> 403, invalid/expired -> 401.
func verifySession(r *http.Request) (int64, int, string) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return 0, http.StatusForbidden, "failed to get EXPIRES value from session"
	}
	var raw [sessionTokenBytes]byte
	n, err := base64.RawURLEncoding.Decode(raw[:], []byte(c.Value))
	if err != nil || n != sessionTokenBytes {
		return 0, http.StatusUnauthorized, "failed to get session"
	}
	mac := hmac.New(sha256.New, sessionSecret)
	mac.Write(raw[0:16])
	sum := mac.Sum(nil)
	if !hmac.Equal(raw[16:32], sum[:16]) {
		return 0, http.StatusUnauthorized, "failed to get session"
	}
	userID := int64(binary.BigEndian.Uint64(raw[0:8]))
	expires := int64(binary.BigEndian.Uint64(raw[8:16]))
	if time.Now().Unix() > expires {
		return 0, http.StatusUnauthorized, "session has expired"
	}
	return userID, 0, ""
}
