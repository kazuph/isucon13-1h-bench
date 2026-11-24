package main

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
)

type LivestreamStatistics struct {
	Rank           int64 `json:"rank"`
	ViewersCount   int64 `json:"viewers_count"`
	TotalReactions int64 `json:"total_reactions"`
	TotalReports   int64 `json:"total_reports"`
	MaxTip         int64 `json:"max_tip"`
}

type UserStatistics struct {
	Rank              int64  `json:"rank"`
	ViewersCount      int64  `json:"viewers_count" db:"viewers_count"`
	TotalReactions    int64  `json:"total_reactions" db:"total_reactions"`
	TotalLivecomments int64  `json:"total_livecomments" db:"total_livecomments"`
	TotalTip          int64  `json:"total_tip" db:"total_tip"`
	FavoriteEmoji     string `json:"favorite_emoji" db:"favorite_emoji"`
}

func getUserStatisticsHandler(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	ctx := c.Request().Context()
	tx, err := dbConn.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	var row struct {
		UserStatistics
		Username string
	}
	err = tx.GetContext(ctx, &row, `
		SELECT u.name AS username,
		       COUNT(c.id) AS total_livecomments, COALESCE(SUM(c.tip), 0) AS total_tip,
		       (SELECT COUNT(*) FROM livestreams l
		        JOIN reactions r ON r.livestream_id = l.id
		        WHERE l.user_id = u.id) AS total_reactions,
		       (SELECT COUNT(*) FROM livestreams l
		        JOIN livestream_viewers_history h ON h.livestream_id = l.id
		        WHERE l.user_id = u.id) AS viewers_count,
		       COALESCE((SELECT r.emoji_name FROM livestreams l
		                 JOIN reactions r ON r.livestream_id = l.id
		                 WHERE l.user_id = u.id
		                 GROUP BY r.emoji_name
		                 ORDER BY COUNT(*) DESC, r.emoji_name DESC LIMIT 1), '') AS favorite_emoji
		FROM users u LEFT JOIN livestreams l ON l.user_id = u.id
		LEFT JOIN livecomments c ON c.livestream_id = l.id
		WHERE u.name = ? GROUP BY u.id, u.name`, c.Param("username"))
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusBadRequest, "not found user that has the given username")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user statistics: "+err.Error())
	}

	if err := tx.GetContext(ctx, &row.Rank, `
		SELECT COUNT(*) + 1
		FROM users u
		LEFT JOIN (
			SELECT l.user_id, COUNT(*) AS total FROM reactions r
			JOIN livestreams l ON l.id = r.livestream_id GROUP BY l.user_id
		) r ON r.user_id = u.id
		LEFT JOIN (
			SELECT l.user_id, SUM(c.tip) AS total FROM livecomments c
			JOIN livestreams l ON l.id = c.livestream_id GROUP BY l.user_id
		) t ON t.user_id = u.id
		WHERE (COALESCE(r.total, 0) + COALESCE(t.total, 0), BINARY u.name) > (?, BINARY ?)`,
		row.TotalReactions+row.TotalTip, row.Username); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to calculate user ranking: "+err.Error())
	}

	return c.JSON(http.StatusOK, row.UserStatistics)
}

func getLivestreamStatisticsHandler(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	livestreamID, err := strconv.ParseInt(c.Param("livestream_id"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}
	ctx := c.Request().Context()
	tx, err := dbConn.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	var row struct {
		LivestreamStatistics
		TipTotal int64 `db:"tip_total"`
	}
	err = tx.GetContext(ctx, &row, `
		SELECT COALESCE(MAX(c.tip), 0) AS maxtip, COALESCE(SUM(c.tip), 0) AS tip_total,
		       (SELECT COUNT(*) FROM reactions r WHERE r.livestream_id = l.id) AS totalreactions,
		       (SELECT COUNT(*) FROM livestream_viewers_history h WHERE h.livestream_id = l.id) AS viewerscount,
		       (SELECT COUNT(*) FROM livecomment_reports r WHERE r.livestream_id = l.id) AS totalreports
		FROM livestreams l LEFT JOIN livecomments c ON c.livestream_id = l.id
		WHERE l.id = ? GROUP BY l.id`, livestreamID)
	if errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusBadRequest, "cannot get stats of not found livestream")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream statistics: "+err.Error())
	}

	if err := tx.GetContext(ctx, &row.Rank, `
		SELECT COUNT(*) + 1
		FROM livestreams l
		LEFT JOIN (
			SELECT livestream_id, COUNT(*) AS total FROM reactions GROUP BY livestream_id
		) r ON r.livestream_id = l.id
		LEFT JOIN (
			SELECT livestream_id, SUM(tip) AS total FROM livecomments GROUP BY livestream_id
		) t ON t.livestream_id = l.id
		WHERE (COALESCE(r.total, 0) + COALESCE(t.total, 0), l.id) > (?, ?)`,
		row.TotalReactions+row.TipTotal, livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to calculate livestream ranking: "+err.Error())
	}

	return c.JSON(http.StatusOK, row.LivestreamStatistics)
}
