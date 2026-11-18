package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
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

func getTagHandler(c echo.Context) error {
	ctx := c.Request().Context()

	catalog := activeTagCatalog.Load()
	if body := catalog.body.Load(); body != nil {
		return c.JSONBlob(http.StatusOK, *body)
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin new transaction: : "+err.Error()+err.Error())
	}
	defer tx.Rollback()

	var tagModels []*TagModel
	if err := tx.SelectContext(ctx, &tagModels, "SELECT * FROM tags"); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get tags: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	tags := make([]*Tag, len(tagModels))
	for i := range tagModels {
		tags[i] = &Tag{
			ID:   tagModels[i].ID,
			Name: tagModels[i].Name,
		}
	}
	body, err := json.Marshal(&TagsResponse{Tags: tags})
	if err != nil {
		return err
	}
	catalog.body.CompareAndSwap(nil, &body)
	return c.JSONBlob(http.StatusOK, body)
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

	if cache := requestCache(ctx); cache != nil {
		if user, ok := cache.sharedUsers.getByName(username, time.Now()); ok {
			return c.JSON(http.StatusOK, user.Theme)
		}
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	userModel := UserModel{}
	err = tx.GetContext(ctx, &userModel, "SELECT id FROM users WHERE name = ?", username)
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found user that has the given username")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
	}

	themeModel := ThemeModel{}
	if err := tx.GetContext(ctx, &themeModel, "SELECT * FROM themes WHERE user_id = ?", userModel.ID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user theme: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	theme := Theme{
		ID:       themeModel.ID,
		DarkMode: themeModel.DarkMode,
	}

	return c.JSON(http.StatusOK, theme)
}
