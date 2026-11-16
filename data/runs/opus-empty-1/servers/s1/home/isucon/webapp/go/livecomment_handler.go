package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	query := "SELECT * FROM livecomments WHERE livestream_id = ? ORDER BY created_at DESC"
	if c.QueryParam("limit") != "" {
		limit, err := strconv.Atoi(c.QueryParam("limit"))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "limit query parameter must be integer")
		}
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	livecommentModels := []LivecommentModel{}
	err = dbConn.SelectContext(ctx, &livecommentModels, query, livestreamID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livecomments: "+err.Error())
	}

	livecomments := make([]Livecomment, len(livecommentModels))
	if len(livecommentModels) > 0 {
		ls, err := buildLivestreamByID(ctx, int64(livestreamID))
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to fil livecomments: "+err.Error())
		}
		for i := range livecommentModels {
			lc := livecommentModels[i]
			u, err := buildUserByID(ctx, lc.UserID)
			if err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to fil livecomments: "+err.Error())
			}
			livecomments[i] = Livecomment{
				ID:         lc.ID,
				User:       u,
				Livestream: ls,
				Comment:    lc.Comment,
				Tip:        lc.Tip,
				CreatedAt:  lc.CreatedAt,
			}
		}
	}

	return c.JSON(http.StatusOK, livecomments)
}

func getNgwords(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		return err
	}

	userID := sessionUserID(c)

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	ngWords := []*NGWord{}
	if err := dbConn.SelectContext(ctx, &ngWords, "SELECT * FROM ng_words WHERE user_id = ? AND livestream_id = ? ORDER BY created_at DESC", userID, livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get NG words: "+err.Error())
	}

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

	userID := sessionUserID(c)

	var req *PostLivecommentRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	livestreamModel, err := getLivestreamByID(ctx, int64(livestreamID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "livestream not found")
		} else {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream: "+err.Error())
		}
	}

	// スパム判定
	if hitsNG(livestreamModel.UserID, livestreamModel.ID, req.Comment) {
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

	rs, err := dbConn.ExecContext(ctx, "INSERT INTO livecomments (user_id, livestream_id, comment, tip, created_at) VALUES (?, ?, ?, ?, ?)", userID, livestreamID, req.Comment, req.Tip, now)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert livecomment: "+err.Error())
	}

	livecommentID, err := rs.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted livecomment id: "+err.Error())
	}
	livecommentModel.ID = livecommentID

	livecomment, err := buildLivecomment(ctx, livecommentModel)
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

	userID := sessionUserID(c)

	if _, err := getLivestreamByID(ctx, int64(livestreamID)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "livestream not found")
		} else {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream: "+err.Error())
		}
	}

	var livecommentModel LivecommentModel
	if err := dbConn.GetContext(ctx, &livecommentModel, "SELECT * FROM livecomments WHERE id = ?", livecommentID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusNotFound, "livecomment not found")
		} else {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livecomment: "+err.Error())
		}
	}

	now := time.Now().Unix()
	reportModel := LivecommentReportModel{
		UserID:        int64(userID),
		LivestreamID:  int64(livestreamID),
		LivecommentID: int64(livecommentID),
		CreatedAt:     now,
	}
	rs, err := dbConn.ExecContext(ctx, "INSERT INTO livecomment_reports(user_id, livestream_id, livecomment_id, created_at) VALUES (?, ?, ?, ?)", userID, livestreamID, livecommentID, now)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert livecomment report: "+err.Error())
	}
	reportID, err := rs.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted livecomment report id: "+err.Error())
	}
	reportModel.ID = reportID

	reporter, err := buildUserByID(ctx, userID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livecomment report: "+err.Error())
	}
	lc, err := buildLivecomment(ctx, livecommentModel)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livecomment report: "+err.Error())
	}

	return c.JSON(http.StatusCreated, LivecommentReport{
		ID:          reportModel.ID,
		Reporter:    reporter,
		Livecomment: lc,
		CreatedAt:   reportModel.CreatedAt,
	})
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

	userID := sessionUserID(c)

	var req *ModerateRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	// 配信者自身の配信に対するmoderateなのかを検証
	ls, err := getLivestreamByID(ctx, int64(livestreamID))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestreams: "+err.Error())
	}
	if err != nil || ls.UserID != userID {
		return echo.NewHTTPError(http.StatusBadRequest, "A streamer can't moderate livestreams that other streamers own")
	}

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	rs, err := tx.ExecContext(ctx, "INSERT INTO ng_words(user_id, livestream_id, word, created_at) VALUES (?, ?, ?, ?)", userID, livestreamID, req.NGWord, time.Now().Unix())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to insert new NG word: "+err.Error())
	}

	wordID, err := rs.LastInsertId()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get last inserted NG word id: "+err.Error())
	}

	cacheAddNG(ngEntry{UserID: userID, LsID: int64(livestreamID), Word: req.NGWord})

	var ngwords []string
	if err := tx.SelectContext(ctx, &ngwords, "SELECT word FROM ng_words WHERE livestream_id = ?", livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get NG words: "+err.Error())
	}

	type lcRow struct {
		ID      int64  `db:"id"`
		Comment string `db:"comment"`
	}
	var lcs []lcRow
	if err := tx.SelectContext(ctx, &lcs, "SELECT id, comment FROM livecomments WHERE livestream_id = ?", livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livecomments: "+err.Error())
	}
	var delIDs []int64
	for _, lc := range lcs {
		for _, w := range ngwords {
			if strings.Contains(lc.Comment, w) {
				delIDs = append(delIDs, lc.ID)
				break
			}
		}
	}
	if len(delIDs) > 0 {
		q, args, err := sqlx.In("DELETE FROM livecomments WHERE id IN (?)", delIDs)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to build delete query: "+err.Error())
		}
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete old livecomments that hit spams: "+err.Error())
		}
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusCreated, map[string]interface{}{
		"word_id": wordID,
	})
}

func buildLivecomment(ctx context.Context, livecommentModel LivecommentModel) (Livecomment, error) {
	commentOwner, err := buildUserByID(ctx, livecommentModel.UserID)
	if err != nil {
		return Livecomment{}, err
	}
	livestream, err := buildLivestreamByID(ctx, livecommentModel.LivestreamID)
	if err != nil {
		return Livecomment{}, err
	}
	return Livecomment{
		ID:         livecommentModel.ID,
		User:       commentOwner,
		Livestream: livestream,
		Comment:    livecommentModel.Comment,
		Tip:        livecommentModel.Tip,
		CreatedAt:  livecommentModel.CreatedAt,
	}, nil
}
