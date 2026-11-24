package main

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
)

type Tag struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type TagsResponse struct {
	Tags []*Tag `json:"tags"`
}

func getTagHandler(c echo.Context) error {
	ctx := c.Request().Context()

	tags := make([]*Tag, 0)
	if err := dbConn.SelectContext(ctx, &tags, "SELECT id, name FROM tags"); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get tags: "+err.Error())
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
		// echo.NewHTTPErrorが返っているのでそのまま出力
		c.Logger().Printf("verifyUserSession: %+v\n", err)
		return err
	}

	var row struct {
		ID       sql.NullInt64 `db:"id"`
		DarkMode sql.NullBool  `db:"dark_mode"`
	}
	err := dbConn.GetContext(ctx, &row, `
		SELECT t.id, t.dark_mode
		FROM users u LEFT JOIN themes t ON t.user_id = u.id WHERE u.name = ?`, c.Param("username"))
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found user that has the given username")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user theme: "+err.Error())
	}
	if !row.ID.Valid {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user theme: "+sql.ErrNoRows.Error())
	}
	return c.JSON(http.StatusOK, Theme{ID: row.ID.Int64, DarkMode: row.DarkMode.Bool})
}
