package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCachedResponsesFollowIconUpdate(t *testing.T) {
	state = newState()
	fallback = []byte("fallback")
	rebuild()
	apply(Event{Kind: "user", User: UserModel{ID: 1, Name: "alice"}, Theme: Theme{ID: 2, DarkMode: true}})
	apply(Event{Kind: "stream", Stream: LivestreamModel{ID: 1, UserID: 1, Title: "<配信>"}})
	before := streamResponse(1)
	old, _ := json.Marshal(before)
	apply(Event{Kind: "icon", ID: 1, UserID: 1, Image: []byte("new image")})
	after := streamResponse(1)
	current, _ := json.Marshal(after)
	if bytes.Equal(old, current) {
		t.Fatal("icon update left stale stream JSON")
	}
	again, _ := json.Marshal(before)
	if !bytes.Equal(old, again) {
		t.Fatal("old response snapshot mutated")
	}
	var decoded Livestream
	if e := json.Unmarshal(current, &decoded); e != nil {
		t.Fatal(e)
	}
	if decoded.Owner.IconHash != hash([]byte("new image")) {
		t.Fatal("owner image hash stale")
	}
	if decoded.Title != "<配信>" {
		t.Fatal("JSON escaping changed value")
	}
}
