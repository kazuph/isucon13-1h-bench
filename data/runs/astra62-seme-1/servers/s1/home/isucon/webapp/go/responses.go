package main

import (
	"encoding/json"
	"strconv"

	"github.com/labstack/echo/v4"
)

func encodeCatalog(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
func (d *catalogData) refreshUser(u User) {
	d.users[u.ID] = u
	d.names[u.Name] = u.ID
	d.userJSON[u.ID] = encodeCatalog(u)
	for _, id := range d.byOwner[u.ID] {
		s := d.streams[id]
		s.Owner = u
		d.streams[id] = s
		d.streamJSON[id] = encodeCatalog(s)
	}
}
func appendComment(buf []byte, m LivecommentModel) []byte {
	buf = append(buf, `{"id":`...)
	buf = strconv.AppendInt(buf, m.ID, 10)
	buf = append(buf, `,"user":`...)
	buf = append(buf, catalog.userJSON[m.UserID]...)
	buf = append(buf, `,"livestream":`...)
	buf = append(buf, catalog.streamJSON[m.LivestreamID]...)
	buf = append(buf, `,"comment":`...)
	buf = append(buf, encodeCatalog(m.Comment)...)
	buf = append(buf, `,"tip":`...)
	buf = strconv.AppendInt(buf, m.Tip, 10)
	buf = append(buf, `,"created_at":`...)
	buf = strconv.AppendInt(buf, m.CreatedAt, 10)
	return append(buf, '}')
}
func appendReaction(buf []byte, m ReactionModel) []byte {
	buf = append(buf, `{"id":`...)
	buf = strconv.AppendInt(buf, m.ID, 10)
	buf = append(buf, `,"user":`...)
	buf = append(buf, catalog.userJSON[m.UserID]...)
	buf = append(buf, `,"livestream":`...)
	buf = append(buf, catalog.streamJSON[m.LivestreamID]...)
	buf = append(buf, `,"emoji_name":`...)
	buf = append(buf, encodeCatalog(m.EmojiName)...)
	buf = append(buf, `,"created_at":`...)
	buf = strconv.AppendInt(buf, m.CreatedAt, 10)
	return append(buf, '}')
}
func timelineJSON[T any](c echo.Context, models []T, encode func([]byte, T) []byte) error {
	buf := make([]byte, 1, len(models)*1400+2)
	buf[0] = '['
	catalogMu.RLock()
	for i := len(models) - 1; i >= 0; i-- {
		if i < len(models)-1 {
			buf = append(buf, ',')
		}
		buf = encode(buf, models[i])
	}
	catalogMu.RUnlock()
	return c.JSONBlob(200, append(buf, ']'))
}
