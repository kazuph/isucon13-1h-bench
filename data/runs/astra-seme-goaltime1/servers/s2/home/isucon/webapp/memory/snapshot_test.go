package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotGenerationAndPartialTail(t *testing.T) {
	if err := flushWAL(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "img"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "img", "NoImage.jpg"), []byte("fallback"), 0644); err != nil {
		t.Fatal(err)
	}
	os.Mkdir(filepath.Join(root, "app"), 0755)
	cwd, _ := os.Getwd()
	os.Chdir(filepath.Join(root, "app"))
	defer os.Chdir(cwd)
	dataDir = filepath.Join(root, "state")
	os.Mkdir(dataDir, 0755)
	generationDir = ""
	state = newState()
	rebuild()
	apply(Event{Kind: "user", User: UserModel{ID: 1, Name: "one"}})
	if err := snapshot(); err != nil {
		t.Fatal(err)
	}
	first := generationDir
	if !walStarted {
		startWAL()
	}
	if err := commit(nil, Event{Kind: "user", User: UserModel{ID: 2, Name: "two"}}); err != nil {
		t.Fatal(err)
	}
	if err := flushWAL(); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.WriteString(`{"Kind":"user","User":`); err != nil {
		t.Fatal(err)
	}
	journal.Close()
	if err := openStore(); err != nil {
		t.Fatal(err)
	}
	if len(state.Users) != 2 {
		t.Fatalf("lost durable users: %d", len(state.Users))
	}
	if err := commit(nil, Event{Kind: "user", User: UserModel{ID: 3, Name: "three"}}); err != nil {
		t.Fatal(err)
	}
	if err := flushWAL(); err != nil {
		t.Fatal(err)
	}
	journal.Close()
	if err := openStore(); err != nil {
		t.Fatal(err)
	}
	if len(state.Users) != 3 {
		t.Fatal("tail recovery failed")
	}
	if err := snapshot(); err != nil {
		t.Fatal(err)
	}
	if generationDir == first {
		t.Fatal("generation unchanged")
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatal("obsolete generation remains")
	}
	journal.Close()
	if err := openStore(); err != nil {
		t.Fatal(err)
	}
	if len(state.Users) != 3 {
		t.Fatal("snapshot lost users")
	}
}
