package main

import (
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
	catalogMu.RLock()
	id, ok := catalog.names[c.Param("username")]
	catalogMu.RUnlock()
	if !ok {
		return echo.NewHTTPError(400)
	}
	return c.JSON(200, rankedUser(id))
}

func getLivestreamStatisticsHandler(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := strconv.ParseInt(c.Param("livestream_id"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(400)
	}
	if _, ok := knownStream(id); !ok {
		return echo.NewHTTPError(400)
	}
	return c.JSON(200, rankedStream(id))
}
