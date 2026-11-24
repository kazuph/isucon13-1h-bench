package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

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

func reserveLivestreamHandler(c echo.Context) error {
	defer c.Request().Body.Close()

	userID, err := verifyAndGetUserID(c)
	if err != nil {
		return err
	}

	var req ReserveLivestreamRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	var (
		termStartAt    = time.Date(2023, 11, 25, 1, 0, 0, 0, time.UTC)
		termEndAt      = time.Date(2024, 11, 25, 1, 0, 0, 0, time.UTC)
		reserveStartAt = time.Unix(req.StartAt, 0)
		reserveEndAt   = time.Unix(req.EndAt, 0)
	)
	if (reserveStartAt.Equal(termEndAt) || reserveStartAt.After(termEndAt)) || (reserveEndAt.Equal(termStartAt) || reserveEndAt.Before(termStartAt)) {
		return echo.NewHTTPError(http.StatusBadRequest, "bad reservation time range")
	}

	var entry *LivestreamCacheEntry
	if nodeRole == roleShard0 || nodeRole == roleShard1 {
		res, err := callPipeReserve(userID, req)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to reserve livestream: "+err.Error())
		}
		if !res.OK {
			return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("予約期間 %d ~ %dに対して、予約区間 %d ~ %dが予約できません", termStartAt.Unix(), termEndAt.Unix(), req.StartAt, req.EndAt))
		}
		entry = &LivestreamCacheEntry{
			Model: res.Model,
			Tags:  res.Tags,
		}
		addLivestreamCache(entry)
	} else {
		ok, matchedSlots := tryReserveSlots(req.StartAt, req.EndAt)
		if !ok {
			return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("予約期間 %d ~ %dに対して、予約区間 %d ~ %dが予約できません", termStartAt.Unix(), termEndAt.Unix(), req.StartAt, req.EndAt))
		}

		livestreamID := nextLivestreamID.Add(1)
		tags := make([]Tag, 0, len(req.Tags))
		for _, tagID := range req.Tags {
			if t, found := getTagByID(tagID); found {
				tags = append(tags, t)
			}
		}

		model := LivestreamModel{
			ID:           livestreamID,
			UserID:       userID,
			Title:        req.Title,
			Description:  req.Description,
			PlaylistUrl:  req.PlaylistUrl,
			ThumbnailUrl: req.ThumbnailUrl,
			StartAt:      req.StartAt,
			EndAt:        req.EndAt,
		}
		entry = &LivestreamCacheEntry{
			Model: model,
			Tags:  tags,
		}
		addLivestreamCache(entry)

		var sd *dbSlotDec
		if matchedSlots {
			sd = &dbSlotDec{StartAt: req.StartAt, EndAt: req.EndAt}
		}
		enqueueReservation(sd, dbLivestreamInsert{Model: model, Tags: req.Tags})
	}

	b, ok := entry.getJSON()
	if !ok {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestream")
	}

	return c.Blob(http.StatusCreated, echo.MIMEApplicationJSONCharsetUTF8, b)
}

func searchLivestreamsHandler(c echo.Context) error {
	keyTagName := c.QueryParam("tag")
	limitStr := c.QueryParam("limit")
	cacheKey := "t:" + keyTagName + "|l:" + limitStr
	if cached, ok := getCachedSearch(cacheKey); ok {
		return c.Blob(http.StatusOK, echo.MIMEApplicationJSONCharsetUTF8, cached)
	}
	ver := searchCacheVer.Load()

	var entries []*LivestreamCacheEntry
	if keyTagName != "" {
		tagID, ok := getTagIDByName(keyTagName)
		if !ok {
			return c.Blob(http.StatusOK, echo.MIMEApplicationJSONCharsetUTF8, []byte("[]"))
		}
		livestreamsMu.RLock()
		src := livestreamsByTagID[tagID]
		entries = make([]*LivestreamCacheEntry, len(src))
		for i := range src {
			entries[i] = src[len(src)-1-i]
		}
		livestreamsMu.RUnlock()
	} else {
		limit := -1
		if limitStr != "" {
			v, err := strconv.Atoi(limitStr)
			if err != nil {
				return echo.NewHTTPError(http.StatusBadRequest, "limit query parameter must be integer")
			}
			limit = v
		}
		livestreamsMu.RLock()
		total := len(allLivestreams)
		cnt := total
		if limit >= 0 && limit < cnt {
			cnt = limit
		}
		entries = make([]*LivestreamCacheEntry, cnt)
		for i := 0; i < cnt; i++ {
			entries[i] = allLivestreams[total-1-i]
		}
		livestreamsMu.RUnlock()
	}

	b := marshalLivestreamEntriesJSON(entries)
	setCachedSearch(cacheKey, ver, b)
	return c.Blob(http.StatusOK, echo.MIMEApplicationJSONCharsetUTF8, b)
}

