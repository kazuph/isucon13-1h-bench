package main

import (
	"bytes"
	"encoding/json"
	"github.com/labstack/echo/v4"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentDurableUpdates(t *testing.T) {
	old, _ := os.Getwd()
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "app"), 0755)
	os.Mkdir(filepath.Join(root, "state"), 0755)
	os.Chdir(filepath.Join(root, "app"))
	defer os.Chdir(old)
	st = fresh()
	st.buildIndexes()
	if e := saveSnapshot(st); e != nil {
		t.Fatal(e)
	}
	var err error
	journal, err = os.OpenFile(dataDir+"/journal.jsonl", os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		t.Fatal(err)
	}
	startJournalWriter()
	e := echo.New()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rr := httptest.NewRecorder()
			c := e.NewContext(httptest.NewRequest("POST", "/", nil), rr)
			mu.Lock()
			dc := &durableContext{Context: c, unlocker: mu.Unlock, captureCommit: true}
			if err := persistFor(dc, Event{Kind: "enter", StreamID: 1, UserID: 1}); err != nil {
				t.Error(err)
				dc.unlock()
				return
			}
			if err := dc.JSON(200, map[string]int64{"count": st.Stats[1].Viewers}); err != nil {
				t.Error(err)
			}
			if rr.Code != 200 {
				t.Errorf("status %d", rr.Code)
			}
		}()
	}
	wg.Wait()
	a, _ := json.Marshal(st)
	b, _ := json.Marshal(committedState)
	if !bytes.Equal(a, b) {
		t.Fatalf("committed read view diverged: %s != %s", a, b)
	}
	if err = flushJournal(); err != nil {
		t.Fatal(err)
	}
	close(walQueue)
	walQueue = nil
	lastCommit = nil
	journal.Close()
	if err = loadState(); err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	if st.Stats[1].Viewers != 100 {
		t.Fatalf("lost writes: %d", st.Stats[1].Viewers)
	}
}

func TestCloneIsolation(t *testing.T) {
	s := fresh()
	s.Tags = []Tag{{ID: 1, Name: "tag"}}
	s.Slots = []ReservationSlotModel{{ID: 1, StartAt: 10, EndAt: 20, Slot: 5}}
	s.apply(Event{Kind: "user", User: &UserModel{ID: 1, Name: "one"}, Theme: Theme{ID: 1}})
	s.apply(Event{Kind: "stream", Stream: &LivestreamModel{ID: 1, UserID: 1, StartAt: 10, EndAt: 20}, Tags: []int64{1}})
	s.apply(Event{Kind: "comment", Comment: &LivecommentModel{ID: 1, UserID: 1, LivestreamID: 1, Comment: "spam", Tip: 2}})
	s.apply(Event{Kind: "reaction", Reaction: &ReactionModel{ID: 1, UserID: 1, LivestreamID: 1, EmojiName: "a"}})
	s.apply(Event{Kind: "enter", UserID: 1, StreamID: 1})
	s.buildIndexes()
	clone := cloneState(s)
	before, _ := json.Marshal(s)
	copied, _ := json.Marshal(clone)
	if !bytes.Equal(before, copied) {
		t.Fatal("clone differed")
	}
	events := []Event{{Kind: "word", Word: &NGWord{ID: 1, UserID: 1, LivestreamID: 1, Word: "spam"}}, {Kind: "reaction", Reaction: &ReactionModel{ID: 2, UserID: 1, LivestreamID: 1, EmojiName: "a"}}, {Kind: "exit", UserID: 1, StreamID: 1}, {Kind: "stream", Stream: &LivestreamModel{ID: 2, UserID: 1, StartAt: 10, EndAt: 20}, Tags: []int64{1}}, {Kind: "icon", UserID: 1, ID: 1, Image: []byte("new")}}
	for _, e := range events {
		s.apply(e)
	}
	still, _ := json.Marshal(clone)
	if !bytes.Equal(before, still) {
		t.Fatal("working updates leaked into committed view")
	}
	for _, e := range events {
		clone.apply(e)
	}
	a, _ := json.Marshal(s)
	b, _ := json.Marshal(clone)
	if !bytes.Equal(a, b) {
		t.Fatal("replay diverged")
	}
}
