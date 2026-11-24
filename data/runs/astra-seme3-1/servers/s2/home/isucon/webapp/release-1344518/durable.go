package main

import (
	"encoding/json"
	"fmt"
	"github.com/labstack/echo/v4"
	"sync/atomic"
	"time"
)

type receipt struct {
	done chan struct{}
	err  error
}
type walItem struct {
	event   *Event
	data    []byte
	receipt *receipt
}

var walQueue chan walItem
var lastCommit *receipt
var walBatches, walEvents, walSyncNanos atomic.Int64

func startJournalWriter() {
	readMu.Lock()
	committedState = cloneState(st)
	readMu.Unlock()

	q := make(chan walItem, 4096)
	walQueue = q
	go func() {
		for first := range q {
			batch := []walItem{first}
			size := len(first.data)
			if size > 0 {
				timer := time.NewTimer(time.Millisecond)
			gather:
				for len(batch) < 256 && size < 1024*1024 {
					select {
					case v, ok := <-q:
						if !ok {
							break gather
						}
						batch = append(batch, v)
						size += len(v.data)
						if len(v.data) == 0 {
							break gather
						}
					case <-timer.C:
						break gather
					}
				}
				timer.Stop()
			}

			var err error
			for _, v := range batch {
				if err == nil && len(v.data) > 0 {
					_, err = journal.Write(v.data)
				}
			}
			if err == nil {
				start := time.Now()
				err = journal.Sync()
				walSyncNanos.Add(time.Since(start).Nanoseconds())
				walBatches.Add(1)
				walEvents.Add(int64(len(batch)))
			}
			if err == nil {
				readMu.Lock()
				for _, v := range batch {
					if v.event != nil {
						committedState.apply(*v.event)
					}
				}
				readMu.Unlock()
			}
			for _, v := range batch {
				v.receipt.err = err
				close(v.receipt.done)
			}
			if err != nil {
				panic(fmt.Errorf("journal failure: %w", err))
			}
		}
	}()
}
func flushJournal() error {
	if walQueue == nil {
		return nil
	}
	r := &receipt{done: make(chan struct{})}
	walQueue <- walItem{receipt: r}
	<-r.done
	return r.err
}
func persistFor(c echo.Context, e Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	r := &receipt{done: make(chan struct{})}
	walQueue <- walItem{data: append(b, '\n'), receipt: r, event: &e}
	st.apply(e)
	lastCommit = r
	return nil
}

type durableContext struct {
	echo.Context
	unlocker      func()
	pending       *receipt
	captureCommit bool
}

func (c *durableContext) unlock() {
	if c.unlocker != nil {
		if c.captureCommit {
			c.pending = lastCommit
		}
		c.unlocker()
		c.unlocker = nil
	}
}
func (c *durableContext) wait() error {
	c.unlock()
	if c.pending != nil {
		<-c.pending.done
		return c.pending.err
	}
	return nil
}
func (c *durableContext) JSON(code int, v interface{}) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if e = c.wait(); e != nil {
		return e
	}
	return c.Context.JSONBlob(code, append(b, '\n'))
}
func (c *durableContext) NoContent(code int) error {
	if e := c.wait(); e != nil {
		return e
	}
	return c.Context.NoContent(code)
}
func (c *durableContext) Blob(code int, typ string, b []byte) error {
	if e := c.wait(); e != nil {
		return e
	}
	return c.Context.Blob(code, typ, b)
}

func walMetrics(c echo.Context) error {
	return c.JSON(200, map[string]int64{"batches": walBatches.Load(), "events": walEvents.Load(), "sync_ns": walSyncNanos.Load()})
}
