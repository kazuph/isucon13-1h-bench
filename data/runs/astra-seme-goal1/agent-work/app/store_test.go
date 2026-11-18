package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestEventReplayAndModeration(t *testing.T) {
	events := []Event{
		{Kind: "user", User: &StoredUser{Model: UserModel{ID: 1, Name: "owner"}, Theme: Theme{ID: 1}}},
		{Kind: "stream", Stream: &StreamData{Model: LivestreamModel{ID: 1, UserID: 1}, Tags: []int64{2}}},
		{Kind: "comment", Comment: &LivecommentModel{ID: 1, UserID: 1, LivestreamID: 1, Comment: "spam", Tip: 100}},
		{Kind: "comment", Comment: &LivecommentModel{ID: 2, UserID: 1, LivestreamID: 1, Comment: "ok", Tip: 7}},
		{Kind: "reaction", Reaction: &ReactionModel{ID: 1, UserID: 1, LivestreamID: 1, EmojiName: "good"}},
		{Kind: "enter", UID: 1, SID: 1}, {Kind: "enter", UID: 1, SID: 1}, {Kind: "exit", UID: 1, SID: 1},
		{Kind: "ng", NG: &NGWord{ID: 1, UserID: 1, LivestreamID: 1, Word: "spam"}, Deleted: []int64{1}},
	}
	live, replayed := newState(), newState()
	for _, e := range events {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		var decoded Event
		if err = json.Unmarshal(b, &decoded); err != nil {
			t.Fatal(err)
		}
		live.apply(e)
		replayed.apply(decoded)
	}
	if !reflect.DeepEqual(live, replayed) {
		t.Fatal("replay changed domain state")
	}
	d := live.Streams[1]
	if d.Tips != 7 || d.MaxTip != 7 || d.CommentCount != 1 || live.TotalTip != 7 || d.ReactionCount != 1 || len(d.Viewers) != 0 {
		t.Fatalf("incorrect aggregate: %+v", d)
	}
	if live.userStats(1).TotalTip != 7 {
		t.Fatal("user aggregate not updated")
	}
}
func TestLike(t *testing.T) {
	for _, tc := range []struct {
		text, word string
		want       bool
	}{
		{"こんいすー", "いす", true}, {"ABC", "abc", true}, {"が", "か", false}, {"é", "e", true}, {"Ａ", "A", false}, {"😀", "😂", true}, {"abcdef", "a%f", true}, {"あいう", "あ_う", true}, {"あいう", "あ__う", false}, {"a%b", `a\%b`, true}, {"axb", `a\%b`, false}, {"anything", "", true},
	} {
		if got := like(tc.text, tc.word); got != tc.want {
			t.Errorf("like(%q,%q)=%v want %v", tc.text, tc.word, got, tc.want)
		}
	}
}

func TestDurabilityBarrier(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "wal")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d := newDurableWAL(f)
	var writers sync.WaitGroup
	for i := 0; i < 64; i++ {
		writers.Add(1)
		go func(i int) {
			defer writers.Done()
			if _, err := f.WriteString(fmt.Sprintf("%d\n", i)); err != nil {
				t.Error(err)
				return
			}
			if err := <-d.barrier(); err != nil {
				t.Error(err)
			}
		}(i)
	}
	writers.Wait()
	if err = d.close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, line := range strings.Fields(string(b)) {
		seen[line] = true
	}
	if len(seen) != 64 {
		t.Fatalf("lost acknowledged records: %d", len(seen))
	}
}

func TestEmptyIconReplay(t *testing.T) {
	s := newState()
	s.apply(Event{Kind: "user", User: &StoredUser{Model: UserModel{ID: 1, Name: "empty-icon"}, Hash: "default"}})
	original := Event{Kind: "icon", UID: 1, ID: 1, Image: []byte{}}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var replay Event
	if err = json.Unmarshal(encoded, &replay); err != nil {
		t.Fatal(err)
	}
	s.apply(replay)
	if s.Users[1].Hash != hashImage([]byte{}) || len(s.Users[1].Image) != 0 {
		t.Fatal("empty image changed during replay")
	}
}
