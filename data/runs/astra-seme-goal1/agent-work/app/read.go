package main

import (
	"github.com/labstack/echo/v4"
)

func readAPI(c echo.Context, sid int64, limit int) (any, int, error) {
	s := state
	uid := sessionUID(c)
	p := c.Path()
	d := s.Streams[sid]
	switch p {
	case "/api/tag":
		tags := make([]Tag, 0, len(s.TagIDs))
		for _, id := range s.TagIDs {
			tags = append(tags, s.Tags[id])
		}
		return map[string]any{"tags": tags}, 200, nil
	case "/api/payment":
		return PaymentResult{TotalTip: s.TotalTip}, 200, nil
	case "/api/user/me":
		if s.Users[uid] == nil {
			return nil, 0, echo.NewHTTPError(404, "user not found")
		}
		return s.user(uid), 200, nil
	case "/api/user/:username", "/api/user/:username/theme", "/api/user/:username/livestream", "/api/user/:username/statistics":
		id, ok := s.Names[c.Param("username")]
		if !ok {
			code := 404
			if p == "/api/user/:username/statistics" {
				code = 400
			}
			return nil, 0, echo.NewHTTPError(code, "user not found")
		}
		switch p {
		case "/api/user/:username":
			return s.user(id), 200, nil
		case "/api/user/:username/theme":
			return s.Users[id].Theme, 200, nil
		case "/api/user/:username/statistics":
			return s.userStats(id), 200, nil
		default:
			out := make([]Livestream, 0, len(s.ByUser[id]))
			for _, id := range s.ByUser[id] {
				out = append(out, s.stream(id))
			}
			return out, 200, nil
		}
	case "/api/livestream":
		out := make([]Livestream, 0, len(s.ByUser[uid]))
		for _, id := range s.ByUser[uid] {
			out = append(out, s.stream(id))
		}
		return out, 200, nil
	case "/api/livestream/search":
		ids := s.StreamIDs
		if name := c.QueryParam("tag"); name != "" {
			ids = nil
			for id, t := range s.Tags {
				if t.Name == name {
					ids = s.ByTag[id]
					break
				}
			}
			limit = len(ids)
		}
		out := make([]Livestream, 0)
		for i := len(ids) - 1; i >= 0 && len(out) < limit; i-- {
			out = append(out, s.stream(ids[i]))
		}
		return out, 200, nil
	case "/api/livestream/:livestream_id":
		if d == nil {
			return nil, 0, echo.NewHTTPError(404, "livestream not found")
		}
		return s.stream(sid), 200, nil
	case "/api/livestream/:livestream_id/livecomment":
		out := make([]Livecomment, 0)
		if d != nil {
			for i := len(d.Comments) - 1; i >= 0 && len(out) < limit; i-- {
				id := d.Comments[i]
				if _, ok := s.Comments[id]; ok {
					out = append(out, s.comment(id))
				}
			}
		}
		return out, 200, nil
	case "/api/livestream/:livestream_id/reaction":
		out := make([]Reaction, 0)
		if d != nil {
			for i := len(d.Reactions) - 1; i >= 0 && len(out) < limit; i-- {
				out = append(out, s.reaction(d.Reactions[i]))
			}
		}
		return out, 200, nil
	case "/api/livestream/:livestream_id/ngwords":
		out := make([]NGWord, 0)
		if d != nil {
			for i := len(d.NG) - 1; i >= 0; i-- {
				if d.NG[i].UserID == uid {
					out = append(out, d.NG[i])
				}
			}
		}
		return out, 200, nil
	case "/api/livestream/:livestream_id/report":
		if d == nil {
			return nil, 0, echo.NewHTTPError(500, "livestream not found")
		}
		if d.Model.UserID != uid {
			return nil, 0, echo.NewHTTPError(403, "not owner")
		}
		out := make([]LivecommentReport, 0, len(d.Reports))
		for _, id := range d.Reports {
			out = append(out, s.report(id))
		}
		return out, 200, nil
	case "/api/livestream/:livestream_id/statistics":
		if d == nil {
			return nil, 0, echo.NewHTTPError(400, "livestream not found")
		}
		return s.streamStats(sid), 200, nil
	}
	return nil, 0, echo.NewHTTPError(404, "not found")
}
func (s *State) streamStats(id int64) LivestreamStatistics {
	d := s.Streams[id]
	score := d.Tips + d.ReactionCount
	rank := int64(1)
	for oid, o := range s.Streams {
		v := o.Tips + o.ReactionCount
		if v > score || (v == score && oid > id) {
			rank++
		}
	}
	viewers := int64(0)
	for _, n := range d.Viewers {
		viewers += n
	}
	return LivestreamStatistics{Rank: rank, ViewersCount: viewers, TotalReactions: d.ReactionCount, TotalReports: int64(len(d.Reports)), MaxTip: d.MaxTip}
}
func (s *State) userStats(id int64) UserStatistics {
	var r UserStatistics
	emoji := map[string]int64{}
	for _, sid := range s.ByUser[id] {
		d := s.Streams[sid]
		r.TotalReactions += d.ReactionCount
		r.TotalLivecomments += d.CommentCount
		r.TotalTip += d.Tips
		for _, n := range d.Viewers {
			r.ViewersCount += n
		}
		for e, n := range d.Emoji {
			emoji[e] += n
		}
	}
	var best int64
	for e, n := range emoji {
		if n > best || (n == best && e > r.FavoriteEmoji) {
			r.FavoriteEmoji = e
			best = n
		}
	}
	score := r.TotalReactions + r.TotalTip
	name := s.Users[id].Model.Name
	r.Rank = 1
	for oid, u := range s.Users {
		var v int64
		for _, sid := range s.ByUser[oid] {
			d := s.Streams[sid]
			v += d.Tips + d.ReactionCount
		}
		if v > score || (v == score && u.Model.Name > name) {
			r.Rank++
		}
	}
	return r
}
