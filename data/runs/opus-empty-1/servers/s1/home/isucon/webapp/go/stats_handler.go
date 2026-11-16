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

type idCount struct {
	ID  int64 `db:"id"`
	Cnt int64 `db:"cnt"`
}

func getUserStatisticsHandler(c echo.Context) error {
	ctx := c.Request().Context()

	if err := verifyUserSession(c); err != nil {
		// echo.NewHTTPErrorが返っているのでそのまま出力
		return err
	}

	username := c.Param("username")

	user, err := getUserByName(ctx, username)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusBadRequest, "not found user that has the given username")
		} else {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get user: "+err.Error())
		}
	}

	// ランク算出
	var reacts []idCount
	if err := dbConn.SelectContext(ctx, &reacts, "SELECT l.user_id AS id, COUNT(*) AS cnt FROM reactions r INNER JOIN livestreams l ON l.id = r.livestream_id GROUP BY l.user_id"); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count reactions: "+err.Error())
	}
	var tips []idCount
	if err := dbConn.SelectContext(ctx, &tips, "SELECT l.user_id AS id, IFNULL(SUM(c.tip), 0) AS cnt FROM livecomments c INNER JOIN livestreams l ON l.id = c.livestream_id GROUP BY l.user_id"); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count tips: "+err.Error())
	}
	scores := make(map[int64]int64, len(reacts)+len(tips))
	var totalReactions int64
	for _, r := range reacts {
		scores[r.ID] += r.Cnt
		if r.ID == user.ID {
			totalReactions = r.Cnt
		}
	}
	for _, t := range tips {
		scores[t.ID] += t.Cnt
	}
	myScore := scores[user.ID]
	var rank int64 = 1
	cacheMu.RLock()
	for id, u := range usersByID {
		sc := scores[id]
		if sc > myScore || (sc == myScore && u.Name > username) {
			rank++
		}
	}
	cacheMu.RUnlock()

	// ライブコメント数、チップ合計
	var lcStat struct {
		Cnt int64 `db:"cnt"`
		Tip int64 `db:"tip"`
	}
	if err := dbConn.GetContext(ctx, &lcStat, "SELECT COUNT(*) AS cnt, IFNULL(SUM(c.tip), 0) AS tip FROM livecomments c INNER JOIN livestreams l ON l.id = c.livestream_id WHERE l.user_id = ?", user.ID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livecomments: "+err.Error())
	}

	// 合計視聴者数
	var viewersCount int64
	if err := dbConn.GetContext(ctx, &viewersCount, "SELECT COUNT(*) FROM livestream_viewers_history h INNER JOIN livestreams l ON l.id = h.livestream_id WHERE l.user_id = ?", user.ID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream_view_history: "+err.Error())
	}

	// お気に入り絵文字
	var favoriteEmoji string
	query := `
	SELECT r.emoji_name
	FROM livestreams l
	INNER JOIN reactions r ON r.livestream_id = l.id
	WHERE l.user_id = ?
	GROUP BY emoji_name
	ORDER BY COUNT(*) DESC, emoji_name DESC
	LIMIT 1
	`
	if err := dbConn.GetContext(ctx, &favoriteEmoji, query, user.ID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to find favorite emoji: "+err.Error())
	}

	stats := UserStatistics{
		Rank:              rank,
		ViewersCount:      viewersCount,
		TotalReactions:    totalReactions,
		TotalLivecomments: lcStat.Cnt,
		TotalTip:          lcStat.Tip,
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

	if _, err := getLivestreamByID(ctx, livestreamID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return echo.NewHTTPError(http.StatusBadRequest, "cannot get stats of not found livestream")
		} else {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream: "+err.Error())
		}
	}

	var reacts []idCount
	if err := dbConn.SelectContext(ctx, &reacts, "SELECT livestream_id AS id, COUNT(*) AS cnt FROM reactions GROUP BY livestream_id"); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count reactions: "+err.Error())
	}
	var tips []idCount
	if err := dbConn.SelectContext(ctx, &tips, "SELECT livestream_id AS id, IFNULL(SUM(tip), 0) AS cnt FROM livecomments GROUP BY livestream_id"); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count tips: "+err.Error())
	}
	scores := make(map[int64]int64, len(reacts)+len(tips))
	var totalReactions int64
	for _, r := range reacts {
		scores[r.ID] += r.Cnt
		if r.ID == livestreamID {
			totalReactions = r.Cnt
		}
	}
	for _, t := range tips {
		scores[t.ID] += t.Cnt
	}
	myScore := scores[livestreamID]
	var rank int64 = 1
	cacheMu.RLock()
	for lid := range livestreamsByID {
		sc := scores[lid]
		if sc > myScore || (sc == myScore && lid > livestreamID) {
			rank++
		}
	}
	cacheMu.RUnlock()

	// 視聴者数算出
	var viewersCount int64
	if err := dbConn.GetContext(ctx, &viewersCount, `SELECT COUNT(*) FROM livestream_viewers_history WHERE livestream_id = ?`, livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to count livestream viewers: "+err.Error())
	}

	// 最大チップ額
	var maxTip int64
	if err := dbConn.GetContext(ctx, &maxTip, `SELECT IFNULL(MAX(tip), 0) FROM livecomments WHERE livestream_id = ?`, livestreamID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to find maximum tip livecomment: "+err.Error())
	}

	// スパム報告数
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
