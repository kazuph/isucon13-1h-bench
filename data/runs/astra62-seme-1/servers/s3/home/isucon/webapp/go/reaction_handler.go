package main

import (
	"context"
	"database/sql"
	"encoding/json"

	"net/http"
	"strconv"
	"time"

	"github.com/jmoiron/sqlx"

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
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := strconv.ParseInt(c.Param("livestream_id"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(400)
	}

	activityMu.RLock()
	var models []ReactionModel
	if a := activities[id]; a != nil {
		n, err := pageSize(c, len(a.reactions))
		if err != nil {
			activityMu.RUnlock()
			return err
		}
		models = append(models, a.reactions[len(a.reactions)-n:]...)
	}
	activityMu.RUnlock()
	return timelineJSON(c, models, appendReaction)
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
	userID := c.Get(defaultUserIDKey).(int64)

	var req *PostReactionRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	unlock := lockStream(int64(livestreamID))
	defer unlock()
	if _, ok := knownStream(int64(livestreamID)); !ok {
		return echo.NewHTTPError(404)
	}
	reactionModel := ReactionModel{
		UserID:       int64(userID),
		LivestreamID: int64(livestreamID),
		EmojiName:    req.EmojiName,
		CreatedAt:    time.Now().Unix(),
	}

	reactionModel.ID = sequence.Add(1)
	if err := (event{Kind: "reaction", ID: reactionModel.ID, Stream: int64(livestreamID), User: userID, At: reactionModel.CreatedAt, Text: req.EmojiName}).persist(); err != nil {
		return err
	}

	reaction, err := fillReactionResponse(ctx, nil, reactionModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill reaction: "+err.Error())
	}

	return c.JSON(http.StatusCreated, reaction)
}

func fillReactionResponse(ctx context.Context, tx *sqlx.Tx, reactionModel ReactionModel) (Reaction, error) {
	user, ok := knownUser(reactionModel.UserID)
	if !ok {
		return Reaction{}, sql.ErrNoRows
	}
	livestream, ok := knownStream(reactionModel.LivestreamID)
	if !ok {
		return Reaction{}, sql.ErrNoRows
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
