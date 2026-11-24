package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

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
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	userID := c.Get(defaultUserIDKey).(int64)

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

	slots, available := matchingSlots(req.StartAt, req.EndAt)
	if !available {
		return echo.NewHTTPError(http.StatusBadRequest, "no available reservation slot")
	}
	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	var (
		livestreamModel = &LivestreamModel{
			UserID:       int64(userID),
			Title:        req.Title,
			Description:  req.Description,
			PlaylistUrl:  req.PlaylistUrl,
			ThumbnailUrl: req.ThumbnailUrl,
			StartAt:      req.StartAt,
			EndAt:        req.EndAt,
		}
	)

	result, err := tx.ExecContext(ctx, "UPDATE reservation_slots SET slot=slot-1 WHERE start_at>=? AND start_at<? AND end_at<=? AND slot>0", req.StartAt, req.EndAt, req.EndAt)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != int64(len(slots)) {
		return echo.NewHTTPError(http.StatusBadRequest, "no available reservation slot")
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

	tags := make([]Tag, 0, len(req.Tags))
	catalog.RLock()
	for _, id := range req.Tags {
		t, ok := catalog.tagByID[id]
		if !ok {
			catalog.RUnlock()
			return echo.NewHTTPError(http.StatusBadRequest, "unknown tag")
		}
		tags = append(tags, t)
	}
	owner := catalog.users[userID]
	catalog.RUnlock()
	if len(tags) > 0 {
		values := make([]string, len(tags))
		args := make([]interface{}, 0, len(tags)*2)
		for i, t := range tags {
			values[i] = "(?,?)"
			args = append(args, livestreamID, t.ID)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO livestream_tags (livestream_id,tag_id) VALUES "+strings.Join(values, ","), args...); err != nil {
			return err
		}
	}
	livestream := Livestream{ID: livestreamID, Owner: owner, Title: req.Title, Tags: tags, Description: req.Description, PlaylistUrl: req.PlaylistUrl, ThumbnailUrl: req.ThumbnailUrl, StartAt: req.StartAt, EndAt: req.EndAt}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	bookedSlots(slots)
	catalog.Lock()
	catalog.addStream(livestream)
	catalog.Unlock()

	return c.JSON(http.StatusCreated, livestream)
}

func searchLivestreamsHandler(c echo.Context) error {
	tag := c.QueryParam("tag")
	limit := -1
	if value := c.QueryParam("limit"); tag == "" && value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "limit must be a nonnegative integer")
		}
		limit = n
	}
	return c.JSONBlob(http.StatusOK, listStreamsJSON(0, tag, limit))
}

func getMyLivestreamsHandler(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	owner := c.Get(defaultUserIDKey).(int64)
	return c.JSONBlob(http.StatusOK, listStreamsJSON(owner, "", -1))
}

func getUserLivestreamsHandler(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	user, ok := userByName(c.Param("username"))
	if !ok {
		return echo.NewHTTPError(http.StatusNotFound, "user not found")
	}
	owner := user.ID
	return c.JSONBlob(http.StatusOK, listStreamsJSON(owner, "", -1))
}

// viewerテーブルの廃止
var viewerLocks [256]sync.Mutex

func viewerLock(userID int64, streamID int) *sync.Mutex {
	return &viewerLocks[(uint64(userID)*31+uint64(streamID))%uint64(len(viewerLocks))]
}

func enterLivestreamHandler(c echo.Context) error {
	ctx := c.Request().Context()
	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	userID := c.Get(defaultUserIDKey).(int64)

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id must be integer")
	}

	viewer := LivestreamViewerModel{
		UserID:       int64(userID),
		LivestreamID: int64(livestreamID),
		CreatedAt:    time.Now().Unix(),
	}

	lock := viewerLock(userID, livestreamID)
	lock.Lock()
	defer lock.Unlock()
	if _, err := dbConn.NamedExecContext(ctx, "INSERT INTO livestream_viewers_history (user_id, livestream_id, created_at) VALUES(:user_id, :livestream_id, :created_at)", viewer); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert livestream_view_history: "+err.Error())
	}

	noteView(int64(livestreamID), 1)
	return c.NoContent(http.StatusOK)
}

func exitLivestreamHandler(c echo.Context) error {
	ctx := c.Request().Context()
	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	userID := c.Get(defaultUserIDKey).(int64)

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	lock := viewerLock(userID, livestreamID)
	lock.Lock()
	defer lock.Unlock()
	result, err := dbConn.ExecContext(ctx, "DELETE FROM livestream_viewers_history WHERE user_id = ? AND livestream_id = ?", userID, livestreamID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete livestream_view_history: "+err.Error())
	}

	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	noteView(int64(livestreamID), -n)
	return c.NoContent(http.StatusOK)
}

func getLivestreamHandler(c echo.Context) error {

	if err := verifyUserSession(c); err != nil {
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	livestream, err := catalogStream(int64(livestreamID))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "livestream not found")
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

	stream, err := catalogStream(int64(livestreamID))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "stream not found")
	}
	if stream.Owner.ID != c.Get(defaultUserIDKey).(int64) {
		return echo.NewHTTPError(http.StatusForbidden, "can't get other streamer's livecomment reports")
	}
	metrics.RLock()
	m := metrics.streams[int64(livestreamID)]
	empty := m == nil || m.reports == 0
	metrics.RUnlock()
	if empty {
		return c.JSON(http.StatusOK, []LivecommentReport{})
	}
	rows, err := dbConn.QueryContext(ctx, `SELECT r.id,r.user_id,r.created_at,c.id,c.user_id,c.livestream_id,c.comment,c.tip,c.created_at
		FROM livecomment_reports r JOIN livecomments c ON c.id=r.livecomment_id WHERE r.livestream_id=? ORDER BY r.id`, livestreamID)
	if err != nil {
		return err
	}
	defer rows.Close()
	reports := make([]LivecommentReport, 0)
	for rows.Next() {
		var r LivecommentReport
		var uid int64
		var m LivecommentModel
		if err := rows.Scan(&r.ID, &uid, &r.CreatedAt, &m.ID, &m.UserID, &m.LivestreamID, &m.Comment, &m.Tip, &m.CreatedAt); err != nil {
			return err
		}
		r.Reporter, err = catalogUser(uid)
		if err != nil {
			return err
		}
		r.Livecomment, err = fillLivecommentResponse(m)
		if err != nil {
			return err
		}
		reports = append(reports, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, reports)
}
