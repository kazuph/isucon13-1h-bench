package main

import (
	"errors"
	"github.com/go-sql-driver/mysql"

	"encoding/json"

	"net/http"
	"strconv"
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

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

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

	if _, err := tx.ExecContext(ctx, "UPDATE reservation_slots SET slot = slot - 1 WHERE start_at >= ? AND start_at < ? AND end_at <= ?", req.StartAt, req.EndAt, req.EndAt); err != nil {
		var dbErr *mysql.MySQLError
		if errors.As(err, &dbErr) && dbErr.Number == 3819 {
			return echo.NewHTTPError(http.StatusBadRequest, "予約枠がありません")
		}
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

	// タグ追加
	tags := make([]Tag, 0, len(req.Tags))
	links := make([]LivestreamTagModel, 0, len(req.Tags))
	catalogMu.RLock()
	owner := catalog.users[userID]
	for _, tagID := range req.Tags {
		t, ok := catalog.tags[tagID]
		if !ok {
			catalogMu.RUnlock()
			return echo.NewHTTPError(400, "unknown tag")
		}
		tags = append(tags, t)
		links = append(links, LivestreamTagModel{LivestreamID: livestreamID, TagID: tagID})
	}
	catalogMu.RUnlock()
	if len(links) > 0 {
		if _, err := tx.NamedExecContext(ctx, "INSERT INTO livestream_tags(livestream_id,tag_id) VALUES(:livestream_id,:tag_id)", links); err != nil {
			return err
		}
	}
	livestream := Livestream{ID: livestreamID, Owner: owner, Title: req.Title, Description: req.Description, PlaylistUrl: req.PlaylistUrl, ThumbnailUrl: req.ThumbnailUrl, StartAt: req.StartAt, EndAt: req.EndAt, Tags: tags}
	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	catalogMu.Lock()
	catalog.indexStream(livestream)
	catalogMu.Unlock()
	return c.JSON(http.StatusCreated, livestream)
}

func searchLivestreamsHandler(c echo.Context) error {
	tag := c.QueryParam("tag")
	limit := -1
	if value := c.QueryParam("limit"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return echo.NewHTTPError(400)
		}
		limit = n
	}
	catalogMu.RLock()
	ids := catalog.byTag[tag]
	if tag == "" {
		ids = catalog.allStreams
	}
	size := len(ids)
	if limit >= 0 && size > limit {
		size = limit
	}
	buf := make([]byte, 1, size*1024+2)
	buf[0] = '['
	for i := len(ids) - 1; i >= len(ids)-size; i-- {
		if i < len(ids)-1 {
			buf = append(buf, ',')
		}
		buf = append(buf, catalog.streamJSON[ids[i]]...)
	}
	catalogMu.RUnlock()
	return c.JSONBlob(200, append(buf, ']'))
}

func getMyLivestreamsHandler(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id := c.Get(defaultUserIDKey).(int64)
	return c.JSON(200, streamsForOwner(id))
}

func getUserLivestreamsHandler(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	catalogMu.RLock()
	id, ok := catalog.names[c.Param("username")]
	u := catalog.users[id]
	catalogMu.RUnlock()
	if !ok {
		return echo.NewHTTPError(404)
	}
	return c.JSON(200, streamsForOwner(u.ID))
}

// viewerテーブルの廃止
func enterLivestreamHandler(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := strconv.ParseInt(c.Param("livestream_id"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(400)
	}
	uid := c.Get(defaultUserIDKey).(int64)
	unlock := lockStream(id)
	defer unlock()
	if err := (event{Kind: "enter", Stream: id, User: uid, At: time.Now().Unix()}).persist(); err != nil {
		return err
	}
	return c.NoContent(200)
}

func exitLivestreamHandler(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := strconv.ParseInt(c.Param("livestream_id"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(400)
	}
	uid := c.Get(defaultUserIDKey).(int64)
	unlock := lockStream(id)
	defer unlock()
	if err := (event{Kind: "exit", Stream: id, User: uid, At: time.Now().Unix()}).persist(); err != nil {
		return err
	}
	return c.NoContent(200)
}

func getLivestreamHandler(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := strconv.ParseInt(c.Param("livestream_id"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(400)
	}
	stream, ok := knownStream(id)
	if !ok {
		return echo.NewHTTPError(404)
	}
	return c.JSON(200, stream)
}

func getLivecommentReportsHandler(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := strconv.ParseInt(c.Param("livestream_id"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(400)
	}
	uid := c.Get(defaultUserIDKey).(int64)
	stream, ok := knownStream(id)
	if !ok {
		return echo.NewHTTPError(404)
	}
	if stream.Owner.ID != uid {
		return echo.NewHTTPError(403)
	}
	activityMu.RLock()
	var models []LivecommentReportModel
	if a := activities[id]; a != nil {
		models = append(models, a.reports...)
	}
	activityMu.RUnlock()
	result := make([]LivecommentReport, len(models))
	for i, m := range models {
		r, err := fillLivecommentReportResponse(c.Request().Context(), nil, m)
		if err != nil {
			return err
		}
		result[i] = r
	}
	return c.JSON(200, result)
}
