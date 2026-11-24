package main

import (
	"encoding/json"
	"github.com/labstack/echo/v4"
	"reflect"
	"strconv"
	"strings"
	"time"
)

func decode(c echo.Context, v any) error {
	if prepared := c.Get("decoded-request"); prepared != nil {
		reflect.ValueOf(v).Elem().Set(reflect.ValueOf(prepared).Elem())
		return nil
	}
	if err := json.NewDecoder(c.Request().Body).Decode(v); err != nil {
		return echo.NewHTTPError(400, "invalid request body")
	}
	return nil
}

// Match MySQL LIKE including wildcard and escape semantics, using Unicode characters.
func like(text, word string) bool {
	text = generalCI(text)
	word = generalCI(word)
	if !strings.ContainsAny(word, "%_\\") {
		return strings.Contains(text, word)
	}
	p := []rune("%" + word + "%")
	t := []rune(text)
	prev := make([]bool, len(t)+1)
	prev[0] = true
	for i := 0; i < len(p); i++ {
		ch := p[i]
		escaped := false
		if ch == '\\' && i+1 < len(p) {
			i++
			ch = p[i]
			escaped = true
		}
		next := make([]bool, len(t)+1)
		if ch == '%' && !escaped {
			next[0] = prev[0]
			for j := 1; j <= len(t); j++ {
				next[j] = prev[j] || next[j-1]
			}
		} else {
			for j := 1; j <= len(t); j++ {
				next[j] = prev[j-1] && ((ch == '_' && !escaped) || ch == t[j-1])
			}
		}
		prev = next
	}
	return prev[len(t)]
}
func writeAPI(c echo.Context, sid int64) (any, int, error) {
	s := state
	uid := sessionUID(c)
	d := s.Streams[sid]
	now := time.Now().Unix()
	switch c.Path() {
	case "/api/icon":
		var r PostIconRequest
		if err := decode(c, &r); err != nil {
			return nil, 0, err
		}
		if s.Users[uid] == nil {
			return nil, 0, echo.NewHTTPError(404, "user not found")
		}
		id := s.NextIcon + 1
		if err := commit(Event{Kind: "icon", UID: uid, ID: id, Image: r.Image}); err != nil {
			return nil, 0, err
		}
		return PostIconResponse{ID: id}, 201, nil
	case "/api/livestream/reservation":
		var r ReserveLivestreamRequest
		if err := decode(c, &r); err != nil {
			return nil, 0, err
		}
		if r.StartAt >= time.Date(2024, 11, 25, 1, 0, 0, 0, time.UTC).Unix() || r.EndAt <= time.Date(2023, 11, 25, 1, 0, 0, 0, time.UTC).Unix() {
			return nil, 0, echo.NewHTTPError(400, "bad reservation time range")
		}
		for _, slot := range s.Slots {
			if slot.StartAt >= r.StartAt && slot.EndAt <= r.EndAt && slot.Slot < 1 {
				return nil, 0, echo.NewHTTPError(400, "no reservation capacity")
			}
		}
		for _, id := range r.Tags {
			if _, ok := s.Tags[id]; !ok {
				return nil, 0, echo.NewHTTPError(500, "tag not found")
			}
		}
		id := s.NextStream + 1
		d := &StreamData{Model: LivestreamModel{ID: id, UserID: uid, Title: r.Title, Description: r.Description, PlaylistUrl: r.PlaylistUrl, ThumbnailUrl: r.ThumbnailUrl, StartAt: r.StartAt, EndAt: r.EndAt}, Tags: r.Tags}
		if err := commit(Event{Kind: "stream", Stream: d}); err != nil {
			return nil, 0, err
		}
		return s.stream(id), 201, nil
	case "/api/livestream/:livestream_id/enter", "/api/livestream/:livestream_id/exit":
		if d == nil {
			return nil, 0, echo.NewHTTPError(404, "livestream not found")
		}
		kind := "enter"
		if c.Request().Method == "DELETE" {
			kind = "exit"
		}
		if err := commit(Event{Kind: kind, UID: uid, SID: sid}); err != nil {
			return nil, 0, err
		}
		return nil, 200, nil
	case "/api/livestream/:livestream_id/livecomment":
		var r PostLivecommentRequest
		if err := decode(c, &r); err != nil {
			return nil, 0, err
		}
		if d == nil {
			return nil, 0, echo.NewHTTPError(404, "livestream not found")
		}
		for _, n := range d.NG {
			if n.UserID == d.Model.UserID && like(r.Comment, n.Word) {
				return nil, 0, echo.NewHTTPError(400, "このコメントがスパム判定されました")
			}
		}
		id := s.NextComment + 1
		v := LivecommentModel{ID: id, UserID: uid, LivestreamID: sid, Comment: r.Comment, Tip: r.Tip, CreatedAt: now}
		if err := commit(Event{Kind: "comment", Comment: &v}); err != nil {
			return nil, 0, err
		}
		return s.comment(id), 201, nil
	case "/api/livestream/:livestream_id/reaction":
		var r PostReactionRequest
		if err := decode(c, &r); err != nil {
			return nil, 0, err
		}
		if d == nil {
			return nil, 0, echo.NewHTTPError(404, "livestream not found")
		}
		id := s.NextReaction + 1
		v := ReactionModel{ID: id, UserID: uid, LivestreamID: sid, EmojiName: r.EmojiName, CreatedAt: now}
		if err := commit(Event{Kind: "reaction", Reaction: &v}); err != nil {
			return nil, 0, err
		}
		return s.reaction(id), 201, nil
	case "/api/livestream/:livestream_id/livecomment/:livecomment_id/report":
		cid, err := strconv.ParseInt(c.Param("livecomment_id"), 10, 64)
		if err != nil {
			return nil, 0, echo.NewHTTPError(400, "invalid comment id")
		}
		if d == nil {
			return nil, 0, echo.NewHTTPError(404, "livestream not found")
		}
		if _, ok := s.Comments[cid]; !ok {
			return nil, 0, echo.NewHTTPError(404, "comment not found")
		}
		id := s.NextReport + 1
		r := LivecommentReportModel{ID: id, UserID: uid, LivestreamID: sid, LivecommentID: cid, CreatedAt: now}
		if err := commit(Event{Kind: "report", Report: &r}); err != nil {
			return nil, 0, err
		}
		return s.report(id), 201, nil
	case "/api/livestream/:livestream_id/moderate":
		var r ModerateRequest
		if err := decode(c, &r); err != nil {
			return nil, 0, err
		}
		if d == nil || d.Model.UserID != uid {
			return nil, 0, echo.NewHTTPError(400, "not owner")
		}
		id := s.NextNG + 1
		n := NGWord{ID: id, UserID: uid, LivestreamID: sid, Word: r.NGWord, CreatedAt: now}
		del := make([]int64, 0)
		for _, cid := range d.Comments {
			if v, ok := s.Comments[cid]; ok {
				matched := like(v.Comment, r.NGWord)
				if !matched && !d.Moderated {
					for _, n := range d.NG {
						if like(v.Comment, n.Word) {
							matched = true
							break
						}
					}
				}
				if matched {
					del = append(del, cid)
				}
			}
		}
		if err := commit(Event{Kind: "ng", NG: &n, Deleted: del, Moderation: true}); err != nil {
			return nil, 0, err
		}
		return map[string]int64{"word_id": id}, 201, nil
	}
	return nil, 0, echo.NewHTTPError(404, "not found")
}

func prepareRequest(c echo.Context) error {
	var v any
	switch c.Path() {
	case "/api/icon":
		v = &PostIconRequest{}
	case "/api/livestream/reservation":
		v = &ReserveLivestreamRequest{}
	case "/api/livestream/:livestream_id/livecomment":
		v = &PostLivecommentRequest{}
	case "/api/livestream/:livestream_id/reaction":
		v = &PostReactionRequest{}
	case "/api/livestream/:livestream_id/moderate":
		v = &ModerateRequest{}
	}
	if v != nil {
		if err := decode(c, v); err != nil {
			return err
		}
		c.Set("decoded-request", v)
	}
	return nil
}
