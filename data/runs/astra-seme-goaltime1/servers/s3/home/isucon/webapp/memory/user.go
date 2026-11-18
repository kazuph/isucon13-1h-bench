package main

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/gorilla/sessions"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
	"strconv"
	"time"
)

const (
	defaultSessionIDKey      = "SESSIONID"
	defaultSessionExpiresKey = "EXPIRES"
	defaultUserIDKey         = "USERID"
	defaultUsernameKey       = "USERNAME"
	bcryptDefaultCost        = bcrypt.MinCost
)

func verifyUserSession(c echo.Context) error {
	s, e := session.Get(defaultSessionIDKey, c)
	if e != nil {
		return echo.NewHTTPError(401, "invalid session")
	}
	v, ok := s.Values[defaultSessionExpiresKey]
	if !ok {
		return echo.NewHTTPError(403, "missing session")
	}
	if _, ok = s.Values[defaultUserIDKey].(int64); !ok {
		return echo.NewHTTPError(401, "invalid session")
	}
	exp, ok := v.(int64)
	if !ok || time.Now().Unix() > exp {
		return echo.NewHTTPError(401, "expired session")
	}
	return nil
}
func uid(c echo.Context) int64 {
	s, _ := session.Get(defaultSessionIDKey, c)
	return s.Values[defaultUserIDKey].(int64)
}
func decode(c echo.Context, v interface{}) error {
	if err := json.NewDecoder(c.Request().Body).Decode(v); err != nil {
		return echo.NewHTTPError(400, "invalid JSON")
	}
	return nil
}
func paramID(c echo.Context, key string) (int64, error) {
	v, e := strconv.ParseInt(c.Param(key), 10, 64)
	if e != nil {
		return 0, echo.NewHTTPError(400, "invalid ID")
	}
	return v, nil
}
func limit(c echo.Context) (int, error) {
	if c.QueryParam("limit") == "" {
		return int(^uint(0) >> 1), nil
	}
	v, e := strconv.Atoi(c.QueryParam("limit"))
	if e != nil || v < 0 {
		return 0, echo.NewHTTPError(400, "invalid limit")
	}
	return v, nil
}
func registerHandler(c echo.Context) error {
	var r PostUserRequest
	if e := decode(c, &r); e != nil {
		return e
	}
	if r.Name == "pipe" {
		return echo.NewHTTPError(400, "reserved username")
	}
	h, e := authRequest("hash", r.Password, "")
	if e != nil {
		return e
	}
	mu.Lock()
	defer mu.Unlock()
	if names[r.Name] != 0 {
		return echo.NewHTTPError(500, "duplicate username")
	}
	u := UserModel{ID: next("user"), Name: r.Name, DisplayName: r.DisplayName, Description: r.Description, HashedPassword: string(h)}
	t := Theme{ID: next("theme"), DarkMode: r.Theme.DarkMode}
	if e := updateDNS([]string{r.Name}, false); e != nil {
		return e
	}
	if e := commit(Event{Kind: "user", User: u, Theme: t}); e != nil {
		return e
	}
	return c.JSON(201, userResponse(u.ID))
}
func loginHandler(c echo.Context) error {
	var r LoginRequest
	if e := decode(c, &r); e != nil {
		return e
	}
	mu.RLock()
	id := names[r.Username]
	u := state.Users[id]
	mu.RUnlock()
	if id == 0 {
		return echo.NewHTTPError(401, "invalid username or password")
	}
	if _, e := authRequest("compare", r.Password, u.HashedPassword); e != nil {
		return echo.NewHTTPError(401, "invalid username or password")
	}
	s, e := session.Get(defaultSessionIDKey, c)
	if e != nil {
		return echo.NewHTTPError(401, "invalid session")
	}
	s.Options = &sessions.Options{Domain: "u.isucon.dev", MaxAge: 60000, Path: "/"}
	s.Values[defaultSessionIDKey] = uuid.NewString()
	s.Values[defaultUserIDKey] = id
	s.Values[defaultUsernameKey] = u.Name
	s.Values[defaultSessionExpiresKey] = time.Now().Add(time.Hour).Unix()
	if e := s.Save(c.Request(), c.Response()); e != nil {
		return e
	}
	return c.NoContent(200)
}
func getMeHandler(c echo.Context) error {
	if e := verifyUserSession(c); e != nil {
		return e
	}
	mu.RLock()
	defer mu.RUnlock()
	id := uid(c)
	if _, ok := state.Users[id]; !ok {
		return echo.NewHTTPError(404)
	}
	return c.JSON(200, userResponse(id))
}
func getUserHandler(c echo.Context) error {
	if e := verifyUserSession(c); e != nil {
		return e
	}
	mu.RLock()
	defer mu.RUnlock()
	id := names[c.Param("username")]
	if id == 0 {
		return echo.NewHTTPError(404)
	}
	return c.JSON(200, userResponse(id))
}
func getStreamerThemeHandler(c echo.Context) error {
	if e := verifyUserSession(c); e != nil {
		return e
	}
	mu.RLock()
	defer mu.RUnlock()
	id := names[c.Param("username")]
	if id == 0 {
		return echo.NewHTTPError(404)
	}
	return c.JSON(200, state.Themes[id])
}
func getIconHandler(c echo.Context) error {
	mu.RLock()
	defer mu.RUnlock()
	id := names[c.Param("username")]
	if id == 0 {
		return echo.NewHTTPError(404)
	}
	etag := "\"" + hashes[id] + "\""
	c.Response().Header().Set("ETag", etag)
	if c.Request().Header.Get("If-None-Match") == etag {
		return c.NoContent(http.StatusNotModified)
	}
	return c.Blob(200, "image/jpeg", icon(id))
}
func postIconHandler(c echo.Context) error {
	if e := verifyUserSession(c); e != nil {
		return e
	}
	var r PostIconRequest
	if e := decode(c, &r); e != nil {
		return e
	}
	mu.Lock()
	defer mu.Unlock()
	id := next("icon")
	if e := commit(Event{Kind: "icon", ID: id, UserID: uid(c), Image: r.Image}); e != nil {
		return e
	}
	return c.JSON(201, PostIconResponse{ID: id})
}
func getTagHandler(c echo.Context) error {
	mu.RLock()
	defer mu.RUnlock()
	tags := make([]*Tag, 0, len(state.Tags))
	for id := int64(1); id <= int64(len(state.Tags)); id++ {
		t := state.Tags[id]
		tags = append(tags, &t)
	}
	return c.JSON(200, TagsResponse{Tags: tags})
}
