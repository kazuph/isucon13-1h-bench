package main

import (
	"os"
	"strconv"

	"github.com/labstack/echo/v4"
)

type ReserveLivestreamRequest struct {
	Tags         []int64 `json:"tags"`
	Title        string  `json:"title"`
	Description  string  `json:"description"`
	PlaylistUrl  string  `json:"playlist_url"`
	ThumbnailUrl string  `json:"thumbnail_url"`
	StartAt      int64   `json:"start_at"`
	EndAt        int64   `json:"end_at"`
}

type LivestreamViewerModel struct {
	UserID       int64 `db:"user_id" json:"user_id"`
	LivestreamID int64 `db:"livestream_id" json:"livestream_id"`
	CreatedAt    int64 `db:"created_at" json:"created_at"`
}

type LivestreamModel struct {
	ID           int64  `db:"id" json:"id"`
	UserID       int64  `db:"user_id" json:"user_id"`
	Title        string `db:"title" json:"title"`
	Description  string `db:"description" json:"description"`
	PlaylistUrl  string `db:"playlist_url" json:"playlist_url"`
	ThumbnailUrl string `db:"thumbnail_url" json:"thumbnail_url"`
	StartAt      int64  `db:"start_at" json:"start_at"`
	EndAt        int64  `db:"end_at" json:"end_at"`
}

type Livestream struct {
	ID           int64  `json:"id"`
	Owner        User   `json:"owner"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	PlaylistUrl  string `json:"playlist_url"`
	ThumbnailUrl string `json:"thumbnail_url"`
	Tags         []Tag  `json:"tags"`
	StartAt      int64  `json:"start_at"`
	EndAt        int64  `json:"end_at"`
}

type LivestreamTagModel struct {
	ID           int64 `db:"id" json:"id"`
	LivestreamID int64 `db:"livestream_id" json:"livestream_id"`
	TagID        int64 `db:"tag_id" json:"tag_id"`
}

type ReservationSlotModel struct {
	ID      int64 `db:"id" json:"id"`
	Slot    int64 `db:"slot" json:"slot"`
	StartAt int64 `db:"start_at" json:"start_at"`
	EndAt   int64 `db:"end_at" json:"end_at"`
}

func searchLivestreamsHandler(c echo.Context) error {
	limit := -1
	tag := c.QueryParam("tag")
	if raw := c.QueryParam("limit"); raw != "" && tag == "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return echo.NewHTTPError(400, "invalid limit")
		}
		limit = n
	}
	data.RLock()
	ids := data.allStreams
	if tag != "" {
		ids = data.tagStreams[tag]
	}
	if os.Getenv("ISUPIPE_SEARCH_CACHE") == "off" {
		streams := data.listStreams(ids, limit, true)
		data.RUnlock()
		return c.JSONBlob(200, streams)
	}
	result, hit := searchResponse(ids, searchKey{tag, limit})
	data.RUnlock()
	return result.send(c, hit && os.Getenv("ISUPIPE_SEARCH_GZIP") == "yes")
}

func getMyLivestreamsHandler(c echo.Context) error {
	id, err := sessionIdentity(c)
	if err != nil {
		return err
	}
	data.RLock()
	streams := data.listStreams(data.ownerStreams[id], -1, false)
	data.RUnlock()
	return c.JSONBlob(200, streams)
}

func getUserLivestreamsHandler(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	data.RLock()
	id, ok := data.names[c.Param("username")]
	streams := data.listStreams(data.ownerStreams[id], -1, false)
	data.RUnlock()
	if !ok {
		return echo.NewHTTPError(404, "user not found")
	}
	return c.JSONBlob(200, streams)
}

// viewerテーブルの廃止

func getLivestreamHandler(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := strconv.ParseInt(c.Param("livestream_id"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(400, "invalid id")
	}
	s, err := lookupStream(id)
	if err != nil {
		return echo.NewHTTPError(404, "livestream not found")
	}
	return c.JSON(200, s)
}
