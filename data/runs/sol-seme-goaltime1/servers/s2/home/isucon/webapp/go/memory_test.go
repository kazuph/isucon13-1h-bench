package main

import (
	"encoding/json"
	"github.com/labstack/echo/v4"
	"net/http/httptest"
	"testing"
)

func TestIndexesAndStatistics(t *testing.T) {
	old := mem
	defer func() { mem = old }()
	mem = memoryState{
		users: map[int64]User{1: {ID: 1, Name: "alice"}, 2: {ID: 2, Name: "bob"}, 3: {ID: 3, Name: "zara"}}, names: map[string]int64{"alice": 1, "bob": 2, "zara": 3},
		streams:   map[int64]Livestream{10: {ID: 10, Owner: User{ID: 1}, Tags: []Tag{{ID: 1, Name: "music"}}}, 20: {ID: 20, Owner: User{ID: 2}, Tags: []Tag{{ID: 1, Name: "music"}}}, 30: {ID: 30, Owner: User{ID: 1}}},
		comments:  map[int64]LivecommentModel{1: {ID: 1, LivestreamID: 10, Tip: 5, CreatedAt: 10}, 2: {ID: 2, LivestreamID: 10, Tip: 2, CreatedAt: 9}, 3: {ID: 3, LivestreamID: 20, Tip: 7, CreatedAt: 10}},
		reactions: map[int64]ReactionModel{1: {ID: 1, LivestreamID: 10, EmojiName: "a"}, 2: {ID: 2, LivestreamID: 20, EmojiName: "z"}},
		viewers:   []LivestreamViewerModel{{UserID: 2, LivestreamID: 10}, {UserID: 2, LivestreamID: 10}}, reports: map[int64]LivecommentReportModel{1: {ID: 1, LivestreamID: 10}},
	}
	mem.buildIndexes()
	if got := mem.commentIDs[10]; len(got) != 2 || got[0] != 2 || got[1] != 1 {
		t.Fatalf("timeline order: %v", got)
	}
	if got := mem.tagStreams["music"]; len(got) != 2 || got[0] != 10 || got[1] != 20 {
		t.Fatalf("tag order: %v", got)
	}
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest("GET", "/", nil), rec)
	c.SetPath("/api/livestream/:livestream_id/statistics")
	if err := memoryStats(c, 10); err != nil {
		t.Fatal(err)
	}
	var st LivestreamStatistics
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.Rank != 2 || st.TotalReactions != 1 || st.ViewersCount != 2 || st.MaxTip != 5 || st.TotalReports != 1 {
		t.Fatalf("stream stats: %+v", st)
	}
	rec = httptest.NewRecorder()
	c = e.NewContext(httptest.NewRequest("GET", "/", nil), rec)
	c.SetPath("/api/user/:username/statistics")
	c.SetParamNames("username")
	c.SetParamValues("alice")
	if err := memoryStats(c, 0); err != nil {
		t.Fatal(err)
	}
	var ust UserStatistics
	if err := json.Unmarshal(rec.Body.Bytes(), &ust); err != nil {
		t.Fatal(err)
	}
	if ust.Rank != 2 || ust.TotalTip != 7 || ust.TotalLivecomments != 2 || ust.TotalReactions != 1 || ust.FavoriteEmoji != "a" || ust.ViewersCount != 2 {
		t.Fatalf("user stats: %+v", ust)
	}
	delete(mem.comments, 1)
	rec = httptest.NewRecorder()
	c = e.NewContext(httptest.NewRequest("GET", "/", nil), rec)
	c.SetPath("/api/user/:username/statistics")
	c.SetParamNames("username")
	c.SetParamValues("alice")
	if err := memoryStats(c, 0); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(rec.Body.Bytes(), &ust)
	if ust.TotalTip != 2 || ust.TotalLivecomments != 1 {
		t.Fatalf("moderation totals: %+v", ust)
	}
}

func TestSpamSQLSemantics(t *testing.T) {
	for _, tt := range []struct {
		text, word string
		want       bool
	}{{"BAD comment", "bad", true}, {"café", "cafe", true}, {"abc", "a_c", true}, {"abc", "a%c", true}, {"x%y", "x\\%y", true}, {"hello", "world", false}, {"あいうえお", "いう", true}} {
		if got := matchesSpam(tt.text, tt.word); got != tt.want {
			t.Errorf("%q LIKE %q got %v", tt.text, tt.word, got)
		}
	}
}
