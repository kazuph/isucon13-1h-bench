package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func stopTestEngine(m *memoryEngine) {
	done := make(chan error, 1)
	m.queue <- durableItem{Done: done}
	<-done
	close(m.queue)
	<-m.writerStopped
	m.log.Close()
}
func TestPersistentGenerationAndTornTail(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ISUPIPE_DATA", dir)
	oldFallback := fallbackImage
	fallbackImage = filepath.Join(dir, "fallback.jpg")
	t.Cleanup(func() { fallbackImage = oldFallback })
	if err := os.WriteFile(fallbackImage, []byte("fallback"), 0600); err != nil {
		t.Fatal(err)
	}
	seed := &memoryEngine{S: emptyState()}
	seed.S.Tags[1] = TagModel{ID: 1, Name: "tag"}
	if err := seed.snapshot(); err != nil {
		t.Fatal(err)
	}
	m := &memoryEngine{}
	if err := m.open(); err != nil {
		t.Fatal(err)
	}
	events := []mutation{
		{Kind: "user", User: UserModel{ID: 1, Name: "durable"}, Theme: ThemeModel{ID: 1, UserID: 1, DarkMode: true}},
		{Kind: "stream", Stream: LivestreamModel{ID: 10, UserID: 1, Title: "stream"}, Tags: []int64{1}},
		{Kind: "comment", Comment: LivecommentModel{ID: 5, UserID: 1, LivestreamID: 10, Comment: "spam", Tip: 123, CreatedAt: 1}},
		{Kind: "report", Report: LivecommentReportModel{ID: 2, UserID: 1, LivestreamID: 10, LivecommentID: 5, CreatedAt: 2}},
		{Kind: "word", Word: NGWord{ID: 2, UserID: 1, LivestreamID: 10, Word: "spam"}, Deleted: []int64{5}},
		{Kind: "icon", ID: 1, Image: []byte("icon"), Theme: ThemeModel{ID: 3}},
	}
	for _, e := range events {
		m.apply(e)
		if err := <-m.persist(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.newGeneration(); err != nil {
		t.Fatal(err)
	}
	e := mutation{Kind: "reaction", Reaction: ReactionModel{ID: 7, UserID: 1, LivestreamID: 10, EmojiName: "tada", CreatedAt: 3}}
	m.apply(e)
	if err := <-m.persist(e); err != nil {
		t.Fatal(err)
	}
	wantReport := m.report(2)
	wantStats := m.streamStatistics(10)
	logPath := m.statePath("events.jsonl")
	stopTestEngine(m)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"Kind":"comment","Comment":`)
	f.Close()
	restored := &memoryEngine{}
	if err := restored.open(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(wantReport, restored.report(2)) {
		t.Fatal("reported comment lost after moderation/recovery")
	}
	if !reflect.DeepEqual(wantStats, restored.streamStatistics(10)) {
		t.Fatal("statistics not recovered")
	}
	if !reflect.DeepEqual(restored.AllStreams, []int64{10}) || !reflect.DeepEqual(restored.TagStreams["tag"], []int64{10}) {
		t.Fatal("search indexes not rebuilt")
	}
	if restored.S.Next["reaction"] != 7 || restored.S.Next["icon"] != 3 {
		t.Fatal("ID sequences not recovered")
	}
	e = mutation{Kind: "enter", Viewer: LivestreamViewerModel{UserID: 1, LivestreamID: 10}}
	restored.apply(e)
	if err := <-restored.persist(e); err != nil {
		t.Fatal(err)
	}
	stopTestEngine(restored)
	twice := &memoryEngine{}
	if err := twice.open(); err != nil {
		t.Fatal("torn tail corrupted future appends:", err)
	}
	defer stopTestEngine(twice)
	if twice.streamStatistics(10).ViewersCount != 1 {
		t.Fatal("post-recovery append missing")
	}
}
func TestModerationSQLPattern(t *testing.T) {
	cases := []struct {
		Text, Word string
		Match      bool
	}{
		{"これはスパムです", "スパム", true}, {"normal", "spam", false}, {"hello", "h_llo", true}, {"abXYZcd", "ab%cd", true}, {"100% genuine", `100\%`, true}, {"100x genuine", `100\%`, false}, {"あいう", "あ_う", true}, {"あう", "あ_う", false},
	}
	for _, c := range cases {
		if got := sqlLikeContains(c.Text, c.Word); got != c.Match {
			t.Errorf("text=%q word=%q got %v", c.Text, c.Word, got)
		}
	}
}
