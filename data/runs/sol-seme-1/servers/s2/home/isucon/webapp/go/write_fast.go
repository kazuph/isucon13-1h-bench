package main

import (
	"context"
	"encoding/json"
	"github.com/labstack/echo/v4"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func memoryPostComment(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := pathID(c)
	if err != nil {
		return err
	}
	uid := sessionUserID(c)
	var req PostLivecommentRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(400, "invalid JSON")
	}
	unlock := lockStream(id)
	defer unlock()
	state.RLock()
	_, exists := state.streams[id]
	spam := false
	for _, w := range state.words[id] {
		if strings.ContainsAny(w.Word, "%_\\") {
			spam = likePattern(req.Comment, "%"+w.Word+"%")
		} else {
			spam = strings.Contains(req.Comment, w.Word)
		}
		if spam {
			break
		}
	}
	state.RUnlock()
	if !exists {
		return echo.NewHTTPError(404, "stream not found")
	}
	if spam {
		return echo.NewHTTPError(400, "このコメントがスパム判定されました")
	}
	model := LivecommentModel{UserID: uid, LivestreamID: id, Comment: req.Comment, Tip: req.Tip, CreatedAt: time.Now().Unix()}
	rs, err := dbConn.ExecContext(context.WithoutCancel(c.Request().Context()), "INSERT INTO livecomments(user_id,livestream_id,comment,tip,created_at) VALUES(?,?,?,?,?)", uid, id, model.Comment, model.Tip, model.CreatedAt)
	if err != nil {
		return err
	}
	model.ID, err = rs.LastInsertId()
	if err != nil {
		return err
	}
	applyComment(model)
	state.RLock()
	response := memoryComment(model)
	state.RUnlock()
	return c.JSON(201, response)
}
func memoryPostReaction(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := pathID(c)
	if err != nil {
		return err
	}
	uid := sessionUserID(c)
	var req PostReactionRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(400, "invalid JSON")
	}
	state.RLock()
	_, exists := state.streams[id]
	state.RUnlock()
	if !exists {
		return echo.NewHTTPError(404, "stream not found")
	}
	m := ReactionModel{UserID: uid, LivestreamID: id, EmojiName: req.EmojiName, CreatedAt: time.Now().Unix()}
	rs, err := dbConn.ExecContext(context.WithoutCancel(c.Request().Context()), "INSERT INTO reactions(user_id,livestream_id,emoji_name,created_at) VALUES(?,?,?,?)", uid, id, m.EmojiName, m.CreatedAt)
	if err != nil {
		return err
	}
	m.ID, err = rs.LastInsertId()
	if err != nil {
		return err
	}
	applyReaction(m)
	state.RLock()
	response := memoryReaction(m)
	state.RUnlock()
	return c.JSON(201, response)
}
func memoryEnter(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := pathID(c)
	if err != nil {
		return err
	}
	uid := sessionUserID(c)
	unlock := lockStream(id)
	defer unlock()
	if _, err := dbConn.ExecContext(context.WithoutCancel(c.Request().Context()), "INSERT INTO livestream_viewers_history(user_id,livestream_id,created_at) VALUES(?,?,?)", uid, id, time.Now().Unix()); err != nil {
		return err
	}
	applyEnter(id, uid)
	return c.NoContent(200)
}
func memoryExit(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := pathID(c)
	if err != nil {
		return err
	}
	uid := sessionUserID(c)
	unlock := lockStream(id)
	defer unlock()
	if _, err := dbConn.ExecContext(context.WithoutCancel(c.Request().Context()), "DELETE FROM livestream_viewers_history WHERE user_id=? AND livestream_id=?", uid, id); err != nil {
		return err
	}
	applyExit(id, uid)
	return c.NoContent(200)
}
func memoryReport(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	id, err := pathID(c)
	if err != nil {
		return err
	}
	cid, err := strconv.ParseInt(c.Param("livecomment_id"), 10, 64)
	if err != nil {
		return echo.NewHTTPError(400, "invalid livecomment_id")
	}
	uid := sessionUserID(c)
	unlock := lockStream(id)
	defer unlock()
	state.RLock()
	cm, exists := state.comments[id][cid]
	reporter := state.users[uid]
	response := LivecommentReport{Reporter: reporter, Livecomment: memoryComment(cm)}
	state.RUnlock()
	if !exists {
		return echo.NewHTTPError(404, "comment not found")
	}
	m := LivecommentReportModel{UserID: uid, LivestreamID: id, LivecommentID: cid, CreatedAt: time.Now().Unix()}
	rs, err := dbConn.ExecContext(context.WithoutCancel(c.Request().Context()), "INSERT INTO livecomment_reports(user_id,livestream_id,livecomment_id,created_at) VALUES(?,?,?,?)", uid, id, cid, m.CreatedAt)
	if err != nil {
		return err
	}
	m.ID, err = rs.LastInsertId()
	if err != nil {
		return err
	}
	applyReport(m)
	response.ID = m.ID
	response.CreatedAt = m.CreatedAt
	return c.JSON(http.StatusCreated, response)
}

func memoryPostIcon(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	uid := sessionUserID(c)
	unlock := lockStream(-uid)
	defer unlock()
	var req PostIconRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return echo.NewHTTPError(400, "invalid JSON")
	}
	rs, err := dbConn.ExecContext(context.WithoutCancel(c.Request().Context()), "REPLACE INTO icons(user_id,image) VALUES(?,?)", uid, req.Image)
	if err != nil {
		return err
	}
	id, err := rs.LastInsertId()
	if err != nil {
		return err
	}
	applyIcon(uid, req.Image)
	return c.JSON(201, PostIconResponse{ID: id})
}
