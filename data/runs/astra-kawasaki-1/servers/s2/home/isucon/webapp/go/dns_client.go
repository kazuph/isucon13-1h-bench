package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

var dnsHTTPClient = &http.Client{Timeout: 10 * time.Second}

func updateDNS(ctx context.Context, path string, data interface{}) error {
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "http://10.99.0.11:8053/"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := dnsHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 204 {
		b, _ := io.ReadAll(res.Body)
		return fmt.Errorf("DNS update failed: %s %s", res.Status, b)
	}
	return nil
}
