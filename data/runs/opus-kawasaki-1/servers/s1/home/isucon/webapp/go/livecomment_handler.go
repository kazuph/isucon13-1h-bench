package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

type PostLivecommentRequest struct {
	Comment string `json:"comment"`
	Tip     int64  `json:"tip"`
}

type LivecommentModel struct {
	ID           int64  `db:"id"`
	UserID       int64  `db:"user_id"`
	LivestreamID int64  `db:"livestream_id"`
	Comment      string `db:"comment"`
	Tip          int64  `db:"tip"`
	CreatedAt    int64  `db:"created_at"`
}

type Livecomment struct {
	ID         int64      `json:"id"`
	User       User       `json:"user"`
	Livestream Livestream `json:"livestream"`
	Comment    string     `json:"comment"`
	Tip        int64      `json:"tip"`
	CreatedAt  int64      `json:"created_at"`
}

type LivecommentReport struct {
	ID          int64       `json:"id"`
	Reporter    User        `json:"reporter"`
	Livecomment Livecomment `json:"livecomment"`
	CreatedAt   int64       `json:"created_at"`
}

type LivecommentReportModel struct {
	ID            int64 `db:"id"`
	UserID        int64 `db:"user_id"`
	LivestreamID  int64 `db:"livestream_id"`
	LivecommentID int64 `db:"livecomment_id"`
	CreatedAt     int64 `db:"created_at"`
}

type ModerateRequest struct {
	NGWord string `json:"ng_word"`
}

type NGWord struct {
	ID           int64  `json:"id" db:"id"`
	UserID       int64  `json:"user_id" db:"user_id"`
	LivestreamID int64  `json:"livestream_id" db:"livestream_id"`
	Word         string `json:"word" db:"word"`
	CreatedAt    int64  `json:"created_at" db:"created_at"`
}

func getLivecommentsHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	limit := -1
	if c.QueryParam("limit") != "" {
		limit, err = strconv.Atoi(c.QueryParam("limit"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "limit query parameter must be integer")
		}
	}

	livecommentModels := storeGetComments(int64(livestreamID), limit)

	livecomments, err := fillLivecomments(ctx, dbConn, livecommentModels)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fil livecomments: "+err.Error())
	}

	return c.JSON(http.StatusOK, livecomments)
}

func getNgwords(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}

	sess, _ := session.Get(defaultSessionIDKey, c)
	userID := sess.Values[defaultUserIDKey].(int64)

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	ngWords := storeGetNGWords(int64(livestreamID), userID)

	return c.JSON(http.StatusOK, ngWords)
}

func postLivecommentHandler(c echo.Context) error {
	ctx := c.Request().Context()
	defer c.Request().Body.Close()

	if err := verifyUserSession(c); err != nil {
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	sess, _ := session.Get(defaultSessionIDKey, c)
	userID := sess.Values[defaultUserIDKey].(int64)

	var req *PostLivecommentRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	livestreamModel, err := getLivestreamByID(ctx, dbConn, int64(livestreamID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "livestream not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream: "+err.Error())
	}

	// スパム判定
	var hitSpam int
	hit, certain := storeSpamCheck(livestreamModel.ID, livestreamModel.UserID, req.Comment)
	if hit {
		hitSpam = 1
	} else if !certain {
		if err := dbConn.GetContext(ctx, &hitSpam, "SELECT COUNT(*) FROM ng_words WHERE user_id = ? AND livestream_id = ? AND (? COLLATE utf8mb4_general_ci) LIKE CONCAT('%', word, '%')", livestreamModel.UserID, livestreamModel.ID, req.Comment); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get hitspam: "+err.Error())
		}
	}
	if hitSpam >= 1 {
		return echo.NewHTTPError(http.StatusBadRequest, "このコメントがスパム判定されました")
	}

	now := time.Now().Unix()
	livecommentModel := LivecommentModel{
		UserID:       userID,
		LivestreamID: int64(livestreamID),
		Comment:      req.Comment,
		Tip:          req.Tip,
		CreatedAt:    now,
	}

	livecommentModel.ID = livecommentID.Add(1)
	enqueueWrite(&writeOp{kind: opLivecomment, lc: livecommentModel})
	storeAddComment(livecommentModel)

	lcs, err := fillLivecomments(ctx, dbConn, []LivecommentModel{livecommentModel})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livecomment: "+err.Error())
	}

	return c.JSON(http.StatusCreated, lcs[0])
}

func reportLivecommentHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	livecommentID, err := strconv.Atoi(c.Param("livecomment_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livecomment_id in path must be integer")
	}

	sess, _ := session.Get(defaultSessionIDKey, c)
	userID := sess.Values[defaultUserIDKey].(int64)

	if _, err := getLivestreamByID(ctx, dbConn, int64(livestreamID)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "livestream not found")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream: "+err.Error())
	}

	livecommentModel, ok := storeGetCommentByID(int64(livecommentID))
	if !ok {
		return echo.NewHTTPError(http.StatusNotFound, "livecomment not found")
	}

	now := time.Now().Unix()
	reportModel := LivecommentReportModel{
		UserID:        int64(userID),
		LivestreamID:  int64(livestreamID),
		LivecommentID: int64(livecommentID),
		CreatedAt:     now,
	}
	reportModel.ID = reportID.Add(1)
	enqueueWrite(&writeOp{kind: opReport, rep: reportModel})
	storeAddReport(&reportModel)

	reports, err := fillLivecommentReportsWithComments(ctx, dbConn, []*LivecommentReportModel{&reportModel}, map[int64]LivecommentModel{livecommentModel.ID: livecommentModel})
	if err != nil || len(reports) == 0 {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livecomment report")
	}

	return c.JSON(http.StatusCreated, reports[0])
}

// NGワードを登録
func moderateHandler(c echo.Context) error {
	ctx := c.Request().Context()
	defer c.Request().Body.Close()

	if err := verifyUserSession(c); err != nil {
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	sess, _ := session.Get(defaultSessionIDKey, c)
	userID := sess.Values[defaultUserIDKey].(int64)

	var req *ModerateRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	ls, err := getLivestreamByID(ctx, dbConn, int64(livestreamID))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestreams: "+err.Error())
	}
	if ls == nil || ls.UserID != userID {
		return echo.NewHTTPError(http.StatusBadRequest, "A streamer can't moderate livestreams that other streamers own")
	}

	now := time.Now().Unix()
	rs, err := dbConn.ExecContext(ctx, "INSERT INTO ng_words(user_id, livestream_id, word, created_at) VALUES (?, ?, ?, ?)", userID, livestreamID, req.NGWord, now)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert new NG word: "+err.Error())
	}

	wordID, err := rs.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted NG word id: "+err.Error())
	}
	storeAddNGWord(&NGWord{ID: wordID, UserID: userID, LivestreamID: int64(livestreamID), Word: req.NGWord, CreatedAt: now})

	// NGワードにヒットする過去の投稿も全削除する
	flushWrites()
	var delIDs []int64
	if err := dbConn.SelectContext(ctx, &delIDs, `
		SELECT DISTINCT lc.id FROM livecomments lc
		INNER JOIN ng_words w ON w.livestream_id = lc.livestream_id
		WHERE lc.livestream_id = ? AND (lc.comment COLLATE utf8mb4_general_ci) LIKE CONCAT('%', w.word, '%')`, livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to find old livecomments that hit spams: "+err.Error())
	}
	if len(delIDs) > 0 {
		q, args, err := sqlx.In("DELETE FROM livecomments WHERE id IN (?)", delIDs)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to build delete: "+err.Error())
		}
		if _, err := dbConn.ExecContext(ctx, q, args...); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete old livecomments that hit spams: "+err.Error())
		}
		storeRemoveComments(int64(livestreamID), delIDs)
	}

	return c.JSON(http.StatusCreated, map[string]interface{}{
		"word_id": wordID,
	})
}

func fillLivecommentReports(ctx context.Context, db dbx, reportModels []*LivecommentReportModel) ([]LivecommentReport, error) {
	if len(reportModels) == 0 {
		return []LivecommentReport{}, nil
	}
	ids := make([]int64, 0, len(reportModels))
	for _, r := range reportModels {
		ids = append(ids, r.LivecommentID)
	}
	q, args, err := sqlx.In("SELECT * FROM livecomments WHERE id IN (?)", uniqIDs(ids))
	if err != nil {
		return nil, err
	}
	var lcs []LivecommentModel
	if err := db.SelectContext(ctx, &lcs, q, args...); err != nil {
		return nil, err
	}
	m := make(map[int64]LivecommentModel, len(lcs))
	for _, lc := range lcs {
		m[lc.ID] = lc
	}
	return fillLivecommentReportsWithComments(ctx, db, reportModels, m)
}

func fillLivecommentReportsWithComments(ctx context.Context, db dbx, reportModels []*LivecommentReportModel, lcMap map[int64]LivecommentModel) ([]LivecommentReport, error) {
	lcModels := make([]LivecommentModel, 0, len(lcMap))
	for _, lc := range lcMap {
		lcModels = append(lcModels, lc)
	}
	lcs, err := fillLivecomments(ctx, db, lcModels)
	if err != nil {
		return nil, err
	}
	lcResp := make(map[int64]Livecomment, len(lcs))
	for _, lc := range lcs {
		lcResp[lc.ID] = lc
	}
	reporterIDs := make([]int64, 0, len(reportModels))
	for _, r := range reportModels {
		reporterIDs = append(reporterIDs, r.UserID)
	}
	users, err := fillUsers(ctx, db, reporterIDs)
	if err != nil {
		return nil, err
	}
	res := make([]LivecommentReport, 0, len(reportModels))
	for _, r := range reportModels {
		lc, ok := lcResp[r.LivecommentID]
		if !ok {
			continue
		}
		res = append(res, LivecommentReport{
			ID:          r.ID,
			Reporter:    users[r.UserID],
			Livecomment: lc,
			CreatedAt:   r.CreatedAt,
		})
	}
	return res, nil
}
