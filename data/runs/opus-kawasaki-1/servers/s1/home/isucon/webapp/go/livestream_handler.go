package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

type ReserveLivestreamRequest struct {
	Tags         []int64 `json:"tags"`
	Title        string  `json:"title"`
	Description  string  `json:"description"`
	PlaylistUrl  string  `json:"playlist_url"`
	ThumbnailUrl string  `json:"thumbnail_url"`
	StartAt      int64   `json:"start_at"`
	EndAt        int64   `json:"end_at"`
}

type LivestreamViewerModel struct {
	UserID       int64 `db:"user_id" json:"user_id"`
	LivestreamID int64 `db:"livestream_id" json:"livestream_id"`
	CreatedAt    int64 `db:"created_at" json:"created_at"`
}

type LivestreamModel struct {
	ID           int64  `db:"id" json:"id"`
	UserID       int64  `db:"user_id" json:"user_id"`
	Title        string `db:"title" json:"title"`
	Description  string `db:"description" json:"description"`
	PlaylistUrl  string `db:"playlist_url" json:"playlist_url"`
	ThumbnailUrl string `db:"thumbnail_url" json:"thumbnail_url"`
	StartAt      int64  `db:"start_at" json:"start_at"`
	EndAt        int64  `db:"end_at" json:"end_at"`
}

