package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/jmoiron/sqlx"
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
	ID           int64           `db:"id" json:"id"`
	UserID       int64           `db:"user_id" json:"user_id"`
	Title        string          `db:"title" json:"title"`
	Description  string          `db:"description" json:"description"`
	PlaylistUrl  string          `db:"playlist_url" json:"playlist_url"`
	ThumbnailUrl string          `db:"thumbnail_url" json:"thumbnail_url"`
	StartAt      int64           `db:"start_at" json:"start_at"`
	EndAt        int64           `db:"end_at" json:"end_at"`
	TagsJSON     json.RawMessage `db:"tags_json" json:"-"`
}

type Livestream struct {
	ID           int64           `json:"id"`
	Owner        User            `json:"owner"`
	Title        string          `json:"title"`
	Description  string          `json:"description"`
	PlaylistUrl  string          `json:"playlist_url"`
	ThumbnailUrl string          `json:"thumbnail_url"`
	Tags         json.RawMessage `json:"tags"`
	StartAt      int64           `json:"start_at"`
	EndAt        int64           `json:"end_at"`
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
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

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

	// 予約枠をみて、予約が可能か調べる
	// NOTE: 並列な予約のoverbooking防止にFOR UPDATEが必要
	var slots []int64
	if err := tx.SelectContext(ctx, &slots, "SELECT slot FROM reservation_slots WHERE start_at BETWEEN ? AND ? AND end_at <= ? FOR UPDATE", req.StartAt, req.EndAt, req.EndAt); err != nil {
		c.Logger().Warnf("予約枠一覧取得でエラー発生: %+v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get reservation_slots: "+err.Error())
	}
	for _, slot := range slots {
		if slot < 1 {
			return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("予約期間 %d ~ %dに対して、予約区間 %d ~ %dが予約できません", termStartAt.Unix(), termEndAt.Unix(), req.StartAt, req.EndAt))
		}
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

	if _, err := tx.ExecContext(ctx, "UPDATE reservation_slots SET slot = slot - 1 WHERE start_at BETWEEN ? AND ? AND end_at <= ?", req.StartAt, req.EndAt, req.EndAt); err != nil {
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
		tags := make([]LivestreamTagModel, len(req.Tags))
		for i, tagID := range req.Tags {
			tags[i] = LivestreamTagModel{LivestreamID: livestreamID, TagID: tagID}
		}
		if _, err := tx.NamedExecContext(ctx, "INSERT INTO livestream_tags (livestream_id, tag_id) VALUES (:livestream_id, :tag_id)", tags); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert livestream tags: "+err.Error())
		}
	}

	tags := []Tag{}
	if err := tx.SelectContext(ctx, &tags, `
		SELECT t.id, t.name FROM livestream_tags lt
		LEFT JOIN tags t ON t.id = lt.tag_id
		WHERE lt.livestream_id = ? ORDER BY lt.id`, livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream tags: "+err.Error())
	}
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to encode livestream tags: "+err.Error())
	}
	if _, err := tx.ExecContext(ctx, "UPDATE livestreams SET tags_json = ? WHERE id = ?", string(tagsJSON), livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save livestream tags: "+err.Error())
	}
	livestreamModel.TagsJSON = tagsJSON

	livestream, err := fillLivestreamResponse(ctx, tx, *livestreamModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestream: "+err.Error())
	}

	if _, err := tx.ExecContext(ctx, "UPDATE stream_catalog_version SET revision = revision + 1 WHERE id = 1"); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to update stream catalog: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusCreated, livestream)
}

const cachedSearchLimit = 100

type streamSearchCacheEntry struct {
	revision uint64
	expires  time.Time
	payload  []byte
}

var streamSearchCache [cachedSearchLimit + 1]atomic.Pointer[streamSearchCacheEntry]

