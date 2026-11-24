package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"time"

	"github.com/google/uuid"
	"github.com/gorilla/sessions"

	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
)

const (
	defaultSessionIDKey      = "SESSIONID"
	defaultSessionExpiresKey = "EXPIRES"
	defaultUserIDKey         = "USERID"
	defaultUsernameKey       = "USERNAME"
	bcryptDefaultCost        = bcrypt.MinCost
)

var knownIconObjects sync.Map
var iconLocks [64]sync.Mutex
var fallbackImage = "../img/NoImage.jpg"

type UserModel struct {
	ID             int64  `db:"id"`
	Name           string `db:"name"`
	DisplayName    string `db:"display_name"`
	Description    string `db:"description"`
	HashedPassword string `db:"password"`
}

type User struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name,omitempty"`
	Description string `json:"description,omitempty"`
	Theme       Theme  `json:"theme,omitempty"`
	IconHash    string `json:"icon_hash,omitempty"`
}

type Theme struct {
	ID       int64 `json:"id"`
	DarkMode bool  `json:"dark_mode"`
}

type ThemeModel struct {
	ID       int64 `db:"id"`
	UserID   int64 `db:"user_id"`
	DarkMode bool  `db:"dark_mode"`
}

type PostUserRequest struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	// Password is non-hashed password.
	Password string               `json:"password"`
	Theme    PostUserRequestTheme `json:"theme"`
}

type PostUserRequestTheme struct {
	DarkMode bool `json:"dark_mode"`
}

type LoginRequest struct {
	Username string `json:"username"`
	// Password is non-hashed password.
	Password string `json:"password"`
}

type PostIconRequest struct {
	Image []byte `json:"image"`
}

type PostIconResponse struct {
	ID int64 `json:"id"`
}

func getIconHandler(c echo.Context) error {
	data.RLock()
	id, ok := data.names[c.Param("username")]
	u := data.users[id]
	img, custom := data.images[id]
	if !custom {
		img = data.fallback
	}
	data.RUnlock()
	if !ok {
		return echo.NewHTTPError(404, "user not found")
	}
	etag := strconv.Quote(u.IconHash)
	c.Response().Header().Set("ETag", etag)
	if c.Request().Header.Get("If-None-Match") == etag {
		return c.NoContent(304)
	}
	return c.Blob(200, "image/jpeg", img)
}

func postIconHandler(c echo.Context) error {
	userID, err := sessionIdentity(c)
	if err != nil {
		return err
	}

	lock := &iconLocks[uint64(userID)%uint64(len(iconLocks))]
	lock.Lock()
	defer lock.Unlock()
	ref, img, err := receiveIcon(c, userID)
	if err != nil {
		return err
	}
	hash := ref.Hash
	iconID := ref.ID

	data.Lock()
	data.setImageHash(userID, img, hash)
	name := data.users[userID].Name
	data.Unlock()
	if os.Getenv("ISUPIPE_PUBLISH_ICONS") == "yes" {
		if err := publishKnownIcon(name, hash, img); err != nil {
			return err
		}
	}
	return c.JSON(http.StatusCreated, &PostIconResponse{
		ID: iconID,
	})
}

func getMeHandler(c echo.Context) error {
	userID, err := sessionIdentity(c)
	if err != nil {
		return err
	}
	u, err := lookupUser(userID)
	if err != nil {
		return echo.NewHTTPError(404, "user not found")
	}
	return c.JSON(200, u)
}

// ユーザ登録API
// POST /api/register
func registerHandler(c echo.Context) error {
	ctx := c.Request().Context()
	defer c.Request().Body.Close()

	req := PostUserRequest{}
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	if req.Name == "pipe" {
		return echo.NewHTTPError(http.StatusBadRequest, "the username 'pipe' is reserved")
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcryptDefaultCost)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to generate hashed password: "+err.Error())
	}

	var row struct {
		ID      int64 `db:"id"`
		ThemeID int64 `db:"theme_id"`
	}
	if err := dbConn.GetContext(ctx, &row, "CALL register_account(?,?,?,?,?)", req.Name, req.DisplayName, req.Description, string(hashedPassword), req.Theme.DarkMode); err != nil {
		return echo.NewHTTPError(500, "failed to register user: "+err.Error())
	}
	user := User{ID: row.ID, Name: req.Name, DisplayName: req.DisplayName, Description: req.Description, IconHash: fmt.Sprintf("%x", sha256.Sum256(data.fallback)), Theme: Theme{row.ThemeID, req.Theme.DarkMode}}
	if os.Getenv("ISUPIPE_ACCOUNTS_LISTEN") == "" {
		if err := publishDomain(user.Name); err != nil {
			return err
		}
		installUser(user)
	}
	return c.JSON(http.StatusCreated, user)
}

