package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
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
	fallbackIconHash string
	fallbackIconData []byte
)

func initializeFallbackIcon() error {
	image, err := os.ReadFile(fallbackImage)
	if err != nil {
		return err
	}
	fallbackIconData = image
	fallbackIconHash = fmt.Sprintf("%x", sha256.Sum256(image))
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

func getIconHandler(c echo.Context) error {
	ctx := c.Request().Context()

	requestedHashes := []string{""}
	for _, value := range c.Request().Header.Values("If-None-Match") {
		for _, tag := range strings.Split(value, ",") {
			tag = strings.TrimPrefix(strings.TrimSpace(tag), "W/")
			if len(tag) == 66 && tag[0] == '"' && tag[65] == '"' {
				requestedHashes = append(requestedHashes, tag[1:65])
			}
		}
	}

	var icon struct {
		Hash     string `db:"image_hash"`
		HasImage bool   `db:"has_image"`
		Matches  bool   `db:"matches"`
		Image    []byte `db:"image"`
	}
	query, args, err := sqlx.In(`
		SELECT COALESCE(i.image_hash, ?) AS image_hash, i.id IS NOT NULL AS has_image,
		       COALESCE(BINARY i.image_hash, BINARY ?) IN (?) AS matches,
		       CASE WHEN BINARY i.image_hash IN (?) THEN NULL ELSE i.image END AS image
		FROM users u LEFT JOIN icons i ON i.user_id = u.id
		WHERE u.name = ?
	`, fallbackIconHash, fallbackIconHash, requestedHashes, requestedHashes, c.Param("username"))
	if err != nil {
		return err
	}
	err = dbConn.GetContext(ctx, &icon, query, args...)
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found user that has the given username")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user icon: "+err.Error())
	}

	c.Response().Header().Set("ETag", `"`+icon.Hash+`"`)
	if icon.Matches {
		return c.NoContent(http.StatusNotModified)
	}
	if !icon.HasImage {
		return c.Blob(http.StatusOK, "image/jpeg", fallbackIconData)
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

	rs, err := dbConn.ExecContext(ctx, "REPLACE INTO icons (user_id, image) VALUES (?, ?)", userID, req.Image)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to replace user icon: "+err.Error())
	}

	iconID, err := rs.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted icon id: "+err.Error())
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

	var row userResponseRow
	err := dbConn.GetContext(ctx, &row, userResponseSelect+" WHERE u.id = ?", fallbackIconHash, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found user that has the userid in session")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
	}

	return c.JSON(http.StatusOK, row.user())
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

	if err := addStreamerDNSRecord(ctx, req.Name); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to add DNS record: "+err.Error())
	}

	user, err := getUserByID(ctx, tx, userModel.ID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill user: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusCreated, user)
}

const streamerDNSZone = "u.isucon.dev."

var powerDNSClient = &http.Client{Timeout: 3 * time.Second}

func addStreamerDNSRecord(ctx context.Context, username string) error {
	type dnsRecord struct {
		Content  string `json:"content"`
		Disabled bool   `json:"disabled"`
	}
	type dnsRRSet struct {
		Name       string      `json:"name"`
		Type       string      `json:"type"`
		TTL        int         `json:"ttl"`
		ChangeType string      `json:"changetype"`
		Records    []dnsRecord `json:"records"`
	}
	recordName := username + "." + streamerDNSZone
	if username == "." {
		recordName = streamerDNSZone
	}
	data, err := json.Marshal(struct {
		RRSets []dnsRRSet `json:"rrsets"`
	}{RRSets: []dnsRRSet{{
		Name: recordName, Type: "A", TTL: 60, ChangeType: "EXTEND",
		Records: []dnsRecord{{Content: powerDNSSubdomainAddress}},
	}}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch,
		"http://127.0.0.1:8081/api/v1/servers/localhost/zones/"+streamerDNSZone, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", os.Getenv("ISUPIPE_PDNS_API_KEY"))
	res, err := powerDNSClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		return fmt.Errorf("PowerDNS returned %s: %s", res.Status, body)
	}
	return nil
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

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	userModel := UserModel{}
	// usernameはUNIQUEなので、whereで一意に特定できる
	err = tx.GetContext(ctx, &userModel, "SELECT * FROM users WHERE name = ?", req.Username)
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid username or password")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
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

	var row userResponseRow
	err := dbConn.GetContext(ctx, &row, userResponseSelect+" WHERE u.name = ?", fallbackIconHash, username)
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found user that has the given username")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
	}

	return c.JSON(http.StatusOK, row.user())
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

type userResponseRow struct {
	ID          int64  `db:"id"`
	Name        string `db:"name"`
	DisplayName string `db:"display_name"`
	Description string `db:"description"`
	ThemeID     int64  `db:"theme_id"`
	DarkMode    bool   `db:"dark_mode"`
	IconHash    string `db:"icon_hash"`
}

const userResponseSelect = `
	SELECT u.id, u.name, u.display_name, u.description, t.id AS theme_id, t.dark_mode,
	       COALESCE(i.image_hash, ?) AS icon_hash
	FROM users u JOIN themes t ON t.user_id = u.id
	LEFT JOIN icons i ON i.user_id = u.id
`

func (row userResponseRow) user() User {
	return User{
		ID:          row.ID,
		Name:        row.Name,
		DisplayName: row.DisplayName,
		Description: row.Description,
		Theme:       Theme{ID: row.ThemeID, DarkMode: row.DarkMode},
		IconHash:    row.IconHash,
	}
}

func getUsersByID(ctx context.Context, tx *sqlx.Tx, ids []int64) (map[int64]User, error) {
	query, args, err := sqlx.In(userResponseSelect+" WHERE u.id IN (?)", fallbackIconHash, ids)
	if err != nil {
		return nil, err
	}
	var rows []userResponseRow
	if err := tx.SelectContext(ctx, &rows, query, args...); err != nil {
		return nil, err
	}

	users := make(map[int64]User, len(rows))
	for _, row := range rows {
		users[row.ID] = row.user()
	}
	return users, nil
}

func getUserByID(ctx context.Context, tx *sqlx.Tx, id int64) (User, error) {
	var row userResponseRow
	if err := tx.GetContext(ctx, &row, userResponseSelect+" WHERE u.id = ?", fallbackIconHash, id); err != nil {
		return User{}, err
	}
	return row.user(), nil
}
