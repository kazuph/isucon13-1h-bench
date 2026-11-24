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

	"github.com/go-sql-driver/mysql"
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

var fallbackBytes = func() []byte {
	image, err := os.ReadFile(fallbackImage)
	if err != nil {
		panic(err)
	}
	return image
}()
var fallbackHash = fmt.Sprintf("%x", sha256.Sum256(fallbackBytes))

func getIconHandler(c echo.Context) error {
	ctx := c.Request().Context()
	username := c.Param("username")
	user, ok := userByName(username)
	if !ok {
		return echo.NewHTTPError(http.StatusNotFound, "user not found")
	}
	etag := strconv.Quote(user.IconHash)
	if c.Request().Header.Get("If-None-Match") == etag {
		c.Response().Header().Set("ETag", etag)
		return c.NoContent(http.StatusNotModified)
	}
	image := fallbackBytes
	if user.IconHash != fallbackHash {
		err := dbConn.GetContext(ctx, &image, "SELECT COALESCE(o.image,i.image) FROM icons i LEFT JOIN icon_objects o ON o.digest=i.object_digest WHERE i.user_id=?", user.ID)
		if err != nil {
			return err
		}
		etag = strconv.Quote(fmt.Sprintf("%x", sha256.Sum256(image)))
	}
	c.Response().Header().Set("ETag", etag)
	return c.Blob(http.StatusOK, "image/jpeg", image)
}

var iconLocks [256]sync.Mutex

var iconBuffers = sync.Pool{New: func() any { return new(bytes.Buffer) }}

func readIcon(r io.Reader) ([]byte, error) {
	buf := iconBuffers.Get().(*bytes.Buffer)
	buf.Reset()
	defer func() {
		if buf.Cap() <= 1<<20 {
			iconBuffers.Put(buf)
		}
	}()
	if _, err := buf.ReadFrom(r); err != nil {
		return nil, err
	}
	body := bytes.TrimSpace(buf.Bytes())
	prefix := []byte(`{"image":"`)
	suffix := []byte(`"}`)
	if len(body) >= len(prefix)+len(suffix) && bytes.HasPrefix(body, prefix) && bytes.HasSuffix(body, suffix) {
		payload := body[len(prefix) : len(body)-len(suffix)]
		data := make([]byte, base64.StdEncoding.DecodedLen(len(payload)))
		if bytes.IndexAny(payload, "\r\n") == -1 {
			if n, err := base64.StdEncoding.Decode(data, payload); err == nil {
				return data[:n], nil
			}
		}
	}
	var req PostIconRequest
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&req); err != nil {
		return nil, err
	}
	return req.Image, nil
}

func postIconHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	userID := c.Get(defaultUserIDKey).(int64)

	image, err := readIcon(c.Request().Body)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode icon: "+err.Error())
	}

	lock := &iconLocks[uint64(userID)%uint64(len(iconLocks))]
	lock.Lock()
	defer lock.Unlock()
	digest := sha256.Sum256(image)
	if err := storeIconObject(ctx, digest, image); err != nil {
		return err
	}
	account, err := catalogUser(userID)
	if err != nil {
		return err
	}
	if err := iconMarker(ctx, account.Name, ""); err != nil {
		return err
	}
	var rs sql.Result
	for attempt := 0; attempt < 3; attempt++ {
		rs, err = dbConn.ExecContext(ctx, "REPLACE INTO icons (user_id, object_digest) VALUES (?, ?)", userID, digest[:])
		var dbErr *mysql.MySQLError
		if !errors.As(err, &dbErr) || dbErr.Number != 1213 || ctx.Err() != nil {
			break
		}
		// MySQL rolled back this autocommit statement, so retrying cannot duplicate an upload.
	}

	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert new user icon: "+err.Error())
	}

	iconID, err := rs.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted icon id: "+err.Error())
	}

	hash := fmt.Sprintf("%x", digest)
	catalog.Lock()
	user := catalog.users[userID]
	user.IconHash = hash
	catalog.users[userID] = user
	for _, id := range catalog.byOwner[userID] {
		catalog.renderStream(id)
	}
	catalog.Unlock()
	if err := iconMarker(ctx, user.Name, hash); err != nil {
		c.Logger().Warnf("could not publish icon validator: %v", err)
	}
	return c.JSON(http.StatusCreated, &PostIconResponse{
		ID: iconID,
	})
}

func getMeHandler(c echo.Context) error {

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	userID := c.Get(defaultUserIDKey).(int64)

	user, err := catalogUser(userID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "user not found")
	}
	return c.JSON(http.StatusOK, user)
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

	var label strings.Builder
	for _, octet := range []byte(req.Name) {
		fmt.Fprintf(&label, "\\%03d", octet)
	}
	if label.Len() == 0 || label.Len() > 252 {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid DNS label")
	}
	if err := updateZone(ctx, label.String()); err != nil {
		return err
	}

	themeID, err := themeResult.LastInsertId()
	if err != nil {
		return err
	}
	user := User{ID: userID, Name: req.Name, DisplayName: req.DisplayName, Description: req.Description, Theme: Theme{ID: themeID, DarkMode: req.Theme.DarkMode}, IconHash: fallbackHash}
	lock := &iconLocks[uint64(userID)%uint64(len(iconLocks))]
	lock.Lock()
	defer lock.Unlock()
	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	catalog.Lock()
	catalog.users[user.ID] = user
	catalog.names[user.Name] = user.ID
	catalog.Unlock()

	if err := iconMarker(ctx, user.Name, user.IconHash); err != nil {
		c.Logger().Warnf("could not publish icon validator: %v", err)
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
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	username := c.Param("username")

	user, ok := userByName(username)
	if !ok {
		return echo.NewHTTPError(http.StatusNotFound, "user not found")
	}
	return c.JSON(http.StatusOK, user)
}

type verifiedSession struct {
	userID  int64
	expires int64
}

var verifiedCookies = struct {
	sync.RWMutex
	values map[string]verifiedSession
}{values: make(map[string]verifiedSession)}

func verifyUserSession(c echo.Context) error {
	now := time.Now().Unix()
	cookie, cookieErr := c.Request().Cookie(defaultSessionIDKey)
	if cookieErr == nil {
		verifiedCookies.RLock()
		entry, ok := verifiedCookies.values[cookie.Value]
		verifiedCookies.RUnlock()
		if ok {
			if now > entry.expires {
				return echo.NewHTTPError(http.StatusUnauthorized, "session has expired")
			}
			c.Set(defaultUserIDKey, entry.userID)
			return nil
		}
	}
	sess, err := session.Get(defaultSessionIDKey, c)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "failed to get session")
	}
	expires, ok := sess.Values[defaultSessionExpiresKey].(int64)
	if !ok {
		return echo.NewHTTPError(http.StatusForbidden, "failed to get EXPIRES value from session")
	}
	userID, ok := sess.Values[defaultUserIDKey].(int64)
	if !ok {
		return echo.NewHTTPError(http.StatusUnauthorized, "failed to get USERID value from session")
	}
	if now > expires {
		return echo.NewHTTPError(http.StatusUnauthorized, "session has expired")
	}
	if cookieErr == nil {
		verifiedCookies.Lock()
		if len(verifiedCookies.values) >= 32768 {
			clear(verifiedCookies.values)
		}
		verifiedCookies.values[cookie.Value] = verifiedSession{userID, expires}
		verifiedCookies.Unlock()
	}
	c.Set(defaultUserIDKey, userID)
	return nil
}
