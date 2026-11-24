package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

var moderationLocks [256]sync.RWMutex

type PostLivecommentRequest struct {
	Comment string `json:"comment"`
	Tip     int64  `json:"tip"`
}

type LivecommentModel struct {
	Moderated    bool   `db:"moderated"`
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
	searchText   string
	ID           int64  `json:"id" db:"id"`
	UserID       int64  `json:"user_id" db:"user_id"`
	LivestreamID int64  `json:"livestream_id" db:"livestream_id"`
	Word         string `json:"word" db:"word"`
	CreatedAt    int64  `json:"created_at" db:"created_at"`
}

func getLivecommentsHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	livecommentModels, err := comments.page(ctx, int64(livestreamID), c.QueryParam("limit"))
	if err != nil {
		return err
	}

	livecomments := make([]Livecomment, len(livecommentModels))
	for i := range livecommentModels {
		livecomment, err := fillLivecommentResponse(livecommentModels[i])
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to fil livecomments: "+err.Error())
		}

		livecomments[i] = livecomment
	}

	return c.JSON(http.StatusOK, livecomments)
}

func getNgwords(c echo.Context) error {

	if err := verifyUserSession(c); err != nil {
		return err
	}

	// error already checked
	userID := c.Get(defaultUserIDKey).(int64)

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	return c.JSON(http.StatusOK, wordsFor(int64(livestreamID), userID))
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

	// error already checked
	userID := c.Get(defaultUserIDKey).(int64)

	var req *PostLivecommentRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	lock := &moderationLocks[uint(livestreamID)%uint(len(moderationLocks))]
	lock.RLock()
	defer lock.RUnlock()
	stream, err := catalogStream(int64(livestreamID))
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "livestream not found")
	}
	// スパム判定
	hitSpam := false
	sqlPatterns := false
	rules := wordsFor(stream.ID, stream.Owner.ID)
	var searchText string
	if len(rules) > 0 {
		searchText = foldModerationText(req.Comment)
	}
	for _, w := range rules {
		if strings.ContainsAny(w.Word, "%_\\") {
			sqlPatterns = true
			continue
		}
		if strings.Contains(searchText, w.searchText) {
			hitSpam = true
			break
		}
	}
	if sqlPatterns && !hitSpam {
		if err := dbConn.GetContext(ctx, &hitSpam, "SELECT EXISTS(SELECT 1 FROM ng_words WHERE user_id=? AND livestream_id=? AND CONVERT(? USING utf8mb4) COLLATE utf8mb4_general_ci LIKE CONCAT('%',word,'%') COLLATE utf8mb4_general_ci)", stream.Owner.ID, stream.ID, req.Comment); err != nil {
			return err
		}
	}
	if hitSpam {
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

	livecomment, err := fillLivecommentResponse(livecommentModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livecomment: "+err.Error())
	}

	livecommentID, err := commentInserts.insert(ctx, userID, livestreamID, req.Comment, req.Tip, now)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert livecomment: "+err.Error())
	}
	livecommentModel.ID = livecommentID

	livecomment.ID = livecommentID
	noteComment(livecommentModel)
	comments.push(int64(livestreamID), livecommentModel)
	return c.JSON(http.StatusCreated, livecomment)
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

	// error already checked
	userID := c.Get(defaultUserIDKey).(int64)

	if _, err := catalogStream(int64(livestreamID)); err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "livestream not found")
	}
	var model LivecommentModel
	if err := dbConn.GetContext(ctx, &model, "SELECT * FROM livecomments WHERE id=? AND livestream_id=?", livecommentID, livestreamID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "livecomment not found")
		}
		return err
	}
	reporter, err := catalogUser(userID)
	if err != nil {
		return err
	}
	comment, err := fillLivecommentResponse(model)
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	reportModel := LivecommentReportModel{
		UserID:        int64(userID),
		LivestreamID:  int64(livestreamID),
		LivecommentID: int64(livecommentID),
		CreatedAt:     now,
	}
	rs, err := dbConn.NamedExecContext(ctx, "INSERT INTO livecomment_reports(user_id, livestream_id, livecomment_id, created_at) VALUES (:user_id, :livestream_id, :livecomment_id, :created_at)", &reportModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert livecomment report: "+err.Error())
	}
	reportID, err := rs.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted livecomment report id: "+err.Error())
	}
	reportModel.ID = reportID

	report := LivecommentReport{ID: reportID, Reporter: reporter, Livecomment: comment, CreatedAt: now}
	metrics.Lock()
	metricFor(metrics.streams, int64(livestreamID)).reports++
	metrics.Unlock()
	return c.JSON(http.StatusCreated, report)
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

	// error already checked
	userID := c.Get(defaultUserIDKey).(int64)

	var req *ModerateRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	lock := &moderationLocks[uint(livestreamID)%uint(len(moderationLocks))]
	lock.Lock()
	defer lock.Unlock()
	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	// 配信者自身の配信に対するmoderateなのかを検証
	stream, streamErr := catalogStream(int64(livestreamID))
	if streamErr != nil || stream.Owner.ID != userID {
		return echo.NewHTTPError(http.StatusBadRequest, "A streamer can't moderate livestreams that other streamers own")
	}

	word := NGWord{
		UserID:       int64(userID),
		LivestreamID: int64(livestreamID),
		Word:         req.NGWord,
		CreatedAt:    time.Now().Unix(),
	}
	rs, err := tx.NamedExecContext(ctx, "INSERT INTO ng_words(user_id, livestream_id, word, created_at) VALUES (:user_id, :livestream_id, :word, :created_at)", &word)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert new NG word: "+err.Error())
	}

	wordID, err := rs.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted NG word id: "+err.Error())
	}

	updated, err := tx.ExecContext(ctx, "UPDATE livecomments SET moderated=TRUE WHERE moderated=FALSE AND livestream_id = ? AND comment COLLATE utf8mb4_general_ci LIKE CONCAT('%', ?, '%') COLLATE utf8mb4_general_ci", livestreamID, req.NGWord)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to remove moderated comments: "+err.Error())
	}
	n, err := updated.RowsAffected()
	if err != nil {
		return err
	}
	var totals commentTotals
	if n > 0 {
		if err := tx.GetContext(ctx, &totals, "SELECT COUNT(*) n,COALESCE(SUM(tip),0) tips,COALESCE(MAX(tip),0) max_tip FROM livecomments WHERE moderated=FALSE AND livestream_id=?", livestreamID); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	if n > 0 {
		metrics.Lock()
		metricFor(metrics.streams, int64(livestreamID)).comments = totals
		metrics.Unlock()
	}
	word.ID = wordID
	publishWord(word)
	comments.invalidate(int64(livestreamID))
	return c.JSON(http.StatusCreated, map[string]interface{}{
		"word_id": wordID,
	})
}

func fillLivecommentResponse(livecommentModel LivecommentModel) (Livecomment, error) {
	commentOwner, err := catalogUser(livecommentModel.UserID)
	if err != nil {
		return Livecomment{}, err
	}
	livestream, err := catalogStream(livecommentModel.LivestreamID)
	if err != nil {
		return Livecomment{}, err
	}

	livecomment := Livecomment{
		ID:         livecommentModel.ID,
		User:       commentOwner,
		Livestream: livestream,
		Comment:    livecommentModel.Comment,
		Tip:        livecommentModel.Tip,
		CreatedAt:  livecommentModel.CreatedAt,
	}

	return livecomment, nil
}
