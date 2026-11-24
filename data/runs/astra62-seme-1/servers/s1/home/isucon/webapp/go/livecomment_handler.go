package main

import (
	"context"
	"database/sql"
	"encoding/json"

	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

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
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := strconv.ParseInt(c.Param("livestream_id"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(400)
	}

	activityMu.RLock()
	var models []LivecommentModel
	if a := activities[id]; a != nil {
		n, err := pageSize(c, len(a.comments))
		if err != nil {
			activityMu.RUnlock()
			return err
		}
		models = append(models, a.comments[len(a.comments)-n:]...)
	}
	activityMu.RUnlock()
	return timelineJSON(c, models, appendComment)
}

func getNgwords(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := strconv.ParseInt(c.Param("livestream_id"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(400)
	}
	uid := c.Get(defaultUserIDKey).(int64)
	return c.JSON(200, newestWords(id, uid))
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

	unlock := lockStream(int64(livestreamID))
	defer unlock()
	stream, ok := knownStream(int64(livestreamID))
	if !ok {
		return echo.NewHTTPError(404)
	}
	activityMu.RLock()
	spam := false
	if a := activities[int64(livestreamID)]; a != nil {
		for _, w := range a.words {
			if w.UserID == stream.Owner.ID && strings.Contains(req.Comment, w.Word) {
				spam = true
				break
			}
		}
	}
	activityMu.RUnlock()
	if spam {
		return echo.NewHTTPError(400, "このコメントがスパム判定されました")
	}
	now := time.Now().Unix()
	livecommentModel := LivecommentModel{
		UserID:       userID,
		LivestreamID: int64(livestreamID),
		Comment:      req.Comment,
		Tip:          req.Tip,
		CreatedAt:    now,
	}

	livecommentModel.ID = sequence.Add(1)
	if err := (event{Kind: "comment", ID: livecommentModel.ID, Stream: int64(livestreamID), User: userID, At: livecommentModel.CreatedAt, Text: req.Comment, Tip: req.Tip}).persist(); err != nil {
		return err
	}

	livecomment, err := fillLivecommentResponse(ctx, nil, livecommentModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livecomment: "+err.Error())
	}

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

	if _, ok := knownStream(int64(livestreamID)); !ok {
		return echo.NewHTTPError(404)
	}
	activityMu.RLock()
	m, ok := commentByID[int64(livecommentID)]
	activityMu.RUnlock()
	if !ok || m.LivestreamID != int64(livestreamID) {
		return echo.NewHTTPError(404)
	}
	now := time.Now().Unix()
	reportModel := LivecommentReportModel{
		UserID:        int64(userID),
		LivestreamID:  int64(livestreamID),
		LivecommentID: int64(livecommentID),
		CreatedAt:     now,
	}
	reportModel.ID = sequence.Add(1)
	if err := (event{Kind: "report", ID: reportModel.ID, Stream: int64(livestreamID), User: userID, Ref: int64(livecommentID), At: now}).persist(); err != nil {
		return err
	}
	report, err := fillLivecommentReportResponse(ctx, nil, reportModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livecomment report: "+err.Error())
	}
	return c.JSON(http.StatusCreated, report)
}

// NGワードを登録
func moderateHandler(c echo.Context) error {

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

	unlock := lockStream(int64(livestreamID))
	defer unlock()
	stream, ok := knownStream(int64(livestreamID))
	if !ok || stream.Owner.ID != userID {
		return echo.NewHTTPError(400)
	}
	wordID := sequence.Add(1)
	if err := (event{Kind: "moderate", ID: wordID, Stream: int64(livestreamID), User: userID, Text: req.NGWord, At: time.Now().Unix()}).persist(); err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, map[string]interface{}{
		"word_id": wordID,
	})
}

func fillLivecommentResponse(ctx context.Context, tx *sqlx.Tx, livecommentModel LivecommentModel) (Livecomment, error) {
	commentOwner, ok := knownUser(livecommentModel.UserID)
	if !ok {
		return Livecomment{}, sql.ErrNoRows
	}
	livestream, ok := knownStream(livecommentModel.LivestreamID)
	if !ok {
		return Livecomment{}, sql.ErrNoRows
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

func fillLivecommentReportResponse(ctx context.Context, tx *sqlx.Tx, reportModel LivecommentReportModel) (LivecommentReport, error) {
	reporter, ok := knownUser(reportModel.UserID)
	if !ok {
		return LivecommentReport{}, sql.ErrNoRows
	}
	activityMu.RLock()
	livecommentModel, ok := commentByID[reportModel.LivecommentID]
	activityMu.RUnlock()
	if !ok {
		return LivecommentReport{}, sql.ErrNoRows
	}
	livecomment, err := fillLivecommentResponse(ctx, tx, livecommentModel)
	if err != nil {
		return LivecommentReport{}, err
	}

	report := LivecommentReport{
		ID:          reportModel.ID,
		Reporter:    reporter,
		Livecomment: livecomment,
		CreatedAt:   reportModel.CreatedAt,
	}
	return report, nil
}
