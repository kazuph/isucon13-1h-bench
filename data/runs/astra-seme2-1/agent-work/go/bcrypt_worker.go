package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"os"
	"time"
)

type passwordWork struct {
	Password []byte
	Hash     []byte
	Verify   bool
}

var passwordClient = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{MaxIdleConns: 128, MaxIdleConnsPerHost: 128, MaxConnsPerHost: 128}}

func servePasswordWorker() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/password", func(w http.ResponseWriter, r *http.Request) {
		var p passwordWork
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, "bad JSON", 400)
			return
		}
		if p.Verify {
			if err := bcrypt.CompareHashAndPassword(p.Hash, p.Password); err != nil {
				http.Error(w, "mismatch", 401)
				return
			}
			w.WriteHeader(200)
			return
		}
		b, err := bcrypt.GenerateFromPassword(p.Password, bcryptDefaultCost)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Write(b)
	})
	return http.ListenAndServe(":8090", mux)
}
func passwordHash(password []byte) ([]byte, error) {
	endpoint := os.Getenv("BCRYPT_ENDPOINT")
	if endpoint == "" {
		return bcrypt.GenerateFromPassword(password, bcryptDefaultCost)
	}
	b, _ := json.Marshal(passwordWork{Password: password})
	r, err := passwordClient.Post(endpoint, "application/json", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return nil, fmt.Errorf("password worker status %d", r.StatusCode)
	}
	var result bytes.Buffer
	_, err = result.ReadFrom(r.Body)
	return result.Bytes(), err
}
func passwordVerify(hash, password []byte) error {
	endpoint := os.Getenv("BCRYPT_ENDPOINT")
	if endpoint == "" {
		return bcrypt.CompareHashAndPassword(hash, password)
	}
	b, _ := json.Marshal(passwordWork{Password: password, Hash: hash, Verify: true})
	r, err := passwordClient.Post(endpoint, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return fmt.Errorf("password verification status %d", r.StatusCode)
	}
	return nil
}
