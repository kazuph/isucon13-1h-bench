package main

import (
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/miekg/dns"
)

const zone = "u.isucon.dev."

var (
	dnsNames sync.Map // name(lowercase label) -> struct{}
	dnsIP    net.IP
	dnsTTL   uint32 = 0
	dnsDrop  bool
	userIPs  []net.IP
)

func dnsReset() {
	dnsNames.Range(func(k, v any) bool { dnsNames.Delete(k); return true })
	dnsNames.Store("ns1", 0)
	dnsNames.Store("pipe", 0)
	if zoneLabels == nil {
		b, err := os.ReadFile("/home/isucon/webapp/pdns/u.isucon.dev.zone")
		if err != nil {
			log.Printf("zone read: %v", err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) >= 4 && f[2] == "IN" && f[3] == "A" && f[0] != "@" {
				zoneLabels = append(zoneLabels, strings.ToLower(f[0]))
			}
		}
	}
	for _, l := range zoneLabels {
		if _, ok := dnsNames.Load(l); !ok {
			dnsNames.Store(l, 1)
		}
	}
}

var zoneLabels []string

func dnsAdd(name string) {
	dnsNames.Store(strings.ToLower(name), 1)
}

func soaRR() dns.RR {
	return &dns.SOA{
		Hdr:     dns.RR_Header{Name: zone, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 3600},
		Ns:      "ns1." + zone,
		Mbox:    "hostmaster." + zone,
		Serial:  0,
		Refresh: 10800,
		Retry:   3600,
		Expire:  604800,
		Minttl:  3600,
	}
}

func ipFor(label string, kind int) net.IP {
	if len(userIPs) > 0 {
		h := uint32(2166136261)
		for i := 0; i < len(label); i++ {
			h ^= uint32(label[i])
			h *= 16777619
		}
		return userIPs[h%uint32(len(userIPs))]
	}
	return dnsIP
}

func handleDNS(w dns.ResponseWriter, r *dns.Msg) {
	if len(r.Question) != 1 {
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeFormatError)
		w.WriteMsg(m)
		return
	}
	q := r.Question[0]
	qn := strings.ToLower(q.Name)
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true
	if qn == zone {
		switch q.Qtype {
		case dns.TypeA:
			m.Answer = append(m.Answer, &dns.A{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: dnsTTL}, A: dnsIP})
		case dns.TypeSOA:
			m.Answer = append(m.Answer, soaRR())
		case dns.TypeNS:
			m.Answer = append(m.Answer, &dns.NS{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeNS, Class: dns.ClassINET, Ttl: 0}, Ns: "ns1." + zone})
		default:
			m.Ns = append(m.Ns, soaRR())
		}
		w.WriteMsg(m)
		return
	}
	if !strings.HasSuffix(qn, "."+zone) {
		m.SetRcode(r, dns.RcodeRefused)
		m.Authoritative = false
		w.WriteMsg(m)
		return
	}
	label := qn[:len(qn)-len(zone)-1]
	v, ok := dnsNames.Load(label)
	if !ok {
		if dnsDrop {
			return
		}
		m.SetRcode(r, dns.RcodeNameError)
		m.Ns = append(m.Ns, soaRR())
		w.WriteMsg(m)
		return
	}
	if q.Qtype == dns.TypeA {
		m.Answer = append(m.Answer, &dns.A{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: dnsTTL}, A: ipFor(label, v.(int))})
	} else {
		m.Ns = append(m.Ns, soaRR())
	}
	w.WriteMsg(m)
}

func startDNS() {
	ip := os.Getenv("ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS")
	if ip == "" {
		ip = "127.0.0.1"
	}
	dnsIP = net.ParseIP(ip).To4()
	if v := os.Getenv("DNS_TTL"); v != "" {
		n, _ := strconv.Atoi(v)
		dnsTTL = uint32(n)
	}
	dnsDrop = os.Getenv("DNS_DROP") == "1"
	if v := os.Getenv("DNS_USER_IPS"); v != "" {
		for _, s := range strings.Split(v, ",") {
			userIPs = append(userIPs, net.ParseIP(strings.TrimSpace(s)).To4())
		}
	}
	addr := os.Getenv("DNS_ADDR")
	if addr == "" {
		addr = ":53"
	}
	dns.HandleFunc(".", handleDNS)
	for i := 0; i < 4; i++ {
		go func() {
			srv := &dns.Server{Addr: addr, Net: "udp", ReusePort: true}
			if err := srv.ListenAndServe(); err != nil {
				log.Printf("dns server error: %v", err)
			}
		}()
	}
}
