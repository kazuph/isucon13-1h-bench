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
	"sync"

	"os/exec"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/sessions"

	"github.com/labstack/echo-contrib/session"
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
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	userID := c.Get(defaultUserIDKey).(int64)

	img, err := decodeIcon(c)
	if err != nil {
		return echo.NewHTTPError(400, "invalid icon upload")
	}

	lock := &iconLocks[uint64(userID)%uint64(len(iconLocks))]
	lock.Lock()
	defer lock.Unlock()
	hash := fmt.Sprintf("%x", sha256.Sum256(img))
	if _, known := knownIconObjects.Load(hash); !known {
		if _, err := dbConn.ExecContext(ctx, "INSERT IGNORE INTO icon_objects(hash,image) VALUES(?,?)", hash, img); err != nil {
			return err
		}
		knownIconObjects.Store(hash, true)
	}
	rs, err := dbConn.ExecContext(ctx, "REPLACE INTO icon_refs(user_id,hash) VALUES(?,?)", userID, hash)
	if err != nil {
		return err
	}
	iconID, err := rs.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted icon id: "+err.Error())
	}

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
	if err := verifyUserSession(c); err != nil {
		return err
	}
	u, err := lookupUser(c.Get(defaultUserIDKey).(int64))
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

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	userModel := UserModel{
		Name:           req.Name,
		DisplayName:    req.DisplayName,
		Description:    req.Description,
		HashedPassword: string(hashedPassword),
	}

	result, err := tx.NamedExecContext(ctx, "INSERT INTO users (name, display_name, description, password) VALUES(:name, :display_name, :description, :password)", userModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert user: "+err.Error())
	}

	userID, err := result.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted user id: "+err.Error())
	}

	userModel.ID = userID

	themeModel := ThemeModel{
		UserID:   userID,
		DarkMode: req.Theme.DarkMode,
	}
	themeResult, err := tx.NamedExecContext(ctx, "INSERT INTO themes (user_id, dark_mode) VALUES(:user_id, :dark_mode)", themeModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert user theme: "+err.Error())
	}

	if _, err := tx.ExecContext(ctx, "INSERT INTO registered_domains(user_id,name) VALUES(?,?)", userID, req.Name); err != nil {
		return err
	}
	tid, err := themeResult.LastInsertId()
	if err != nil {
		return err
	}
	user := User{ID: userID, Name: req.Name, DisplayName: req.DisplayName, Description: req.Description, IconHash: fmt.Sprintf("%x", sha256.Sum256(data.fallback)), Theme: Theme{tid, req.Theme.DarkMode}}
	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	if os.Getenv("ISUPIPE_ZONE_URL") != "" {
		if err := zoneRequest("register", req.Name); err != nil {
			return err
		}
	} else if zone.db != nil {
		if err := addAddress(req.Name); err != nil {
			return err
		}
	} else if out, err := exec.Command("pdnsutil", "add-record", "u.isucon.dev", req.Name, "A", "0", powerDNSSubdomainAddress).CombinedOutput(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, string(out)+": "+err.Error())
	}

	if os.Getenv("ISUPIPE_ACCOUNTS_LISTEN") == "" {
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

	sess, err := session.Get(defaultSessionIDKey, c)
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

func verifyUserSession(c echo.Context) error {
	cookie, _ := c.Request().Cookie(defaultSessionIDKey)
	now := time.Now().Unix()
	if cookie != nil {
		sessionCache.RLock()
		v, ok := sessionCache.entries[cookie.Value]
		sessionCache.RUnlock()
		if ok && now <= v.expires {
			c.Set(defaultUserIDKey, v.userID)
			return nil
		}
	}
	sess, err := session.Get(defaultSessionIDKey, c)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "failed to get session")
	}

	sessionExpires, ok := sess.Values[defaultSessionExpiresKey]
	if !ok {
		return echo.NewHTTPError(http.StatusForbidden, "failed to get EXPIRES value from session")
	}

	_, ok = sess.Values[defaultUserIDKey].(int64)
	if !ok {
		return echo.NewHTTPError(http.StatusUnauthorized, "failed to get USERID value from session")
	}

	expires, ok := sessionExpires.(int64)
	if !ok {
		return echo.NewHTTPError(401, "invalid expiration")
	}
	if now > expires {
		return echo.NewHTTPError(http.StatusUnauthorized, "session has expired")
	}

	uid := sess.Values[defaultUserIDKey].(int64)
	c.Set(defaultUserIDKey, uid)
	if cookie != nil {
		sessionCache.Lock()
		if len(sessionCache.entries) >= 32768 {
			sessionCache.entries = make(map[string]validatedSession)
		}
		sessionCache.entries[cookie.Value] = validatedSession{uid, expires}
		sessionCache.Unlock()
	}
	return nil
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
