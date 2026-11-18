package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestJournalReplayAndPartialTail(t *testing.T) {
	oldState, oldPath := st, walPath
	defer func() { st = oldState; walPath = oldPath }()
	walPath = filepath.Join(t.TempDir(), "state.wal")
	events := []event{
		{Kind: "user", User: &memUser{Model: UserModel{ID: 1, Name: "user", HashedPassword: "bcrypt"}, Theme: Theme{ID: 1}, Hash: "fallback"}},
		{Kind: "stream", Stream: newStream(LivestreamModel{ID: 1, UserID: 1, StartAt: 10, EndAt: 20}, []Tag{{ID: 1, Name: "tag"}})},
		{Kind: "icon", ID: 1, UID: 1, Image: []byte{}},
		{Kind: "comment", Comment: &LivecommentModel{ID: 1, UserID: 1, LivestreamID: 1, Comment: "good", Tip: 30}},
		{Kind: "comment", Comment: &LivecommentModel{ID: 2, UserID: 1, LivestreamID: 1, Comment: "spam", Tip: 10}},
		{Kind: "reaction", Reaction: &ReactionModel{ID: 1, UserID: 1, LivestreamID: 1, EmojiName: "smile"}},
		{Kind: "report", Report: &LivecommentReportModel{ID: 1, UserID: 1, LivestreamID: 1, LivecommentID: 1}},
		{Kind: "enter", UID: 1, SID: 1}, {Kind: "enter", UID: 1, SID: 1}, {Kind: "exit", UID: 1, SID: 1},
		{Kind: "word", Word: &NGWord{ID: 1, UserID: 1, LivestreamID: 1, Word: "spam"}},
	}
	seed := func() { st = newState(); st.Slots = []ReservationSlotModel{{ID: 1, Slot: 10, StartAt: 10, EndAt: 20}} }
	seed()
	var wal bytes.Buffer
	for _, e := range events {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		wal.Write(b)
		wal.WriteByte('\n')
		apply(e)
	}
	expected, _ := json.Marshal(st)
	if st.TotalTip != 30 || len(st.Comments) != 1 || st.Slots[0].Slot != 9 {
		t.Fatal("bad aggregates")
	}
	if st.Users[1].Image == nil {
		t.Fatal("empty uploaded icon replaced with fallback")
	}
	validLen := wal.Len()
	wal.WriteString(`{"Kind":"comment"`)
	if err := os.WriteFile(walPath, wal.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	seed()
	if err := replay(); err != nil {
		t.Fatal(err)
	}
	actual, _ := json.Marshal(st)
	if !bytes.Equal(actual, expected) {
		t.Fatalf("replay differs\nexpected %s\nactual %s", expected, actual)
	}
	info, _ := os.Stat(walPath)
	if info.Size() != int64(validLen) {
		t.Fatal("incomplete journal record was not discarded")
	}
	if st.Next["comment"] != 2 || st.Next["word"] != 1 {
		t.Fatal("sequence recovery failed")
	}
}
func TestLikeWildcards(t *testing.T) {
	for _, v := range []struct {
		s, w string
		want bool
	}{{"abc", "b", true}, {"abc", "a_c", true}, {"abc", "a%c", true}, {"a%c", `a\%c`, true}, {"abc", `a\%c`, false}, {"あいう", "あ_う", true}, {"abc", "z", false}} {
		if got := likeMatch(v.s, v.w); got != v.want {
			t.Errorf("LIKE %q %q: got %v", v.s, v.w, got)
		}
	}
}
