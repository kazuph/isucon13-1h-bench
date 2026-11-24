package main

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
)

// pre-rendered JSON fragments; built only under write lock (mu.Lock) and
// read under RLock, so no races.

var emptyUserJS, _ = json.Marshal(UserResp{})
var emptyStreamJS, _ = json.Marshal(LivestreamResp{Tags: []Tag{}})

func renderUser(u *User) {
	u.JS, _ = json.Marshal(userResp(u))
}

type streamRender struct {
	ID           int64           `json:"id"`
	Owner        json.RawMessage `json:"owner"`
	Title        string          `json:"title"`
	Description  string          `json:"description"`
	PlaylistUrl  string          `json:"playlist_url"`
	ThumbnailUrl string          `json:"thumbnail_url"`
	Tags         []Tag           `json:"tags"`
	StartAt      int64           `json:"start_at"`
	EndAt        int64           `json:"end_at"`
}

func renderStream(ls *Livestream) {
	owner := emptyUserJS
	if ls.Owner != nil && ls.Owner.JS != nil {
		owner = ls.Owner.JS
	}
	ls.JS, _ = json.Marshal(streamRender{ID: ls.ID, Owner: owner, Title: ls.Title, Description: ls.Description, PlaylistUrl: ls.PlaylistUrl, ThumbnailUrl: ls.ThumbnailUrl, Tags: ls.Tags, StartAt: ls.StartAt, EndAt: ls.EndAt})
}

func userJS(id int64) []byte {
	if u := usersByID[id]; u != nil && u.JS != nil {
		return u.JS
	}
	return emptyUserJS
}

func streamJS(id int64) []byte {
	if ls := streams[id]; ls != nil && ls.JS != nil {
		return ls.JS
	}
	return emptyStreamJS
}

func appendStr(b []byte, s string) []byte {
	js, _ := json.Marshal(s)
	return append(b, js...)
}

func appendComment(b []byte, cm *Comment) []byte {
	b = append(b, `{"id":`...)
	b = strconv.AppendInt(b, cm.ID, 10)
	b = append(b, `,"user":`...)
	b = append(b, userJS(cm.UserID)...)
	b = append(b, `,"livestream":`...)
	b = append(b, streamJS(cm.LivestreamID)...)
	b = append(b, `,"comment":`...)
	b = appendStr(b, cm.Comment)
	b = append(b, `,"tip":`...)
	b = strconv.AppendInt(b, cm.Tip, 10)
	b = append(b, `,"created_at":`...)
	b = strconv.AppendInt(b, cm.CreatedAt, 10)
	return append(b, '}')
}

func appendReaction(b []byte, r *Reaction) []byte {
	b = append(b, `{"id":`...)
	b = strconv.AppendInt(b, r.ID, 10)
	b = append(b, `,"emoji_name":`...)
	b = appendStr(b, r.EmojiName)
	b = append(b, `,"user":`...)
	b = append(b, userJS(r.UserID)...)
	b = append(b, `,"livestream":`...)
	b = append(b, streamJS(r.LivestreamID)...)
	b = append(b, `,"created_at":`...)
	b = strconv.AppendInt(b, r.CreatedAt, 10)
	return append(b, '}')
}

func appendReport(b []byte, r *Report) []byte {
	b = append(b, `{"id":`...)
	b = strconv.AppendInt(b, r.ID, 10)
	b = append(b, `,"reporter":`...)
	b = append(b, userJS(r.UserID)...)
	b = append(b, `,"livecomment":`...)
	if cm := commentsAll[r.LivecommentID]; cm != nil {
		b = appendComment(b, cm)
	} else {
		b = append(b, `{"id":0,"user":`...)
		b = append(b, emptyUserJS...)
		b = append(b, `,"livestream":`...)
		b = append(b, emptyStreamJS...)
		b = append(b, `,"comment":"","tip":0,"created_at":0}`...)
	}
	b = append(b, `,"created_at":`...)
	b = strconv.AppendInt(b, r.CreatedAt, 10)
	return append(b, '}')
}

func jsonBytes(c echo.Context, code int, b []byte) error {
	return c.Blob(code, echo.MIMEApplicationJSONCharsetUTF8, b)
}

var _ = http.StatusOK
