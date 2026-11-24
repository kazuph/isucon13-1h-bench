package main

import (
	"context"
	"database/sql"
	"encoding/json"

	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

type ReactionModel struct {
	ID           int64  `db:"id"`
	EmojiName    string `db:"emoji_name"`
	UserID       int64  `db:"user_id"`
	LivestreamID int64  `db:"livestream_id"`
	CreatedAt    int64  `db:"created_at"`
}

type Reaction struct {
	ID         int64      `json:"id"`
	EmojiName  string     `json:"emoji_name"`
	User       User       `json:"user"`
	Livestream Livestream `json:"livestream"`
	CreatedAt  int64      `json:"created_at"`
}

type PostReactionRequest struct {
	EmojiName string `json:"emoji_name"`
}

func getReactionsHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	var reactionModels []ReactionModel
	err = listTimeline(ctx, &reactionModels, "SELECT * FROM reactions WHERE livestream_id=? ORDER BY created_at DESC,id DESC", livestreamID, c.QueryParam("limit"))
	if err != nil {
		return err
	}
	reactions := make([]Reaction, len(reactionModels))
	if len(reactionModels) > 0 {
		ls, err := streamByID(ctx, dbConn, int64(livestreamID))
		if err != nil {
			return err
		}
		ids := make([]int64, len(reactionModels))
		for i, m := range reactionModels {
			ids[i] = m.UserID
		}
		users, err := loadUsers(ctx, dbConn, ids)
		if err != nil {
			return err
		}
		for i, m := range reactionModels {
			reactions[i] = Reaction{ID: m.ID, CreatedAt: m.CreatedAt, User: users[m.UserID], Livestream: ls, EmojiName: m.EmojiName}
		}
	}
	return c.JSON(http.StatusOK, reactions)
}

func postReactionHandler(c echo.Context) error {
	ctx := c.Request().Context()
	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	var req *PostReactionRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	reactionModel := ReactionModel{
		UserID:       int64(userID),
		LivestreamID: int64(livestreamID),
		EmojiName:    req.EmojiName,
		CreatedAt:    time.Now().Unix(),
	}

	reaction, err := fillReactionResponse(ctx, dbConn, reactionModel)
	if err != nil {
		return err
	}
	result, err := dbConn.NamedExecContext(ctx, "INSERT INTO reactions (user_id, livestream_id, emoji_name, created_at) VALUES (:user_id, :livestream_id, :emoji_name, :created_at)", reactionModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert reaction: "+err.Error())
	}

	reactionID, err := result.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted reaction id: "+err.Error())
	}
	reaction.ID = reactionID
	return c.JSON(http.StatusCreated, reaction)
}

func fillReactionResponse(ctx context.Context, tx readDB, reactionModel ReactionModel) (Reaction, error) {
	users, err := loadUsers(ctx, tx, []int64{reactionModel.UserID})
	if err != nil {
		return Reaction{}, err
	}
	user, ok := users[reactionModel.UserID]
	if !ok {
		return Reaction{}, sql.ErrNoRows
	}
	livestream, err := streamByID(ctx, tx, reactionModel.LivestreamID)
	if err != nil {
		return Reaction{}, err
	}
	reaction := Reaction{
		ID:         reactionModel.ID,
		EmojiName:  reactionModel.EmojiName,
		User:       user,
		Livestream: livestream,
		CreatedAt:  reactionModel.CreatedAt,
	}

	return reaction, nil
}
