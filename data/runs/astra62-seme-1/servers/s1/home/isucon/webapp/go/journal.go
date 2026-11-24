package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sync/atomic"
)

type event struct {
	Kind                           string
	Stream, User, ID, At, Ref, Tip int64
	Text                           string
}
type pendingEvent struct {
	record []byte
	done   chan struct{}
}

var journal *os.File
var pending = make(chan pendingEvent, 1024)
var sequence atomic.Int64

func openJournal() error {
	var err error
	journal, err = os.OpenFile("../activity.jsonl", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	dir, err := os.Open("..")
	if err != nil {
		return err
	}
	err = dir.Sync()
	dir.Close()
	if err != nil {
		return err
	}
	go func() {
		for first := range pending {
			batch := []pendingEvent{first}
			n := len(pending)
			for i := 0; i < n; i++ {
				batch = append(batch, <-pending)
			}
			var buf bytes.Buffer
			for _, p := range batch {
				buf.Write(p.record)
				buf.WriteByte('\n')
			}
			if _, err := journal.Write(buf.Bytes()); err != nil {
				log.Fatal(err)
			}
			if err := journal.Sync(); err != nil {
				log.Fatal(err)
			}
			for _, p := range batch {
				close(p.done)
			}
		}
	}()
	return nil
}
func restoreJournal() error {
	var highest int64
	if err := dbConn.Get(&highest, "SELECT GREATEST(COALESCE((SELECT MAX(id) FROM reactions),0),COALESCE((SELECT MAX(id) FROM livecomments),0),COALESCE((SELECT MAX(id) FROM livecomment_reports),0),COALESCE((SELECT MAX(id) FROM ng_words),0))"); err != nil {
		return err
	}
	sequence.Store(highest)
	if _, err := journal.Seek(0, 0); err != nil {
		return err
	}
	reader := bufio.NewReader(journal)
	var offset int64
	for {
		line, err := reader.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			if len(line) > 0 {
				return journal.Truncate(offset)
			}
			return nil
		}
		if err != nil {
			return err
		}
		var entry event
		if err = json.Unmarshal(line, &entry); err != nil {
			return err
		}
		if err = entry.apply(); err != nil {
			return err
		}
		if entry.ID > sequence.Load() {
			sequence.Store(entry.ID)
		}
		offset += int64(len(line))
	}
}
func (e event) persist() error {
	record, err := json.Marshal(e)
	if err != nil {
		return err
	}
	p := pendingEvent{record, make(chan struct{})}
	pending <- p
	<-p.done
	activityMu.Lock()
	defer activityMu.Unlock()
	return e.apply()
}
func (e event) apply() error {
	a := activity(e.Stream)
	switch e.Kind {
	case "comment":
		m := LivecommentModel{ID: e.ID, UserID: e.User, LivestreamID: e.Stream, Comment: e.Text, Tip: e.Tip, CreatedAt: e.At}
		a.addComment(m)
		commentByID[e.ID] = m
	case "reaction":
		a.emojis[e.Text]++
		a.reactions = append(a.reactions, ReactionModel{ID: e.ID, UserID: e.User, LivestreamID: e.Stream, EmojiName: e.Text, CreatedAt: e.At})
	case "moderate":
		a.words = append(a.words, NGWord{ID: e.ID, UserID: e.User, LivestreamID: e.Stream, Word: e.Text, CreatedAt: e.At})
		a.moderate(e.Text)
	case "report":
		a.reports = append(a.reports, LivecommentReportModel{ID: e.ID, UserID: e.User, LivestreamID: e.Stream, LivecommentID: e.Ref, CreatedAt: e.At})
	case "enter":
		a.viewers[e.User]++
		a.viewerCount++
	case "exit":
		a.viewerCount -= a.viewers[e.User]
		delete(a.viewers, e.User)
	default:
		return fmt.Errorf("unknown journal event %q", e.Kind)
	}
	return nil
}
