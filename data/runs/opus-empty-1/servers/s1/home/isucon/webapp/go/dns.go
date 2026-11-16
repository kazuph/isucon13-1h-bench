package main

import (
	"bufio"
	"log"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/miekg/dns"
)

const dnsZone = "u.isucon.dev."

var (
	dnsMu    sync.RWMutex
	dnsNames = map[string]struct{}{}
	dnsIP    net.IP
)

func dnsAddName(name string) {
	dnsMu.Lock()
	dnsNames[strings.ToLower(name)] = struct{}{}
	dnsMu.Unlock()
}

func dnsLoadZoneFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		log.Printf("dns: zone file open failed: %v", err)
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	dnsMu.Lock()
	defer dnsMu.Unlock()
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) >= 4 && fs[len(fs)-3] == "IN" && fs[len(fs)-2] == "A" {
			n := fs[0]
			if n == "@" {
				n = ""
			}
			dnsNames[strings.ToLower(n)] = struct{}{}
		}
	}
}

func dnsLoadUsers() {
	cacheMu.RLock()
	names := make([]string, 0, len(usersByName))
	for n := range usersByName {
		names = append(names, n)
	}
	cacheMu.RUnlock()
	dnsMu.Lock()
	for _, n := range names {
		dnsNames[strings.ToLower(n)] = struct{}{}
	}
	dnsMu.Unlock()
}

func dnsHandle(w dns.ResponseWriter, r *dns.Msg) {
	if len(r.Question) != 1 {
		return
	}
	q := r.Question[0]
	qname := strings.ToLower(q.Name)
	var sub string
	if qname == dnsZone {
		sub = ""
	} else if strings.HasSuffix(qname, "."+dnsZone) {
		sub = strings.TrimSuffix(qname, "."+dnsZone)
	} else {
		return
	}
	dnsMu.RLock()
	_, ok := dnsNames[sub]
	dnsMu.RUnlock()
	if !ok {
		// unknown name (water torture): don't answer
		return
	}
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true
	if q.Qtype == dns.TypeA {
		m.Answer = append(m.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 3600},
			A:   dnsIP,
		})
	}
	w.WriteMsg(m)
}

func startDNS(addr string) {
	dnsIP = net.ParseIP(addr).To4()
	dnsLoadZoneFile("../pdns/u.isucon.dev.zone")
	dnsLoadUsers()
	dns.HandleFunc(".", dnsHandle)
	go func() {
		srv := &dns.Server{Addr: ":53", Net: "udp"}
		if err := srv.ListenAndServe(); err != nil {
			log.Printf("dns server failed: %v", err)
		}
	}()
}
