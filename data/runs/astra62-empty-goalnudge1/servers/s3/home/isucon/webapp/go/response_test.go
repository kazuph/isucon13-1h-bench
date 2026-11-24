package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestIconPayloadMatchesJSONDecoder(t *testing.T) {
	inputs := []string{
		`{"image":"/+8="}`,
		`{"image":"\u002f+8="}`,
		`{"image":"\/+8="}`,
		`{ "ignored": {}, "image": "YWJj" }`,
		`{"image":null}`,
		`{"image":""}`,
		`{"image":"!not-base64!"}`,
		"{\"image\":\"YW\nJj\"}",
	}
	for _, input := range inputs {
		var want PostIconRequest
		wantErr := json.NewDecoder(strings.NewReader(input)).Decode(&want)
		got, err := readIcon(strings.NewReader(input))
		if (err == nil) != (wantErr == nil) || err == nil && !bytes.Equal(got, want.Image) {
			t.Errorf("payload %q: got %x, %v; want %x, %v", input, got, err, want.Image, wantErr)
		}
	}
}

func TestTimelineRetainsNewestDistinctEvents(t *testing.T) {
	feed := timeline[ReactionModel]{keys: reactionFeed.keys}
	feed.reset()
	shard := feed.shard(42)
	shard.streams[42] = nil
	for i := int64(0); i < timelinePageSize+8; i++ {
		m := ReactionModel{ID: i + 1, CreatedAt: i / 3}
		feed.push(42, m)
		feed.push(42, m)
	}
	feed.push(42, ReactionModel{ID: 8888, CreatedAt: -1})
	list := shard.streams[42]
	if len(list) != timelinePageSize {
		t.Fatalf("page length: %d", len(list))
	}
	for i, m := range list {
		if want := int64(timelinePageSize + 8 - i); m.ID != want {
			t.Fatalf("index %d: got %d, want %d", i, m.ID, want)
		}
	}
}

func TestRenderedListRefreshesOwnerIcon(t *testing.T) {
	catalog.Lock()
	old := catalog.catalogState
	owner := User{ID: 1, Name: "owner", IconHash: "old"}
	catalog.catalogState = catalogState{
		streams: map[int64]Livestream{}, users: map[int64]User{1: owner}, byOwner: map[int64][]int64{},
		byTag: map[string][]int64{}, streamJSON: map[int64][]byte{},
	}
	catalog.addStream(Livestream{ID: 9, Owner: owner, Title: "<配信>", Tags: []Tag{}})
	catalog.addStream(Livestream{ID: 3, Owner: owner, Title: "past", Tags: []Tag{}})
	owner.IconHash = "new"
	catalog.users[1] = owner
	for _, id := range catalog.byOwner[1] {
		catalog.renderStream(id)
	}
	catalog.Unlock()
	defer func() { catalog.Lock(); catalog.catalogState = old; catalog.Unlock() }()
	var result []Livestream
	if err := json.Unmarshal(listStreamsJSON(0, "", 1), &result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].ID != 9 || result[0].Owner.IconHash != "new" || result[0].Title != "<配信>" {
		t.Fatalf("wrong rendered result: %+v", result)
	}
}
