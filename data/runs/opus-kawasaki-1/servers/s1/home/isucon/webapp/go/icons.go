package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// Icons are stored on disk so that nginx can answer conditional GETs directly.
//   <iconDir>/<username>/<sha256>  : empty marker for the current icon hash (always present for every user)
//   <iconDir>/<username>.jpg       : custom icon image (absent => fallback image)

var (
	iconDir     = "/home/isucon/icons"
	iconCounter atomic.Int64
	iconFileMu  sync.Mutex
)

func init() {
	if v := os.Getenv("ISUCON13_ICON_DIR"); v != "" {
		iconDir = v
	}
}

func validUsernameForPath(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, "/\\\x00")
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// setIconMarker makes <name>/<hash> the only marker for the user.
func setIconMarker(name, hash string) error {
	dir := filepath.Join(iconDir, name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, hash), nil, 0644); err != nil {
		return err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if e.Name() != hash {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return nil
}

func saveIcon(name string, image []byte, hash string) error {
	if !validUsernameForPath(name) {
		return nil
	}
	iconFileMu.Lock()
	defer iconFileMu.Unlock()
	if err := os.MkdirAll(iconDir, 0755); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(iconDir, name+".jpg"), image); err != nil {
		return err
	}
	return setIconMarker(name, hash)
}

func readIconFile(name string) ([]byte, bool) {
	if !validUsernameForPath(name) {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(iconDir, name+".jpg"))
	if err != nil {
		return nil, false
	}
	return b, true
}

func ensureFallbackMarker(name string) {
	if !validUsernameForPath(name) {
		return
	}
	iconFileMu.Lock()
	defer iconFileMu.Unlock()
	os.MkdirAll(filepath.Join(iconDir, name), 0755)
	os.WriteFile(filepath.Join(iconDir, name, fallbackImageHash), nil, 0644)
}

// resetIcons wipes the icon dir and creates fallback markers for all users.
func resetIcons(ctx context.Context) error {
	iconFileMu.Lock()
	defer iconFileMu.Unlock()
	os.RemoveAll(iconDir)
	if err := os.MkdirAll(iconDir, 0755); err != nil {
		return err
	}
	iconCounter.Store(0)
	var names []string
	if err := dbConn.SelectContext(ctx, &names, "SELECT name FROM users"); err != nil {
		return err
	}
	for _, n := range names {
		if !validUsernameForPath(n) {
			continue
		}
		d := filepath.Join(iconDir, n)
		os.Mkdir(d, 0755)
		os.WriteFile(filepath.Join(d, fallbackImageHash), nil, 0644)
	}
	return nil
}

// loadIconsFromDisk fills the hash cache (by user id) from disk at startup.
func loadIconsFromDisk(ctx context.Context) error {
	type urow struct {
		ID   int64  `db:"id"`
		Name string `db:"name"`
	}
	var users []urow
	if err := dbConn.SelectContext(ctx, &users, "SELECT id, name FROM users"); err != nil {
		return err
	}
	if err := os.MkdirAll(iconDir, 0755); err != nil {
		return err
	}
	var custom int64
	for _, u := range users {
		if !validUsernameForPath(u.Name) {
			continue
		}
		dir := filepath.Join(iconDir, u.Name)
		ents, err := os.ReadDir(dir)
		hash := ""
		if err == nil {
			for _, e := range ents {
				if len(e.Name()) == 64 {
					hash = e.Name()
				}
			}
		}
		if hash == "" {
			os.MkdirAll(dir, 0755)
			os.WriteFile(filepath.Join(dir, fallbackImageHash), nil, 0644)
			hash = fallbackImageHash
		}
		if hash != fallbackImageHash {
			custom++
		}
		setIconHash(u.ID, hash)
	}
	iconCounter.Store(custom)
	return nil
}
