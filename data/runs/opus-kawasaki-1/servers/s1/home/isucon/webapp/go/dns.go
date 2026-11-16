package main

import (
	"context"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

const dnsZone = "u.isucon.dev."

var (
	dnsMu      sync.RWMutex
	dnsRecords = map[string]net.IP{}
	dnsTTL     uint32 = 3600
	dnsSOA     dns.RR
)

func dnsEnabled() bool {
	return os.Getenv("ISUCON13_EMBED_DNS") == "1"
}

func loadDNSRecords(ctx context.Context) error {
	type rec struct {
		Name    string `db:"name"`
		Content string `db:"content"`
	}
	var rows []rec
	if err := dnsConn.SelectContext(ctx, &rows, "SELECT name, content FROM records WHERE type = 'A'"); err != nil {
		return err
	}
	m := make(map[string]net.IP, len(rows)+1024)
	for _, r := range rows {
		m[strings.ToLower(r.Name)+"."] = net.ParseIP(r.Content).To4()
	}
	dnsMu.Lock()
	dnsRecords = m
	dnsMu.Unlock()
	return nil
}

func addDNSName(name string) {
	ip := net.ParseIP(powerDNSSubdomainAddress).To4()
	dnsMu.Lock()
	dnsRecords[strings.ToLower(name)+"."+dnsZone] = ip
	dnsMu.Unlock()
}

var (
	nxMode  = os.Getenv("ISUCON13_NX_MODE")
	nxDelay time.Duration
)

func startDNSServer() {
	if v := os.Getenv("ISUCON13_NX_DELAY_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			nxDelay = time.Duration(n) * time.Millisecond
		}
	}
	if v := os.Getenv("ISUCON13_DNS_TTL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			dnsTTL = uint32(n)
		}
	}
	soa, err := dns.NewRR("u.isucon.dev. 3600 IN SOA ns1.u.isucon.dev. hostmaster.u.isucon.dev. 0 10800 3600 604800 3600")
	if err != nil {
		panic(err)
	}
	dnsSOA = soa
	if err := loadDNSRecords(context.Background()); err != nil {
		log.Printf("failed to load dns records: %v", err)
	}
	h := dns.HandlerFunc(handleDNS)
	for i := 0; i < 1; i++ {
		srv := &dns.Server{Addr: ":53", Net: "udp", Handler: h, ReusePort: true}
		go func() {
			if err := srv.ListenAndServe(); err != nil {
				log.Printf("dns server error: %v", err)
			}
		}()
	}
}

func handleDNS(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true
	m.Compress = true
	if len(r.Question) != 1 {
		m.Rcode = dns.RcodeFormatError
		w.WriteMsg(m)
		return
	}
	q := r.Question[0]
	name := strings.ToLower(q.Name)
	if !strings.HasSuffix(name, dnsZone) && name != dnsZone {
		m.Rcode = dns.RcodeRefused
		m.Authoritative = false
		w.WriteMsg(m)
		return
	}
	dnsMu.RLock()
	ip, ok := dnsRecords[name]
	dnsMu.RUnlock()
	switch {
	case ok && (q.Qtype == dns.TypeA || q.Qtype == dns.TypeANY):
		m.Answer = append(m.Answer, &dns.A{
			Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: dnsTTL},
			A:   ip,
		})
	case name == dnsZone && q.Qtype == dns.TypeSOA:
		m.Answer = append(m.Answer, dnsSOA)
	case name == dnsZone && q.Qtype == dns.TypeNS:
		m.Answer = append(m.Answer, &dns.NS{
			Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeNS, Class: dns.ClassINET, Ttl: 3600},
			Ns:  "ns1.u.isucon.dev.",
		})
	case ok:
		m.Ns = append(m.Ns, dnsSOA)
	default:
		m.Rcode = dns.RcodeNameError
		m.Ns = append(m.Ns, dnsSOA)
		switch {
		case nxMode == "drop":
			return
		case nxDelay > 0:
			time.AfterFunc(nxDelay, func() { w.WriteMsg(m) })
			return
		}
	}
	w.WriteMsg(m)
}
