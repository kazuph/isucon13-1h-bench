package main

import (
	"bytes"
	"encoding/json"
	"os"
	"sync"
	"testing"
)

func TestImageContentAddressedPersistence(t *testing.T) {
	dataDir = t.TempDir()
	blobCache = sync.Map{}
	data := bytes.Repeat([]byte("image content"), 8192)
	h := hash(data)
	a, e := putBlob(h, data)
	if e != nil {
		t.Fatal(e)
	}
	b, e := putBlob(h, append([]byte{}, data...))
	if e != nil {
		t.Fatal(e)
	}
	if &a[0] != &b[0] {
		t.Fatal("duplicate content allocated twice")
	}
	event := Event{Kind: "icon", ID: 1, UserID: 1, Image: a, ImageHash: h}
	encoded, e := json.Marshal(event)
	if e != nil {
		t.Fatal(e)
	}
	if len(encoded) > 256 {
		t.Fatalf("image bytes leaked into event log: %d", len(encoded))
	}
	var restored Event
	if e = json.Unmarshal(encoded, &restored); e != nil {
		t.Fatal(e)
	}
	if len(restored.Image) != 0 {
		t.Fatal("event unexpectedly contains image")
	}
	state = newState()
	state.Users[1] = UserModel{ID: 1}
	rebuild()
	apply(restored)
	blobCache = sync.Map{}
	if e := hydrateIcons(); e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(icon(1), data) || hashes[1] != h {
		t.Fatal("image reference replay failed")
	}
	blobCache = sync.Map{}
	if e = os.WriteFile(blobPath(h), []byte("corrupt"), 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = readBlob(h); e == nil {
		t.Fatal("image corruption not detected")
	}
	blobCache = sync.Map{}
}
