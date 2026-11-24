package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

const markerRoot = "/run/isupipe-edge/icons"

var safeName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,63}$`)
var digest = regexp.MustCompile(`^[a-f0-9]{64}$`)
var markerLock sync.Mutex

// A marker is valid only for the named account and its committed image digest.
func writeMarker(name, hash string) error {
	if !safeName.MatchString(name) {
		return nil
	}
	account := filepath.Join(markerRoot, name)
	if hash == "" {
		return os.RemoveAll(account)
	}
	if !digest.MatchString(hash) {
		return errors.New("invalid digest")
	}
	if err := os.MkdirAll(account, 0755); err != nil {
		return err
	}
	marker, err := os.OpenFile(filepath.Join(account, hash), os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	return marker.Close()
}

func iconUpdate(w http.ResponseWriter, r *http.Request) {
	markerLock.Lock()
	defer markerLock.Unlock()
	var err error
	switch r.URL.Path {
	case "/icons/reset":
		err = os.RemoveAll(markerRoot)
		if err == nil {
			err = os.MkdirAll(markerRoot, 0755)
		}
	case "/icons/load":
		var manifest map[string]string
		if err = json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&manifest); err == nil {
			for name, hash := range manifest {
				if err = writeMarker(name, hash); err != nil {
					break
				}
			}
		}
	case "/icons/update":
		name := r.URL.Query().Get("name")
		hash := r.URL.Query().Get("hash")
		err = writeMarker(name, "")
		if err == nil && hash != "" {
			err = writeMarker(name, hash)
		}
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(204)
}
