package main

import (
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
	data.RLock()
	tags := data.tagList
	data.RUnlock()
	return c.JSON(200, tags)
}

// 配信者のテーマ取得API
// GET /api/user/:username/theme
func getStreamerThemeHandler(c echo.Context) error {
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
	return c.JSON(200, u.Theme)
}
