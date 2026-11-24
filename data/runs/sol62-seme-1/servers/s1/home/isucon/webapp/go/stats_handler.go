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
	TotalReactions int64 `json:"total_reactions" db:"total_reactions"`
	TotalReports   int64 `json:"total_reports"`
	MaxTip         int64 `json:"max_tip"`
}

type UserStatistics struct {
	Rank              int64  `json:"rank" db:"rank"`
	ViewersCount      int64  `json:"viewers_count"`
	TotalReactions    int64  `json:"total_reactions" db:"total_reactions"`
	TotalLivecomments int64  `json:"total_livecomments" db:"total_livecomments"`
	TotalTip          int64  `json:"total_tip" db:"total_tip"`
	FavoriteEmoji     string `json:"favorite_emoji"`
}

func getUserStatisticsHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	username := c.Param("username")
	// ユーザごとに、紐づく配信について、累計リアクション数、累計ライブコメント数、累計売上金額を算出
	// また、現在の合計視聴者数もだす

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	var user UserModel
	if err := tx.GetContext(ctx, &user, "SELECT * FROM users WHERE name = ?", username); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusBadRequest, "not found user that has the given username")
		} else {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
		}
	}

	var stats UserStatistics
	const rankingQuery = `
		WITH activity AS (
			SELECT l.user_id, COUNT(*) AS reactions, 0 AS comments, 0 AS tips
			FROM reactions r JOIN livestreams l ON l.id = r.livestream_id GROUP BY l.user_id
			UNION ALL
			SELECT l.user_id, 0, COUNT(*), SUM(c.tip)
			FROM livecomments c JOIN livestreams l ON l.id = c.livestream_id GROUP BY l.user_id
		), totals AS (
			SELECT user_id, SUM(reactions) AS reactions, SUM(comments) AS comments,
				SUM(tips) AS tips, SUM(reactions + tips) AS score
			FROM activity GROUP BY user_id
		), ranked AS (
			SELECT u.id, COALESCE(t.reactions,0) AS total_reactions,
				COALESCE(t.comments,0) AS total_livecomments, COALESCE(t.tips,0) AS total_tip,
				ROW_NUMBER() OVER (ORDER BY COALESCE(t.score,0) DESC, CAST(u.name AS BINARY) DESC) AS user_rank
			FROM users u LEFT JOIN totals t ON t.user_id = u.id
		)
		SELECT user_rank AS ` + "`rank`" + `, total_reactions, total_livecomments, total_tip
		FROM ranked WHERE id = ?`
	if err := tx.GetContext(ctx, &stats, rankingQuery, user.ID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to rank users: "+err.Error())
	}
	if err := tx.GetContext(ctx, &stats.ViewersCount, `
		SELECT COUNT(*) FROM livestream_viewers_history h
		JOIN livestreams l ON l.id = h.livestream_id WHERE l.user_id = ?`, user.ID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count viewers: "+err.Error())
	}

	// お気に入り絵文字
	query := `
	SELECT r.emoji_name
	FROM users u
	INNER JOIN livestreams l ON l.user_id = u.id
	INNER JOIN reactions r ON r.livestream_id = l.id
	WHERE u.name = ?
	GROUP BY emoji_name
	ORDER BY COUNT(*) DESC, emoji_name DESC
	LIMIT 1
	`
	if err := tx.GetContext(ctx, &stats.FavoriteEmoji, query, username); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to find favorite emoji: "+err.Error())
	}

	return c.JSON(http.StatusOK, stats)
}

func getLivestreamStatisticsHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		return err
	}

	id, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}
	livestreamID := int64(id)

	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to begin transaction: "+err.Error())
	}
	defer tx.Rollback()

	var livestream LivestreamModel
	if err := tx.GetContext(ctx, &livestream, "SELECT * FROM livestreams WHERE id = ?", livestreamID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusBadRequest, "cannot get stats of not found livestream")
		} else {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream: "+err.Error())
		}
	}

	var rank int64
	const rankingQuery = `
		WITH activity AS (
			SELECT livestream_id, COUNT(*) AS score FROM reactions GROUP BY livestream_id
			UNION ALL
			SELECT livestream_id, SUM(tip) FROM livecomments GROUP BY livestream_id
		), totals AS (
			SELECT livestream_id, SUM(score) AS score FROM activity GROUP BY livestream_id
		), target AS (
			SELECT COALESCE((SELECT score FROM totals WHERE livestream_id = ?), 0) AS score
		)
		SELECT COUNT(*) + 1 FROM livestreams l
		LEFT JOIN totals t ON t.livestream_id = l.id CROSS JOIN target
		WHERE COALESCE(t.score, 0) > target.score
			OR (COALESCE(t.score, 0) = target.score AND l.id > ?)`
	if err := tx.GetContext(ctx, &rank, rankingQuery, livestreamID, livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to rank livestreams: "+err.Error())
	}

	// 視聴者数算出
	var viewersCount int64
	if err := tx.GetContext(ctx, &viewersCount, `SELECT COUNT(*) FROM livestreams l INNER JOIN livestream_viewers_history h ON h.livestream_id = l.id WHERE l.id = ?`, livestreamID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count livestream viewers: "+err.Error())
	}

	// 最大チップ額
	var maxTip int64
	if err := tx.GetContext(ctx, &maxTip, `SELECT IFNULL(MAX(tip), 0) FROM livestreams l INNER JOIN livecomments l2 ON l2.livestream_id = l.id WHERE l.id = ?`, livestreamID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to find maximum tip livecomment: "+err.Error())
	}

	// リアクション数
	var totalReactions int64
	if err := tx.GetContext(ctx, &totalReactions, "SELECT COUNT(*) FROM livestreams l INNER JOIN reactions r ON r.livestream_id = l.id WHERE l.id = ?", livestreamID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count total reactions: "+err.Error())
	}

	// スパム報告数
	var totalReports int64
	if err := tx.GetContext(ctx, &totalReports, `SELECT COUNT(*) FROM livestreams l INNER JOIN livecomment_reports r ON r.livestream_id = l.id WHERE l.id = ?`, livestreamID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count total spam reports: "+err.Error())
	}

	if err := tx.Commit(); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to commit: "+err.Error())
	}

	return c.JSON(http.StatusOK, LivestreamStatistics{
		Rank:           rank,
		ViewersCount:   viewersCount,
		MaxTip:         maxTip,
		TotalReactions: totalReactions,
		TotalReports:   totalReports,
	})
}
