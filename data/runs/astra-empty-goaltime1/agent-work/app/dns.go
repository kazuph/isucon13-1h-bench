package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

var dnsHTTPClient = &http.Client{Timeout: 5 * time.Second}

func addDNSRecord(ctx context.Context, username string) error {
	baseURL := os.Getenv("ISUCON_DNS_API_URL")
	if baseURL == "" {
		baseURL = "http://10.99.0.11:8081"
	}
	key := os.Getenv("ISUCON_DNS_API_KEY")
	if key == "" {
		key = "isudns"
	}
	name := username
	if !strings.HasSuffix(name, ".") {
		name += ".u.isucon.dev."
	}
	payload := map[string]interface{}{"rrsets": []interface{}{map[string]interface{}{
		"name": name, "type": "A", "ttl": 60, "changetype": "REPLACE",
		"records": []interface{}{map[string]interface{}{"content": powerDNSSubdomainAddress, "disabled": false}},
	}}}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, baseURL+"/api/v1/servers/localhost/zones/u.isucon.dev.", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := dnsHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	message, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("DNS update returned %d: %s", resp.StatusCode, message)
	}
	return nil
}
