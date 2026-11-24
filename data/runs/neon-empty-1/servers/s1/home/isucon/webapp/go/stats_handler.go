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
	if _, err := verifyAndGetUserID(c); err != nil {
		return err
	}

	username := c.Param("username")
	user, ok := getUserByName(username)
	if !ok {
		return echo.NewHTTPError(http.StatusBadRequest, "not found user that has the given username")
	}

	if nodeRole == rolePipe {
		ownerShard := shardForUsername(user.Name)
		ownerURL := shardInternalURL(ownerShard)
		otherURL := shardInternalURL(1 - ownerShard)

		ownerRes, err := fetchShardUserStats(ownerURL, user.ID, user.Name)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get shard user stats: "+err.Error())
		}
		otherBeat, err := fetchShardUserBeatCount(otherURL, ownerRes.TargetScore, user.Name)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get other shard rank: "+err.Error())
		}
		return c.JSON(http.StatusOK, UserStatistics{
			Rank:              1 + ownerRes.BeatCount + otherBeat,
			ViewersCount:      ownerRes.ViewersCount,
			TotalReactions:    ownerRes.TotalReactions,
			TotalLivecomments: ownerRes.TotalLivecomments,
			TotalTip:          ownerRes.TotalTip,
			FavoriteEmoji:     ownerRes.FavoriteEmoji,
		})
	}

	usersMu.RLock()
	statsMu.RLock()

	var targetScore, viewersCount, totalReactions, totalLivecomments, totalTip int64
	var favoriteEmoji string
	if us := userStatsMap[user.ID]; us != nil {
		totalReactions = us.reactions
		totalTip = us.totalTip
		totalLivecomments = us.livecomments
		viewersCount = us.viewers
		favoriteEmoji = us.favEmoji
		targetScore = totalReactions + totalTip
	}

	var rank int64 = 1
	for _, u := range usersByID {
		if u.ID == user.ID {
			continue
		}
		var sc int64
		if us := userStatsMap[u.ID]; us != nil {
			sc = us.reactions + us.totalTip
		}
		if sc > targetScore || (sc == targetScore && u.Name > user.Name) {
			rank++
		}
	}

	statsMu.RUnlock()
	usersMu.RUnlock()

	return c.JSON(http.StatusOK, UserStatistics{
		Rank:              rank,
		ViewersCount:      viewersCount,
		TotalReactions:    totalReactions,
		TotalLivecomments: totalLivecomments,
		TotalTip:          totalTip,
		FavoriteEmoji:     favoriteEmoji,
	})
}

func getLivestreamStatisticsHandler(c echo.Context) error {
	if _, err := verifyAndGetUserID(c); err != nil {
		return err
	}

	id, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}
	livestreamID := int64(id)

	if _, ok := getLivestreamEntryByID(livestreamID); !ok {
		return echo.NewHTTPError(http.StatusBadRequest, "cannot get stats of not found livestream")
	}

	if nodeRole == roleShard0 || nodeRole == roleShard1 {
		myShard := 0
		if nodeRole == roleShard1 {
			myShard = 1
		}

		livestreamsMu.RLock()
		usersMu.RLock()
		statsMu.RLock()

		var targetScore, viewersCount, maxTip, totalReactions, totalReports int64
		if lss := lsStatsMap[livestreamID]; lss != nil {
			totalReactions = lss.reactions
			viewersCount = lss.viewers
			maxTip = lss.maxTip
			totalReports = lss.reports
			targetScore = lss.reactions + lss.totalTip
		}

		var localBeat int64
		for lsID, ls := range livestreamByID {
			if lsID == livestreamID {
				continue
			}
			u, ok := usersByID[ls.Model.UserID]
			if !ok || shardForUsername(u.Name) != myShard {
				continue
			}
			var sc int64
			if lss := lsStatsMap[lsID]; lss != nil {
				sc = lss.reactions + lss.totalTip
			}
			if sc > targetScore || (sc == targetScore && lsID > livestreamID) {
				localBeat++
			}
		}

		statsMu.RUnlock()
		usersMu.RUnlock()
		livestreamsMu.RUnlock()

		remoteBeat, err := fetchShardLSBeatCount(otherShardInternalURL(), targetScore, livestreamID)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get remote shard livestream rank: "+err.Error())
		}

		return c.JSON(http.StatusOK, LivestreamStatistics{
			Rank:           1 + localBeat + remoteBeat,
			ViewersCount:   viewersCount,
			MaxTip:         maxTip,
			TotalReactions: totalReactions,
			TotalReports:   totalReports,
		})
	}

	livestreamsMu.RLock()
	statsMu.RLock()

	var targetScore, viewersCount, maxTip, totalReactions, totalReports int64
	if lss := lsStatsMap[livestreamID]; lss != nil {
		totalReactions = lss.reactions
		viewersCount = lss.viewers
		maxTip = lss.maxTip
		totalReports = lss.reports
		targetScore = lss.reactions + lss.totalTip
	}

	var rank int64 = 1
	for lsID := range livestreamByID {
		if lsID == livestreamID {
			continue
		}
		var sc int64
		if lss := lsStatsMap[lsID]; lss != nil {
			sc = lss.reactions + lss.totalTip
		}
		if sc > targetScore || (sc == targetScore && lsID > livestreamID) {
			rank++
		}
	}

	statsMu.RUnlock()
	livestreamsMu.RUnlock()

	return c.JSON(http.StatusOK, LivestreamStatistics{
		Rank:           rank,
		ViewersCount:   viewersCount,
		MaxTip:         maxTip,
		TotalReactions: totalReactions,
		TotalReports:   totalReports,
	})
}
