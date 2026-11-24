package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

type PostLivecommentRequest struct {
	Comment string `json:"comment"`
	Tip     int64  `json:"tip"`
}

type LivecommentModel struct {
	ID           int64  `db:"id"`
	UserID       int64  `db:"user_id"`
	LivestreamID int64  `db:"livestream_id"`
	Comment      string `db:"comment"`
	Tip          int64  `db:"tip"`
	CreatedAt    int64  `db:"created_at"`
	LowerComment string `db:"-"`
}

type LivecommentReportModel struct {
	ID            int64 `db:"id"`
	UserID        int64 `db:"user_id"`
	LivestreamID  int64 `db:"livestream_id"`
	LivecommentID int64 `db:"livecomment_id"`
	CreatedAt     int64 `db:"created_at"`
}

type ModerateRequest struct {
	NGWord string `json:"ng_word"`
}

type NGWord struct {
	ID           int64  `json:"id" db:"id"`
	UserID       int64  `json:"user_id" db:"user_id"`
	LivestreamID int64  `json:"livestream_id" db:"livestream_id"`
	Word         string `json:"word" db:"word"`
	CreatedAt    int64  `json:"created_at" db:"created_at"`
	LowerWord    string `json:"-" db:"-"`
	HasWildcard  bool   `json:"-" db:"-"`
}

func getLivecommentsHandler(c echo.Context) error {
	if _, err := verifyAndGetUserID(c); err != nil {
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}
	lsID := int64(livestreamID)

	limit := -1
	if limitStr := c.QueryParam("limit"); limitStr != "" {
		v, err := strconv.Atoi(limitStr)
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "limit query parameter must be integer")
		}
		limit = v
	}

	lcMu.RLock()
	src := livecommentsByLS[lsID]
	total := len(src)
	if total == 0 || limit == 0 {
		lcMu.RUnlock()
		return c.Blob(http.StatusOK, echo.MIMEApplicationJSONCharsetUTF8, []byte("[]"))
	}
	cnt := total
	if limit > 0 && limit < cnt {
		cnt = limit
	}
	models := make([]*LivecommentModel, cnt)
	for i := 0; i < cnt; i++ {
		models[i] = src[total-1-i]
	}
	lcMu.RUnlock()

	lsEntry, ok := getLivestreamEntryByID(lsID)
	if !ok {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get livestream")
	}
	lsJSON, ok := lsEntry.getJSON()
	if !ok {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to build livestream")
	}

	buf := getPooledBuf()
	defer putPooledBuf(buf)
	buf.Grow(cnt*(len(lsJSON)+256) + 2)
	buf.WriteByte('[')
	for i, m := range models {
		u, ok := getUserByID(m.UserID)
		if !ok {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to get comment user")
		}
		if i > 0 {
			buf.WriteByte(',')
		}
		appendLivecommentJSON(buf, m.ID, u.getJSON(), lsJSON, m.Comment, m.Tip, m.CreatedAt)
	}
	buf.WriteByte(']')

	return c.Blob(http.StatusOK, echo.MIMEApplicationJSONCharsetUTF8, buf.Bytes())
}

func getNgwords(c echo.Context) error {
	userID, err := verifyAndGetUserID(c)
	if err != nil {
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}

	ngWords := getNGWordsForUserAndLivestream(userID, int64(livestreamID))
	return c.JSON(http.StatusOK, ngWords)
}

func postLivecommentHandler(c echo.Context) error {
	defer c.Request().Body.Close()

	userID, err := verifyAndGetUserID(c)
	if err != nil {
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}
	lsID := int64(livestreamID)

	var req PostLivecommentRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	lsEntry, ok := getLivestreamEntryByID(lsID)
	if !ok {
		return echo.NewHTTPError(http.StatusNotFound, "livestream not found")
	}

	lowerComment := strings.ToLower(req.Comment)
	ngwords := getNGWordsSnapshot(lsID)
	for _, nw := range ngwords {
		if nw.UserID == lsEntry.Model.UserID && matchesNGWordFast(lowerComment, nw) {
			return echo.NewHTTPError(http.StatusBadRequest, "このコメントがスパム判定されました")
		}
	}

	now := time.Now().Unix()
	lcID := allocShardedID(&nextLivecommentID)
	m := &LivecommentModel{
		ID:           lcID,
		UserID:       userID,
		LivestreamID: lsID,
		Comment:      req.Comment,
		Tip:          req.Tip,
		CreatedAt:    now,
		LowerComment: lowerComment,
	}

	lcMu.Lock()
	livecommentsByLS[lsID] = append(livecommentsByLS[lsID], m)
	livecommentByID[lcID] = m
	lcMu.Unlock()

	statsOnLivecomment(lsID, lsEntry.Model.UserID, req.Tip)
	recordTipMetric(req.Tip)
	enqueueLivecomment(*m)

	u, ok := getUserByID(userID)
	if !ok {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livecomment user")
	}
	lsJSON, ok := lsEntry.getJSON()
	if !ok {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to fill livecomment livestream")
	}

	buf := getPooledBuf()
	defer putPooledBuf(buf)
	buf.Grow(len(lsJSON) + 300)
	appendLivecommentJSON(buf, lcID, u.getJSON(), lsJSON, req.Comment, req.Tip, now)
	return c.Blob(http.StatusCreated, echo.MIMEApplicationJSONCharsetUTF8, buf.Bytes())
}

