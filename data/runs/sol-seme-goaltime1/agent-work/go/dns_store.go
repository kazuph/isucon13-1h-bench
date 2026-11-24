package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/jmoiron/sqlx"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
)

var dnsDB *sqlx.DB
var dnsOnce sync.Once
var dnsErr error

func registerDNS(name string) error {
	// The authoritative DNS database lives on the ingress server.
	// Parameter binding preserves the username as data.
	dnsOnce.Do(func() {
		addr := os.Getenv("ISUCON_DNS_DB_ADDRESS")
		if addr == "" {
			addr = "10.99.0.11:3306"
		}
		var err error
		dnsDB, err = sqlx.Open("mysql", "isudns:isudns@tcp("+addr+")/isudns?parseTime=true")
		if err != nil {
			dnsErr = err
			return
		}
		dnsDB.SetMaxOpenConns(8)
		dnsDB.SetMaxIdleConns(8)
	})
	if dnsErr != nil {
		return dnsErr
	}
	result, err := dnsDB.Exec("INSERT INTO records(domain_id,name,type,content,ttl,prio,disabled,auth) SELECT id,?,'A',?,60,0,0,1 FROM domains WHERE name='u.isucon.dev'", name+".u.isucon.dev", powerDNSSubdomainAddress)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("DNS zone missing")
	}
	data, _ := json.Marshal(map[string]string{"Name": name + ".u.isucon.dev", "Address": powerDNSSubdomainAddress})
	return notifyDNS("/record", data)
}

var dnsHTTP = &http.Client{Timeout: 3 * time.Second}

func notifyDNS(path string, body []byte) error {
	base := os.Getenv("ISUCON_DNS_CONTROL_URL")
	if base == "" {
		return nil
	}
	req, err := http.NewRequest(http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("X-Zone-Token", "isupipe-authoritative-zone-update")
	req.Header.Set("Content-Type", "application/json")
	resp, err := dnsHTTP.Do(req)
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

func notifyIconCache(name string, image []byte, useDefault bool) error {
	base := os.Getenv("ISUCON_ICON_CACHE_URL")
	if base == "" {
		return nil
	}
	path := "/iconcache/put"
	if useDefault {
		path = "/iconcache/default"
	}
	if name == "" {
		path = "/iconcache/reload"
	}
	req, err := http.NewRequest(http.MethodPost, base+path+"?username="+url.QueryEscape(name), bytes.NewReader(image))
	if err != nil {
		return err
	}
	req.Header.Set("X-Zone-Token", "isupipe-authoritative-zone-update")
	resp, err := dnsHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 204 {
		return fmt.Errorf("icon cache update returned %d", resp.StatusCode)
	}
	return nil
}
