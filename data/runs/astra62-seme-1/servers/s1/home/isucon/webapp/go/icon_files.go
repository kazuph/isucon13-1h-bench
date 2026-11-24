package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

const iconDirectory = "/var/lib/isupipe-icons"

var fileMu sync.Mutex
var fileDatabase *sql.DB
var fileRoot string
var defaultHash string
var fileUsername = regexp.MustCompile("^[a-zA-Z0-9_-]+$")

func installImage(root string, image []byte) (string, error) {
	sum := sha256.Sum256(image)
	hash := hex.EncodeToString(sum[:])
	dir := filepath.Join(root, "objects", hash)
	if _, err := os.Stat(filepath.Join(dir, "image.jpg")); err == nil {
		return hash, nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "match."+hash), nil, 0644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "image.jpg"), image, 0644); err != nil {
		return "", err
	}
	return hash, nil
}
func linkImage(root, name, hash string) error {
	if !fileUsername.MatchString(name) {
		return nil
	}
	users := filepath.Join(root, "users")
	temp := filepath.Join(users, ".incoming")
	if err := os.Symlink(filepath.Join("..", "objects", hash), temp); err != nil {
		return err
	}
	defer os.Remove(temp)
	return os.Rename(temp, filepath.Join(users, name))
}
func refreshFiles() error {
	fileMu.Lock()
	defer fileMu.Unlock()
	root, err := os.MkdirTemp(iconDirectory, "set-")
	if err != nil {
		return err
	}
	installed := false
	defer func() {
		if !installed {
			os.RemoveAll(root)
		}
	}()
	if err = os.Chmod(root, 0755); err != nil {
		return err
	}
	if err = os.Mkdir(filepath.Join(root, "users"), 0755); err != nil {
		return err
	}
	image, err := os.ReadFile(fallbackImage)
	if err != nil {
		return err
	}
	fallback, err := installImage(root, image)
	if err != nil {
		return err
	}
	rows, err := fileDatabase.Query("SELECT u.name,i.image FROM users u LEFT JOIN icons i ON i.user_id=u.id")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var custom []byte
		if err = rows.Scan(&name, &custom); err != nil {
			return err
		}
		hash := fallback
		if custom != nil {
			hash, err = installImage(root, custom)
			if err != nil {
				return err
			}
		}
		if err = linkImage(root, name, hash); err != nil {
			return err
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	next := filepath.Join(iconDirectory, ".next")
	os.Remove(next)
	if err = os.Symlink(root, next); err != nil {
		return err
	}
	if err = os.Rename(next, filepath.Join(iconDirectory, "current")); err != nil {
		return err
	}
	installed = true
	fileRoot = root
	defaultHash = fallback
	stale, err := filepath.Glob(filepath.Join(iconDirectory, "set-*"))
	if err != nil {
		return err
	}
	for _, dir := range stale {
		if dir != root {
			os.RemoveAll(dir)
		}
	}
	return nil
}
func updateFile(name string, image []byte) error {
	fileMu.Lock()
	defer fileMu.Unlock()
	if fileRoot == "" {
		return fmt.Errorf("icon directory unavailable")
	}
	hash := defaultHash
	if image != nil {
		var err error
		hash, err = installImage(fileRoot, image)
		if err != nil {
			return err
		}
	}
	return linkImage(fileRoot, name, hash)
}
