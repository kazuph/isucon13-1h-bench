package main

import (
	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type dnsRecord struct {
	Name    string `db:"name"`
	Type    string `db:"type"`
	Content string `db:"content"`
	TTL     uint32 `db:"ttl"`
	Prio    uint16 `db:"prio"`
}

var dnsNames struct {
	sync.RWMutex
	names map[string][]dnsRecord
}

func reloadDNS() error {
	var rows []dnsRecord
	if err := dbConn.Select(&rows, "SELECT name,type,content,ttl,COALESCE(prio,0) prio FROM isudns.records WHERE disabled=0"); err != nil {
		return err
	}
	m := map[string][]dnsRecord{}
	for _, r := range rows {
		name := strings.ToLower(strings.TrimSuffix(r.Name, ".")) + "."
		m[name] = append(m[name], r)
	}
	dnsNames.Lock()
	dnsNames.names = m
	dnsNames.Unlock()
	return nil
}
func dnsAnswer(b *dnsmessage.Builder, name dnsmessage.Name, r dnsRecord) {
	h := dnsmessage.ResourceHeader{Name: name, Class: dnsmessage.ClassINET, TTL: 60}
	switch r.Type {
	case "A":
		ip := net.ParseIP(r.Content).To4()
		if ip != nil {
			b.AResource(h, dnsmessage.AResource{A: [4]byte{ip[0], ip[1], ip[2], ip[3]}})
		}
	case "AAAA":
		ip := net.ParseIP(r.Content).To16()
		if ip != nil {
			var v [16]byte
			copy(v[:], ip)
			b.AAAAResource(h, dnsmessage.AAAAResource{AAAA: v})
		}
	case "NS":
		n, e := dnsmessage.NewName(strings.TrimSuffix(r.Content, ".") + ".")
		if e == nil {
			b.NSResource(h, dnsmessage.NSResource{NS: n})
		}
	case "SOA":
		v := strings.Fields(r.Content)
		if len(v) != 7 {
			return
		}
		ns, e := dnsmessage.NewName(strings.TrimSuffix(v[0], ".") + ".")
		if e != nil {
			return
		}
		mb, e := dnsmessage.NewName(strings.TrimSuffix(v[1], ".") + ".")
		if e != nil {
			return
		}
		nums := [5]uint32{}
		for i := range nums {
			n, _ := strconv.ParseUint(v[i+2], 10, 32)
			nums[i] = uint32(n)
		}
		b.SOAResource(h, dnsmessage.SOAResource{NS: ns, MBox: mb, Serial: nums[0], Refresh: nums[1], Retry: nums[2], Expire: nums[3], MinTTL: nums[4]})
	case "TXT":
		b.TXTResource(h, dnsmessage.TXTResource{TXT: []string{r.Content}})
	}
}
func runDNS() {
	if err := reloadDNS(); err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/reload", func(w http.ResponseWriter, r *http.Request) {
		if err := reloadDNS(); err != nil {
			http.Error(w, err.Error(), 500)
		}
	})
	mux.HandleFunc("/add", func(w http.ResponseWriter, r *http.Request) {
		ip := net.ParseIP(r.URL.Query().Get("ip")).To4()
		if ip == nil {
			http.Error(w, "bad ip", 400)
			return
		}
		name := strings.ToLower(r.URL.Query().Get("name")) + ".u.isucon.dev."
		dnsNames.Lock()
		dnsNames.names[name] = []dnsRecord{{Name: name, Type: "A", Content: ip.String(), TTL: 60}}
		dnsNames.Unlock()
	})
	go func() { log.Fatal(http.ListenAndServe(":8053", mux)) }()
	raw, err := net.ListenPacket("udp4", ":53")
	if err != nil {
		log.Fatal(err)
	}
	c := ipv4.NewPacketConn(raw)
	if err := c.SetControlMessage(ipv4.FlagDst|ipv4.FlagInterface, true); err != nil {
		log.Fatal(err)
	}
	buf := make([]byte, 4096)
	for {
		n, cm, addr, err := c.ReadFrom(buf)
		if err != nil {
			continue
		}
		var p dnsmessage.Parser
		h, err := p.Start(buf[:n])
		if err != nil {
			continue
		}
		q, err := p.Question()
		if err != nil {
			continue
		}
		if h.Response {
			continue
		}
		name := strings.ToLower(q.Name.String())
		dnsNames.RLock()
		rrs, ok := dnsNames.names[name]
		dnsNames.RUnlock()
		rh := dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true, RecursionDesired: h.RecursionDesired}
		if !ok {
			rh.RCode = dnsmessage.RCodeNameError
		}
		b := dnsmessage.NewBuilder(make([]byte, 0, 512), rh)
		b.EnableCompression()
		b.StartQuestions()
		b.Question(q)
		if ok {
			b.StartAnswers()
			for _, r := range rrs {
				if q.Type == dnsmessage.TypeALL || q.Type.String() == "Type"+r.Type {
					dnsAnswer(&b, q.Name, r)
				}
			}
		}
		out, e := b.Finish()
		if e == nil {
			if _, err := c.WriteTo(out, &ipv4.ControlMessage{Src: cm.Dst, IfIndex: cm.IfIndex}, addr); err != nil {
				log.Printf("DNS reply to %s: %v", addr, err)
			}
		}
	}
}

var dnsHTTP = &http.Client{Timeout: 5 * time.Second}

func notifyDNS(path string) error {
	host := os.Getenv("DNS_CONTROL")
	if host == "" {
		return nil
	}
	r, e := dnsHTTP.Get(host + path)
	if e != nil {
		return e
	}
	io.Copy(io.Discard, r.Body)
	r.Body.Close()
	if r.StatusCode != 200 {
		return &dnsNotifyError{}
	}
	return nil
}

type dnsNotifyError struct{}

func (*dnsNotifyError) Error() string { return "DNS update failed" }