func searchLivestreamsHandler(c echo.Context) error {
	ctx := c.Request().Context()
	var query string
	var cacheKey int
	var args []interface{}
	if tag := c.QueryParam("tag"); tag != "" {
		query += `
			JOIN livestream_tags lt ON lt.livestream_id = l.id
			JOIN tags t ON t.id = lt.tag_id
			WHERE t.name = ? ORDER BY lt.livestream_id DESC`
		args = append(args, tag)
	} else {
		query += " ORDER BY l.id DESC"
		if rawLimit := c.QueryParam("limit"); rawLimit != "" {
			limit, err := strconv.Atoi(rawLimit)
			if err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, "limit query parameter must be integer")
			}
			query += fmt.Sprintf(" LIMIT %d", limit)
			if limit > 0 && limit <= cachedSearchLimit && c.QueryParam("pretty") == "" {
				cacheKey = limit
			}
		}
	}

	var revision uint64
	if cacheKey > 0 {
		if err := dbConn.GetContext(ctx, &revision, "SELECT revision FROM stream_catalog_version WHERE id = 1"); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to load stream catalog: "+err.Error())
		}
		if entry := streamSearchCache[cacheKey].Load(); entry != nil && entry.revision == revision && time.Now().Before(entry.expires) {
			return c.JSONBlob(http.StatusOK, entry.payload)
		}
	}

	streams, err := listLivestreams(ctx, query, args...)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to search livestreams: "+err.Error())
	}
	if cacheKey > 0 {
		payload, err := json.Marshal(streams)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to encode livestreams: "+err.Error())
		}
		expires := responseCacheExpiry(ctx)
		if len(payload) <= 256*1024 && time.Now().Before(expires) {
			streamSearchCache[cacheKey].Store(&streamSearchCacheEntry{revision: revision, expires: expires, payload: payload})
		}
		return c.JSONBlob(http.StatusOK, payload)
	}

	return c.JSON(http.StatusOK, streams)
}

func getMyLivestreamsHandler(c echo.Context) error {
	ctx := c.Request().Context()
	if err := verifyUserSession(c); err != nil {
		return err
	}

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	livestreams, err := listLivestreams(ctx, " WHERE l.user_id = ? ORDER BY l.id", userID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestreams: "+err.Error())
	}

	return c.JSON(http.StatusOK, livestreams)
}

func getUserLivestreamsHandler(c echo.Context) error {
	ctx := c.Request().Context()
	if err := verifyUserSession(c); err != nil {
		return err
	}

	username := c.Param("username")

	livestreams, err := listLivestreams(ctx, " WHERE u.name = ? ORDER BY l.id", username)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestreams: "+err.Error())
	}
	if len(livestreams) == 0 {
		var exists bool
		if err := dbConn.GetContext(ctx, &exists, "SELECT EXISTS (SELECT 1 FROM users WHERE name = ?)", username); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
		}
		if !exists {
			return echo.NewHTTPError(http.StatusNotFound, "user not found")
		}
	}

	return c.JSON(http.StatusOK, livestreams)
}

// viewerテーブルの廃止
func enterLivestreamHandler(c echo.Context) error {
	ctx := c.Request().Context()
	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id must be integer")
	}

	viewer := LivestreamViewerModel{
		UserID:       int64(userID),
		LivestreamID: int64(livestreamID),
		CreatedAt:    time.Now().Unix(),
	}

	if _, err := dbConn.NamedExecContext(ctx, "INSERT INTO livestream_viewers_history (user_id, livestream_id, created_at) VALUES(:user_id, :livestream_id, :created_at)", viewer); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert livestream_view_history: "+err.Error())
	}

	return c.NoContent(http.StatusOK)
}

