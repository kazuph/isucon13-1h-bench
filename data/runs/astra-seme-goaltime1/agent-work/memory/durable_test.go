package main

import (
	"encoding/json"
	"io"
	"os"
	"sync"
	"testing"
)

func TestGroupedDurabilityReplay(t *testing.T) {
	state = newState()
	rebuild()
	var err error
	journal, err = os.CreateTemp(t.TempDir(), "journal")
	if err != nil {
		t.Fatal(err)
	}
	startWAL()
	var wg sync.WaitGroup
	for k := 0; k < 20; k++ {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				mu.Lock()
				err := commit(nil, Event{Kind: "user", User: UserModel{ID: int64(k*20 + i + 1)}})
				ticket := latestTicket.Load()
				mu.Unlock()
				if err != nil {
					t.Error(err)
					return
				}
				<-ticket.done
				if ticket.err != nil {
					t.Error(ticket.err)
					return
				}
			}
		}(k)
	}
	wg.Wait()
	if err := flushWAL(); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(journal.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	n := 0
	for {
		var e Event
		err := dec.Decode(&e)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if e.Kind != "user" {
			t.Fatal(e.Kind)
		}
		n++
	}
	if n != 400 {
		t.Fatalf("want 400 durable events, got %d", n)
	}
}
