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
	"sync"
)

type DNSUpdate struct {
	Names []string
	Reset bool
}

func dnsUpdate(names []string, reset bool) error {
	b, _ := json.Marshal(DNSUpdate{names, reset})
	r, e := internalClient.Post("http://10.99.0.11:8091/names", "application/json", bytes.NewReader(b))
	if e != nil {
		return e
	}
	defer r.Body.Close()
	io.Copy(io.Discard, r.Body)
	if r.StatusCode != 200 {
		return fmt.Errorf("DNS update failed: %d", r.StatusCode)
	}
	return nil
}
func runDNS() {
	var lock sync.RWMutex
	names := map[string]bool{}
	makeNames := func(ns []string) map[string]bool {
		m := map[string]bool{"u.isucon.dev.": true, "ns1.u.isucon.dev.": true, "pipe.u.isucon.dev.": true}
		zone, err := os.ReadFile("../pdns/u.isucon.dev.zone")
		if err != nil {
			log.Fatal(err)
		}
		for _, line := range strings.Split(string(zone), "\n") {
			f := strings.Fields(line)
			if len(f) >= 5 && f[2] == "IN" && f[3] == "A" && f[0] != "@" {
				m[strings.ToLower(f[0])+".u.isucon.dev."] = true
			}
		}
		for _, n := range ns {
			m[strings.ToLower(n)+".u.isucon.dev."] = true
		}
		return m
	}
	b, err := os.ReadFile("../data/dns.json")
	if err == nil {
		if err = json.Unmarshal(b, &names); err != nil {
			log.Fatal(err)
		}
	} else if !os.IsNotExist(err) {
		log.Fatal(err)
	} else {
		names = makeNames(nil)
	}
	var updateLock sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("/names", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method", 405)
			return
		}
		var q DNSUpdate
		if json.NewDecoder(r.Body).Decode(&q) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		updateLock.Lock()
		defer updateLock.Unlock()
		m := makeNames(q.Names)
		if !q.Reset {
			lock.RLock()
			for n := range names {
				m[n] = true
			}
			lock.RUnlock()
		}
		if e := os.MkdirAll("../data", 0755); e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		b, e := json.Marshal(m)
		if e == nil {
			e = os.WriteFile("../data/dns.json.tmp", b, 0644)
		}
		if e == nil {
			var f *os.File
			f, e = os.Open("../data/dns.json.tmp")
			if e == nil {
				e = f.Sync()
				f.Close()
			}
		}
		if e == nil {
			e = os.Rename("../data/dns.json.tmp", "../data/dns.json")
		}
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		directory, e := os.Open("../data")
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		e = directory.Sync()
		directory.Close()
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		lock.Lock()
		names = m
		lock.Unlock()
		w.WriteHeader(200)
	})
	go func() { log.Fatal(http.ListenAndServe(":8091", mux)) }()
	addr := &net.UDPAddr{Port: 53}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		log.Fatal(err)
	}
	conn.SetReadBuffer(4 << 20)
	conn.SetWriteBuffer(4 << 20)
	for i := 0; i < 4; i++ {
		go func() {
			buf := make([]byte, 4096)
			for {
				n, peer, e := conn.ReadFromUDP(buf)
				if e != nil {
					continue
				}
				var parser dnsmessage.Parser
				h, e := parser.Start(buf[:n])
				if e != nil || h.Response {
					continue
				}
				q, e := parser.Question()
				if e != nil {
					continue
				}
				lock.RLock()
				exists := names[strings.ToLower(q.Name.String())]
				lock.RUnlock()
				out := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true, RecursionDesired: h.RecursionDesired, RCode: func() dnsmessage.RCode {
					if exists {
						return dnsmessage.RCodeSuccess
					}
					return dnsmessage.RCodeNameError
				}()})
				out.EnableCompression()
				out.StartQuestions()
				out.Question(q)
				if exists && q.Type == dnsmessage.TypeA {
					out.StartAnswers()
					out.AResource(dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.AResource{A: [4]byte{10, 99, 0, 11}})
				}
				b, e := out.Finish()
				if e == nil {
					conn.WriteToUDP(b, peer)
				}
			}
		}()
	}
	select {}
}
