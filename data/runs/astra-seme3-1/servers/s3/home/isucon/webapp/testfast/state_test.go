package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestJournalRecoveryAndModeration(t *testing.T) {
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "app"), 0755)
	os.Mkdir(filepath.Join(root, "state"), 0755)
	if err = os.Chdir(filepath.Join(root, "app")); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	fallbackHash = "default"
	st = fresh()
	st.Tags = []Tag{{ID: 1, Name: "music"}}
	st.Slots = []ReservationSlotModel{{ID: 1, StartAt: 100, EndAt: 200, Slot: 2}}
	st.buildIndexes()
	if err = saveSnapshot(st); err != nil {
		t.Fatal(err)
	}
	journal, err = os.OpenFile(dataDir+"/journal.jsonl", os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{
		{Kind: "user", User: &UserModel{ID: 1, Name: "alice"}, Theme: Theme{ID: 1}},
		{Kind: "user", User: &UserModel{ID: 2, Name: "bob"}, Theme: Theme{ID: 2, DarkMode: true}},
		{Kind: "stream", Stream: &LivestreamModel{ID: 1, UserID: 1, StartAt: 100, EndAt: 200}, Tags: []int64{1}},
		{Kind: "icon", UserID: 1, ID: 1, Image: []byte("image")},
		{Kind: "comment", Comment: &LivecommentModel{ID: 1, UserID: 2, LivestreamID: 1, Comment: "hello", Tip: 12}},
		{Kind: "comment", Comment: &LivecommentModel{ID: 2, UserID: 2, LivestreamID: 1, Comment: "spam", Tip: 30}},
		{Kind: "reaction", Reaction: &ReactionModel{ID: 1, UserID: 2, LivestreamID: 1, EmojiName: "smile"}},
		{Kind: "report", Report: &LivecommentReportModel{ID: 1, UserID: 1, LivestreamID: 1, LivecommentID: 2}},
		{Kind: "enter", StreamID: 1, UserID: 2}, {Kind: "enter", StreamID: 1, UserID: 2},
		{Kind: "exit", StreamID: 1, UserID: 2},
		{Kind: "word", Word: &NGWord{ID: 1, UserID: 1, LivestreamID: 1, Word: "spam"}},
	}
	for _, e := range events {
		if err = persist(e); err != nil {
			t.Fatal(err)
		}
	}
	z := st.Stats[1]
	if z.Tips != 12 || z.Comments != 1 || z.MaxTip != 12 || z.Viewers != 0 || z.Reactions != 1 || z.Reports != 1 {
		t.Fatalf("wrong stats: %+v", z)
	}
	if st.Slots[0].Slot != 1 {
		t.Fatal("reservation not accounted")
	}
	want, _ := json.Marshal(st)
	journal.Write([]byte(`{"Kind":"comment"`))
	journal.Close()
	st = nil
	if err = loadState(); err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	got, _ := json.Marshal(st)
	if !reflect.DeepEqual(want, got) {
		t.Fatal("recovery changed state")
	}
	if err = persist(Event{Kind: "enter", StreamID: 1, UserID: 2}); err != nil {
		t.Fatal(err)
	}
	journal.Close()
	if err = loadState(); err != nil {
		t.Fatal(err)
	}
	if st.Stats[1].Viewers != 1 {
		t.Fatal("journal was not repaired")
	}
}
func TestLikePatterns(t *testing.T) {
	for _, v := range []struct {
		text, pattern string
		want          bool
	}{{"日本語コメント", "語コ", true}, {"hello", "h_llo", true}, {"hello", "h%o", true}, {"hello", "world", false}, {"a%b", `a\%b`, true}, {"axb", `a\%b`, false}} {
		if got := like(v.text, v.pattern); got != v.want {
			t.Fatalf("like(%q,%q)=%v", v.text, v.pattern, got)
		}
	}
}

func TestEmptyTagSnapshotRecovery(t *testing.T) {
	old, _ := os.Getwd()
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "app"), 0755)
	os.Mkdir(filepath.Join(root, "state"), 0755)
	os.Chdir(filepath.Join(root, "app"))
	defer os.Chdir(old)
	st = fresh()
	st.apply(Event{Kind: "stream", Stream: &LivestreamModel{ID: 1, UserID: 1}})
	st.buildIndexes()
	before, e := json.Marshal(st)
	if e != nil {
		t.Fatal(e)
	}
	if e = saveSnapshot(st); e != nil {
		t.Fatal(e)
	}
	if e = loadState(); e != nil {
		t.Fatal(e)
	}
	defer journal.Close()
	after, e := json.Marshal(st)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("empty tag arrays changed after recovery: %s != %s", before, after)
	}
}
