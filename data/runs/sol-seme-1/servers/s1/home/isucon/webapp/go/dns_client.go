package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

var dnsClient = &http.Client{Timeout: 3 * time.Second}

func dnsCall(path string, body []byte) error {
	resp, err := dnsClient.Post("http://10.99.0.11:9080/"+path, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 204 {
		return fmt.Errorf("DNS update returned %d", resp.StatusCode)
	}
	return nil
}
func reloadDNS() error { return dnsCall("reload", nil) }
func publishDNS(name string) error {
	b, _ := json.Marshal(map[string]string{"Name": name + ".u.isucon.dev", "IP": powerDNSSubdomainAddress})
	return dnsCall("record", b)
}
