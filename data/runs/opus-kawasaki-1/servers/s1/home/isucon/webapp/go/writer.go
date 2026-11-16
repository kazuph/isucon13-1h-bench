package main

import (
	"context"
	"log"
	"strings"
	"sync/atomic"
)

// Asynchronous, ordered DB writer for high-volume inserts.

type writeOp struct {
	kind    int
	lc      LivecommentModel
	r       ReactionModel
	rep     LivecommentReportModel
	userID  int64
	lsID    int64
	created int64
	done    chan struct{}
}

const (
	opLivecomment = iota + 1
	opReaction
	opReport
	opViewerEnter
	opViewerExit
	opBarrier
)

var (
	writeCh       = make(chan *writeOp, 100000)
	livecommentID atomic.Int64
	reactionID    atomic.Int64
	reportID      atomic.Int64
)

func initIDCounters(ctx context.Context) error {
	var v int64
	if err := dbConn.GetContext(ctx, &v, "SELECT IFNULL(MAX(id), 0) FROM livecomments"); err != nil {
		return err
	}
	livecommentID.Store(v)
	if err := dbConn.GetContext(ctx, &v, "SELECT IFNULL(MAX(id), 0) FROM reactions"); err != nil {
		return err
	}
	reactionID.Store(v)
	if err := dbConn.GetContext(ctx, &v, "SELECT IFNULL(MAX(id), 0) FROM livecomment_reports"); err != nil {
		return err
	}
	reportID.Store(v)
	return nil
}

func enqueueWrite(op *writeOp) {
	writeCh <- op
}

// flushWrites blocks until all previously enqueued writes are applied.
func flushWrites() {
	done := make(chan struct{})
	writeCh <- &writeOp{kind: opBarrier, done: done}
	<-done
}

func startWriter() {
	go func() {
		batch := make([]*writeOp, 0, 512)
		for op := range writeCh {
			batch = append(batch[:0], op)
		drain:
			for len(batch) < 500 {
				select {
				case o := <-writeCh:
					batch = append(batch, o)
				default:
					break drain
				}
			}
			applyBatch(batch)
		}
	}()
}

func applyBatch(batch []*writeOp) {
	i := 0
	for i < len(batch) {
		j := i + 1
		k := batch[i].kind
		if k == opLivecomment || k == opReaction || k == opReport || k == opViewerEnter {
			for j < len(batch) && batch[j].kind == k {
				j++
			}
		}
		group := batch[i:j]
		switch k {
		case opBarrier:
			close(batch[i].done)
		case opViewerExit:
			if _, err := dbConn.Exec("DELETE FROM livestream_viewers_history WHERE user_id = ? AND livestream_id = ?", batch[i].userID, batch[i].lsID); err != nil {
				log.Printf("writer: viewer exit: %v", err)
			}
		default:
			execInsertGroup(k, group)
		}
		i = j
	}
}

func execInsertGroup(kind int, group []*writeOp) {
	var sb strings.Builder
	args := make([]interface{}, 0, len(group)*6)
	switch kind {
	case opLivecomment:
		sb.WriteString("INSERT INTO livecomments (id, user_id, livestream_id, comment, tip, created_at) VALUES ")
		for n, o := range group {
			if n > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("(?,?,?,?,?,?)")
			args = append(args, o.lc.ID, o.lc.UserID, o.lc.LivestreamID, o.lc.Comment, o.lc.Tip, o.lc.CreatedAt)
		}
	case opReaction:
		sb.WriteString("INSERT INTO reactions (id, user_id, livestream_id, emoji_name, created_at) VALUES ")
		for n, o := range group {
			if n > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("(?,?,?,?,?)")
			args = append(args, o.r.ID, o.r.UserID, o.r.LivestreamID, o.r.EmojiName, o.r.CreatedAt)
		}
	case opReport:
		sb.WriteString("INSERT INTO livecomment_reports (id, user_id, livestream_id, livecomment_id, created_at) VALUES ")
		for n, o := range group {
			if n > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("(?,?,?,?,?)")
			args = append(args, o.rep.ID, o.rep.UserID, o.rep.LivestreamID, o.rep.LivecommentID, o.rep.CreatedAt)
		}
	case opViewerEnter:
		sb.WriteString("INSERT INTO livestream_viewers_history (user_id, livestream_id, created_at) VALUES ")
		for n, o := range group {
			if n > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("(?,?,?)")
			args = append(args, o.userID, o.lsID, o.created)
		}
	}
	if _, err := dbConn.Exec(sb.String(), args...); err != nil {
		log.Printf("writer: insert kind=%d n=%d: %v", kind, len(group), err)
	}
}
