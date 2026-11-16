package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSnapshotAndJournalRecovery(t *testing.T) {
	old, e := os.Getwd()
	if e != nil {
		t.Fatal(e)
	}
	d := t.TempDir()
	if e = os.Chdir(d); e != nil {
		t.Fatal(e)
	}
	defer os.Chdir(old)
	st = fresh()
	reindex()
	apply(Event{Kind: "user", User: &UserData{Model: UserModel{ID: 1, Name: "UserOne"}, Hash: "hash"}})
	apply(Event{Kind: "stream", Stream: &StreamData{Model: LivestreamModel{ID: 1, UserID: 1}, Tags: []Tag{}}})
	if e = snapshot(); e != nil {
		t.Fatal(e)
	}
	wal, e = os.OpenFile("state.wal", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	for _, ev := range []Event{
		{Kind: "comment", Comment: &LivecommentModel{ID: 1, LivestreamID: 1, UserID: 1, Tip: 123}},
		{Kind: "icon", ID: 7, UserID: 1, Image: []byte{1, 2, 3}},
		{Kind: "enter", StreamID: 1, UserID: 1, Time: 3},
		{Kind: "reaction", Reaction: &ReactionModel{ID: 1, LivestreamID: 1, UserID: 1, EmojiName: "smile"}},
	} {
		if e = record(ev); e != nil {
			t.Fatal(e)
		}
	}
	if e = syncWAL(); e != nil {
		t.Fatal(e)
	}
	wal.Close()
	before, _ := json.Marshal(st)
	st = nil
	if e = recoverState(); e != nil {
		t.Fatal(e)
	}
	defer wal.Close()
	after, _ := json.Marshal(st)
	if string(before) != string(after) {
		t.Fatalf("recovered state differs\nbefore %s\nafter %s", before, after)
	}
	if !dnsUsers["userone"] {
		t.Fatal("recovered DNS name missing")
	}
	if totalTips != 123 {
		t.Fatal("recovered total tip incorrect")
	}
}
