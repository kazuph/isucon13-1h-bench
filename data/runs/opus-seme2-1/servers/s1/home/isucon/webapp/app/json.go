package main

import (
	"strconv"
	"unicode/utf8"
)

const hexd = "0123456789abcdef"

func appendStr(b []byte, s string) []byte {
	b = append(b, '"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if c >= 0x20 && c != '"' && c != '\\' && c != '<' && c != '>' && c != '&' {
				i++
				continue
			}
			b = append(b, s[start:i]...)
			switch c {
			case '"':
				b = append(b, '\\', '"')
			case '\\':
				b = append(b, '\\', '\\')
			case '\n':
				b = append(b, '\\', 'n')
			case '\r':
				b = append(b, '\\', 'r')
			case '\t':
				b = append(b, '\\', 't')
			default:
				b = append(b, '\\', 'u', '0', '0', hexd[c>>4], hexd[c&0xf])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b = append(b, s[start:i]...)
			b = append(b, `�`...)
			i += size
			start = i
			continue
		}
		if r == ' ' || r == ' ' {
			b = append(b, s[start:i]...)
			b = append(b, '\\', 'u', '2', '0', '2', hexd[r&0xf])
			i += size
			start = i
			continue
		}
		i += size
	}
	b = append(b, s[start:]...)
	return append(b, '"')
}

func appendInt(b []byte, n int64) []byte { return strconv.AppendInt(b, n, 10) }

func appendTag(b []byte, t *Tag) []byte {
	b = append(b, `{"id":`...)
	b = appendInt(b, t.ID)
	b = append(b, `,"name":`...)
	b = appendStr(b, t.Name)
	return append(b, '}')
}

func buildUserJSON(u *User) []byte {
	b := make([]byte, 0, 256)
	b = append(b, `{"id":`...)
	b = appendInt(b, u.ID)
	b = append(b, `,"name":`...)
	b = appendStr(b, u.Name)
	if u.DisplayName != "" {
		b = append(b, `,"display_name":`...)
		b = appendStr(b, u.DisplayName)
	}
	if u.Description != "" {
		b = append(b, `,"description":`...)
		b = appendStr(b, u.Description)
	}
	b = append(b, `,"theme":{"id":`...)
	b = appendInt(b, u.ThemeID)
	if u.Dark {
		b = append(b, `,"dark_mode":true}`...)
	} else {
		b = append(b, `,"dark_mode":false}`...)
	}
	if u.IconHash != "" {
		b = append(b, `,"icon_hash":"`...)
		b = append(b, u.IconHash...)
		b = append(b, '"')
	}
	return append(b, '}')
}

func buildLSJSON(l *Livestream) []byte {
	b := make([]byte, 0, 512+len(l.Owner.json))
	b = append(b, `{"id":`...)
	b = appendInt(b, l.ID)
	b = append(b, `,"owner":`...)
	b = append(b, l.Owner.json...)
	b = append(b, `,"title":`...)
	b = appendStr(b, l.Title)
	b = append(b, `,"description":`...)
	b = appendStr(b, l.Description)
	b = append(b, `,"playlist_url":`...)
	b = appendStr(b, l.PlaylistURL)
	b = append(b, `,"thumbnail_url":`...)
	b = appendStr(b, l.ThumbnailURL)
	b = append(b, `,"tags":[`...)
	for i, t := range l.Tags {
		if i > 0 {
			b = append(b, ',')
		}
		b = appendTag(b, t)
	}
	b = append(b, `],"start_at":`...)
	b = appendInt(b, l.StartAt)
	b = append(b, `,"end_at":`...)
	b = appendInt(b, l.EndAt)
	return append(b, '}')
}

func appendComment(b []byte, c *Comment) []byte {
	b = append(b, `{"id":`...)
	b = appendInt(b, c.ID)
	b = append(b, `,"user":`...)
	b = append(b, c.User.json...)
	b = append(b, `,"livestream":`...)
	b = append(b, c.LS.json...)
	b = append(b, `,"comment":`...)
	b = appendStr(b, c.Comment)
	b = append(b, `,"tip":`...)
	b = appendInt(b, c.Tip)
	b = append(b, `,"created_at":`...)
	b = appendInt(b, c.CreatedAt)
	return append(b, '}')
}

func appendReaction(b []byte, r *Reaction) []byte {
	b = append(b, `{"id":`...)
	b = appendInt(b, r.ID)
	b = append(b, `,"emoji_name":`...)
	b = appendStr(b, r.Emoji)
	b = append(b, `,"user":`...)
	b = append(b, r.User.json...)
	b = append(b, `,"livestream":`...)
	b = append(b, r.LS.json...)
	b = append(b, `,"created_at":`...)
	b = appendInt(b, r.CreatedAt)
	return append(b, '}')
}

func appendReport(b []byte, r *Report) []byte {
	b = append(b, `{"id":`...)
	b = appendInt(b, r.ID)
	b = append(b, `,"reporter":`...)
	b = append(b, r.User.json...)
	b = append(b, `,"livecomment":`...)
	b = appendComment(b, r.Comment)
	b = append(b, `,"created_at":`...)
	b = appendInt(b, r.CreatedAt)
	return append(b, '}')
}

func appendNG(b []byte, n *NGWord) []byte {
	b = append(b, `{"id":`...)
	b = appendInt(b, n.ID)
	b = append(b, `,"user_id":`...)
	b = appendInt(b, n.UserID)
	b = append(b, `,"livestream_id":`...)
	b = appendInt(b, n.LivestreamID)
	b = append(b, `,"word":`...)
	b = appendStr(b, n.Word)
	b = append(b, `,"created_at":`...)
	b = appendInt(b, n.CreatedAt)
	return append(b, '}')
}
