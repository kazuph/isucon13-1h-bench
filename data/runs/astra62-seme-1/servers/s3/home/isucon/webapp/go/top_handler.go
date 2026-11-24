package main

import (
	"net/http"

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

func getTagHandler(c echo.Context) error {
	catalogMu.RLock()
	body := catalog.tagsJSON
	catalogMu.RUnlock()
	return c.JSONBlob(http.StatusOK, body)
}

// 配信者のテーマ取得API
// GET /api/user/:username/theme
func getStreamerThemeHandler(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	catalogMu.RLock()
	id, ok := catalog.names[c.Param("username")]
	u := catalog.users[id]
	catalogMu.RUnlock()
	if !ok {
		return echo.NewHTTPError(404)
	}
	return c.JSON(200, u.Theme)
}
