package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"
)

var dnsRefreshClient = &http.Client{Timeout: 4 * time.Second}

func refreshDNS(ctx context.Context) error {
	url := os.Getenv("ISUPIPE_DNS_REFRESH_URL")
	if url == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("ISUPIPE_DNS_REFRESH_TOKEN"))
	resp, err := dnsRefreshClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("DNS refresh returned %s", resp.Status)
	}
	return nil
}
