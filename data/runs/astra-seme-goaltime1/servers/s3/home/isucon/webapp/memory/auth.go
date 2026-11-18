package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

var authClient = &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{MaxIdleConns: 128, MaxIdleConnsPerHost: 128}}

func authRequest(path, password, hash string) ([]byte, error) {
	b, _ := json.Marshal(map[string]string{"Password": password, "Hash": hash})
	res, e := authClient.Post("http://10.99.0.13:8090/"+path, "application/json", bytes.NewReader(b))
	if e != nil {
		return nil, e
	}
	defer res.Body.Close()
	body, e := io.ReadAll(res.Body)
	if e != nil {
		return nil, e
	}
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("bcrypt worker status %d", res.StatusCode)
	}
	return body, nil
}
