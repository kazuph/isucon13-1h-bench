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

var (
	dnsAddr    = "10.99.0.11"
	zoneNames  = map[string]bool{}
	zoneMu     sync.RWMutex
	dnsDropNX  = os.Getenv("DNS_DROP_NX") == "1"
	dnsTTL     = uint32(3600)
	userDnsIPs []net.IP
	apexDnsIP  net.IP
)

const zone = "u.isucon.dev."

func loadZoneNames() {
	f, err := os.Open("../pdns/u.isucon.dev.zone")
	if err != nil {
		log.Printf("zone file: %v", err)
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) >= 4 && fs[2] == "IN" && fs[3] == "A" {
			n := fs[0]
			if n == "@" {
				continue
			}
			zoneNames[strings.ToLower(n)] = true
		}
	}
}

func nameExists(label string) bool {
	if zoneNames[label] {
		return true
	}
	mu.RLock()
	ok := lowerNames[label]
	mu.RUnlock()
	return ok
}

func dnsHandler(w dns.ResponseWriter, r *dns.Msg) {
	if len(r.Question) != 1 {
		return
	}
	q := r.Question[0]
	name := strings.ToLower(q.Name)
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true
	var ip net.IP
	if name == zone {
		ip = apexDnsIP
	} else if strings.HasSuffix(name, "."+zone) {
		label := name[:len(name)-len(zone)-1]
		if !strings.Contains(label, ".") && nameExists(label) {
			if label == "pipe" {
				ip = apexDnsIP
			} else {
				h := uint32(2166136261)
				for i := 0; i < len(label); i++ {
					h = (h ^ uint32(label[i])) * 16777619
				}
				ip = userDnsIPs[h%uint32(len(userDnsIPs))]
			}
		}
	}
	if ip == nil {
		if dnsDropNX {
			return
		}
		m.Rcode = dns.RcodeNameError
		m.Ns = []dns.RR{soaRR}
		w.WriteMsg(m)
		return
	}
	if q.Qtype == dns.TypeA {
		m.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: dnsTTL}, A: ip}}
	}
	w.WriteMsg(m)
}

var soaRR dns.RR

func startDNS() {
	loadZoneNames()
	for _, s := range strings.Split(envOr("DNS_USER_IPS", dnsAddr), ",") {
		userDnsIPs = append(userDnsIPs, net.ParseIP(strings.TrimSpace(s)).To4())
	}
	apexDnsIP = net.ParseIP(envOr("DNS_APEX_IP", dnsAddr)).To4()
	soaRR, _ = dns.NewRR("u.isucon.dev. 0 IN SOA ns1.u.isucon.dev. hostmaster.u.isucon.dev. 0 10800 3600 604800 0")
	dns.HandleFunc(".", dnsHandler)
	srv := &dns.Server{Addr: ":53", Net: "udp", UDPSize: 65535}
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("dns server: %v", err)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
