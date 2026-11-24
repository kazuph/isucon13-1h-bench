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
	return c.Blob(http.StatusOK, echo.MIMEApplicationJSONCharsetUTF8, getTagsJSON())
}

// 配信者のテーマ取得API
// GET /api/user/:username/theme
func getStreamerThemeHandler(c echo.Context) error {
	if _, err := verifyAndGetUserID(c); err != nil {
		return err
	}

	username := c.Param("username")
	u, ok := getUserByName(username)
	if !ok {
		return echo.NewHTTPError(http.StatusNotFound, "not found user that has the given username")
	}

	return c.JSON(http.StatusOK, u.Theme)
}