func getMyLivestreamsHandler(c echo.Context) error {
	userID, err := verifyAndGetUserID(c)
	if err != nil {
		return err
	}

	livestreamsMu.RLock()
	src := livestreamsByUserID[userID]
	entries := make([]*LivestreamCacheEntry, len(src))
	copy(entries, src)
	livestreamsMu.RUnlock()

	return c.Blob(http.StatusOK, echo.MIMEApplicationJSONCharsetUTF8, marshalLivestreamEntriesJSON(entries))
}

func getUserLivestreamsHandler(c echo.Context) error {
	if _, err := verifyAndGetUserID(c); err != nil {
		return err
	}

	username := c.Param("username")
	u, ok := getUserByName(username)
	if !ok {
		return echo.NewHTTPError(http.StatusNotFound, "user not found")
	}

	livestreamsMu.RLock()
	src := livestreamsByUserID[u.ID]
	entries := make([]*LivestreamCacheEntry, len(src))
	copy(entries, src)
	livestreamsMu.RUnlock()

	return c.Blob(http.StatusOK, echo.MIMEApplicationJSONCharsetUTF8, marshalLivestreamEntriesJSON(entries))
}

func enterLivestreamHandler(c echo.Context) error {
	userID, err := verifyAndGetUserID(c)
	if err != nil {
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id must be integer")
	}
	lsID := int64(livestreamID)
	now := time.Now().Unix()

	viewersMu.Lock()
	viewersCountByUserLS[[2]int64{userID, lsID}]++
	viewersMu.Unlock()

	if ls, ok := getLivestreamEntryByID(lsID); ok {
		statsOnViewerDelta(lsID, ls.Model.UserID, 1)
	}
	enqueueEnterViewer(dbViewerOp{UserID: userID, LivestreamID: lsID, CreatedAt: now})

	return c.NoContent(http.StatusOK)
}

func exitLivestreamHandler(c echo.Context) error {
	userID, err := verifyAndGetUserID(c)
	if err != nil {
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}
	lsID := int64(livestreamID)

	viewersMu.Lock()
	key := [2]int64{userID, lsID}
	n := viewersCountByUserLS[key]
	if n > 0 {
		delete(viewersCountByUserLS, key)
	}
	viewersMu.Unlock()

	if n > 0 {
		if ls, ok := getLivestreamEntryByID(lsID); ok {
			statsOnViewerDelta(lsID, ls.Model.UserID, -n)
		}
	}
	enqueueExitViewer(dbViewerOp{UserID: userID, LivestreamID: lsID})

	return c.NoContent(http.StatusOK)
}

func getLivestreamHandler(c echo.Context) error {
	if _, err := verifyAndGetUserID(c); err != nil {
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	entry, ok := getLivestreamEntryByID(int64(livestreamID))
	if !ok {
		return echo.NewHTTPError(http.StatusNotFound, "not found livestream that has the given id")
	}

	b, ok := entry.getJSON()
	if !ok {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livestream")
	}

	return c.Blob(http.StatusOK, echo.MIMEApplicationJSONCharsetUTF8, b)
}

func getLivecommentReportsHandler(c echo.Context) error {
	userID, err := verifyAndGetUserID(c)
	if err != nil {
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}
	lsID := int64(livestreamID)

	entry, ok := getLivestreamEntryByID(lsID)
	if !ok {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream")
	}

	if entry.Model.UserID != userID {
		return echo.NewHTTPError(http.StatusForbidden, "can't get other streamer's livecomment reports")
	}

	repMu.RLock()
	srcReps := reportsByLS[lsID]
	if len(srcReps) == 0 {
		repMu.RUnlock()
		return c.Blob(http.StatusOK, echo.MIMEApplicationJSONCharsetUTF8, []byte("[]"))
	}
	reps := make([]*LivecommentReportModel, len(srcReps))
	copy(reps, srcReps)
	repMu.RUnlock()

	lsJSON, ok := entry.getJSON()
	if !ok {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to build livestream")
	}

	var buf bytes.Buffer
	buf.Grow(len(reps)*512 + 2)
	buf.WriteByte('[')
	first := true

	lcMu.RLock()
	for _, r := range reps {
		lc, exists := livecommentByID[r.LivecommentID]
		if !exists {
			continue
		}
		reporter, ok := getUserByID(r.UserID)
		if !ok {
			continue
		}
		commentOwner, ok := getUserByID(lc.UserID)
		if !ok {
			continue
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		appendReportJSON(&buf, r.ID, reporter.getJSON(), lc, commentOwner.getJSON(), lsJSON, r.CreatedAt)
	}
	lcMu.RUnlock()

	buf.WriteByte(']')
	return c.Blob(http.StatusOK, echo.MIMEApplicationJSONCharsetUTF8, buf.Bytes())
}
