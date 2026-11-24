package main

import (
	"github.com/labstack/echo/v4"
	"time"
)

func reserveLivestreamHandler(c echo.Context) error {
	if e := verifyUserSession(c); e != nil {
		return e
	}
	var r ReserveLivestreamRequest
	if e := decode(c, &r); e != nil {
		return e
	}
	if r.StartAt >= time.Date(2024, 11, 25, 1, 0, 0, 0, time.UTC).Unix() || r.EndAt <= time.Date(2023, 11, 25, 1, 0, 0, 0, time.UTC).Unix() {
		return echo.NewHTTPError(400, "bad reservation time range")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, s := range state.Slots {
		if s.StartAt >= r.StartAt && s.EndAt <= r.EndAt && s.Slot < 1 {
			return echo.NewHTTPError(400, "no reservation slots")
		}
	}
	for _, id := range r.Tags {
		if _, ok := state.Tags[id]; !ok {
			return echo.NewHTTPError(400, "unknown tag")
		}
	}
	s := LivestreamModel{ID: next("stream"), UserID: uid(c), Title: r.Title, Description: r.Description, PlaylistUrl: r.PlaylistUrl, ThumbnailUrl: r.ThumbnailUrl, StartAt: r.StartAt, EndAt: r.EndAt}
	if e := commit(c, Event{Kind: "stream", Stream: s, Tags: r.Tags}); e != nil {
		return e
	}
	return reply(c, 201, streamResponse(s.ID))
}
func searchLivestreamsHandler(c echo.Context) error {
	n, e := limit(c)
	if e != nil {
		return e
	}
	mu.RLock()
	defer mu.RUnlock()
	ids := allStreams
	tag := c.QueryParam("tag")
	if tag != "" {
		ids = tagStreams[tag]
		n = len(ids)
	}
	if n > len(ids) {
		n = len(ids)
	}
	out := make([]Livestream, 0, n)
	for i := len(ids) - 1; i >= len(ids)-n; i-- {
		out = append(out, streamResponse(ids[i]))
	}
	return reply(c, 200, out)
}
func getMyLivestreamsHandler(c echo.Context) error {
	if e := verifyUserSession(c); e != nil {
		return e
	}
	mu.RLock()
	defer mu.RUnlock()
	return listUserStreams(c, uid(c))
}
func getUserLivestreamsHandler(c echo.Context) error {
	if e := verifyUserSession(c); e != nil {
		return e
	}
	mu.RLock()
	defer mu.RUnlock()
	id := names[c.Param("username")]
	if id == 0 {
		return echo.NewHTTPError(404)
	}
	return listUserStreams(c, id)
}
func listUserStreams(c echo.Context, id int64) error {
	out := make([]Livestream, 0, len(userStreams[id]))
	for _, id := range userStreams[id] {
		out = append(out, streamResponse(id))
	}
	return reply(c, 200, out)
}
func getLivestreamHandler(c echo.Context) error {
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
		return echo.NewHTTPError(404)
	}
	return reply(c, 200, streamResponse(id))
}
func enterLivestreamHandler(c echo.Context) error { return viewerChange(c, "enter") }
func exitLivestreamHandler(c echo.Context) error  { return viewerChange(c, "exit") }
func viewerChange(c echo.Context, kind string) error {
	if e := verifyUserSession(c); e != nil {
		return e
	}
	id, e := paramID(c, "livestream_id")
	if e != nil {
		return e
	}
	mu.Lock()
	defer mu.Unlock()
	if e := commit(c, Event{Kind: kind, Viewer: LivestreamViewerModel{UserID: uid(c), LivestreamID: id, CreatedAt: time.Now().Unix()}}); e != nil {
		return e
	}
	return reply(c, 200, nil)
}
