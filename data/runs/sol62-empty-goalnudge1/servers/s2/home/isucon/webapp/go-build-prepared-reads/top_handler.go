package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

type Tag struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type TagModel struct {
	ID   int64  `db:"id"`
	Name string `db:"name"`
}

type TagsResponse struct {
	Tags []*Tag `json:"tags"`
}

var tagCatalog struct {
	mu        sync.Mutex
	expiresAt time.Time
	response  TagsResponse
	json      []byte
}

func getTagHandler(c echo.Context) error {
	tagCatalog.mu.Lock()
	now := time.Now()
	if !now.Before(tagCatalog.expiresAt) {
		tags := []*Tag{}
		if err := dbConn.SelectContext(c.Request().Context(), &tags, "SELECT id, name FROM tags ORDER BY id"); err != nil {
			tagCatalog.mu.Unlock()
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get tags: "+err.Error())
		}
		response := TagsResponse{Tags: tags}
		payload, err := json.Marshal(response)
		if err != nil {
			tagCatalog.mu.Unlock()
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to encode tags: "+err.Error())
		}
		tagCatalog.response = response
		tagCatalog.json = payload
		tagCatalog.expiresAt = now.Add(time.Second)
	}
	response, payload := tagCatalog.response, tagCatalog.json
	tagCatalog.mu.Unlock()

	if c.QueryParam("pretty") != "" {
		return c.JSON(http.StatusOK, response)
	}
	return c.JSONBlob(http.StatusOK, payload)
}

// 配信者のテーマ取得API
// GET /api/user/:username/theme
func getStreamerThemeHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		c.Logger().Printf("verifyUserSession: %+v\n", err)
		return err
	}

	username := c.Param("username")

	var theme struct {
		ID       sql.NullInt64
		DarkMode sql.NullBool `db:"dark_mode"`
	}
	err := dbConn.GetContext(ctx, &theme, `
		SELECT theme_id AS id, dark_mode FROM users WHERE name = ?`, username)
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found user that has the given username")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user theme: "+err.Error())
	}
	if !theme.ID.Valid {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user theme")
	}
	return c.JSON(http.StatusOK, Theme{ID: theme.ID.Int64, DarkMode: theme.DarkMode.Bool})
}
