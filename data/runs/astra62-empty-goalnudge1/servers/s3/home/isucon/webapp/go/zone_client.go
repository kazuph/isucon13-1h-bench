package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"time"
)

var zoneClient = http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     30 * time.Second,
	},
}

func updateZone(ctx context.Context, label string) error {
	base := os.Getenv("ZONE_CONTROL_URL")
	if base == "" {
		var cmd *exec.Cmd
		if label == "" {
			cmd = exec.CommandContext(ctx, "../pdns/init_zone.sh")
		} else {
			cmd = exec.CommandContext(ctx, "../pdns/add_record.sh", label, powerDNSSubdomainAddress)
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("zone update: %s: %w", out, err)
		}
		return nil
	}
	path := "/reset"
	if label != "" {
		path = "/add?label=" + url.QueryEscape(label)
	}
	return edgePost(ctx, path, nil)
}

func edgePost(ctx context.Context, path string, body io.Reader) error {
	base := os.Getenv("ZONE_CONTROL_URL")
	if base == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("ZONE_CONTROL_TOKEN"))
	res, err := zoneClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		return fmt.Errorf("zone update returned %s", res.Status)
	}
	return nil
}

func publishIcons(ctx context.Context, users map[int64]User) error {
	if err := edgePost(ctx, "/icons/reset", nil); err != nil {
		return err
	}
	manifest := make(map[string]string, len(users))
	for _, user := range users {
		manifest[user.Name] = user.IconHash
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	return edgePost(ctx, "/icons/load", bytes.NewReader(data))
}

func iconMarker(ctx context.Context, name, hash string) error {
	return edgePost(ctx, "/icons/update?name="+url.QueryEscape(name)+"&hash="+url.QueryEscape(hash), nil)
}
