package main

import (
	"math/rand"
	"reflect"
	"testing"
)

func TestIncrementalAggregates(t *testing.T) {
	state = newState()
	fallback = []byte("image")
	rebuild()
	for i := int64(1); i <= 10; i++ {
		apply(Event{Kind: "user", User: UserModel{ID: i}})
		apply(Event{Kind: "stream", Stream: LivestreamModel{ID: i, UserID: i}})
	}
	rng := rand.New(rand.NewSource(19))
	for i := int64(1); i <= 1000; i++ {
		sid := int64(rng.Intn(10) + 1)
		switch rng.Intn(6) {
		case 0:
			apply(Event{Kind: "comment", Comment: LivecommentModel{ID: i, LivestreamID: sid, Comment: []string{"normal", "spam"}[rng.Intn(2)], Tip: int64(rng.Intn(100))}})
		case 1:
			apply(Event{Kind: "reaction", Reaction: ReactionModel{ID: i, LivestreamID: sid, EmojiName: []string{"a", "b"}[rng.Intn(2)]}})
		case 2:
			apply(Event{Kind: "report", Report: LivecommentReportModel{ID: i, LivestreamID: sid}})
		case 3:
			apply(Event{Kind: "enter", Viewer: LivestreamViewerModel{UserID: 1, LivestreamID: sid}})
		case 4:
			apply(Event{Kind: "exit", Viewer: LivestreamViewerModel{UserID: 1, LivestreamID: sid}})
		case 5:
			apply(Event{Kind: "word", Word: NGWord{ID: i, LivestreamID: sid, Word: "spam"}})
		}
		oldS, oldU := streamStats, userStats
		newS, newU := rebuildAggregates()
		if !reflect.DeepEqual(oldS, newS) || !reflect.DeepEqual(oldU, newU) {
			t.Fatalf("aggregate mismatch at event %d", i)
		}
	}
}
