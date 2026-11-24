package main

import (
	"context"
	"sync"
)

type objectShard struct {
	sync.Mutex
	known map[[32]byte]bool
}

var objects [64]objectShard

// Once written, an object is immutable. Account updates publish only its digest.
func storeIconObject(ctx context.Context, digest [32]byte, image []byte) error {
	shard := &objects[int(digest[0])%len(objects)]
	shard.Lock()
	defer shard.Unlock()
	if shard.known[digest] {
		return nil
	}
	if _, err := dbConn.ExecContext(ctx, "INSERT INTO icon_objects(digest,image) VALUES (?,?) ON DUPLICATE KEY UPDATE digest=digest", digest[:], image); err != nil {
		return err
	}
	if shard.known == nil {
		shard.known = make(map[[32]byte]bool)
	}
	shard.known[digest] = true
	return nil
}
func forgetIconObjects() {
	for i := range objects {
		objects[i].Lock()
		objects[i].known = nil
		objects[i].Unlock()
	}
}
