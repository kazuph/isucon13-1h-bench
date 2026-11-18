package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestFastJSONMatchesStandardEncoder(t *testing.T) {
	state = newState()
	fallback = []byte("fallback")
	rebuild()
	apply(Event{Kind: "user", User: UserModel{ID: 1, Name: "a\"<あ", Description: "\u2028\x00\\"}})
	apply(Event{Kind: "stream", Stream: LivestreamModel{ID: 1, UserID: 1, Title: "😀 配信"}})
	apply(Event{Kind: "comment", Comment: LivecommentModel{ID: 1, UserID: 1, LivestreamID: 1, Comment: "a\"\\\n<>&😀\u2028", Tip: -123, CreatedAt: 987654321}})
	apply(Event{Kind: "reaction", Reaction: ReactionModel{ID: 1, UserID: 1, LivestreamID: 1, EmojiName: "smile\"", CreatedAt: 1}})
	apply(Event{Kind: "report", Report: LivecommentReportModel{ID: 1, UserID: 1, LivestreamID: 1, LivecommentID: 1, CreatedAt: 2}})
	for _, v := range []interface{}{userResponse(1), streamResponse(1), commentResponse(1), reactionResponse(1), reportResponse(1), []Livestream{streamResponse(1), streamResponse(1)}, []Livecomment{commentResponse(1), commentResponse(1)}, []Reaction{reactionResponse(1)}, []LivecommentReport{reportResponse(1)}, []Livestream{}, []Livecomment(nil)} {
		a, e := encodeResponse(v)
		if e != nil {
			t.Fatal(e)
		}
		b, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		var x, y interface{}
		if e = json.Unmarshal(a, &x); e != nil {
			t.Fatalf("invalid fast JSON %T: %v", v, e)
		}
		if e = json.Unmarshal(b, &y); e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(x, y) {
			t.Fatalf("encoding mismatch %T\n%s\n%s", v, a, b)
		}
	}
}
