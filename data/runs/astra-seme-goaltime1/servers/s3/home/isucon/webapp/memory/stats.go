package main

import "github.com/labstack/echo/v4"

type aggregate struct {
	tips, comments, reactions, viewers, reports, maxTip int64
	emoji                                               map[string]int64
}

func aggregates() (map[int64]*aggregate, map[int64]*aggregate) {
	ss := make(map[int64]*aggregate, len(state.Streams))
	us := make(map[int64]*aggregate, len(state.Users))
	for id := range state.Users {
		us[id] = &aggregate{emoji: map[string]int64{}}
	}
	for id := range state.Streams {
		ss[id] = &aggregate{}
	}
	for _, c := range state.Comments {
		a := ss[c.LivestreamID]
		if a == nil {
			continue
		}
		u := us[state.Streams[c.LivestreamID].UserID]
		a.tips += c.Tip
		a.comments++
		if c.Tip > a.maxTip {
			a.maxTip = c.Tip
		}
		u.tips += c.Tip
		u.comments++
	}
	for _, r := range state.Reactions {
		a := ss[r.LivestreamID]
		if a == nil {
			continue
		}
		u := us[state.Streams[r.LivestreamID].UserID]
		a.reactions++
		u.reactions++
		u.emoji[r.EmojiName]++
	}
	for _, v := range state.Viewers {
		a := ss[v.LivestreamID]
		if a == nil {
			continue
		}
		a.viewers++
		us[state.Streams[v.LivestreamID].UserID].viewers++
	}
	for _, r := range state.Reports {
		if a := ss[r.LivestreamID]; a != nil {
			a.reports++
		}
	}
	return ss, us
}
func getUserStatisticsHandler(c echo.Context) error {
	if e := verifyUserSession(c); e != nil {
		return e
	}
	mu.RLock()
	defer mu.RUnlock()
	name := c.Param("username")
	id := names[name]
	if id == 0 {
		return echo.NewHTTPError(400)
	}
	_, us := aggregates()
	a := us[id]
	rank := int64(1)
	score := a.tips + a.reactions
	for i, v := range us {
		if v.tips+v.reactions > score || (v.tips+v.reactions == score && state.Users[i].Name > name) {
			rank++
		}
	}
	emoji := ""
	cnt := int64(0)
	for s, n := range a.emoji {
		if n > cnt || (n == cnt && s > emoji) {
			emoji = s
			cnt = n
		}
	}
	return c.JSON(200, UserStatistics{Rank: rank, ViewersCount: a.viewers, TotalReactions: a.reactions, TotalLivecomments: a.comments, TotalTip: a.tips, FavoriteEmoji: emoji})
}
func getLivestreamStatisticsHandler(c echo.Context) error {
	if e := verifyUserSession(c); e != nil {
		return e
	}
	id, e := paramID(c, "livestream_id")
	if e != nil {
		return e
	}
	mu.RLock()
	defer mu.RUnlock()
	if _, ok := state.Streams[id]; !ok {
		return echo.NewHTTPError(400)
	}
	ss, _ := aggregates()
	a := ss[id]
	rank := int64(1)
	score := a.tips + a.reactions
	for i, v := range ss {
		if v.tips+v.reactions > score || (v.tips+v.reactions == score && i > id) {
			rank++
		}
	}
	return c.JSON(200, LivestreamStatistics{Rank: rank, ViewersCount: a.viewers, TotalReactions: a.reactions, TotalReports: a.reports, MaxTip: a.maxTip})
}
func GetPaymentResult(c echo.Context) error {
	mu.RLock()
	defer mu.RUnlock()
	var tip int64
	for _, v := range state.Comments {
		tip += v.Tip
	}
	return c.JSON(200, PaymentResult{TotalTip: tip})
}
