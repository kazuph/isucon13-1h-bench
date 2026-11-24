package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Activity is persisted as an ordered log. Group sync releases the HTTP
// writers only after all events in their batch have reached stable storage.
type activityEvent struct {
	Kind   string `json:"k"`
	ID     int64  `json:"i"`
	Stream int64  `json:"s"`
	User   int64  `json:"u"`
	At     int64  `json:"t"`
	Text   string `json:"x,omitempty"`
	Value  int64  `json:"v,omitempty"`
}
type pendingEvent struct {
	event activityEvent
	done  chan commitResult
}
type commitResult struct {
	id  int64
	err error
}

var activityLog struct {
	file       *os.File
	queue      chan pendingEvent
	sequence   atomic.Int64
	flushError atomic.Pointer[flushFailure]
	fileMu     sync.RWMutex
}

type flushFailure struct{ err error }

func openActivityLog() error {
	path := "../activity.events"
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0640)
	if err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	err = dir.Sync()
	dir.Close()
	if err != nil {
		return err
	}
	activityLog.file = f
	activityLog.queue = make(chan pendingEvent, 1024)
	if err := restoreActivityLog(); err != nil {
		return err
	}
	if os.Getenv("ISUPIPE_SYNC_MODE") == "interval" {
		ms, _ := strconv.Atoi(os.Getenv("ISUPIPE_SYNC_MS"))
		if ms <= 0 {
			ms = 5
		}
		go flushActivity(time.Duration(ms) * time.Millisecond)
	}
	go syncActivity()
	return nil
}
func restoreActivityLog() error {
	var seq int64
	err := dbConn.Get(&seq, "SELECT GREATEST(COALESCE((SELECT MAX(id) FROM livecomments),0),COALESCE((SELECT MAX(id) FROM reactions),0),COALESCE((SELECT MAX(id) FROM ng_words),0),COALESCE((SELECT MAX(id) FROM livecomment_reports),0))")
	if err != nil {
		return err
	}
	f := activityLog.file
	if _, err = f.Seek(0, 0); err != nil {
		return err
	}
	r := bufio.NewReader(f)
	var valid int64
	for {
		line, err := r.ReadBytes('\n')
		if err == io.EOF {
			if len(line) > 0 {
				if err = f.Truncate(valid); err != nil {
					return err
				}
			}
			break
		}
		if err != nil {
			return err
		}
		var e activityEvent
		if err = json.Unmarshal(line, &e); err != nil {
			return fmt.Errorf("activity log at %d: %w", valid, err)
		}
		if err = applyEvent(e); err != nil {
			return err
		}
		valid += int64(len(line))
		if e.ID > seq {
			seq = e.ID
		}
	}
	activityLog.sequence.Store(seq)
	return nil
}
func resetActivityLog() error {
	activityLog.fileMu.Lock()
	defer activityLog.fileMu.Unlock()
	if err := activityLog.file.Truncate(0); err != nil {
		return err
	}
	if err := activityLog.file.Sync(); err != nil {
		return err
	}
	return restoreActivityLog()
}
func flushActivity(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var previous uint64
	for range ticker.C {
		count := committedEvents.Load()
		if count == previous {
			continue
		}
		start := time.Now()
		activityLog.fileMu.RLock()
		err := unix.Fdatasync(int(activityLog.file.Fd()))
		activityLog.fileMu.RUnlock()
		if err != nil {
			activityLog.flushError.Store(&flushFailure{err: err})
			return
		}
		syncTiming.observe(time.Since(start))
		syncBatches.Add(1)
		previous = count
	}
}
func syncActivity() {
	var failure error
	var buf bytes.Buffer
	batch := make([]pendingEvent, 0, 256)
	delay, _ := strconv.Atoi(os.Getenv("ISUPIPE_GROUP_US"))
	mode := os.Getenv("ISUPIPE_SYNC_MODE")
	dataOnly := mode == "data"
	for first := range activityLog.queue {
		batch = append(batch[:0], first)
		if delay > 0 {
			time.Sleep(time.Duration(delay) * time.Microsecond)
		} else if delay < 0 {
			runtime.Gosched()
		}
	drain:
		for len(batch) < cap(batch) {
			select {
			case v := <-activityLog.queue:
				batch = append(batch, v)
			default:
				break drain
			}
		}
		if f := activityLog.flushError.Load(); f != nil {
			failure = f.err
		}
		if failure == nil {
			buf.Reset()
			encoder := json.NewEncoder(&buf)
			for i := range batch {
				e := &batch[i].event
				e.ID = activityLog.sequence.Add(1)
				if failure = encoder.Encode(e); failure != nil {
					break
				}
			}
			if failure == nil {
				_, failure = activityLog.file.Write(buf.Bytes())
			}
			if failure == nil && mode != "interval" {
				start := time.Now()
				if dataOnly {
					failure = unix.Fdatasync(int(activityLog.file.Fd()))
				} else {
					failure = activityLog.file.Sync()
				}
				syncTiming.observe(time.Since(start))
				syncBatches.Add(1)
			}
			if failure == nil {
				committedEvents.Add(uint64(len(batch)))
				activity.Lock()
				for _, p := range batch {
					if failure = applyEvent(p.event); failure != nil {
						break
					}
				}
				activity.Unlock()
			}
		}
		for i, p := range batch {
			p.done <- commitResult{p.event.ID, failure}
			batch[i] = pendingEvent{}
		}
	}
}
func commitEvent(e activityEvent) (int64, error) {
	done := make(chan commitResult, 1)
	activityLog.queue <- pendingEvent{event: e, done: done}
	result := <-done
	return result.id, result.err
}

// Live commits and startup replay apply the exact same ordered events.
func applyEvent(e activityEvent) error {
	if e.Kind == "reserve" {
		return restoreReservation(e)
	}
	s := activity.stream(e.Stream)
	switch e.Kind {
	case "comment":
		activity.addComment(LivecommentModel{ID: e.ID, UserID: e.User, LivestreamID: e.Stream, CreatedAt: e.At, Comment: e.Text, Tip: e.Value})
	case "reaction":
		activity.addReaction(ReactionModel{ID: e.ID, UserID: e.User, LivestreamID: e.Stream, CreatedAt: e.At, EmojiName: e.Text})
	case "report":
		s.reports = append(s.reports, LivecommentReportModel{ID: e.ID, UserID: e.User, LivestreamID: e.Stream, CreatedAt: e.At, LivecommentID: e.Value})
	case "enter":
		s.viewers[e.User]++
		s.viewersCount++
	case "exit":
		s.viewersCount -= s.viewers[e.User]
		delete(s.viewers, e.User)
	case "moderate":
		m := NGWord{ID: e.ID, UserID: e.User, LivestreamID: e.Stream, CreatedAt: e.At, Word: e.Text}
		pattern := patternFor(m.Word)
		s.words = append(s.words, m)
		s.patterns = append(s.patterns, pattern)
		kept := s.comments[:0]
		s.maxTip = 0
		for _, c := range s.comments {
			if pattern.matches(c.Comment) {
				activity.totalTip -= c.Tip
				s.tip -= c.Tip
			} else {
				kept = append(kept, c)
				if c.Tip > s.maxTip {
					s.maxTip = c.Tip
				}
			}
		}
		s.comments = kept
	default:
		return fmt.Errorf("unknown activity event %q", e.Kind)
	}
	return nil
}
