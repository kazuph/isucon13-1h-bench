package main

import (
	"database/sql"
	"errors"
	"net/http"
	"sort"
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

type LivestreamRankingEntry struct {
	LivestreamID int64
	Score        int64
}
type LivestreamRanking []LivestreamRankingEntry

func (r LivestreamRanking) Len() int      { return len(r) }
func (r LivestreamRanking) Swap(i, j int) { r[i], r[j] = r[j], r[i] }
func (r LivestreamRanking) Less(i, j int) bool {
	if r[i].Score == r[j].Score {
		return r[i].LivestreamID < r[j].LivestreamID
	} else {
		return r[i].Score < r[j].Score
	}
}

type UserStatistics struct {
	Rank              int64  `json:"rank"`
	ViewersCount      int64  `json:"viewers_count"`
	TotalReactions    int64  `json:"total_reactions"`
	TotalLivecomments int64  `json:"total_livecomments"`
	TotalTip          int64  `json:"total_tip"`
	FavoriteEmoji     string `json:"favorite_emoji"`
}

type UserRankingEntry struct {
	Username string
	Score    int64
}
type UserRanking []UserRankingEntry

func (r UserRanking) Len() int      { return len(r) }
func (r UserRanking) Swap(i, j int) { r[i], r[j] = r[j], r[i] }
func (r UserRanking) Less(i, j int) bool {
	if r[i].Score == r[j].Score {
		return r[i].Username < r[j].Username
	} else {
		return r[i].Score < r[j].Score
	}
}

func getUserStatisticsHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		return err
	}

	username := c.Param("username")
	flushWrites()

	user, err := getUserByName(ctx, dbConn, username)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusBadRequest, "not found user that has the given username")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
	}

	// ランク算出
	type scoreRow struct {
		Name  string `db:"name"`
		Score int64  `db:"score"`
	}
	var rows []scoreRow
	if err := dbConn.SelectContext(ctx, &rows, `
		SELECT u.name AS name, IFNULL(r.c, 0) + IFNULL(t.s, 0) AS score FROM users u
		LEFT JOIN (SELECT l.user_id, COUNT(*) AS c FROM reactions r INNER JOIN livestreams l ON l.id = r.livestream_id GROUP BY l.user_id) r ON r.user_id = u.id
		LEFT JOIN (SELECT l.user_id, SUM(lc.tip) AS s FROM livecomments lc INNER JOIN livestreams l ON l.id = lc.livestream_id GROUP BY l.user_id) t ON t.user_id = u.id`); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get ranking: "+err.Error())
	}
	ranking := make(UserRanking, 0, len(rows))
	for _, r := range rows {
		ranking = append(ranking, UserRankingEntry{Username: r.Name, Score: r.Score})
	}
	sort.Sort(ranking)

	var rank int64 = 1
	for i := len(ranking) - 1; i >= 0; i-- {
		entry := ranking[i]
		if entry.Username == username {
			break
		}
		rank++
	}

	var totalReactions int64
	if err := dbConn.GetContext(ctx, &totalReactions, `SELECT COUNT(*) FROM livestreams l INNER JOIN reactions r ON r.livestream_id = l.id WHERE l.user_id = ?`, user.ID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count total reactions: "+err.Error())
	}

	var lc struct {
		Cnt int64 `db:"cnt"`
		Tip int64 `db:"tip"`
	}
	if err := dbConn.GetContext(ctx, &lc, `SELECT COUNT(*) AS cnt, IFNULL(SUM(lc.tip), 0) AS tip FROM livestreams l INNER JOIN livecomments lc ON lc.livestream_id = l.id WHERE l.user_id = ?`, user.ID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livecomments: "+err.Error())
	}

	var viewersCount int64
	if err := dbConn.GetContext(ctx, &viewersCount, `SELECT COUNT(*) FROM livestreams l INNER JOIN livestream_viewers_history h ON h.livestream_id = l.id WHERE l.user_id = ?`, user.ID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream_view_history: "+err.Error())
	}

	var favoriteEmoji string
	if err := dbConn.GetContext(ctx, &favoriteEmoji, `
	SELECT r.emoji_name
	FROM livestreams l
	INNER JOIN reactions r ON r.livestream_id = l.id
	WHERE l.user_id = ?
	GROUP BY emoji_name
	ORDER BY COUNT(*) DESC, emoji_name DESC
	LIMIT 1`, user.ID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to find favorite emoji: "+err.Error())
	}

	stats := UserStatistics{
		Rank:              rank,
		ViewersCount:      viewersCount,
		TotalReactions:    totalReactions,
		TotalLivecomments: lc.Cnt,
		TotalTip:          lc.Tip,
		FavoriteEmoji:     favoriteEmoji,
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
	flushWrites()

	if _, err := getLivestreamByID(ctx, dbConn, livestreamID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusBadRequest, "cannot get stats of not found livestream")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream: "+err.Error())
	}

	type scoreRow struct {
		ID    int64 `db:"id"`
		Score int64 `db:"score"`
	}
	var rows []scoreRow
	if err := dbConn.SelectContext(ctx, &rows, `
		SELECT l.id AS id, IFNULL(r.c, 0) + IFNULL(t.s, 0) AS score FROM livestreams l
		LEFT JOIN (SELECT livestream_id, COUNT(*) AS c FROM reactions GROUP BY livestream_id) r ON r.livestream_id = l.id
		LEFT JOIN (SELECT livestream_id, SUM(tip) AS s FROM livecomments GROUP BY livestream_id) t ON t.livestream_id = l.id`); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get ranking: "+err.Error())
	}
	ranking := make(LivestreamRanking, 0, len(rows))
	for _, r := range rows {
		ranking = append(ranking, LivestreamRankingEntry{LivestreamID: r.ID, Score: r.Score})
	}
	sort.Sort(ranking)

	var rank int64 = 1
	for i := len(ranking) - 1; i >= 0; i-- {
		entry := ranking[i]
		if entry.LivestreamID == livestreamID {
			break
		}
		rank++
	}

	var viewersCount int64
	if err := dbConn.GetContext(ctx, &viewersCount, `SELECT COUNT(*) FROM livestream_viewers_history WHERE livestream_id = ?`, livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count livestream viewers: "+err.Error())
	}

	var maxTip int64
	if err := dbConn.GetContext(ctx, &maxTip, `SELECT IFNULL(MAX(tip), 0) FROM livecomments WHERE livestream_id = ?`, livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to find maximum tip livecomment: "+err.Error())
	}

	var totalReactions int64
	if err := dbConn.GetContext(ctx, &totalReactions, "SELECT COUNT(*) FROM reactions WHERE livestream_id = ?", livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count total reactions: "+err.Error())
	}

	var totalReports int64
	if err := dbConn.GetContext(ctx, &totalReports, `SELECT COUNT(*) FROM livecomment_reports WHERE livestream_id = ?`, livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count total spam reports: "+err.Error())
	}

	return c.JSON(http.StatusOK, LivestreamStatistics{
		Rank:           rank,
		ViewersCount:   viewersCount,
		MaxTip:         maxTip,
		TotalReactions: totalReactions,
		TotalReports:   totalReports,
	})
}
