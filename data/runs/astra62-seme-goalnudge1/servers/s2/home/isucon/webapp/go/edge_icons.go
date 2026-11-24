package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"github.com/jmoiron/sqlx"
)

var safeIconName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type edgeIcons struct {
	root     string
	fallback string
	db       *sqlx.DB
	mu       sync.Mutex
}

var edge edgeIcons

func (e *edgeIcons) store(image []byte) (string, error) {
	hash := fmt.Sprintf("%x", sha256.Sum256(image))
	dest := filepath.Join(e.root, "objects", hash)
	if _, err := os.Stat(filepath.Join(dest, hash)); err == nil {
		return hash, nil
	}
	tmp, err := os.MkdirTemp(filepath.Join(e.root, "objects"), ".img-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err = os.Chmod(tmp, 0755); err != nil {
		return "", err
	}
	if err = os.WriteFile(filepath.Join(tmp, "image"), image, 0644); err != nil {
		return "", err
	}
	if err = os.WriteFile(filepath.Join(tmp, hash), nil, 0644); err != nil {
		return "", err
	}
	if err = os.Rename(tmp, dest); err != nil {
		return "", err
	}
	return hash, nil
}
func (e *edgeIcons) link(dir, name, hash string) error {
	if !safeIconName.MatchString(name) {
		return nil
	}
	tmp, err := os.CreateTemp(dir, ".link-")
	if err != nil {
		return err
	}
	path := tmp.Name()
	tmp.Close()
	os.Remove(path)
	defer os.Remove(path)
	if err = os.Symlink(filepath.Join(e.root, "objects", hash), path); err != nil {
		return err
	}
	return os.Rename(path, filepath.Join(dir, name))
}
func (e *edgeIcons) reset() error {
	if e.root == "" {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	dir, err := os.MkdirTemp(e.root, "users-")
	if err != nil {
		return err
	}
	if err = os.Chmod(dir, 0755); err != nil {
		return err
	}
	var users []struct {
		Name  string
		Image []byte
	}
	if err = e.db.Select(&users, "SELECT u.name,COALESCE(o.image,i.image) image FROM users u LEFT JOIN icons i ON i.user_id=u.id LEFT JOIN icon_refs r ON r.user_id=u.id LEFT JOIN icon_objects o ON o.hash=r.hash"); err != nil {
		return err
	}
	for _, u := range users {
		hash := e.fallback
		if u.Image != nil {
			hash, err = e.store(u.Image)
			if err != nil {
				return err
			}
		}
		if err = e.link(dir, u.Name, hash); err != nil {
			return err
		}
	}
	current := filepath.Join(e.root, "current")
	old, _ := os.Readlink(current)
	next := filepath.Join(e.root, "next")
	os.Remove(next)
	if err = os.Symlink(dir, next); err != nil {
		return err
	}
	if err = os.Rename(next, current); err != nil {
		return err
	}
	if old != "" && filepath.Dir(old) == e.root && filepath.Base(old) != "objects" {
		os.RemoveAll(old)
	}
	return nil
}
func (e *edgeIcons) publish(name string, image []byte) error {
	if e.root == "" {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	hash := e.fallback
	if image != nil {
		var err error
		hash, err = e.store(image)
		if err != nil {
			return err
		}
	}
	return e.link(filepath.Join(e.root, "current"), name, hash)
}
func startEdgeIcons() error {
	edge.root = os.Getenv("ISUPIPE_EDGE_ICONS")
	if edge.root == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(edge.root, "objects"), 0755); err != nil {
		return err
	}
	var err error
	edge.db, err = sqlx.Connect("mysql", os.Getenv("ISUPIPE_EDGE_DB"))
	if err != nil {
		return err
	}
	img, err := os.ReadFile(fallbackImage)
	if err != nil {
		return err
	}
	edge.fallback, err = edge.store(img)
	if err != nil {
		return err
	}
	return edge.reset()
}

func (e *edgeIcons) publishHash(name, hash string) error {
	if len(hash) != 64 {
		return os.ErrNotExist
	}
	for _, r := range hash {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return os.ErrNotExist
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := os.Stat(filepath.Join(e.root, "objects", hash, hash)); err != nil {
		return err
	}
	return e.link(filepath.Join(e.root, "current"), name, hash)
}
