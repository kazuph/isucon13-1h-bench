package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"golang.org/x/net/dns/dnsmessage"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

func serveDNS() {
	conn, e := net.ListenPacket("udp", ":53")
	if e != nil {
		panic(e)
	}
	for i := 0; i < 4; i++ {
		go func() {
			b := make([]byte, 4096)
			for {
				n, addr, e := conn.ReadFrom(b)
				if e != nil {
					return
				}
				var p dnsmessage.Parser
				h, e := p.Start(b[:n])
				if e != nil {
					continue
				}
				q, e := p.Question()
				if e != nil {
					continue
				}
				name := strings.ToLower(q.Name.String())
				dnsMu.RLock()
				ok := dnsNames[name]
				dnsMu.RUnlock()
				h.Response = true
				h.Authoritative = true
				h.RecursionAvailable = false
				h.RCode = dnsmessage.RCodeSuccess
				if !ok {
					h.RCode = dnsmessage.RCodeNameError
				}
				builder := dnsmessage.NewBuilder(nil, h)
				builder.EnableCompression()
				builder.StartQuestions()
				builder.Question(q)
				builder.StartAnswers()
				if ok && q.Type == dnsmessage.TypeA {
					builder.AResource(dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.AResource{A: [4]byte{10, 99, 0, 11}})
				}
				out, e := builder.Finish()
				if e == nil {
					conn.WriteTo(out, addr)
				}
			}
		}()
	}
}

var dnsMu sync.RWMutex
var dnsWriteMu sync.Mutex
var dnsGeneration uint64
var dnsNames = map[string]bool{}

func pushDNS(names map[string]bool, replace bool) error {
	b, _ := json.Marshal(names)
	url := "http://10.99.0.11:8082/dns"
	if replace {
		url += "?replace=1"
	}
	resp, e := authClient.Post(url, "application/json", bytes.NewReader(b))
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("DNS update: %d", resp.StatusCode)
	}
	return nil
}
func dnsMain() {
	if b, e := os.ReadFile("/home/isucon/dns.json"); e == nil {
		json.Unmarshal(b, &dnsNames)
	}
	serveDNS()
	http.HandleFunc("/dns", func(w http.ResponseWriter, r *http.Request) {
		dnsWriteMu.Lock()
		defer dnsWriteMu.Unlock()
		var names map[string]bool
		if json.NewDecoder(r.Body).Decode(&names) != nil {
			http.Error(w, "bad data", 400)
			return
		}
		dnsMu.Lock()
		dnsGeneration++
		if r.URL.Query().Get("replace") == "1" {
			dnsNames = names
		} else {
			for n, v := range names {
				dnsNames[n] = v
			}
		}
		b, _ := json.Marshal(dnsNames)
		dnsMu.Unlock()
		os.WriteFile("/home/isucon/dns.json.tmp", b, 0644)
		os.Rename("/home/isucon/dns.json.tmp", "/home/isucon/dns.json")
		w.WriteHeader(200)
	})
	go func() {
		for {
			dnsMu.RLock()
			generation := dnsGeneration
			dnsMu.RUnlock()
			resp, e := authClient.Get("http://10.99.0.12:8080/internal/dns")
			if e == nil {
				var names map[string]bool
				if json.NewDecoder(resp.Body).Decode(&names) == nil {
					dnsMu.Lock()
					if dnsGeneration == generation {
						dnsNames = names
					}
					dnsMu.Unlock()
				}
				resp.Body.Close()
			}
			time.Sleep(time.Second)
		}
	}()
	if e := http.ListenAndServe(":8082", nil); e != nil {
		panic(e)
	}
}
