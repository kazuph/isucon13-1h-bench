package main

import (
	"github.com/labstack/echo/v4"
	"sort"
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
	if e := commit(Event{Kind: "stream", Stream: s, Tags: r.Tags}); e != nil {
		return e
	}
	return c.JSON(201, streamResponse(s.ID))
}
func searchLivestreamsHandler(c echo.Context) error {
	n, e := limit(c)
	if e != nil {
		return e
	}
	mu.RLock()
	defer mu.RUnlock()
	ids := make([]int64, 0, len(state.Streams))
	tag := c.QueryParam("tag")
	for id := range state.Streams {
		if tag != "" {
			for _, t := range state.StreamTags[id] {
				if state.Tags[t].Name == tag {
					ids = append(ids, id)
					break
				}
			}
		} else {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	if tag == "" && len(ids) > n {
		ids = ids[:n]
	}
	out := make([]Livestream, 0, len(ids))
	for _, id := range ids {
		out = append(out, streamResponse(id))
	}
	return c.JSON(200, out)
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
	return c.JSON(200, out)
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
	return c.JSON(200, streamResponse(id))
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
	if e := commit(Event{Kind: kind, Viewer: LivestreamViewerModel{UserID: uid(c), LivestreamID: id, CreatedAt: time.Now().Unix()}}); e != nil {
		return e
	}
	return c.NoContent(200)
}
