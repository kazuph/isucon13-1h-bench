package main

import (
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"sync"
	"time"
)

type cachedSession struct {
	UID     int64
	Expires int64
}

var authMu sync.RWMutex
var authCache = map[string]cachedSession{}

func memorySession(c echo.Context) (int64, error) {
	cookie, err := c.Cookie(defaultSessionIDKey)
	if err == nil {
		authMu.RLock()
		v, ok := authCache[cookie.Value]
		authMu.RUnlock()
		if ok {
			if time.Now().Unix() > v.Expires {
				return 0, echo.NewHTTPError(401, "session has expired")
			}
			return v.UID, nil
		}
	}
	if err := verifyUserSession(c); err != nil {
		return 0, err
	}
	sess, _ := session.Get(defaultSessionIDKey, c)
	uid := sess.Values[defaultUserIDKey].(int64)
	if cookie != nil {
		authMu.Lock()
		if len(authCache) >= 65536 {
			authCache = map[string]cachedSession{}
		}
		authCache[cookie.Value] = cachedSession{UID: uid, Expires: sess.Values[defaultSessionExpiresKey].(int64)}
		authMu.Unlock()
	}
	return uid, nil
}
