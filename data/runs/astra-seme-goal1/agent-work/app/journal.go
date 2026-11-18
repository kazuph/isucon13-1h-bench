package main

import (
	"golang.org/x/sys/unix"
	"os"
	"sync/atomic"
	"time"
)

var syncCalls atomic.Int64
var syncNanos atomic.Int64
var syncRequests atomic.Int64

// Every successful mutation waits for a completed fsync covering its write.
// The sync worker batches waiters, without holding the domain-state mutex.
type durableWAL struct {
	file     *os.File
	requests chan chan error
	done     chan struct{}
}

var durability *durableWAL

func newDurableWAL(f *os.File) *durableWAL {
	d := &durableWAL{file: f, requests: make(chan chan error, 65536), done: make(chan struct{})}
	go func() {
		defer close(d.done)
		for first := range d.requests {
			batch := []chan error{first}
		drain:
			for {
				select {
				case ch, ok := <-d.requests:
					if !ok {
						break drain
					}
					batch = append(batch, ch)
				default:
					break drain
				}
			}
			start := time.Now()
			err := unix.Fdatasync(int(d.file.Fd()))
			for err == unix.EINTR {
				err = unix.Fdatasync(int(d.file.Fd()))
			}
			syncCalls.Add(1)
			syncRequests.Add(int64(len(batch)))
			syncNanos.Add(time.Since(start).Nanoseconds())
			for _, ch := range batch {
				ch <- err
			}
		}
	}()
	return d
}
func (d *durableWAL) barrier() <-chan error { ch := make(chan error, 1); d.requests <- ch; return ch }
func (d *durableWAL) close() error          { err := <-d.barrier(); close(d.requests); <-d.done; return err }
