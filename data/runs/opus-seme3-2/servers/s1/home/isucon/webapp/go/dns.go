package main

import (
	"bufio"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
)

const zone = "u.isucon.dev."

var (
	zoneNames []string
	dnsIP     net.IP
	dnsTTL    uint32 = 3600
)

func loadZoneNames() {
	f, err := os.Open("../pdns/u.isucon.dev.zone")
	if err != nil {
		log.Printf("zone: %v", err)
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) >= 4 && fs[2] == "IN" && fs[3] == "A" {
			if fs[0] == "@" {
				zoneNames = append(zoneNames, "")
			} else {
				zoneNames = append(zoneNames, fs[0])
			}
		}
	}
}

// must hold mu (write)
func rebuildDNS() {
	m := make(map[string]struct{}, len(usersByName)+len(zoneNames)+1024)
	for _, n := range zoneNames {
		m[n] = struct{}{}
	}
	m[""] = struct{}{}
	m["pipe"] = struct{}{}
	m["ns1"] = struct{}{}
	for n := range usersByName {
		m[strings.ToLower(n)] = struct{}{}
	}
	dnsNames.Store(&m)
}

// must hold mu (write); copy-on-write
func addDNSName(n string) {
	old := *dnsNames.Load()
	n = strings.ToLower(n)
	if _, ok := old[n]; ok {
		return
	}
	m := make(map[string]struct{}, len(old)+1)
	for k := range old {
		m[k] = struct{}{}
	}
	m[n] = struct{}{}
	dnsNames.Store(&m)
}

var (
	dnsQ, dnsNX atomic.Int64
	dnsDropNX   = os.Getenv("DNS_DROP_NX") != ""
)

func startDNS() {
	go func() {
		for range time.Tick(10 * time.Second) {
			q, nx := dnsQ.Swap(0), dnsNX.Swap(0)
			if q > 0 {
				log.Printf("dns q=%d nx=%d", q, nx)
			}
		}
	}()
	ipS := os.Getenv("ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS")
	if ipS == "" {
		ipS = "127.0.0.1"
	}
	dnsIP = net.ParseIP(ipS).To4()
	if v := os.Getenv("DNS_TTL"); v != "" {
		if t, err := strconv.Atoi(v); err == nil {
			dnsTTL = uint32(t)
		}
	}
	srv := &dns.Server{Addr: ":53", Net: "udp", Handler: dns.HandlerFunc(handleDNS), UDPSize: 1232}
	log.Fatal(srv.ListenAndServe())
}

var soa = &dns.SOA{
	Hdr:     dns.RR_Header{Name: zone, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 3600},
	Ns:      "ns1.u.isucon.dev.",
	Mbox:    "hostmaster.u.isucon.dev.",
	Serial:  1,
	Refresh: 10800,
	Retry:   3600,
	Expire:  604800,
	Minttl:  3600,
}

func handleDNS(w dns.ResponseWriter, req *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(req)
	m.Authoritative = true
	m.RecursionAvailable = false
	if len(req.Question) != 1 {
		m.Rcode = dns.RcodeFormatError
		w.WriteMsg(m)
		return
	}
	q := req.Question[0]
	name := strings.ToLower(q.Name)
	if !strings.HasSuffix(name, zone) && name != zone {
		m.Rcode = dns.RcodeRefused
		w.WriteMsg(m)
		return
	}
	sub := ""
	if name != zone {
		sub = strings.TrimSuffix(name, "."+zone)
	}
	np := dnsNames.Load()
	if np == nil {
		m.Rcode = dns.RcodeServerFailure
		w.WriteMsg(m)
		return
	}
	names := *np
	dnsQ.Add(1)
	if _, ok := names[sub]; !ok {
		dnsNX.Add(1)
		if dnsDropNX {
			return
		}
		m.Rcode = dns.RcodeNameError
		m.Ns = []dns.RR{soa}
		w.WriteMsg(m)
		return
	}
	if q.Qtype == dns.TypeA {
		m.Answer = []dns.RR{&dns.A{
			Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: dnsTTL},
			A:   dnsIP,
		}}
	} else {
		m.Ns = []dns.RR{soa}
	}
	w.WriteMsg(m)
}
