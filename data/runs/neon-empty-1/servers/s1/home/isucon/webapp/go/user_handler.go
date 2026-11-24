package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
)

const (
	bcryptDefaultCost = bcrypt.MinCost
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
	Name        string               `json:"name"`
	DisplayName string               `json:"display_name"`
	Description string               `json:"description"`
	Password    string               `json:"password"`
	Theme       PostUserRequestTheme `json:"theme"`
}

type PostUserRequestTheme struct {
	DarkMode bool `json:"dark_mode"`
}

type LoginRequest struct {
	Username string `json:"username"`
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
	username := c.Param("username")

	u, ok := getUserByName(username)
	if !ok {
		return echo.NewHTTPError(http.StatusNotFound, "not found user that has the given username")
	}

	iconHash := getIconHash(u.ID)
	if inm := c.Request().Header.Get("If-None-Match"); inm != "" {
		trimmed := strings.TrimSpace(inm)
		if strings.HasPrefix(trimmed, "W/") {
			trimmed = strings.TrimPrefix(trimmed, "W/")
		}
		trimmed = strings.Trim(trimmed, "\"")
		if trimmed == iconHash {
			icon304Cnt.Add(1)
			return c.NoContent(http.StatusNotModified)
		}
	}

	img, actualHash, err := getIconImage(ctx, u.ID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user icon: "+err.Error())
	}

	icon200Cnt.Add(1)
	c.Response().Header().Set("ETag", `"`+actualHash+`"`)
	return c.Blob(http.StatusOK, "image/jpeg", img)
}

func decodePostIconFast(body []byte) ([]byte, error) {
	keyIdx := bytes.Index(body, []byte(`"image"`))
	if keyIdx >= 0 {
		rest := body[keyIdx+7:]
		colonIdx := bytes.IndexByte(rest, ':')
		if colonIdx >= 0 {
			rest = bytes.TrimLeft(rest[colonIdx+1:], " \t\r\n")
			if len(rest) > 0 && rest[0] == '"' {
				rest = rest[1:]
				endQuote := bytes.IndexByte(rest, '"')
				if endQuote >= 0 {
					b64 := rest[:endQuote]
					if bytes.IndexByte(b64, '\\') < 0 {
						dst := make([]byte, base64.StdEncoding.DecodedLen(len(b64)))
						n, err := base64.StdEncoding.Decode(dst, b64)
						if err == nil {
							return dst[:n], nil
						}
					}
				}
			}
		}
	}
	var req PostIconRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	return req.Image, nil
}

func postIconHandler(c echo.Context) error {
	userID, err := verifyAndGetUserID(c)
	if err != nil {
		return err
	}

	var body []byte
	if cl := c.Request().ContentLength; cl > 0 && cl < 10*1024*1024 {
		body = make([]byte, cl)
		if _, err = io.ReadFull(c.Request().Body, body); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "failed to read request body")
		}
	} else {
		body, err = io.ReadAll(c.Request().Body)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "failed to read request body")
		}
	}
	imgBytes, err := decodePostIconFast(body)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	sum := sha256.Sum256(imgBytes)
	hashStr := fmt.Sprintf("%x", sum)

	iconID := nextIconID.Add(1)
	setIconCache(userID, hashStr, imgBytes)
	enqueueIcon(dbIconUpsert{ID: iconID, UserID: userID, Hash: hashStr, Image: imgBytes})
	broadcastIconHashAsync(userID, hashStr)

	return c.JSON(http.StatusCreated, &PostIconResponse{
		ID: iconID,
	})
}

func getMeHandler(c echo.Context) error {
	userID, err := verifyAndGetUserID(c)
	if err != nil {
		return err
	}

	u, ok := getUserByID(userID)
	if !ok {
		return echo.NewHTTPError(http.StatusNotFound, "not found user that has the userid in session")
	}

	return c.Blob(http.StatusOK, echo.MIMEApplicationJSONCharsetUTF8, u.getJSON())
}

// ユーザ登録API
// POST /api/register
func registerHandler(c echo.Context) error {
	defer c.Request().Body.Close()

	req := PostUserRequest{}
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	if req.Name == "pipe" {
		return echo.NewHTTPError(http.StatusBadRequest, "the username 'pipe' is reserved")
	}

	// Quick pre-check before spending bcrypt CPU on duplicate usernames
	if _, exists := getUserByName(req.Name); exists {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert user: duplicate username")
	}

	hashedPassword, err := generateBcryptHash(req.Password)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to generate hashed password: "+err.Error())
	}

	entry, ok := tryReserveAndAddUser(req, string(hashedPassword))
	if !ok {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert user: duplicate username")
	}
	recordRegMetric()

	if isClustered() {
		if err := broadcastNewUser(entry); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to sync user: "+err.Error())
		}
	} else {
		dnsAddRecord(req.Name)
	}
	enqueueUser(dbUserInsert{
		ID:             entry.ID,
		Name:           entry.Name,
		DisplayName:    entry.DisplayName,
		Description:    entry.Description,
		HashedPassword: entry.HashedPassword,
		ThemeID:        entry.Theme.ID,
		DarkMode:       entry.Theme.DarkMode,
	})

	return c.Blob(http.StatusCreated, echo.MIMEApplicationJSONCharsetUTF8, entry.getJSON())
}

// ユーザログインAPI
// POST /api/login
func loginHandler(c echo.Context) error {
	defer c.Request().Body.Close()

	req := LoginRequest{}
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	u, ok := getUserByName(req.Username)
	if !ok {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid username or password")
	}

	err := compareBcryptHash(u.HashedPassword, req.Password)
	if err == bcrypt.ErrMismatchedHashAndPassword {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid username or password")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to compare hash and password: "+err.Error())
	}

	setUserSessionCookie(c, u.ID)
	return c.NoContent(http.StatusOK)
}

// ユーザ詳細API
// GET /api/user/:username
func getUserHandler(c echo.Context) error {
	if _, err := verifyAndGetUserID(c); err != nil {
		return err
	}

	username := c.Param("username")
	u, ok := getUserByName(username)
	if !ok {
		return echo.NewHTTPError(http.StatusNotFound, "not found user that has the given username")
	}

	return c.Blob(http.StatusOK, echo.MIMEApplicationJSONCharsetUTF8, u.getJSON())
}
