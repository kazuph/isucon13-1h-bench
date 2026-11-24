package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var blobCache sync.Map
var blobWriteMu sync.Mutex

func blobPath(h string) string { return filepath.Join(dataDir, "blobs", h[:2], h) }
func putBlob(h string, b []byte) ([]byte, error) {
	if v, ok := blobCache.Load(h); ok {
		return v.([]byte), nil
	}
	blobWriteMu.Lock()
	defer blobWriteMu.Unlock()
	if v, ok := blobCache.Load(h); ok {
		return v.([]byte), nil
	}
	path := blobPath(h)
	if _, e := os.Stat(path); os.IsNotExist(e) {
		dir := filepath.Dir(path)
		if e = os.MkdirAll(dir, 0755); e != nil {
			return nil, e
		}
		f, e := os.CreateTemp(dir, ".write-")
		if e != nil {
			return nil, e
		}
		tmp := f.Name()
		defer os.Remove(tmp)
		if _, e = f.Write(b); e == nil {
			e = f.Sync()
		}
		f.Close()
		if e != nil {
			return nil, e
		}
		if e = os.Rename(tmp, path); e != nil {
			return nil, e
		}
		if e = syncDir(dir); e != nil {
			return nil, e
		}
		if e = syncDir(filepath.Dir(dir)); e != nil {
			return nil, e
		}
		if e = syncDir(dataDir); e != nil {
			return nil, e
		}
	} else if e != nil {
		return nil, e
	}
	blobCache.Store(h, b)
	return b, nil
}
func readBlob(h string) ([]byte, error) {
	if v, ok := blobCache.Load(h); ok {
		return v.([]byte), nil
	}
	b, e := os.ReadFile(blobPath(h))
	if e != nil {
		return nil, e
	}
	if hash(b) != h {
		return nil, fmt.Errorf("corrupt image blob %s", h)
	}
	v, _ := blobCache.LoadOrStore(h, b)
	return v.([]byte), nil
}
func hydrateIcons() error {
	for id, h := range state.IconRefs {
		var b []byte
		var e error
		if existing := state.Icons[id]; existing != nil {
			b, e = putBlob(h, existing)
		} else if h == hash(nil) {
			b, e = putBlob(h, nil)
		} else {
			b, e = readBlob(h)
		}
		if e != nil {
			return e
		}
		state.Icons[id] = b
		hashes[id] = h
	}
	return nil
}
