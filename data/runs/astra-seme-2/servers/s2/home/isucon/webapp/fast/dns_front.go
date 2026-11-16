package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"golang.org/x/net/dns/dnsmessage"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

var dnsHTTP = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 64}}

func loadZone() error {
	zone, err := os.ReadFile("../pdns/u.isucon.dev.zone")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(zone), "\n") {
		f := strings.Fields(line)
		if len(f) >= 5 && f[2] == "IN" && f[3] == "A" {
			name := f[0] + ".u.isucon.dev."
			if f[0] == "@" {
				name = "u.isucon.dev."
			}
			dnsNames[name] = true
		}
	}
	return nil
}
func publishDNS(name string) error {
	var data []byte
	path := "/add"
	if name == "" {
		path = "/replace"
		data, _ = json.Marshal(dnsUsers)
	} else {
		data = []byte(strings.ToLower(name))
	}
	resp, err := dnsHTTP.Post("http://10.99.0.11:8091"+path, "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("DNS update: %d", resp.StatusCode)
	}
	return nil
}
func dnsFront() {
	if err := loadZone(); err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/replace", func(w http.ResponseWriter, r *http.Request) {
		var names map[string]bool
		if err := json.NewDecoder(r.Body).Decode(&names); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		mu.Lock()
		dnsUsers = names
		mu.Unlock()
		w.WriteHeader(200)
	})
	mux.HandleFunc("/add", func(w http.ResponseWriter, r *http.Request) {
		b, e := io.ReadAll(r.Body)
		if e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		mu.Lock()
		dnsUsers[strings.ToLower(string(b))] = true
		mu.Unlock()
		w.WriteHeader(200)
	})
	go func() { log.Fatal(http.ListenAndServe(":8091", mux)) }()
	// Fetch the authoritative name set whenever this service restarts.
	for {
		resp, e := dnsHTTP.Get("http://10.99.0.12:8080/internal/dns-names")
		if e == nil {
			var names map[string]bool
			e = json.NewDecoder(resp.Body).Decode(&names)
			resp.Body.Close()
			if e == nil {
				mu.Lock()
				dnsUsers = names
				mu.Unlock()
				break
			}
		}
		time.Sleep(time.Second)
	}
	addr, e := net.ResolveUDPAddr("udp", ":53")
	if e != nil {
		log.Fatal(e)
	}
	conn, e := net.ListenUDP("udp", addr)
	if e != nil {
		log.Fatal(e)
	}
	conn.SetReadBuffer(4 << 20)
	buf := make([]byte, 4096)
	for {
		n, client, e := conn.ReadFromUDP(buf)
		if e != nil {
			continue
		}
		var p dnsmessage.Parser
		h, e := p.Start(buf[:n])
		if e != nil {
			continue
		}
		q, e := p.Question()
		if e != nil {
			continue
		}
		name := strings.ToLower(q.Name.String())
		known := dnsNames[name]
		if strings.HasSuffix(name, ".u.isucon.dev.") {
			sub := strings.TrimSuffix(name, ".u.isucon.dev.")
			mu.RLock()
			known = known || dnsUsers[sub]
			mu.RUnlock()
		}
		rh := dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true, RecursionDesired: h.RecursionDesired}
		if !known {
			rh.RCode = dnsmessage.RCodeNameError
		}
		b := dnsmessage.NewBuilder(nil, rh)
		b.EnableCompression()
		b.StartQuestions()
		b.Question(q)
		if known && q.Type == dnsmessage.TypeA {
			b.StartAnswers()
			b.AResource(dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.AResource{A: [4]byte{10, 99, 0, 11}})
		}
		out, e := b.Finish()
		if e == nil {
			conn.WriteToUDP(out, client)
		}
	}
}
