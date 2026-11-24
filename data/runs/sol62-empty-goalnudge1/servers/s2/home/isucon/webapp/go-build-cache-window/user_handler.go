package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/maphash"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/sessions"
	"github.com/jmoiron/sqlx"
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

const fallbackImage = "../img/NoImage.jpg"

var (
	fallbackImageData []byte
	fallbackImageHash string
)

func loadFallbackImageHash() error {
	image, err := os.ReadFile(fallbackImage)
	if err != nil {
		return err
	}
	fallbackImageData = image
	fallbackImageHash = fmt.Sprintf("%x", sha256.Sum256(image))
	return nil
}

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

const maximumResponseCacheTTL = time.Second

var iconMetadataTTL = 250 * time.Millisecond

type iconMetadata struct {
	username string
	hash     string
	expires  time.Time
	fallback bool
}

type iconPayload struct {
	hash  string
	image []byte
}

var (
	iconCacheSeed    = maphash.MakeSeed()
	iconCache        [2048]atomic.Pointer[iconMetadata]
	iconPayloadCache [256]atomic.Pointer[iconPayload]
)

func iconPayloadSlot(hash string) *atomic.Pointer[iconPayload] {
	return &iconPayloadCache[maphash.String(iconCacheSeed, hash)%uint64(len(iconPayloadCache))]
}

func iconMatches(matches []string, hash string) bool {
	for _, candidate := range matches {
		if candidate == hash {
			return true
		}
	}
	return false
}

func getIconHandler(c echo.Context) error {
	ctx := c.Request().Context()

	username := c.Param("username")

	// Pass only valid quoted hashes to SQL so matched icons stay in MySQL.
	matches := []string{""}
	for _, header := range c.Request().Header.Values("If-None-Match") {
		for _, value := range strings.Split(header, ",") {
			value = strings.TrimPrefix(strings.TrimSpace(value), "W/")
			if len(value) == 66 && value[0] == '"' && value[65] == '"' {
				matches = append(matches, value[1:65])
			}
		}
	}
	started := time.Now()
	entry := &iconCache[maphash.String(iconCacheSeed, username)%uint64(len(iconCache))]
	if cached := entry.Load(); cached != nil && cached.username == username && started.Before(cached.expires) {
		if iconMatches(matches, cached.hash) {
			c.Response().Header().Set("ETag", `"`+cached.hash+`"`)
			return c.NoContent(http.StatusNotModified)
		}
		if cached.fallback && len(matches) == 1 {
			c.Response().Header().Set("ETag", `"`+cached.hash+`"`)
			return c.Blob(http.StatusOK, "image/jpeg", fallbackImageData)
		}
		if len(matches) == 1 {
			if payload := iconPayloadSlot(cached.hash).Load(); payload != nil && payload.hash == cached.hash {
				c.Response().Header().Set("ETag", `"`+cached.hash+`"`)
				return c.Blob(http.StatusOK, "image/jpeg", payload.image)
			}
		}
	}

	var icon struct {
		Hash  sql.NullString
		Image []byte
	}
	var err error
	if len(matches) <= 2 {
		err = readIconStatement.GetContext(ctx, &icon, matches[len(matches)-1], username)
	} else {
		query, args, queryErr := sqlx.In(`
			SELECT u.icon_hash AS hash, i.image
			FROM users u LEFT JOIN icons i ON i.user_id = u.id
				AND BINARY u.icon_hash NOT IN (?)
			WHERE u.name = ?`, matches, username)
		if queryErr != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to build icon query: "+queryErr.Error())
		}
		err = dbConn.GetContext(ctx, &icon, query, args...)
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "not found user that has the given username")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user icon: "+err.Error())
	}

	hash := fallbackImageHash
	if icon.Hash.Valid {
		hash = icon.Hash.String
	}
	expires := started.Add(iconMetadataTTL)
	if time.Now().Before(expires) {
		entry.Store(&iconMetadata{username: username, hash: hash, expires: expires, fallback: !icon.Hash.Valid})
	}
	c.Response().Header().Set("ETag", `"`+hash+`"`)
	if iconMatches(matches, hash) {
		return c.NoContent(http.StatusNotModified)
	}
	if !icon.Hash.Valid {
		return c.Blob(http.StatusOK, "image/jpeg", fallbackImageData)
	}
	// The immutable payload is usable only with a fresh username-to-hash entry.
	if len(icon.Image) <= 256*1024 {
		iconPayloadSlot(hash).Store(&iconPayload{hash: hash, image: icon.Image})
	}
	return c.Blob(http.StatusOK, "image/jpeg", icon.Image)
}

func postIconHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	var req *PostIconRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	tx, err := dbConn.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	// Serialize concurrent replacements for this user, including the first upload.
	var lockedUserID int64
	if err := tx.GetContext(ctx, &lockedUserID, "SELECT id FROM users WHERE id = ? FOR UPDATE", userID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to lock user for icon update: "+err.Error())
	}
	rs, err := tx.StmtContext(ctx, replaceIconStatement).ExecContext(ctx, userID, req.Image)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert new user icon: "+err.Error())
	}

	iconID, err := rs.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted icon id: "+err.Error())
	}

	if _, err := tx.ExecContext(ctx, "UPDATE users SET icon_hash = (SELECT image_hash FROM icons WHERE id = ?) WHERE id = ?", iconID, userID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to update user icon hash: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusCreated, &PostIconResponse{
		ID: iconID,
	})
}

func getMeHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	user, err := loadUser(ctx, dbConn, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found user that has the userid in session")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
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
	if _, err := tx.NamedExecContext(ctx, "INSERT INTO themes (user_id, dark_mode) VALUES(:user_id, :dark_mode)", themeModel); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert user theme: "+err.Error())
	}

	if out, err := exec.Command("sudo", "-n", "-u", "pdns", "pdnsutil", "add-record", "u.isucon.dev", req.Name, "A", "60", powerDNSSubdomainAddress).CombinedOutput(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, string(out)+": "+err.Error())
	}

	user, err := fillUserResponse(ctx, tx, userModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill user: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
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

	var userModel UserModel
	err := dbConn.GetContext(ctx, &userModel, "SELECT id, name, password FROM users WHERE name = ?", req.Username)
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
	ctx := c.Request().Context()
	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	username := c.Param("username")

	var profile userProfile
	err := dbConn.GetContext(ctx, &profile, userProfileQuery+" WHERE u.name = ? AND u.theme_id IS NOT NULL", fallbackImageHash, username)
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found user that has the given username")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
	}
	user := profile.user()

	return c.JSON(http.StatusOK, user)
}

func verifyUserSession(c echo.Context) error {
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

	now := time.Now()
	if now.Unix() > sessionExpires.(int64) {
		return echo.NewHTTPError(http.StatusUnauthorized, "session has expired")
	}

	return nil
}

func fillUserResponse(ctx context.Context, tx *sqlx.Tx, userModel UserModel) (User, error) {
	var profile struct {
		ID       int64
		DarkMode bool   `db:"dark_mode"`
		IconHash string `db:"icon_hash"`
	}
	if err := tx.GetContext(ctx, &profile, `
		SELECT theme_id AS id, dark_mode, COALESCE(icon_hash, ?) AS icon_hash
		FROM users WHERE id = ? AND theme_id IS NOT NULL`, fallbackImageHash, userModel.ID); err != nil {
		return User{}, err
	}

	user := User{
		ID:          userModel.ID,
		Name:        userModel.Name,
		DisplayName: userModel.DisplayName,
		Description: userModel.Description,
		Theme: Theme{
			ID:       profile.ID,
			DarkMode: profile.DarkMode,
		},
		IconHash: profile.IconHash,
	}

	return user, nil
}