func reportLivecommentHandler(c echo.Context) error {
	userID, err := verifyAndGetUserID(c)
	if err != nil {
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}
	lsID := int64(livestreamID)

	livecommentID, err := strconv.Atoi(c.Param("livecomment_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livecomment_id in path must be integer")
	}
	lcID := int64(livecommentID)

	lsEntry, ok := getLivestreamEntryByID(lsID)
	if !ok {
		return echo.NewHTTPError(http.StatusNotFound, "livestream not found")
	}

	lcMu.RLock()
	lcModel, exists := livecommentByID[lcID]
	lcMu.RUnlock()
	if !exists {
		return echo.NewHTTPError(http.StatusNotFound, "livecomment not found")
	}

	now := time.Now().Unix()
	reportID := allocShardedID(&nextReportID)
	rep := &LivecommentReportModel{
		ID:            reportID,
		UserID:        userID,
		LivestreamID:  lsID,
		LivecommentID: lcID,
		CreatedAt:     now,
	}

	repMu.Lock()
	reportsByLS[lsID] = append(reportsByLS[lsID], rep)
	repMu.Unlock()

	statsOnReport(lsID)
	enqueueReport(*rep)

	reporter, ok := getUserByID(userID)
	if !ok {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get reporter")
	}
	commentOwner, ok := getUserByID(lcModel.UserID)
	if !ok {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to get comment owner")
	}
	lsJSON, ok := lsEntry.getJSON()
	if !ok {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to build livestream")
	}

	var buf bytes.Buffer
	buf.Grow(len(lsJSON) + 512)
	appendReportJSON(&buf, reportID, reporter.getJSON(), lcModel, commentOwner.getJSON(), lsJSON, now)
	return c.Blob(http.StatusCreated, echo.MIMEApplicationJSONCharsetUTF8, buf.Bytes())
}

// NGワードを登録
func moderateHandler(c echo.Context) error {
	defer c.Request().Body.Close()

	userID, err := verifyAndGetUserID(c)
	if err != nil {
		return err
	}

	livestreamID, err := strconv.Atoi(c.Param("livestream_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "livestream_id in path must be integer")
	}
	lsID := int64(livestreamID)

	var req ModerateRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "failed to decode the request body as json")
	}

	lsEntry, ok := getLivestreamEntryByID(lsID)
	if !ok || lsEntry.Model.UserID != userID {
		return echo.NewHTTPError(http.StatusBadRequest, "A streamer can't moderate livestreams that other streamers own")
	}

	now := time.Now().Unix()
	wordID := allocShardedID(&nextNGWordID)
	nw := &NGWord{
		ID:           wordID,
		UserID:       userID,
		LivestreamID: lsID,
		Word:         req.NGWord,
		CreatedAt:    now,
		LowerWord:    strings.ToLower(req.NGWord),
		HasWildcard:  strings.ContainsAny(req.NGWord, "%_\\"),
	}
	addNGWordCache(nw)
	enqueueNGWord(*nw)

	var deleteIDs []int64
	var delTip int64
	var newMaxTip int64

	// Only need to match against the NEW NG word (prior NG words already filtered prior comments)
	lcMu.Lock()
	list := livecommentsByLS[lsID]
	if len(list) > 0 {
		kept := list[:0]
		for _, lc := range list {
			if matchesNGWordFast(lc.LowerComment, nw) {
				deleteIDs = append(deleteIDs, lc.ID)
				delTip += lc.Tip
				delete(livecommentByID, lc.ID)
			} else {
				kept = append(kept, lc)
				if lc.Tip > newMaxTip {
					newMaxTip = lc.Tip
				}
			}
		}
		livecommentsByLS[lsID] = kept
	}
	lcMu.Unlock()

	if len(deleteIDs) > 0 {
		statsOnModerateDelete(lsID, lsEntry.Model.UserID, int64(len(deleteIDs)), delTip, newMaxTip, true)
		enqueueDeleteLivecomments(deleteIDs)
	}

	return c.JSON(http.StatusCreated, map[string]interface{}{
		"word_id": wordID,
	})
}
