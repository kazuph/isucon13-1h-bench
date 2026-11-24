package main

import (
	"bytes"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const zonePath = "/var/lib/isupipe-dns/u.isucon.dev.zone"

var encodedLabel = regexp.MustCompile(`^(?:\\[0-9]{3}){1,63}$`)

type change struct {
	label string
	done  chan error
}

var changes = make(chan change, 256)

func apply(batch []change, address string, template []byte) error {
	lastReset := -1
	for i, change := range batch {
		if change.label == "" {
			lastReset = i
		}
	}
	var content bytes.Buffer
	if lastReset >= 0 {
		content.Write(template)
		content.WriteByte('\n')
	}
	for _, change := range batch[lastReset+1:] {
		fmt.Fprintf(&content, "%s 30 IN A %s\n", change.label, address)
	}
	if lastReset >= 0 {
		tmp := zonePath + ".new"
		if err := os.WriteFile(tmp, content.Bytes(), 0644); err != nil {
			return err
		}
		if err := os.Rename(tmp, zonePath); err != nil {
			return err
		}
	} else {
		file, err := os.OpenFile(zonePath, os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		_, err = file.Write(content.Bytes())
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if fastDNS==nil{return reloadZone()}
	fastDNS.reloadMu.Lock()
	fastDNS.dirty=true
	fastDNS.reloadMu.Unlock()
	if lastReset>=0 {
		if err:=fastDNS.ensureCurrent();err!=nil{return err}
		return fastDNS.load()
	}
	for _,change:=range batch{fastDNS.added(change.label,address)}
	return nil
}

func reloadZone() error {
	out, err := exec.Command("/usr/bin/pdns_control", "bind-reload-now", "u.isucon.dev").CombinedOutput()
	if err != nil {
		return fmt.Errorf("reload: %s: %w", out, err)
	}
	if strings.Contains(strings.ToLower(string(out)), "reject") {
		return errors.New(string(out))
	}

	return nil
}

func consume(address string, template []byte) {
	for first := range changes {
		batch := []change{first}
	drain:
		for len(batch) < cap(changes) {
			select {
			case next := <-changes:
				batch = append(batch, next)
			default:
				break drain
			}
		}
		err := apply(batch, address, template)
		if err != nil {
			log.Print(err)
		}
		for _, change := range batch {
			change.done <- err
		}
	}
}

func update(w http.ResponseWriter, r *http.Request) {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	key := os.Getenv("ZONE_CONTROL_TOKEN")
	if (host != "10.99.0.13" && host != "10.99.0.12") || key == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+key)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method", 405)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/icons/") {
		iconUpdate(w, r)
		return
	}
	ch := change{done: make(chan error, 1)}
	switch r.URL.Path {
	case "/reset":
	case "/add":
		ch.label = r.URL.Query().Get("label")
		if !encodedLabel.MatchString(ch.label) {
			http.Error(w, "invalid DNS label", 400)
			return
		}
	default:
		http.NotFound(w, r)
		return
	}
	select {
	case changes <- ch:
	case <-r.Context().Done():
		return
	}
	select {
	case err := <-ch.done:
		if err != nil {
			http.Error(w, "zone update failed", 500)
			return
		}
	case <-r.Context().Done():
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func main() {
	address := os.Getenv("ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS")
	if net.ParseIP(address).To4() == nil {
		log.Fatal("invalid server address")
	}
	source, err := os.ReadFile("u.isucon.dev.zone")
	if err != nil {
		log.Fatal(err)
	}
	template := bytes.ReplaceAll(source, []byte("<ISUCON_SUBDOMAIN_ADDRESS>"), []byte(address))
	template = bytes.ReplaceAll(template, []byte("0 IN A "), []byte("30 IN A "))
	if err := startDNS(); err != nil {
		log.Fatal(err)
	}
	go consume(address, template)
	control := os.Getenv("ZONE_CONTROL_LISTEN")
	if control == "" {
		control = "10.99.0.11:8053"
	}
	server := http.Server{Addr: control, Handler: http.HandlerFunc(update), ReadHeaderTimeout: 3 * time.Second, IdleTimeout: 30 * time.Second}
	log.Fatal(server.ListenAndServe())
}
