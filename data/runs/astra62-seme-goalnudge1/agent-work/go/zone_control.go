package main

import (
	"bytes"
	"crypto/subtle"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

var zoneClient = &http.Client{Timeout: 12 * time.Second, Transport: clusterTransport()}

func zoneRequest(action, value string) error {
	req, err := http.NewRequest("POST", os.Getenv("ISUPIPE_ZONE_URL")+"/"+action, strings.NewReader(value))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("ISUPIPE_ZONE_TOKEN"))
	resp, err := zoneClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 204 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("zone %s: %d %s", action, resp.StatusCode, b)
	}
	return nil
}
func publishEdgeIcon(name string, img []byte) error {
	req, err := http.NewRequest("POST", os.Getenv("ISUPIPE_ZONE_URL")+"/icon?name="+url.QueryEscape(name), bytes.NewReader(img))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("ISUPIPE_ZONE_TOKEN"))
	resp, err := zoneClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 204 {
		return fmt.Errorf("publish icon: %d", resp.StatusCode)
	}
	return nil
}
func serveZoneControl() error {
	if err := startEdgeIcons(); err != nil {
		return err
	}
	return zoneControlListener()
}
func zoneControlListener() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+os.Getenv("ISUPIPE_ZONE_TOKEN"))) != 1 {
			http.Error(w, "forbidden", 403)
			return
		}
		var err error
		switch r.URL.Path {
		case "/register":
			var name []byte
			name, err = io.ReadAll(http.MaxBytesReader(w, r.Body, 255))
			if err == nil {
				err = addAddress(string(name))
				if err == nil {
					err = edge.publish(string(name), nil)
				}
			}
		case "/icon-ref":
			err = edge.publishHash(r.URL.Query().Get("name"), r.URL.Query().Get("hash"))
			if os.IsNotExist(err) {
				http.NotFound(w, r)
				return
			}
		case "/icon":
			var img []byte
			img, err = io.ReadAll(http.MaxBytesReader(w, r.Body, 10<<20))
			if err == nil {
				err = edge.publish(r.URL.Query().Get("name"), img)
			}
		case "/reset":
			err = exec.Command("../pdns/init_zone.sh").Run()
			if err == nil {
				err = loadZone()
				if err == nil {
					err = edge.reset()
				}
			}
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(204)
	})
	server := http.Server{Addr: os.Getenv("ISUPIPE_ZONE_LISTEN"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return server.ListenAndServe()
}

func publishKnownIcon(name, hash string, img []byte) error {
	if zone.db != nil {
		if err := edge.publishHash(name, hash); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		return edge.publish(name, img)
	}

	req, err := http.NewRequest("POST", os.Getenv("ISUPIPE_ZONE_URL")+"/icon-ref?name="+url.QueryEscape(name)+"&hash="+hash, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("ISUPIPE_ZONE_TOKEN"))
	resp, err := zoneClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode == 204 {
		return nil
	}
	if resp.StatusCode == 404 {
		return publishEdgeIcon(name, img)
	}
	return fmt.Errorf("icon reference: %d", resp.StatusCode)
}

func publishDomain(name string) error {
	if zone.db != nil {
		if err := addAddress(name); err != nil {
			return err
		}
		return edge.publish(name, nil)
	}
	if os.Getenv("ISUPIPE_ZONE_URL") != "" {
		return zoneRequest("register", name)
	}
	return exec.Command("pdnsutil", "add-record", "u.isucon.dev", name, "A", "60", powerDNSSubdomainAddress).Run()
}
