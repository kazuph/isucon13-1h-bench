package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"time"
)

var dnsClient = &http.Client{Timeout: 5 * time.Second}

func pushDNS(v interface{}, path string) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	r, e := dnsClient.Post("http://10.99.0.11:8090"+path, "application/json", bytes.NewReader(b))
	if e != nil {
		return e
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		return fmt.Errorf("dns update status %d", r.StatusCode)
	}
	return nil
}
func pushNames() error {
	a := make([]string, 0, len(names))
	for n := range names {
		a = append(a, n)
	}
	return pushDNS(a, "/names")
}
func pushOneName(n string) error { return pushDNS([]string{n}, "/add") }
func runSeed() ([]byte, error)   { return exec.Command("../sql/init.sh").CombinedOutput() }
