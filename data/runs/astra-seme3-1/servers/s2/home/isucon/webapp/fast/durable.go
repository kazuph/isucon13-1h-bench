package main

import (
	"encoding/json"
	"fmt"
	"github.com/labstack/echo/v4"
)

type receipt struct {
	done chan struct{}
	err  error
}
type walItem struct {
	data    []byte
	receipt *receipt
}

var walQueue chan walItem
var lastCommit *receipt

func startJournalWriter() {
	walQueue = make(chan walItem, 4096)
	go func() {
		for first := range walQueue {
			batch := []walItem{first}
		drain:
			for len(batch) < 4096 {
				select {
				case v := <-walQueue:
					batch = append(batch, v)
				default:
					break drain
				}
			}
			var err error
			for _, v := range batch {
				if err == nil && len(v.data) > 0 {
					_, err = journal.Write(v.data)
				}
			}
			if err == nil {
				err = journal.Sync()
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
	walQueue <- walItem{data: append(b, '\n'), receipt: r}
	st.apply(e)
	lastCommit = r
	return nil
}

type durableContext struct {
	echo.Context
	unlocker func()
	pending  *receipt
}

func (c *durableContext) unlock() {
	if c.unlocker != nil {
		c.pending = lastCommit
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
