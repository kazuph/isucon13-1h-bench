package main

import (
	"reflect"
	"testing"
)

func TestLike(t *testing.T) {
	for _, v := range []struct {
		s, p string
		want bool
	}{{"abcdef", "cd", true}, {"abcdef", "c_e", true}, {"abcdef", "c%f", true}, {"abcdef", "C", false}, {"日本語", "本_", true}, {"abc_def", "\\_", true}, {"abcXdef", "\\_", false}, {"abc%def", "\\%", true}, {"abc", "", true}, {"abc", "d", false}} {
		if got := like(v.s, v.p); got != v.want {
			t.Errorf("like(%q,%q)=%v want %v", v.s, v.p, got, v.want)
		}
	}
}
func TestIncrementalIndexesMatchRebuild(t *testing.T) {
	st = fresh()
	reindex()
	events := []Event{
		{Kind: "user", User: &UserData{Model: UserModel{ID: 1, Name: "one"}}},
		{Kind: "user", User: &UserData{Model: UserModel{ID: 2, Name: "two"}}},
		{Kind: "stream", Stream: &StreamData{Model: LivestreamModel{ID: 1, UserID: 1}, Tags: []Tag{}}},
		{Kind: "stream", Stream: &StreamData{Model: LivestreamModel{ID: 2, UserID: 2}, Tags: []Tag{}}},
		{Kind: "comment", Comment: &LivecommentModel{ID: 1, LivestreamID: 1, UserID: 2, Tip: 100}},
		{Kind: "comment", Comment: &LivecommentModel{ID: 2, LivestreamID: 1, UserID: 2, Tip: 50}},
		{Kind: "reaction", Reaction: &ReactionModel{ID: 1, LivestreamID: 1, EmojiName: "smile"}},
		{Kind: "enter", StreamID: 1, UserID: 2, Time: 1},
		{Kind: "enter", StreamID: 1, UserID: 2, Time: 2},
		{Kind: "report", Report: &LivecommentReportModel{ID: 1, LivestreamID: 1, LivecommentID: 1, UserID: 2}},
		{Kind: "word", Word: &NGWord{ID: 1, LivestreamID: 1, Word: "spam"}, Delete: []int64{1}},
		{Kind: "exit", StreamID: 1, UserID: 2},
	}
	for i, e := range events {
		apply(e)
		a, b, total := streamStats, userStats, totalTips
		reindex()
		if !reflect.DeepEqual(a, streamStats) || !reflect.DeepEqual(b, userStats) || total != totalTips {
			t.Fatalf("event %d incremental aggregate differs from rebuild: old=%+v new=%+v", i, a, streamStats)
		}
	}
}