// ユーザログインAPI
// POST /api/login
func loginHandler(c echo.Context) error {
	ctx := c.Request().Context()
	defer c.Request().Body.Close()

	req := LoginRequest{}
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	userModel := UserModel{}
	// usernameはUNIQUEなので、whereで一意に特定できる
	err := dbConn.GetContext(ctx, &userModel, "SELECT id,name,password FROM users WHERE name = ?", req.Username)
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid username or password")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
	}

	err = bcrypt.CompareHashAndPassword([]byte(userModel.HashedPassword), []byte(req.Password))
	if err == bcrypt.ErrMismatchedHashAndPassword {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid username or password")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to compare hash and password: "+err.Error())
	}

	sessionEndAt := time.Now().Add(1 * time.Hour)

	sessionID := uuid.NewString()

	sess, err := cookieStore.New(c.Request(), defaultSessionIDKey)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "failed to get session")
	}

	sess.Options = &sessions.Options{
		Domain: "u.isucon.dev",
		MaxAge: int(60000),
		Path:   "/",
	}
	sess.Values[defaultSessionIDKey] = sessionID
	sess.Values[defaultUserIDKey] = userModel.ID
	sess.Values[defaultUsernameKey] = userModel.Name
	sess.Values[defaultSessionExpiresKey] = sessionEndAt.Unix()

	if err := sess.Save(c.Request(), c.Response()); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save session: "+err.Error())
	}

	return c.NoContent(http.StatusOK)
}

// ユーザ詳細API
// GET /api/user/:username
func getUserHandler(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	data.RLock()
	id, ok := data.names[c.Param("username")]
	u := data.users[id]
	data.RUnlock()
	if !ok {
		return echo.NewHTTPError(404, "user not found")
	}
	return c.JSON(200, u)
}

func verifyUserSession(c echo.Context) error { _, err := sessionIdentity(c); return err }
func sessionIdentity(c echo.Context) (int64, error) {
	req := c.Request()
	raw, ok := strings.CutPrefix(req.Header.Get("Cookie"), defaultSessionIDKey+"=")
	if !ok || strings.ContainsRune(raw, ';') {
		cookie, err := req.Cookie(defaultSessionIDKey)
		if err == nil {
			raw = cookie.Value
		} else {
			raw = ""
		}
	}
	now := time.Now().Unix()
	if raw != "" {
		sessionCache.RLock()
		v, ok := sessionCache.entries[raw]
		sessionCache.RUnlock()
		if ok && now <= v.expires {
			return v.userID, nil
		}
	}
	sess, err := cookieStore.New(req, defaultSessionIDKey)
	if err != nil {
		return 0, echo.NewHTTPError(http.StatusUnauthorized, "failed to get session")
	}
	value, ok := sess.Values[defaultSessionExpiresKey]
	if !ok {
		return 0, echo.NewHTTPError(http.StatusForbidden, "failed to get EXPIRES value from session")
	}
	expires, ok := value.(int64)
	if !ok || now > expires {
		return 0, echo.NewHTTPError(http.StatusUnauthorized, "session has expired")
	}
	uid, ok := sess.Values[defaultUserIDKey].(int64)
	if !ok {
		return 0, echo.NewHTTPError(http.StatusUnauthorized, "invalid session identity")
	}
	if verifiedCookie, err := req.Cookie(defaultSessionIDKey); err == nil {
		sessionCache.Lock()
		if len(sessionCache.entries) >= 32768 {
			sessionCache.entries = make(map[string]validatedSession)
		}
		sessionCache.entries[verifiedCookie.Value] = validatedSession{uid, expires}
		sessionCache.Unlock()
	}
	return uid, nil
}

type validatedSession struct{ userID, expires int64 }

var sessionCache = struct {
	sync.RWMutex
	entries map[string]validatedSession
}{entries: make(map[string]validatedSession)}

func decodeIcon(c echo.Context) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(c.Response(), c.Request().Body, 10<<20))
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(body)
	const head = `{"image":"`
	const tail = `"}`
	if len(trimmed) >= len(head)+len(tail) && bytes.HasPrefix(trimmed, []byte(head)) && bytes.HasSuffix(trimmed, []byte(tail)) && bytes.IndexAny(trimmed, "\r\n") < 0 {
		encoded := trimmed[len(head) : len(trimmed)-len(tail)]
		img := make([]byte, base64.StdEncoding.DecodedLen(len(encoded)))
		if n, err := base64.StdEncoding.Decode(img, encoded); err == nil {
			return img[:n], nil
		}
	}
	var req PostIconRequest
	if err := json.Unmarshal(trimmed, &req); err != nil {
		return nil, err
	}
	return req.Image, nil
}
