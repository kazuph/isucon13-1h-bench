package main

import "github.com/labstack/echo/v4"

type streamAggregate struct {
	tips, comments, reactions, maxTip int64
	emoji                             map[string]int64
}

func aggregates() map[int64]*streamAggregate {
	out := make(map[int64]*streamAggregate, len(state.Streams))
	for id := range state.Streams {
		out[id] = &streamAggregate{emoji: map[string]int64{}}
	}
	for _, c := range state.Comments {
		a := out[c.LivestreamID]
		if a != nil {
			a.tips += c.Tip
			a.comments++
			if a.comments == 1 || c.Tip > a.maxTip {
				a.maxTip = c.Tip
			}
		}
	}
	for _, r := range state.Reactions {
		a := out[r.LivestreamID]
		if a != nil {
			a.reactions++
			a.emoji[r.EmojiName]++
		}
	}
	return out
}
func memStreamStats(c echo.Context) error {
	if _, err := memSession(c); err != nil {
		return err
	}
	sid, err := memID(c)
	if err != nil {
		return err
	}
	defer lockState(c, false)()
	if _, ok := state.Streams[sid]; !ok {
		return echo.NewHTTPError(400, "stream not found")
	}
	all := aggregates()
	a := all[sid]
	score := a.tips + a.reactions
	rank := int64(1)
	for id, b := range all {
		bs := b.tips + b.reactions
		if bs > score || (bs == score && id > sid) {
			rank++
		}
	}
	var viewers int64
	for _, v := range state.Viewers[sid] {
		viewers += v
	}
	return memoryJSON(c, 200, LivestreamStatistics{Rank: rank, ViewersCount: viewers, TotalReactions: a.reactions, TotalReports: int64(len(streamReports[sid])), MaxTip: a.maxTip})
}
func memUserStats(c echo.Context) error {
	if _, err := memSession(c); err != nil {
		return err
	}
	defer lockState(c, false)()
	name := c.Param("username")
	uid := names[name]
	if uid == 0 {
		return echo.NewHTTPError(400, "user not found")
	}
	all := aggregates()
	scores := map[int64]int64{}
	out := UserStatistics{Rank: 1}
	emojis := map[string]int64{}
	for id, a := range all {
		owner := state.Streams[id].UserID
		scores[owner] += a.tips + a.reactions
		if owner == uid {
			out.TotalTip += a.tips
			out.TotalLivecomments += a.comments
			out.TotalReactions += a.reactions
			for e, n := range a.emoji {
				emojis[e] += n
			}
			for _, n := range state.Viewers[id] {
				out.ViewersCount += n
			}
		}
	}
	for id, u := range state.Users {
		if scores[id] > scores[uid] || (scores[id] == scores[uid] && u.Name > name) {
			out.Rank++
		}
	}
	var max int64
	for e, n := range emojis {
		if n > max || (n == max && e > out.FavoriteEmoji) {
			max = n
			out.FavoriteEmoji = e
		}
	}
	return memoryJSON(c, 200, out)
}
