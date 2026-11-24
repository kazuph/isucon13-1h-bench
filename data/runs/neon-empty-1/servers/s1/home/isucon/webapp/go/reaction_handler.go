package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
)

type ReactionModel struct {
	ID           int64  `db:"id"`
	EmojiName    string `db:"emoji_name"`
	UserID       int64  `db:"user_id"`
	LivestreamID int64  `db:"livestream_id"`
	CreatedAt    int64  `db:"created_at"`
}

type PostReactionRequest struct {
	EmojiName string `json:"emoji_name"`
}

func getReactionsHandler(c echo.Context) error {
	if _, err := verifyAndGetUserID(c); err != nil {
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}
	lsID := int64(livestreamID)

	limit := -1
	if limitStr := c.QueryParam("limit"); limitStr != "" {
		v, err := strconv.Atoi(limitStr)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "limit query parameter must be integer")
		}
		limit = v
	}

	rxMu.RLock()
	src := reactionsByLS[lsID]
	total := len(src)
	if total == 0 || limit == 0 {
		rxMu.RUnlock()
		return c.Blob(http.StatusOK, echo.MIMEApplicationJSONCharsetUTF8, []byte("[]"))
	}
	cnt := total
	if limit > 0 && limit < cnt {
		cnt = limit
	}
	models := make([]*ReactionModel, cnt)
	for i := 0; i < cnt; i++ {
		models[i] = src[total-1-i]
	}
	rxMu.RUnlock()

	lsEntry, ok := getLivestreamEntryByID(lsID)
	if !ok {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream")
	}
	lsJSON, ok := lsEntry.getJSON()
	if !ok {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to build livestream")
	}

	buf := getPooledBuf()
	defer putPooledBuf(buf)
	buf.Grow(cnt*(len(lsJSON)+220) + 2)
	buf.WriteByte('[')
	for i, m := range models {
		u, ok := getUserByID(m.UserID)
		if !ok {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get reaction user")
		}
		if i > 0 {
			buf.WriteByte(',')
		}
		appendReactionJSON(buf, m.ID, m.EmojiName, u.getJSON(), lsJSON, m.CreatedAt)
	}
	buf.WriteByte(']')

	return c.Blob(http.StatusOK, echo.MIMEApplicationJSONCharsetUTF8, buf.Bytes())
}

func postReactionHandler(c echo.Context) error {
	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}
	lsID := int64(livestreamID)

	userID, err := verifyAndGetUserID(c)
	if err != nil {
		return err
	}

	var req PostReactionRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	now := time.Now().Unix()
	rxID := allocShardedID(&nextReactionID)
	m := &ReactionModel{
		ID:           rxID,
		EmojiName:    req.EmojiName,
		UserID:       userID,
		LivestreamID: lsID,
		CreatedAt:    now,
	}

	rxMu.Lock()
	reactionsByLS[lsID] = append(reactionsByLS[lsID], m)
	rxMu.Unlock()

	u, ok := getUserByID(userID)
	if !ok {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill reaction user")
	}
	lsEntry, ok := getLivestreamEntryByID(lsID)
	if !ok {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill reaction livestream")
	}
	statsOnReaction(lsID, lsEntry.Model.UserID, req.EmojiName)
	enqueueReaction(*m)

	lsJSON, ok := lsEntry.getJSON()
	if !ok {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill reaction livestream")
	}

	buf := getPooledBuf()
	defer putPooledBuf(buf)
	buf.Grow(len(lsJSON) + 256)
	appendReactionJSON(buf, rxID, req.EmojiName, u.getJSON(), lsJSON, now)
	return c.Blob(http.StatusCreated, echo.MIMEApplicationJSONCharsetUTF8, buf.Bytes())
}