type Livestream struct {
	ID           int64  `json:"id"`
	Owner        User   `json:"owner"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	PlaylistUrl  string `json:"playlist_url"`
	ThumbnailUrl string `json:"thumbnail_url"`
	Tags         []Tag  `json:"tags"`
	StartAt      int64  `json:"start_at"`
	EndAt        int64  `json:"end_at"`
}

type LivestreamTagModel struct {
	ID           int64 `db:"id" json:"id"`
	LivestreamID int64 `db:"livestream_id" json:"livestream_id"`
	TagID        int64 `db:"tag_id" json:"tag_id"`
}

type ReservationSlotModel struct {
	ID      int64 `db:"id" json:"id"`
	Slot    int64 `db:"slot" json:"slot"`
	StartAt int64 `db:"start_at" json:"start_at"`
	EndAt   int64 `db:"end_at" json:"end_at"`
}

func reserveLivestreamHandler(c echo.Context) error {
	ctx := c.Request().Context()
	defer c.Request().Body.Close()

	if err := verifyUserSession(c); err != nil {
		return err
	}

	sess, _ := session.Get(defaultSessionIDKey, c)
	userID := sess.Values[defaultUserIDKey].(int64)

	var req *ReserveLivestreamRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	// 2023/11/25 10:00からの１年間の期間内であるかチェック
	var (
		termStartAt    = time.Date(2023, 11, 25, 1, 0, 0, 0, time.UTC)
		termEndAt      = time.Date(2024, 11, 25, 1, 0, 0, 0, time.UTC)
		reserveStartAt = time.Unix(req.StartAt, 0)
		reserveEndAt   = time.Unix(req.EndAt, 0)
	)
	if (reserveStartAt.Equal(termEndAt) || reserveStartAt.After(termEndAt)) || (reserveEndAt.Equal(termStartAt) || reserveEndAt.Before(termStartAt)) {
		return echo.NewHTTPError(http.StatusBadRequest, "bad reservation time range")
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	var slots []*ReservationSlotModel
	if err := tx.SelectContext(ctx, &slots, "SELECT * FROM reservation_slots WHERE start_at >= ? AND start_at < ? AND end_at <= ? FOR UPDATE", req.StartAt, req.EndAt, req.EndAt); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get reservation_slots: "+err.Error())
	}
	for _, slot := range slots {
		if slot.Slot < 1 {
			return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("予約期間 %d ~ %dに対して、予約区間 %d ~ %dが予約できません", termStartAt.Unix(), termEndAt.Unix(), req.StartAt, req.EndAt))
		}
	}

	livestreamModel := &LivestreamModel{
		UserID:       int64(userID),
		Title:        req.Title,
		Description:  req.Description,
		PlaylistUrl:  req.PlaylistUrl,
		ThumbnailUrl: req.ThumbnailUrl,
		StartAt:      req.StartAt,
		EndAt:        req.EndAt,
	}

	if _, err := tx.ExecContext(ctx, "UPDATE reservation_slots SET slot = slot - 1 WHERE start_at >= ? AND start_at < ? AND end_at <= ?", req.StartAt, req.EndAt, req.EndAt); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to update reservation_slot: "+err.Error())
	}

	rs, err := tx.NamedExecContext(ctx, "INSERT INTO livestreams (user_id, title, description, playlist_url, thumbnail_url, start_at, end_at) VALUES(:user_id, :title, :description, :playlist_url, :thumbnail_url, :start_at, :end_at)", livestreamModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert livestream: "+err.Error())
	}

	livestreamID, err := rs.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted livestream id: "+err.Error())
	}
	livestreamModel.ID = livestreamID

	if len(req.Tags) > 0 {
		tagRows := make([]*LivestreamTagModel, 0, len(req.Tags))
		for _, tagID := range req.Tags {
			tagRows = append(tagRows, &LivestreamTagModel{LivestreamID: livestreamID, TagID: tagID})
		}
		if _, err := tx.NamedExecContext(ctx, "INSERT INTO livestream_tags (livestream_id, tag_id) VALUES (:livestream_id, :tag_id)", tagRows); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert livestream tag: "+err.Error())
		}
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	putLivestream(livestreamModel)
	storeAddLivestream(livestreamModel)
	if err := loadTags(ctx); err == nil {
		tags := make([]Tag, 0, len(req.Tags))
		cacheMu.Lock()
		for _, tagID := range req.Tags {
			tags = append(tags, tagByID[tagID])
		}
		tagsByStreamID[livestreamID] = tags
		cacheMu.Unlock()
	}

	livestream, err := fillLivestream(ctx, dbConn, livestreamModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestream: "+err.Error())
	}

	return c.JSON(http.StatusCreated, livestream)
}

func searchLivestreamsHandler(c echo.Context) error {
	ctx := c.Request().Context()
	keyTagName := c.QueryParam("tag")

	var livestreamModels []*LivestreamModel
	if keyTagName != "" {
		if err := loadTags(ctx); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get tags: "+err.Error())
		}
		cacheMu.RLock()
		tagID, ok := tagIDByName[keyTagName]
		cacheMu.RUnlock()
		if ok {
			if err := dbConn.SelectContext(ctx, &livestreamModels, "SELECT l.* FROM livestream_tags lt INNER JOIN livestreams l ON l.id = lt.livestream_id WHERE lt.tag_id = ? ORDER BY lt.livestream_id DESC", tagID); err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestreams: "+err.Error())
			}
		}
	} else {
		limit := -1
		if c.QueryParam("limit") != "" {
			l, err := strconv.Atoi(c.QueryParam("limit"))
			if err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, "limit query parameter must be integer")
			}
			limit = l
		}
		livestreamModels = storeLatestLivestreams(limit)
	}

	livestreams, err := fillLivestreams(ctx, dbConn, livestreamModels)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestream: "+err.Error())
	}
	return c.JSON(http.StatusOK, livestreams)
}

func getMyLivestreamsHandler(c echo.Context) error {
	ctx := c.Request().Context()
	if err := verifyUserSession(c); err != nil {
		return err
	}

	sess, _ := session.Get(defaultSessionIDKey, c)
	userID := sess.Values[defaultUserIDKey].(int64)

	livestreamModels := storeUserLivestreams(userID)
	livestreams, err := fillLivestreams(ctx, dbConn, livestreamModels)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestream: "+err.Error())
	}
	return c.JSON(http.StatusOK, livestreams)
}

func getUserLivestreamsHandler(c echo.Context) error {
	ctx := c.Request().Context()
	if err := verifyUserSession(c); err != nil {
		return err
	}

	username := c.Param("username")

	user, err := getUserByName(ctx, dbConn, username)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "user not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
	}

	livestreamModels := storeUserLivestreams(user.ID)
	livestreams, err := fillLivestreams(ctx, dbConn, livestreamModels)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestream: "+err.Error())
	}
	return c.JSON(http.StatusOK, livestreams)
}

func enterLivestreamHandler(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}

	sess, _ := session.Get(defaultSessionIDKey, c)
	userID := sess.Values[defaultUserIDKey].(int64)

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id must be integer")
	}

	enqueueWrite(&writeOp{kind: opViewerEnter, userID: userID, lsID: int64(livestreamID), created: time.Now().Unix()})

	return c.NoContent(http.StatusOK)
}

func exitLivestreamHandler(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}

	sess, _ := session.Get(defaultSessionIDKey, c)
	userID := sess.Values[defaultUserIDKey].(int64)

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	enqueueWrite(&writeOp{kind: opViewerExit, userID: userID, lsID: int64(livestreamID)})

	return c.NoContent(http.StatusOK)
}

func getLivestreamHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	livestreamModel, err := getLivestreamByID(ctx, dbConn, int64(livestreamID))
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found livestream that has the given id")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream: "+err.Error())
	}

	livestream, err := fillLivestream(ctx, dbConn, livestreamModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestream: "+err.Error())
	}

	return c.JSON(http.StatusOK, livestream)
}

func getLivecommentReportsHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	livestreamModel, err := getLivestreamByID(ctx, dbConn, int64(livestreamID))
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream: "+err.Error())
	}

	sess, _ := session.Get(defaultSessionIDKey, c)
	userID := sess.Values[defaultUserIDKey].(int64)

	if livestreamModel.UserID != userID {
		return echo.NewHTTPError(http.StatusForbidden, "can't get other streamer's livecomment reports")
	}

	reportModels := storeGetReports(int64(livestreamID))
	lcMap := make(map[int64]LivecommentModel, len(reportModels))
	for _, r := range reportModels {
		if lc, ok := storeGetCommentByID(r.LivecommentID); ok {
			lcMap[lc.ID] = lc
		}
	}

	reports, err := fillLivecommentReportsWithComments(ctx, dbConn, reportModels, lcMap)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livecomment report: "+err.Error())
	}

	return c.JSON(http.StatusOK, reports)
}
