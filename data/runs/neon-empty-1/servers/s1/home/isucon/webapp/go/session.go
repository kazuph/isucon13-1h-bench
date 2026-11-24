package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

const sessionCookieName = "SESSIONID"

func signSessionCookie(userID int64, expires int64) string {
	payload := strconv.FormatInt(userID, 10) + "." + strconv.FormatInt(expires, 10)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + sig
}

func setUserSessionCookie(c echo.Context, userID int64) {
	expires := time.Now().Add(1 * time.Hour).Unix()
	val := signSessionCookie(userID, expires)
	c.SetCookie(&http.Cookie{
		Name:   sessionCookieName,
		Value:  val,
		Domain: "u.isucon.dev",
		Path:   "/",
		MaxAge: 60000,
	})
}

func verifyAndGetUserID(c echo.Context) (int64, error) {
	cookie, err := c.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return 0, echo.NewHTTPError(http.StatusForbidden, "failed to get EXPIRES value from session")
	}

	val := cookie.Value
	p1 := strings.IndexByte(val, '.')
	if p1 <= 0 {
		return 0, echo.NewHTTPError(http.StatusUnauthorized, "failed to get session")
	}
	p2 := strings.IndexByte(val[p1+1:], '.')
	if p2 <= 0 {
		return 0, echo.NewHTTPError(http.StatusUnauthorized, "failed to get session")
	}
	p2 += p1 + 1

	payload := val[:p2]
	sigStr := val[p2+1:]

	sigBytes, err := base64.RawURLEncoding.DecodeString(sigStr)
	if err != nil {
		return 0, echo.NewHTTPError(http.StatusUnauthorized, "failed to get session")
	}

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	expected := mac.Sum(nil)
	if !hmac.Equal(sigBytes, expected) {
		return 0, echo.NewHTTPError(http.StatusUnauthorized, "failed to get session")
	}

	userID, err := strconv.ParseInt(val[:p1], 10, 64)
	if err != nil {
		return 0, echo.NewHTTPError(http.StatusUnauthorized, "failed to get USERID value from session")
	}
	expires, err := strconv.ParseInt(val[p1+1:p2], 10, 64)
	if err != nil {
		return 0, echo.NewHTTPError(http.StatusForbidden, "failed to get EXPIRES value from session")
	}

	if time.Now().Unix() > expires {
		return 0, echo.NewHTTPError(http.StatusUnauthorized, "session has expired")
	}

	return userID, nil
}
