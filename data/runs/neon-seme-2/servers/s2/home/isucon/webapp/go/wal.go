package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	walOpRegister    byte = 1
	walOpSetIcon     byte = 2
	walOpReserve     byte = 3
	walOpEnter       byte = 4
	walOpExit        byte = 5
	walOpLivecomment byte = 6
	walOpReaction    byte = 7
	walOpReport      byte = 8
	walOpModerate    byte = 9
)

type WAL struct {
	dir     string
	walPath string
	iconDir string

	mu      sync.Mutex
	file    *os.File
	bw      *bufio.Writer
	ch      chan []byte
	flushCh chan chan struct{}
	stopCh  chan struct{}
	doneCh  chan struct{}

	iconMu       sync.Mutex
	persistedImg map[string]bool
}

func NewWAL(dir string) (*WAL, error) {
	iconDir := filepath.Join(dir, "icons")
	if err := os.MkdirAll(iconDir, 0755); err != nil {
		return nil, err
	}
	walPath := filepath.Join(dir, "wal.bin")
	f, err := os.OpenFile(walPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}

	persisted := make(map[string]bool)
	if entries, err := os.ReadDir(iconDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				persisted[e.Name()] = true
			}
		}
	}

	w := &WAL{
		dir:          dir,
		walPath:      walPath,
		iconDir:      iconDir,
		file:         f,
		bw:           bufio.NewWriterSize(f, 256*1024),
		ch:           make(chan []byte, 65536),
		flushCh:      make(chan chan struct{}, 8),
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
		persistedImg: persisted,
	}
	go w.loop()
	return w, nil
}

func (w *WAL) loop() {
	defer close(w.doneCh)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	dirty := false
	for {
		select {
		case rec := <-w.ch:
			var hdr [4]byte
			binary.LittleEndian.PutUint32(hdr[:], uint32(len(rec)))
			w.bw.Write(hdr[:])
			w.bw.Write(rec)
			dirty = true
			// Drain available records in batch
		drainBatch:
			for i := 0; i < 256; i++ {
				select {
				case r2 := <-w.ch:
					binary.LittleEndian.PutUint32(hdr[:], uint32(len(r2)))
					w.bw.Write(hdr[:])
					w.bw.Write(r2)
				default:
					break drainBatch
				}
			}
		case <-ticker.C:
			if dirty {
				w.bw.Flush()
				dirty = false
			}
		case ack := <-w.flushCh:
			w.drainLocked()
			w.bw.Flush()
			w.file.Sync()
			dirty = false
			close(ack)
		case <-w.stopCh:
			w.drainLocked()
			w.bw.Flush()
			w.file.Sync()
			w.file.Close()
			return
		}
	}
}

func (w *WAL) drainLocked() {
	var hdr [4]byte
	for {
		select {
		case rec := <-w.ch:
			binary.LittleEndian.PutUint32(hdr[:], uint32(len(rec)))
			w.bw.Write(hdr[:])
			w.bw.Write(rec)
		default:
			return
		}
	}
}

func (w *WAL) Append(rec []byte) {
	if w == nil {
		return
	}
	w.ch <- rec
}

func (w *WAL) PersistIconBlob(hash string, data []byte) {
	if w == nil || len(data) == 0 {
		return
	}
	w.iconMu.Lock()
	if w.persistedImg[hash] {
		w.iconMu.Unlock()
		return
	}
	w.persistedImg[hash] = true
	w.iconMu.Unlock()

	// Write atomically in background so hot path is never blocked on fsync
	imgCopy := make([]byte, len(data))
	copy(imgCopy, data)
	go func() {
		dst := filepath.Join(w.iconDir, hash)
		tmp := dst + ".tmp"
		if err := os.WriteFile(tmp, imgCopy, 0644); err == nil {
			_ = os.Rename(tmp, dst)
		}
	}()
}

func (w *WAL) FlushSync() {
	if w == nil {
		return
	}
	ack := make(chan struct{})
	w.flushCh <- ack
	<-ack
}

func (w *WAL) Reset() error {
	if w == nil {
		return nil
	}
	w.FlushSync()
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.file.Truncate(0); err != nil {
		return err
	}
	if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	w.bw.Reset(w.file)
	return nil
}

func (w *WAL) Close() {
	if w == nil {
		return
	}
	close(w.stopCh)
	<-w.doneCh
}

// Binary encoding helpers for WAL records
type walBuf []byte

func (b *walBuf) u8(v byte) { *b = append(*b, v) }
func (b *walBuf) i64(v int64) {
	var tmp [8]byte
	binary.LittleEndian.PutUint64(tmp[:], uint64(v))
	*b = append(*b, tmp[:]...)
}
func (b *walBuf) str(s string) {
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:], uint32(len(s)))
	*b = append(*b, tmp[:]...)
	*b = append(*b, s...)
}
func (b *walBuf) bool(v bool) {
	if v {
		*b = append(*b, 1)
	} else {
		*b = append(*b, 0)
	}
}

type walReader struct {
	b   []byte
	pos int
}

func (r *walReader) u8() (byte, bool) {
	if r.pos+1 > len(r.b) {
		return 0, false
	}
	v := r.b[r.pos]
	r.pos++
	return v, true
}
func (r *walReader) i64() (int64, bool) {
	if r.pos+8 > len(r.b) {
		return 0, false
	}
	v := int64(binary.LittleEndian.Uint64(r.b[r.pos : r.pos+8]))
	r.pos += 8
	return v, true
}
func (r *walReader) str() (string, bool) {
	if r.pos+4 > len(r.b) {
		return "", false
	}
	n := int(binary.LittleEndian.Uint32(r.b[r.pos : r.pos+4]))
	r.pos += 4
	if r.pos+n > len(r.b) {
		return "", false
	}
	s := string(r.b[r.pos : r.pos+n])
	r.pos += n
	return s, true
}
func (r *walReader) bool() (bool, bool) {
	v, ok := r.u8()
	return v != 0, ok
}

func (s *Store) ReplayWAL(dir string) (int, error) {
	// First load persisted icon blobs
	iconDir := filepath.Join(dir, "icons")
	if entries, err := os.ReadDir(iconDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || len(e.Name()) != 64 {
				continue
			}
			if data, err := os.ReadFile(filepath.Join(iconDir, e.Name())); err == nil {
				s.iconBlobs[e.Name()] = data
			}
		}
	}

	walPath := filepath.Join(dir, "wal.bin")
	f, err := os.Open(walPath)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()

	br := bufio.NewReaderSize(f, 256*1024)
	count := 0
	var hdr [4]byte
	for {
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return count, err
		}
		n := binary.LittleEndian.Uint32(hdr[:])
		if n == 0 || n > 64*1024*1024 {
			break
		}
		rec := make([]byte, n)
		if _, err := io.ReadFull(br, rec); err != nil {
			break
		}
		if err := s.applyWALRecord(rec); err != nil {
			return count, fmt.Errorf("wal record %d: %w", count, err)
		}
		count++
	}
	return count, nil
}
