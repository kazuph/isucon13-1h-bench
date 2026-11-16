package main

import (
	"database/sql"
	"encoding/json"
	_ "github.com/go-sql-driver/mysql"
	"golang.org/x/net/dns/dnsmessage"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type record struct {
	kind    dnsmessage.Type
	content string
	ttl     uint32
}

var mu sync.RWMutex
var records = make(map[string][]record)
var db *sql.DB

func reload() error {
	rows, err := db.Query("SELECT name,type,content,ttl FROM records WHERE disabled=0")
	if err != nil {
		return err
	}
	defer rows.Close()
	next := make(map[string][]record)
	for rows.Next() {
		var name, kind, content string
		var ttl uint32
		if err := rows.Scan(&name, &kind, &content, &ttl); err != nil {
			return err
		}
		var typ dnsmessage.Type
		switch kind {
		case "A":
			typ = dnsmessage.TypeA
		case "NS":
			typ = dnsmessage.TypeNS
		case "SOA":
			typ = dnsmessage.TypeSOA
		default:
			continue
		}
		name = strings.ToLower(strings.TrimSuffix(name, ".")) + "."
		next[name] = append(next[name], record{typ, content, ttl})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	mu.Lock()
	records = next
	mu.Unlock()
	return nil
}
func reply(packet []byte) ([]byte, error) {
	var p dnsmessage.Parser
	h, err := p.Start(packet)
	if err != nil {
		return nil, err
	}
	q, err := p.Question()
	if err != nil {
		return nil, err
	}
	name := strings.ToLower(q.Name.String())
	mu.RLock()
	rrs, exists := records[name]
	soa := records["u.isucon.dev."]
	mu.RUnlock()
	hdr := dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true, RecursionDesired: h.RecursionDesired}
	if !exists {
		hdr.RCode = dnsmessage.RCodeNameError
	}
	b := dnsmessage.NewBuilder(make([]byte, 0, 512), hdr)
	b.EnableCompression()
	b.StartQuestions()
	b.Question(q)
	b.StartAnswers()
	answered := false
	for _, r := range rrs {
		if r.kind != q.Type {
			continue
		}
		rh := dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: r.ttl}
		switch r.kind {
		case dnsmessage.TypeA:
			ip := net.ParseIP(routeAddress(name, r.content)).To4()
			if ip == nil {
				continue
			}
			var a [4]byte
			copy(a[:], ip)
			b.AResource(rh, dnsmessage.AResource{A: a})
			answered = true
		case dnsmessage.TypeNS:
			n, err := dnsmessage.NewName(strings.TrimSuffix(r.content, ".") + ".")
			if err == nil {
				b.NSResource(rh, dnsmessage.NSResource{NS: n})
				answered = true
			}
		case dnsmessage.TypeSOA:
			writeSOA(&b, rh, r.content)
			answered = true
		}
	}
	if !answered {
		b.StartAuthorities()
		n, _ := dnsmessage.NewName("u.isucon.dev.")
		for _, r := range soa {
			if r.kind == dnsmessage.TypeSOA {
				writeSOA(&b, dnsmessage.ResourceHeader{Name: n, Class: dnsmessage.ClassINET, TTL: 0}, r.content)
			}
		}
	}
	return b.Finish()
}
func writeSOA(b *dnsmessage.Builder, h dnsmessage.ResourceHeader, content string) {
	f := strings.Fields(content)
	if len(f) != 7 {
		return
	}
	ns, err := dnsmessage.NewName(strings.TrimSuffix(f[0], ".") + ".")
	if err != nil {
		return
	}
	mb, err := dnsmessage.NewName(strings.TrimSuffix(f[1], ".") + ".")
	if err != nil {
		return
	}
	v := [5]uint32{}
	for i := range v {
		n, _ := strconv.ParseUint(f[i+2], 10, 32)
		v[i] = uint32(n)
	}
	b.SOAResource(h, dnsmessage.SOAResource{NS: ns, MBox: mb, Serial: v[0], Refresh: v[1], Retry: v[2], Expire: v[3], MinTTL: 0})
}
func main() {
	var err error
	db, err = sql.Open("mysql", "isudns:isudns@tcp(10.99.0.12:3306)/isudns?interpolateParams=true")
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(2)
	for {
		if err = reload(); err == nil {
			break
		}
		log.Print(err)
		time.Sleep(time.Second)
	}
	http.HandleFunc("/reload", func(w http.ResponseWriter, r *http.Request) {
		if err := reload(); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(204)
	})
	http.HandleFunc("/record", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Name string
			IP   string
		}
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil || net.ParseIP(v.IP) == nil {
			http.Error(w, "invalid record", 400)
			return
		}
		name := strings.ToLower(strings.TrimSuffix(v.Name, ".")) + "."
		mu.Lock()
		records[name] = []record{{dnsmessage.TypeA, v.IP, 60}}
		mu.Unlock()
		w.WriteHeader(204)
	})
	go func() { log.Fatal(http.ListenAndServe("10.99.0.11:9080", nil)) }()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("10.99.0.11"), Port: 53})
	if err != nil {
		log.Fatal(err)
	}
	c.SetReadBuffer(4 << 20)
	c.SetWriteBuffer(4 << 20)
	for i := 0; i < 4; i++ {
		go func() {
			buf := make([]byte, 4096)
			for {
				n, addr, err := c.ReadFromUDP(buf)
				if err != nil {
					log.Print(err)
					continue
				}
				resp, err := reply(buf[:n])
				if err == nil {
					c.WriteToUDP(resp, addr)
				}
			}
		}()
	}
	select {}
}

var shardDNS = os.Getenv("ISUPIPE_DNS_SHARD") == "1"

func routeAddress(name, original string) string {
	if !shardDNS {
		return original
	}
	if name == "pipe.u.isucon.dev." || name == "ns1.u.isucon.dev." || name == "u.isucon.dev." {
		return original
	}
	var h uint32 = 2166136261
	for i := 0; i < len(name); i++ {
		h = (h ^ uint32(name[i])) * 16777619
	}
	if h%2 == 1 {
		return "10.99.0.13"
	}
	return "10.99.0.11"
}