func exitLivestreamHandler(c echo.Context) error {
	ctx := c.Request().Context()
	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	// error already checked
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already checked
	userID := sess.Values[defaultUserIDKey].(int64)

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	if _, err := dbConn.ExecContext(ctx, "DELETE FROM livestream_viewers_history WHERE user_id = ? AND livestream_id = ?", userID, livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete livestream_view_history: "+err.Error())
	}

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

	livestream, err := loadLivestream(ctx, dbConn, int64(livestreamID))
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found livestream that has the given id")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream: "+err.Error())
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

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	var livestreamModel LivestreamModel
	if err := tx.GetContext(ctx, &livestreamModel, "SELECT * FROM livestreams WHERE id = ?", livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream: "+err.Error())
	}

	// error already check
	sess, _ := session.Get(defaultSessionIDKey, c)
	// existence already check
	userID := sess.Values[defaultUserIDKey].(int64)

	if livestreamModel.UserID != userID {
		return echo.NewHTTPError(http.StatusForbidden, "can't get other streamer's livecomment reports")
	}

	var rows []struct {
		LivecommentReportModel
		AuthorID         int64  `db:"author_id"`
		Comment          string `db:"comment"`
		Tip              int64  `db:"tip"`
		CommentCreatedAt int64  `db:"comment_created_at"`
	}
	if err := tx.SelectContext(ctx, &rows, `
		SELECT r.*, COALESCE(c.user_id, m.user_id) AS author_id,
		       COALESCE(c.comment, m.comment) AS comment, COALESCE(c.tip, m.tip) AS tip,
		       COALESCE(c.created_at, m.created_at) AS comment_created_at
		FROM livecomment_reports r
		LEFT JOIN livecomments c ON c.id = r.livecomment_id AND c.livestream_id = r.livestream_id
		LEFT JOIN moderated_livecomments m ON m.id = r.livecomment_id AND m.livestream_id = r.livestream_id
		WHERE r.livestream_id = ? ORDER BY r.id`, livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livecomment reports: "+err.Error())
	}

	reports := make([]LivecommentReport, len(rows))
	if len(rows) > 0 {
		userIDs := make([]int64, 0, 1+2*len(rows))
		userIDs = append(userIDs, userID)
		for _, row := range rows {
			userIDs = append(userIDs, row.UserID, row.AuthorID)
		}
		users, err := loadUsers(ctx, tx, userIDs)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to load report profiles: "+err.Error())
		}
		owner, ok := users[userID]
		if !ok {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to find livestream owner")
		}
		stream := livestreamResponse(livestreamModel, owner)
		for i, row := range rows {
			reporter, reporterOK := users[row.UserID]
			author, authorOK := users[row.AuthorID]
			if !reporterOK || !authorOK {
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to find report profiles")
			}
			comment := livecommentResponse(LivecommentModel{
				ID: row.LivecommentID, UserID: row.AuthorID, LivestreamID: row.LivestreamID,
				Comment: row.Comment, Tip: row.Tip, CreatedAt: row.CommentCreatedAt,
			}, author, stream)
			reports[i] = livecommentReportResponse(row.LivecommentReportModel, reporter, comment)
		}
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, reports)
}

func fillLivestreamResponse(ctx context.Context, tx *sqlx.Tx, livestreamModel LivestreamModel) (Livestream, error) {
	owner, err := loadUser(ctx, tx, livestreamModel.UserID)
	if err != nil {
		return Livestream{}, err
	}

	return livestreamResponse(livestreamModel, owner), nil
}

func livestreamResponse(livestreamModel LivestreamModel, owner User) Livestream {
	tags := livestreamModel.TagsJSON
	if len(tags) == 0 {
		tags = json.RawMessage("[]")
	}
	return Livestream{
		ID:           livestreamModel.ID,
		Owner:        owner,
		Title:        livestreamModel.Title,
		Tags:         tags,
		Description:  livestreamModel.Description,
		PlaylistUrl:  livestreamModel.PlaylistUrl,
		ThumbnailUrl: livestreamModel.ThumbnailUrl,
		StartAt:      livestreamModel.StartAt,
		EndAt:        livestreamModel.EndAt,
	}
}
