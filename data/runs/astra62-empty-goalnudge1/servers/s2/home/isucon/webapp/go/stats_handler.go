package main

import (
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
	ViewersCount      int64  `json:"viewers_count"`
	TotalReactions    int64  `json:"total_reactions"`
	TotalLivecomments int64  `json:"total_livecomments"`
	TotalTip          int64  `json:"total_tip"`
	FavoriteEmoji     string `json:"favorite_emoji"`
}

func getUserStatisticsHandler(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	username := c.Param("username")
	catalog.RLock()
	userID, ok := catalog.names[username]
	if !ok {
		catalog.RUnlock()
		return echo.NewHTTPError(http.StatusBadRequest, "user not found")
	}
	metrics.RLock()
	scores := make(map[int64]int64)
	var stats UserStatistics
	emojis := make(map[string]int64)
	for streamID, m := range metrics.streams {
		s, ok := catalog.streams[streamID]
		if !ok {
			continue
		}
		scores[s.Owner.ID] += m.score()
		if s.Owner.ID == userID {
			stats.TotalLivecomments += m.comments.Count
			stats.TotalTip += m.comments.Tips
			stats.TotalReactions += m.reactions
			stats.ViewersCount += m.viewers
			for name, n := range m.emoji {
				emojis[name] += n
			}
		}
	}
	metrics.RUnlock()
	target := scores[userID]
	stats.Rank = 1
	for id, u := range catalog.users {
		score := scores[id]
		if score > target || score == target && u.Name > username {
			stats.Rank++
		}
	}
	catalog.RUnlock()
	for name, n := range emojis {
		best := emojis[stats.FavoriteEmoji]
		if n > best || n == best && name > stats.FavoriteEmoji {
			stats.FavoriteEmoji = name
		}
	}
	return c.JSON(http.StatusOK, stats)
}

func getLivestreamStatisticsHandler(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := strconv.ParseInt(c.Param("livestream_id"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}
	catalog.RLock()
	if _, ok := catalog.streams[id]; !ok {
		catalog.RUnlock()
		return echo.NewHTTPError(http.StatusBadRequest, "livestream not found")
	}
	metrics.RLock()
	var stats LivestreamStatistics
	var target int64
	if m := metrics.streams[id]; m != nil {
		stats.ViewersCount = m.viewers
		stats.TotalReactions = m.reactions
		stats.TotalReports = m.reports
		stats.MaxTip = m.comments.Max
		target = m.score()
	}
	stats.Rank = 1
	for streamID := range catalog.streams {
		var score int64
		if m := metrics.streams[streamID]; m != nil {
			score = m.score()
		}
		if score > target || score == target && streamID > id {
			stats.Rank++
		}
	}
	metrics.RUnlock()
	catalog.RUnlock()
	return c.JSON(http.StatusOK, stats)
}
