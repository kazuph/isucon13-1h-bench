package main

import (
	"os"
	"database/sql"
	"errors"
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
	ctx := c.Request().Context()
	if err := loadTags(ctx); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get tags: "+err.Error())
	}
	cacheMu.RLock()
	tags := allTagsResponse
	cacheMu.RUnlock()
	if v := os.Getenv("ISUCON13_TAG_CACHE_CONTROL"); v != "" {
		c.Response().Header().Set("Cache-Control", v)
	}
	return c.JSON(http.StatusOK, &TagsResponse{
		Tags: tags,
	})
}

// 配信者のテーマ取得API
// GET /api/user/:username/theme
func getStreamerThemeHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		return err
	}

	username := c.Param("username")

	userModel, err := getUserByName(ctx, dbConn, username)
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found user that has the given username")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
	}

	themes, err := getThemes(ctx, dbConn, []int64{userModel.ID})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user theme: "+err.Error())
	}
	themeModel, ok := themes[userModel.ID]
	if !ok {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user theme")
	}

	return c.JSON(http.StatusOK, Theme{
		ID:       themeModel.ID,
		DarkMode: themeModel.DarkMode,
	})
}
