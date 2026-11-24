package main

import (
	"bytes"
	"crypto/subtle"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"time"
)

const dnsControlAddr = "10.99.0.11:8053"

var controlClient = &http.Client{Timeout: 30 * time.Second}

func dnsControl(action, name string) error { return postControl(action, name, nil) }
func postControl(action, name string, body []byte) error {
	req, err := http.NewRequest("POST", "http://"+dnsControlAddr+"/"+action+"?name="+url.QueryEscape(name), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", os.Getenv("DNS_CONTROL_TOKEN"))
	resp, err := controlClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("DNS %s returned %d", action, resp.StatusCode)
	}
	return nil
}
func runNameserver() error {
	var err error
	powerDNSSubdomainAddress = os.Getenv(powerDNSSubdomainAddressEnvKey)
	dnsDB, err = sql.Open("mysql", "isucon:isucon@tcp(127.0.0.1:3306)/isudns")
	if err != nil {
		return err
	}
	if err = loadZone(); err != nil {
		return err
	}
	fileDatabase, err = sql.Open("mysql", "isucon:isucon@tcp(10.99.0.12:3306)/isupipe")
	if err != nil {
		return err
	}
	if err = refreshFiles(); err != nil {
		return err
	}
	if err = serveDNS(); err != nil {
		return err
	}
	secret := os.Getenv("DNS_CONTROL_TOKEN")
	if secret == "" {
		return fmt.Errorf("missing DNS control token")
	}
	server := http.Server{Addr: dnsControlAddr, ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(secret)) != 1 {
			http.Error(w, "forbidden", 403)
			return
		}
		var err error
		switch r.URL.Path {
		case "/register":
			name := r.URL.Query().Get("name")
			err = registerDNS(name)
			if err == nil {
				err = updateFile(name, nil)
			}
		case "/icon":
			image, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 10<<20))
			if e != nil {
				http.Error(w, "invalid body", 400)
				return
			}
			err = updateFile(r.URL.Query().Get("name"), image)
		case "/reset":
			if err = exec.Command("bash", "../pdns/init_zone.sh").Run(); err == nil {
				err = loadZone()
				if err == nil {
					err = refreshFiles()
				}
			}
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "DNS update failed", 500)
			return
		}
		w.WriteHeader(204)
	})}
	return server.ListenAndServe()
}
