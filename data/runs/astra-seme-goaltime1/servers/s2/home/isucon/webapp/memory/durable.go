package main

import (
	"bufio"
	"encoding/json"
	"github.com/labstack/echo/v4"
	"os"
	"sync/atomic"
	"time"
)

type walTicket struct {
	done chan struct{}
	err  error
}
type walJob struct {
	data   []byte
	ticket *walTicket
}

var walQueue = make(chan walJob, 1024)
var walStarted bool
var latestTicket atomic.Pointer[walTicket]

func startWAL() {
	walStarted = true
	go func() {
		w := bufio.NewWriterSize(nil, 256*1024)
		buffered := os.Getenv("WAL_SYNC_MODE") == "buffered"
		lastSync := time.Now()
		for first := range walQueue {
			jobs := []walJob{first}
		collect:
			for len(jobs) < 256 {
				select {
				case j := <-walQueue:
					jobs = append(jobs, j)
				default:
					break collect
				}
			}

			w.Reset(journal)
			var err error
			forceSync := !buffered
			for _, j := range jobs {
				if len(j.data) == 0 {
					forceSync = true
				}
				if err == nil && len(j.data) > 0 {
					_, err = w.Write(j.data)
				}
			}
			if err == nil {
				err = w.Flush()
			}
			if err == nil && forceSync {
				err = journal.Sync()
				lastSync = time.Now()
			}
			for _, j := range jobs {
				j.ticket.err = err
				close(j.ticket.done)
			}
			if err != nil {
				panic(err)
			}
			if buffered && !forceSync && time.Since(lastSync) >= 100*time.Millisecond {
				if e := journal.Sync(); e != nil {
					panic(e)
				}
				lastSync = time.Now()
			}
		}
	}()
}
func flushWAL() error {
	if !walStarted {
		return nil
	}
	t := &walTicket{done: make(chan struct{})}
	walQueue <- walJob{ticket: t}
	<-t.done
	return t.err
}
func commit(c echo.Context, e Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	t := &walTicket{done: make(chan struct{})}
	walQueue <- walJob{data: b, ticket: t}
	latestTicket.Store(t)
	if c != nil {
		c.Set("walTicket", t)
	}
	apply(e)
	return nil
}

type pendingResponse struct {
	code        int
	value       interface{}
	blob        []byte
	contentType string
	ticket      *walTicket
}

func reply(c echo.Context, code int, value interface{}) error {
	c.Set("pendingResponse", pendingResponse{code: code, value: value, ticket: requestTicket(c)})
	return nil
}
func replyBlob(c echo.Context, code int, contentType string, b []byte) error {
	c.Set("pendingResponse", pendingResponse{code: code, blob: b, contentType: contentType, ticket: requestTicket(c)})
	return nil
}
func deferredResponse(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if err := next(c); err != nil {
			return err
		}
		v, ok := c.Get("pendingResponse").(pendingResponse)
		if !ok {
			return nil
		}
		if v.ticket != nil {
			<-v.ticket.done
			if v.ticket.err != nil {
				return v.ticket.err
			}
		}
		if v.contentType != "" {
			return c.Blob(v.code, v.contentType, v.blob)
		}
		if v.value == nil {
			return c.NoContent(v.code)
		}
		b, err := encodeResponse(v.value)
		if err != nil {
			return err
		}
		return c.JSONBlob(v.code, append(b, '\n'))
	}
}

// Keep the log compatible with the original event decoder while omitting unrelated fields.
func (e Event) MarshalJSON() ([]byte, error) {
	m := map[string]interface{}{"Kind": e.Kind}
	switch e.Kind {
	case "user":
		m["User"] = e.User
		m["Theme"] = e.Theme
	case "icon":
		m["ID"] = e.ID
		m["UserID"] = e.UserID
		if e.ImageHash != "" {
			m["ImageHash"] = e.ImageHash
		} else {
			m["Image"] = e.Image
		}
	case "stream":
		m["Stream"] = e.Stream
		m["Tags"] = e.Tags
	case "comment":
		m["Comment"] = e.Comment
	case "reaction":
		m["Reaction"] = e.Reaction
	case "report":
		m["Report"] = e.Report
	case "word":
		m["Word"] = e.Word
	case "enter", "exit":
		m["Viewer"] = e.Viewer
	}
	return json.Marshal(m)
}

func requestTicket(c echo.Context) *walTicket { t, _ := c.Get("walTicket").(*walTicket); return t }
