package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/jmoiron/sqlx"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type cachedIcon struct {
	Data []byte
	Hash string
}

var iconMu sync.RWMutex
var iconUpdates sync.Mutex
var icons = map[string]cachedIcon{}
var defaultIcon cachedIcon
var iconDB *sqlx.DB

func cacheIcon(data []byte) cachedIcon {
	return cachedIcon{Data: data, Hash: fmt.Sprintf("%x", sha256.Sum256(data))}
}
func reloadIcons() error {
	iconUpdates.Lock()
	defer iconUpdates.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var rows []struct {
		Name  string `db:"name"`
		Image []byte `db:"image"`
	}
	if err := iconDB.SelectContext(ctx, &rows, "SELECT u.name,i.image FROM users u LEFT JOIN icons i ON i.user_id=u.id"); err != nil {
		return err
	}
	tags := make([]struct {
		ID   int64  `db:"id" json:"id"`
		Name string `db:"name" json:"name"`
	}, 0)
	if err := iconDB.SelectContext(ctx, &tags, "SELECT id,name FROM tags ORDER BY id"); err != nil {
		return err
	}
	tagJSON, err := json.Marshal(struct {
		Tags interface{} `json:"tags"`
	}{tags})
	if err != nil {
		return err
	}
	const tagPath = "/home/isucon/webapp/public/api-tag.json"
	if err = os.WriteFile(tagPath+".tmp", tagJSON, 0644); err != nil {
		return err
	}
	if err = os.Rename(tagPath+".tmp", tagPath); err != nil {
		return err
	}
	next := make(map[string]cachedIcon, len(rows))
	for _, r := range rows {
		if r.Image == nil {
			next[r.Name] = defaultIcon
		} else {
			next[r.Name] = cacheIcon(r.Image)
		}
	}
	iconMu.Lock()
	icons = next
	iconMu.Unlock()
	return nil
}
func setupIconCache() {
	data, err := os.ReadFile("/home/isucon/webapp/img/NoImage.jpg")
	if err != nil {
		log.Fatal(err)
	}
	defaultIcon = cacheIcon(data)
	iconDB, err = sqlx.Open("mysql", "isucon:isucon@tcp(10.99.0.13:3306)/isupipe?timeout=3s&readTimeout=20s&interpolateParams=true")
	if err != nil {
		log.Fatal(err)
	}
	iconDB.SetMaxOpenConns(2)
	iconDB.SetMaxIdleConns(2)
	http.HandleFunc("/api/user/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(405)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/icon") {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/user/"), "/icon")
		iconMu.RLock()
		icon, ok := icons[name]
		iconMu.RUnlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		etag := "\"" + icon.Hash + "\""
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(304)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(icon.Data)
	})
	http.HandleFunc("/iconcache/reload", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Zone-Token") != updateToken {
			http.Error(w, "forbidden", 403)
			return
		}
		if err := reloadIcons(); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(204)
	})
	put := func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Zone-Token") != updateToken {
			http.Error(w, "forbidden", 403)
			return
		}
		name := r.URL.Query().Get("username")
		if name == "" {
			http.Error(w, "invalid name", 400)
			return
		}
		icon := defaultIcon
		if r.URL.Path == "/iconcache/put" {
			data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 10<<20))
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			icon = cacheIcon(data)
		}
		iconUpdates.Lock()
		iconMu.Lock()
		icons[name] = icon
		iconMu.Unlock()
		iconUpdates.Unlock()
		w.WriteHeader(204)
	}
	http.HandleFunc("/iconcache/put", put)
	http.HandleFunc("/iconcache/default", put)
	go func() {
		for {
			if err := reloadIcons(); err == nil {
				return
			} else {
				log.Printf("icon warmup: %v", err)
			}
			time.Sleep(time.Second)
		}
	}()
}
