package main

import (
	"encoding/json"
	"strconv"
)

func appendUser(b []byte, u User) []byte {
	if u.rawJSON != nil {
		return append(b, u.rawJSON...)
	}
	v, _ := json.Marshal(u)
	return append(b, v...)
}
func appendStream(b []byte, s Livestream) []byte {
	if s.rawJSON != nil {
		return append(b, s.rawJSON...)
	}
	v, _ := json.Marshal(s)
	return append(b, v...)
}
func appendComment(b []byte, c Livecomment) []byte {
	b = append(b, `{"id":`...)
	b = strconv.AppendInt(b, c.ID, 10)
	b = append(b, `,"user":`...)
	b = appendUser(b, c.User)
	b = append(b, `,"livestream":`...)
	b = appendStream(b, c.Livestream)
	b = append(b, `,"comment":`...)
	if c.rawComment != nil {
		b = append(b, c.rawComment...)
	} else {
		v, _ := json.Marshal(c.Comment)
		b = append(b, v...)
	}
	b = append(b, `,"tip":`...)
	b = strconv.AppendInt(b, c.Tip, 10)
	b = append(b, `,"created_at":`...)
	b = strconv.AppendInt(b, c.CreatedAt, 10)
	return append(b, '}')
}
func appendReaction(b []byte, r Reaction) []byte {
	b = append(b, `{"id":`...)
	b = strconv.AppendInt(b, r.ID, 10)
	b = append(b, `,"emoji_name":`...)
	v, _ := json.Marshal(r.EmojiName)
	b = append(b, v...)
	b = append(b, `,"user":`...)
	b = appendUser(b, r.User)
	b = append(b, `,"livestream":`...)
	b = appendStream(b, r.Livestream)
	b = append(b, `,"created_at":`...)
	b = strconv.AppendInt(b, r.CreatedAt, 10)
	return append(b, '}')
}
func appendReport(b []byte, r LivecommentReport) []byte {
	b = append(b, `{"id":`...)
	b = strconv.AppendInt(b, r.ID, 10)
	b = append(b, `,"reporter":`...)
	b = appendUser(b, r.Reporter)
	b = append(b, `,"livecomment":`...)
	b = appendComment(b, r.Livecomment)
	b = append(b, `,"created_at":`...)
	b = strconv.AppendInt(b, r.CreatedAt, 10)
	return append(b, '}')
}
func encodeResponse(value interface{}) ([]byte, error) {
	switch v := value.(type) {
	case User:
		return appendUser(nil, v), nil
	case Livestream:
		return appendStream(nil, v), nil
	case Livecomment:
		return appendComment(make([]byte, 0, 2048), v), nil
	case Reaction:
		return appendReaction(make([]byte, 0, 2048), v), nil
	case LivecommentReport:
		return appendReport(make([]byte, 0, 4096), v), nil
	case []Livestream:
		if v == nil {
			return []byte("null"), nil
		}
		n := 2 + len(v)
		for _, s := range v {
			n += len(s.rawJSON)
		}
		b := make([]byte, 0, n)
		b = append(b, '[')
		for i, s := range v {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendStream(b, s)
		}
		return append(b, ']'), nil
	case []Livecomment:
		if v == nil {
			return []byte("null"), nil
		}
		n := 2 + len(v)*128
		for _, c := range v {
			n += len(c.User.rawJSON) + len(c.Livestream.rawJSON) + len(c.rawComment)
		}
		b := make([]byte, 0, n)
		b = append(b, '[')
		for i, c := range v {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendComment(b, c)
		}
		return append(b, ']'), nil
	case []Reaction:
		if v == nil {
			return []byte("null"), nil
		}
		n := 2 + len(v)*128
		for _, r := range v {
			n += len(r.User.rawJSON) + len(r.Livestream.rawJSON)
		}
		b := make([]byte, 0, n)
		b = append(b, '[')
		for i, r := range v {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendReaction(b, r)
		}
		return append(b, ']'), nil
	case []LivecommentReport:
		if v == nil {
			return []byte("null"), nil
		}
		b := make([]byte, 0, len(v)*2048+2)
		b = append(b, '[')
		for i, r := range v {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendReport(b, r)
		}
		return append(b, ']'), nil
	default:
		return json.Marshal(value)
	}
}
