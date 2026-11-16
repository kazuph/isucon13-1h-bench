package main

import (
	"bufio"
	"encoding/json"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/net/dns/dnsmessage"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

var mu sync.RWMutex
var names = map[string]bool{}

func putNames(a []string, replace bool) {
	mu.Lock()
	defer mu.Unlock()
	if replace {
		names = map[string]bool{"pipe.u.isucon.dev.": true, "ns1.u.isucon.dev.": true, "u.isucon.dev.": true}
	}
	if replace {
		f, e := os.Open("/home/isucon/webapp/pdns/u.isucon.dev.zone")
		if e != nil {
			log.Fatal(e)
		}
		scan := bufio.NewScanner(f)
		for scan.Scan() {
			fields := strings.Fields(scan.Text())
			if len(fields) >= 5 && fields[2] == "IN" && fields[3] == "A" {
				n := fields[0]
				if n == "@" {
					names["u.isucon.dev."] = true
				} else {
					names[strings.ToLower(n)+".u.isucon.dev."] = true
				}
			}
		}
		if e = scan.Err(); e != nil {
			log.Fatal(e)
		}
		f.Close()
	}
	for _, n := range a {
		names[strings.ToLower(n)+".u.isucon.dev."] = true
	}
}
func dnsServe() {
	var c net.PacketConn
	var e error
	for {
		c, e = net.ListenPacket("udp4", "10.99.0.11:53")
		if e == nil {
			break
		}
		log.Printf("waiting for fixed DNS address: %v", e)
		time.Sleep(time.Second)
	}
	b := make([]byte, 4096)
	for {
		n, addr, e := c.ReadFrom(b)
		if e != nil {
			continue
		}
		var msg dnsmessage.Message
		if e = msg.Unpack(b[:n]); e != nil || len(msg.Questions) == 0 {
			continue
		}
		q := msg.Questions[0]
		name := strings.ToLower(q.Name.String())
		mu.RLock()
		exists := names[name]
		mu.RUnlock()
		out := dnsmessage.Message{Header: dnsmessage.Header{ID: msg.ID, Response: true, Authoritative: true, RecursionDesired: msg.RecursionDesired}, Questions: msg.Questions}
		if !exists {
			out.Header.RCode = dnsmessage.RCodeNameError
		} else if q.Type == dnsmessage.TypeA || q.Type == dnsmessage.TypeALL {
			out.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60}, Body: &dnsmessage.AResource{A: [4]byte{10, 99, 0, 11}}}}
		}
		if q.Type == dnsmessage.TypeSOA && name == "u.isucon.dev." {
			ns, _ := dnsmessage.NewName("ns1.u.isucon.dev.")
			mb, _ := dnsmessage.NewName("hostmaster.u.isucon.dev.")
			out.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeSOA, Class: dnsmessage.ClassINET, TTL: 60}, Body: &dnsmessage.SOAResource{NS: ns, MBox: mb, Serial: 1, Refresh: 10800, Retry: 3600, Expire: 604800, MinTTL: 60}}}
		}
		wire, e := out.Pack()
		if e == nil {
			c.WriteTo(wire, addr)
		}
	}
}
func main() {
	dns := os.Getenv("WORKER_DNS") == "1"
	if dns {
		putNames(nil, true)
		go dnsServe()
		go func() {
			for {
				r, e := http.Get("http://10.99.0.12:8080/internal/names")
				if e == nil {
					var a []string
					e = json.NewDecoder(r.Body).Decode(&a)
					r.Body.Close()
					if e == nil {
						putNames(a, false)
						break
					}
				}
				time.Sleep(time.Second)
			}
		}()
	}
	http.HandleFunc("/names", func(w http.ResponseWriter, r *http.Request) {
		var a []string
		if e := json.NewDecoder(r.Body).Decode(&a); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		putNames(a, true)
	})
	http.HandleFunc("/add", func(w http.ResponseWriter, r *http.Request) {
		var a []string
		if e := json.NewDecoder(r.Body).Decode(&a); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		putNames(a, false)
	})
	http.HandleFunc("/password", func(w http.ResponseWriter, r *http.Request) {
		var v map[string]string
		if e := json.NewDecoder(r.Body).Decode(&v); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		if v["op"] == "hash" {
			b, e := bcrypt.GenerateFromPassword([]byte(v["password"]), bcrypt.MinCost)
			if e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			w.Write(b)
		} else {
			if e := bcrypt.CompareHashAndPassword([]byte(v["hash"]), []byte(v["password"])); e != nil {
				http.Error(w, e.Error(), 401)
				return
			}
			io.WriteString(w, "ok")
		}
	})
	log.Fatal(http.ListenAndServe(":8090", nil))
}
