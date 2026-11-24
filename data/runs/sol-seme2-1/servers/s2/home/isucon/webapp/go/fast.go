package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/go-sql-driver/mysql"
	"github.com/labstack/echo/v4"
	"net/http"
	"strconv"
	"time"
)

func fastPostReaction(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	lid, err := memID(c)
	if err != nil {
		return err
	}
	uid := memUID(c)
	var req PostReactionRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(400, "invalid json")
	}
	mem.RLock()
	_, exists := mem.streams[lid]
	mem.RUnlock()
	if !exists {
		return echo.NewHTTPError(404, "livestream not found")
	}
	m := ReactionModel{UserID: uid, LivestreamID: lid, EmojiName: req.EmojiName, CreatedAt: time.Now().Unix()}
	r, err := execWrite(c.Request().Context(), "INSERT INTO reactions(user_id,livestream_id,emoji_name,created_at) VALUES(?,?,?,?)", uid, lid, m.EmojiName, m.CreatedAt)
	if err != nil {
		return err
	}
	m.ID, err = r.LastInsertId()
	if err != nil {
		return err
	}
	memAddReaction(m)
	mem.RLock()
	out := mem.reaction(m.ID)
	mem.RUnlock()
	return c.JSON(201, out)
}
func fastPostComment(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	lid, err := memID(c)
	if err != nil {
		return err
	}
	uid := memUID(c)
	var req PostLivecommentRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(400, "invalid json")
	}
	unlock := lockStream(lid)
	defer unlock()
	mem.RLock()
	_, exists := mem.streams[lid]
	spam := false
	for _, w := range mem.words[lid] {
		if likeWord(req.Comment, w.Word) {
			spam = true
			break
		}
	}
	mem.RUnlock()
	if !exists {
		return echo.NewHTTPError(404, "livestream not found")
	}
	if spam {
		return echo.NewHTTPError(400, "このコメントがスパム判定されました")
	}
	m := LivecommentModel{UserID: uid, LivestreamID: lid, Comment: req.Comment, Tip: req.Tip, CreatedAt: time.Now().Unix()}
	r, err := execWrite(c.Request().Context(), "INSERT INTO livecomments(user_id,livestream_id,comment,tip,created_at) VALUES(?,?,?,?,?)", uid, lid, m.Comment, m.Tip, m.CreatedAt)
	if err != nil {
		return err
	}
	m.ID, err = r.LastInsertId()
	if err != nil {
		return err
	}
	memAddComment(m)
	mem.RLock()
	out := mem.comment(m.ID)
	mem.RUnlock()
	return c.JSON(201, out)
}
func fastEnter(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	lid, err := memID(c)
	if err != nil {
		return err
	}
	uid := memUID(c)
	unlock := lockStream(lid + 1000000000000)
	defer unlock()
	_, err = execWrite(c.Request().Context(), "INSERT INTO livestream_viewers_history(user_id,livestream_id,created_at) VALUES(?,?,?)", uid, lid, time.Now().Unix())
	if err != nil {
		return err
	}
	memEnter(uid, lid)
	return c.NoContent(http.StatusOK)
}
func fastExit(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	lid, err := memID(c)
	if err != nil {
		return err
	}
	uid := memUID(c)
	unlock := lockStream(lid + 1000000000000)
	defer unlock()
	_, err = execWrite(c.Request().Context(), "DELETE FROM livestream_viewers_history WHERE user_id=? AND livestream_id=?", uid, lid)
	if err != nil {
		return err
	}
	memExit(uid, lid)
	return c.NoContent(http.StatusOK)
}
func fastPostIcon(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	uid := memUID(c)
	var req PostIconRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(400, "invalid json")
	}
	unlock := lockStream(-uid)
	defer unlock()
	r, err := execWrite(c.Request().Context(), "INSERT INTO icons(user_id,image) VALUES(?,?) ON DUPLICATE KEY UPDATE id=LAST_INSERT_ID(VALUES(id)),image=VALUES(image)", uid, req.Image)
	if err != nil {
		return err
	}
	id, err := r.LastInsertId()
	if err != nil {
		return err
	}
	memIcon(uid, req.Image)
	return c.JSON(201, PostIconResponse{ID: id})
}
func fastPostReport(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	lid, err := memID(c)
	if err != nil {
		return err
	}
	uid := memUID(c)
	cid, err := strconv.ParseInt(c.Param("livecomment_id"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(400, "invalid livecomment id")
	}
	unlock := lockStream(lid)
	defer unlock()
	mem.RLock()
	_, exists := mem.streams[lid]
	_, commentExists := mem.comments[cid]
	active := false
	for _, id := range mem.commentIDs[lid] {
		if id == cid {
			active = true
			break
		}
	}
	mem.RUnlock()
	if !exists || !commentExists || !active {
		return echo.NewHTTPError(404, "livecomment not found")
	}
	m := LivecommentReportModel{UserID: uid, LivestreamID: lid, LivecommentID: cid, CreatedAt: time.Now().Unix()}
	r, err := execWrite(c.Request().Context(), "INSERT INTO livecomment_reports(user_id,livestream_id,livecomment_id,created_at) VALUES(?,?,?,?)", uid, lid, cid, m.CreatedAt)
	if err != nil {
		return err
	}
	m.ID, err = r.LastInsertId()
	if err != nil {
		return err
	}
	memAddReport(m)
	mem.RLock()
	out := LivecommentReport{ID: m.ID, Reporter: mem.users[uid], Livecomment: mem.comment(cid), CreatedAt: m.CreatedAt}
	mem.RUnlock()
	return c.JSON(201, out)
}

// Each caller issues one autocommit statement. A deadlock rolls the statement
// back, so it can be retried without duplicating a committed update.
func execWrite(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	var result sql.Result
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		result, err = dbConn.ExecContext(ctx, query, args...)
		if err == nil {
			return result, nil
		}
		var me *mysql.MySQLError
		if !errors.As(err, &me) || me.Number != 1213 {
			return result, err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return result, err
}
