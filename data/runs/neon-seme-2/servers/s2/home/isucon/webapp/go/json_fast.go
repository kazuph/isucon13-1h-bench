package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync"
)

var respBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 8192)
		return &b
	},
}

var reqBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 131072)
		return &b
	},
}

func getRespBuf() *[]byte {
	bp := respBufPool.Get().(*[]byte)
	*bp = (*bp)[:0]
	return bp
}

func putRespBuf(bp *[]byte) {
	if cap(*bp) <= 4*1024*1024 {
		respBufPool.Put(bp)
	}
}

func readBodyPooled(r *http.Request) ([]byte, func(), error) {
	bp := reqBufPool.Get().(*[]byte)
	buf := (*bp)[:0]
	if r.ContentLength > 0 && r.ContentLength < 16*1024*1024 {
		if cap(buf) < int(r.ContentLength) {
			buf = make([]byte, int(r.ContentLength))
		} else {
			buf = buf[:int(r.ContentLength)]
		}
		n, err := io.ReadFull(r.Body, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			*bp = buf[:0]
			reqBufPool.Put(bp)
			return nil, nil, err
		}
		buf = buf[:n]
	} else {
		var tmp [32768]byte
		for {
			n, err := r.Body.Read(tmp[:])
			if n > 0 {
				buf = append(buf, tmp[:n]...)
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				*bp = buf[:0]
				reqBufPool.Put(bp)
				return nil, nil, err
			}
		}
	}
	release := func() {
		if cap(buf) <= 4*1024*1024 {
			*bp = buf[:0]
			reqBufPool.Put(bp)
		}
	}
	return buf, release, nil
}

// Fast extraction of raw base64 slice from {"image":"<base64>"} without encoding/json state machine.
// Returns (b64Slice, true) if fast path succeeded, or (nil, false) to fall back to encoding/json.
func extractIconBase64Fast(body []byte) ([]byte, bool) {
	key := []byte(`"image"`)
	idx := bytes.Index(body, key)
	if idx < 0 {
		return nil, false
	}
	p := idx + len(key)
	for p < len(body) && (body[p] == ' ' || body[p] == '\t' || body[p] == '\r' || body[p] == '\n') {
		p++
	}
	if p >= len(body) || body[p] != ':' {
		return nil, false
	}
	p++
	for p < len(body) && (body[p] == ' ' || body[p] == '\t' || body[p] == '\r' || body[p] == '\n') {
		p++
	}
	if p >= len(body) || body[p] != '"' {
		return nil, false
	}
	start := p + 1
	end := bytes.IndexByte(body[start:], '"')
	if end < 0 {
		return nil, false
	}
	b64 := body[start : start+end]
	// If any backslash escape exists, fall back to standard json
	if bytes.IndexByte(b64, '\\') >= 0 {
		return nil, false
	}
	return b64, true
}

func decodeBase64Exact(b64 []byte) ([]byte, error) {
	dst := make([]byte, base64.StdEncoding.DecodedLen(len(b64)))
	n, err := base64.StdEncoding.Decode(dst, b64)
	if err != nil {
		return nil, err
	}
	return dst[:n], nil
}

// Append a JSON-escaped string (with surrounding quotes) to dst.
// Matches encoding/json escaping including <, >, & and control characters.
const hexDigits = "0123456789abcdef"

func appendJSONString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x20 && c != '"' && c != '\\' && c != '<' && c != '>' && c != '&' {
			// Check for U+2028 / U+2029 (UTF-8: E2 80 A8 / E2 80 A9)
			if c == 0xe2 && i+2 < len(s) && s[i+1] == 0x80 && (s[i+2] == 0xa8 || s[i+2] == 0xa9) {
				dst = append(dst, s[start:i]...)
				if s[i+2] == 0xa8 {
					dst = append(dst, `\u2028`...)
				} else {
					dst = append(dst, `\u2029`...)
				}
				i += 2
				start = i + 1
			}
			continue
		}
		dst = append(dst, s[start:i]...)
		switch c {
		case '"':
			dst = append(dst, `\"`...)
		case '\\':
			dst = append(dst, `\\`...)
		case '\n':
			dst = append(dst, `\n`...)
		case '\r':
			dst = append(dst, `\r`...)
		case '\t':
			dst = append(dst, `\t`...)
		default:
			dst = append(dst, `\u00`...)
			dst = append(dst, hexDigits[c>>4], hexDigits[c&0xf])
		}
		start = i + 1
	}
	dst = append(dst, s[start:]...)
	dst = append(dst, '"')
	return dst
}

func buildUserJSONPrefix(id int64, name, displayName, description string, themeID int64, darkMode bool) []byte {
	b := make([]byte, 0, 256)
	b = append(b, `{"id":`...)
	b = strconv.AppendInt(b, id, 10)
	b = append(b, `,"name":`...)
	b = appendJSONString(b, name)
	if displayName != "" {
		b = append(b, `,"display_name":`...)
		b = appendJSONString(b, displayName)
	}
	if description != "" {
		b = append(b, `,"description":`...)
		b = appendJSONString(b, description)
	}
	b = append(b, `,"theme":{"id":`...)
	b = strconv.AppendInt(b, themeID, 10)
	if darkMode {
		b = append(b, `,"dark_mode":true}`...)
	} else {
		b = append(b, `,"dark_mode":false}`...)
	}
	b = append(b, `,"icon_hash":"`...)
	return b
}

func buildLivestreamJSONMid(title, description, playlistUrl, thumbnailUrl string, tags []Tag, startAt, endAt int64) []byte {
	b := make([]byte, 0, 512)
	b = append(b, `,"title":`...)
	b = appendJSONString(b, title)
	b = append(b, `,"description":`...)
	b = appendJSONString(b, description)
	b = append(b, `,"playlist_url":`...)
	b = appendJSONString(b, playlistUrl)
	b = append(b, `,"thumbnail_url":`...)
	b = appendJSONString(b, thumbnailUrl)
	b = append(b, `,"tags":[`...)
	for i, t := range tags {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, `{"id":`...)
		b = strconv.AppendInt(b, t.ID, 10)
		b = append(b, `,"name":`...)
		b = appendJSONString(b, t.Name)
		b = append(b, '}')
	}
	b = append(b, `],"start_at":`...)
	b = strconv.AppendInt(b, startAt, 10)
	b = append(b, `,"end_at":`...)
	b = strconv.AppendInt(b, endAt, 10)
	b = append(b, '}')
	return b
}

func writeBufJSON(w http.ResponseWriter, status int, b []byte) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=UTF-8")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func writeJSONFast(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	writeBufJSON(w, status, b)
}
