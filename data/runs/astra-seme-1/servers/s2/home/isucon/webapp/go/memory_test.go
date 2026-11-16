package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestJournalReplay(t *testing.T) {
	oldDir, oldImage := dataDir, fallbackImage
	dataDir = t.TempDir()
	fallbackImage = filepath.Join(dataDir, "fallback.jpg")
	if err := os.WriteFile(fallbackImage, []byte("fallback"), 0600); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if journal != nil {
			journal.Close()
			journal = nil
		}
		dataDir = oldDir
		fallbackImage = oldImage
	}()
	state = newMemoryState()
	fallbackBytes = []byte("fallback")
	state.Slots = []ReservationSlotModel{{ID: 1, Slot: 2, StartAt: 10, EndAt: 20}}
	rebuildIndexes()
	if err := checkpoint(); err != nil {
		t.Fatal(err)
	}
	events := []memoryEvent{
		{Kind: "user", User: UserModel{ID: 1, Name: "alice", HashedPassword: "hash"}, Theme: Theme{ID: 1, DarkMode: true}},
		{Kind: "icon", UID: 1, ID: 1, Icon: []byte("image")},
		{Kind: "stream", Stream: LivestreamModel{ID: 1, UserID: 1, StartAt: 10, EndAt: 20}},
		{Kind: "comment", Comment: LivecommentModel{ID: 1, UserID: 1, LivestreamID: 1, Comment: "good", Tip: 17}},
		{Kind: "comment", Comment: LivecommentModel{ID: 2, UserID: 1, LivestreamID: 1, Comment: "bad", Tip: 23}},
		{Kind: "reaction", Reaction: ReactionModel{ID: 1, UserID: 1, LivestreamID: 1, EmojiName: "smile"}},
		{Kind: "report", Report: LivecommentReportModel{ID: 1, UserID: 1, LivestreamID: 1, LivecommentID: 2}},
		{Kind: "word", Word: NGWord{ID: 1, UserID: 1, LivestreamID: 1, Word: "bad"}},
		{Kind: "enter", UID: 1, SID: 1}, {Kind: "enter", UID: 1, SID: 1}, {Kind: "exit", UID: 1, SID: 1}, {Kind: "enter", UID: 1, SID: 1},
	}
	for _, e := range events {
		if err := persist(e); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := json.Marshal(state)
	userBefore := userResponse[1]
	journal.Close()
	journal = nil
	// A crash can leave an unacknowledged incomplete trailing event.
	f, e := os.OpenFile(filepath.Join(dataDir, "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	f.WriteString(`{"Kind":"comment"`)
	f.Close()
	state = newMemoryState()
	if err := startMemory(); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(state)
	if string(before) != string(after) {
		t.Fatal("state differs after replay")
	}
	if userResponse[1] != userBefore {
		t.Fatal("icon or profile differs after replay")
	}
	if state.Slots[0].Slot != 1 || state.Viewers[1][1] != 1 {
		t.Fatal("reservation/viewers not restored")
	}
	if _, ok := state.Comments[2]; ok {
		t.Fatal("moderated comment restored")
	}
	a := aggregates()[1]
	if a.tips != 17 || a.comments != 1 || a.reactions != 1 {
		t.Fatalf("wrong aggregate %+v", a)
	}
	if err := persist(memoryEvent{Kind: "comment", Comment: LivecommentModel{ID: 3, UserID: 1, LivestreamID: 1, Tip: 5}}); err != nil {
		t.Fatal(err)
	}
	journal.Close()
	journal = nil
	if err := startMemory(); err != nil {
		t.Fatal(err)
	}
	if state.Comments[3].Tip != 5 {
		t.Fatal("append after partial recovery failed")
	}
}
func TestSQLLike(t *testing.T) {
	for _, c := range []struct {
		s, w string
		want bool
	}{{"hello 世界", "世界", true}, {"HELLO", "hello", false}, {"abc", "a_c", true}, {"abcdef", "a%f", true}, {"ab", "a_c", false}, {"a%c", `a\%c`, true}, {"abc", `a\%c`, false}, {"a_c", `a\_c`, true}, {"hi", "", true}, {"あいう", "あ_う", true}} {
		if got := matchesWord(c.s, c.w); got != c.want {
			t.Errorf("%q LIKE %q = %v", c.s, c.w, got)
		}
	}
}

func TestModerationRechecksSeedWordsAndNegativeMaxTip(t *testing.T) {
	state = newMemoryState()
	fallbackBytes = []byte("fallback")
	state.Streams[1] = LivestreamModel{ID: 1, UserID: 1}
	state.Comments[1] = LivecommentModel{ID: 1, LivestreamID: 1, Comment: "seed spam", Tip: -7}
	state.Comments[2] = LivecommentModel{ID: 2, LivestreamID: 1, Comment: "normal", Tip: -10}
	state.Words[1] = NGWord{ID: 1, LivestreamID: 1, Word: "spam"}
	rebuildIndexes()
	if got := aggregates()[1].maxTip; got != -7 {
		t.Fatalf("negative max tip: %d", got)
	}
	applyEvent(memoryEvent{Kind: "word", Word: NGWord{ID: 2, LivestreamID: 1, Word: "new word"}})
	if _, ok := state.Comments[1]; ok {
		t.Fatal("existing seed NG word was not applied")
	}
	if len(state.Comments) != 1 {
		t.Fatal("unrelated comment deleted")
	}
}
