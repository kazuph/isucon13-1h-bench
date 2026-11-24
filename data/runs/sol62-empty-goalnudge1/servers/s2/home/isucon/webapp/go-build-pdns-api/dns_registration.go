package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"time"
)

const (
	dnsZone       = "u.isucon.dev."
	dnsRecordsAPI = "http://127.0.0.1:8081/api/v1/servers/localhost/zones/" + dnsZone
)

var (
	dnsAPIKey    string
	dnsAPIClient = &http.Client{
		Timeout: 800 * time.Millisecond,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
)

func registerDNS(ctx context.Context, name string) error {
	if dnsAPIKey != "" && isDNSLabel(name) {
		if err := addDNSRecord(ctx, name); err == nil {
			return nil
		} else {
			log.Printf("PowerDNS API update failed, retrying with pdnsutil: %v", err)
		}
	}
	out, err := exec.CommandContext(ctx, "sudo", "-n", "-u", "pdns", "pdnsutil",
		"add-record", "u.isucon.dev", name, "A", "60", powerDNSSubdomainAddress).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w", out, err)
	}
	return nil
}

func isDNSLabel(name string) bool {
	if len(name) == 0 || len(name) > 63 || name[0] == '-' || name[len(name)-1] == '-' {
		return false
	}
	for i := 0; i < len(name); i++ {
		ch := name[i]
		if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') && ch != '-' {
			return false
		}
	}
	return true
}

func addDNSRecord(ctx context.Context, name string) error {
	type record struct {
		Content  string `json:"content"`
		Disabled bool   `json:"disabled"`
	}
	type rrset struct {
		Name       string   `json:"name"`
		Type       string   `json:"type"`
		TTL        int      `json:"ttl"`
		ChangeType string   `json:"changetype"`
		Records    []record `json:"records"`
	}
	payload, err := json.Marshal(struct {
		RRSets []rrset `json:"rrsets"`
	}{
		RRSets: []rrset{{
			Name: name + "." + dnsZone, Type: "A", TTL: 60, ChangeType: "EXTEND",
			Records: []record{{Content: powerDNSSubdomainAddress}},
		}},
	})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPatch, dnsRecordsAPI, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-API-Key", dnsAPIKey)

	response, err := dnsAPIClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("PowerDNS API returned HTTP %d", response.StatusCode)
	}
	return nil
}
