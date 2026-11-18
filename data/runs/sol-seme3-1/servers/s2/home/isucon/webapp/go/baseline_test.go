package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBaselineRoundTripAndCorruption(t *testing.T) {
	oldState, oldPath, oldWeightsPath, oldWeights, oldUse := st, baselinePath, weightsPath, mysqlWeights, useWeights
	defer func() {
		st = oldState
		baselinePath = oldPath
		weightsPath = oldWeightsPath
		mysqlWeights = oldWeights
		useWeights = oldUse
	}()
	dir := t.TempDir()
	baselinePath = filepath.Join(dir, "baseline.gz")
	weightsPath = filepath.Join(dir, "weights.bin")
	st = newState()
	apply(event{Kind: "user", User: &memUser{Model: UserModel{ID: 1, Name: "user", HashedPassword: "hash"}, Theme: Theme{ID: 1}, Hash: "default"}})
	apply(event{Kind: "stream", Stream: newStream(LivestreamModel{ID: 1, UserID: 1}, []Tag{{ID: 1, Name: "tag"}})})
	apply(event{Kind: "comment", Comment: &LivecommentModel{ID: 1, UserID: 1, LivestreamID: 1, Comment: "hello", Tip: 20}})
	apply(event{Kind: "icon", ID: 1, UID: 1, Image: []byte{1, 2, 3}})
	mysqlWeights['a'] = 'A'
	expected, _ := json.Marshal(st)
	if err := saveBaseline(); err != nil {
		t.Fatal(err)
	}
	st = nil
	mysqlWeights['a'] = 0
	if err := loadBaseline(); err != nil {
		t.Fatal(err)
	}
	if err := loadSavedWeights(); err != nil {
		t.Fatal(err)
	}
	actual, _ := json.Marshal(st)
	if !bytes.Equal(actual, expected) {
		t.Fatal("snapshot did not restore the complete state")
	}
	if mysqlWeights['a'] != 'A' {
		t.Fatal("collation not restored")
	}
	b, err := os.ReadFile(baselinePath)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)-1] ^= 255
	os.WriteFile(baselinePath, b, 0600)
	if err := loadBaseline(); err == nil {
		t.Fatal("corrupt snapshot accepted")
	}
	os.WriteFile(weightsPath, []byte{1, 2, 3}, 0600)
	if err := loadSavedWeights(); err == nil {
		t.Fatal("truncated collation table accepted")
	}
}
