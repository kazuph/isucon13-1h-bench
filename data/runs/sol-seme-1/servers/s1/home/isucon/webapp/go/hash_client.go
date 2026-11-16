package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"io"
	"net/http"
	"time"
)

var hashClient = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{MaxIdleConns: 128, MaxIdleConnsPerHost: 128, IdleConnTimeout: 90 * time.Second}}

func hashCall(path string, req interface{}) ([]byte, int, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return nil, 0, err
	}
	resp, err := hashClient.Post("http://10.99.0.13:9090/"+path, "application/json", bytes.NewReader(b))
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return body, resp.StatusCode, err
}
func generatePasswordHash(password []byte) ([]byte, error) {
	body, status, err := hashCall("hash", map[string]string{"Password": string(password)})
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("hash worker status %d: %s", status, body)
	}
	return body, nil
}
func comparePasswordHash(hash, password []byte) error {
	body, status, err := hashCall("compare", map[string]string{"Password": string(password), "Hash": string(hash)})
	if err != nil {
		return err
	}
	if status == 401 {
		return bcrypt.ErrMismatchedHashAndPassword
	}
	if status != 204 {
		return fmt.Errorf("hash worker status %d: %s", status, body)
	}
	return nil
}
