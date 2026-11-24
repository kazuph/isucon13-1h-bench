package main

import (
	"bytes"
	"encoding/json"
	"strconv"
	"sync"
)

var bufPool = sync.Pool{
	New: func() any {
		b := new(bytes.Buffer)
		b.Grow(4096)
		return b
	},
}

func getPooledBuf() *bytes.Buffer {
	b := bufPool.Get().(*bytes.Buffer)
	b.Reset()
	return b
}

func putPooledBuf(b *bytes.Buffer) {
	if b.Cap() <= 256*1024 {
		bufPool.Put(b)
	}
}

func appendJSONString(buf *bytes.Buffer, s string) {
	needsEscape := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == '"' || c == '\\' || c == '<' || c == '>' || c == '&' {
			needsEscape = true
			break
		}
	}
	if !needsEscape {
		buf.WriteByte('"')
		buf.WriteString(s)
		buf.WriteByte('"')
		return
	}
	b, _ := json.Marshal(s)
	buf.Write(b)
}

func appendLivecommentJSON(buf *bytes.Buffer, id int64, userJSON, lsJSON []byte, comment string, tip, createdAt int64) {
	var numBuf [24]byte
	buf.WriteString(`{"id":`)
	buf.Write(strconv.AppendInt(numBuf[:0], id, 10))
	buf.WriteString(`,"user":`)
	buf.Write(userJSON)
	buf.WriteString(`,"livestream":`)
	buf.Write(lsJSON)
	buf.WriteString(`,"comment":`)
	appendJSONString(buf, comment)
	buf.WriteString(`,"tip":`)
	buf.Write(strconv.AppendInt(numBuf[:0], tip, 10))
	buf.WriteString(`,"created_at":`)
	buf.Write(strconv.AppendInt(numBuf[:0], createdAt, 10))
	buf.WriteByte('}')
}

func appendReactionJSON(buf *bytes.Buffer, id int64, emojiName string, userJSON, lsJSON []byte, createdAt int64) {
	var numBuf [24]byte
	buf.WriteString(`{"id":`)
	buf.Write(strconv.AppendInt(numBuf[:0], id, 10))
	buf.WriteString(`,"emoji_name":`)
	appendJSONString(buf, emojiName)
	buf.WriteString(`,"user":`)
	buf.Write(userJSON)
	buf.WriteString(`,"livestream":`)
	buf.Write(lsJSON)
	buf.WriteString(`,"created_at":`)
	buf.Write(strconv.AppendInt(numBuf[:0], createdAt, 10))
	buf.WriteByte('}')
}

func appendReportJSON(buf *bytes.Buffer, id int64, reporterJSON []byte, lc *LivecommentModel, lcUserJSON, lsJSON []byte, createdAt int64) {
	var numBuf [24]byte
	buf.WriteString(`{"id":`)
	buf.Write(strconv.AppendInt(numBuf[:0], id, 10))
	buf.WriteString(`,"reporter":`)
	buf.Write(reporterJSON)
	buf.WriteString(`,"livecomment":`)
	appendLivecommentJSON(buf, lc.ID, lcUserJSON, lsJSON, lc.Comment, lc.Tip, lc.CreatedAt)
	buf.WriteString(`,"created_at":`)
	buf.Write(strconv.AppendInt(numBuf[:0], createdAt, 10))
	buf.WriteByte('}')
}
