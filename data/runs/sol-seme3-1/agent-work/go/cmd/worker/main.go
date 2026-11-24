package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"golang.org/x/crypto/bcrypt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var mu sync.RWMutex
var targetCounter atomic.Uint64
var targets = []byte{11}
var ttl byte = 60
var names = map[string]bool{"pipe.u.isucon.dev.": true}
var baseNames = map[string]bool{}
var namesRevision uint64

func replace(v []string) { replaceAt(v, nil) }
func replaceAt(v []string, revision *uint64) {
	n := make(map[string]bool, len(v)+len(baseNames))
	for s := range baseNames {
		n[s] = true
	}
	for _, s := range v {
		n[strings.ToLower(s)+".u.isucon.dev."] = true
	}
	mu.Lock()
	if revision != nil && *revision != namesRevision {
		mu.Unlock()
		return
	}
	namesRevision++
	names = n
	mu.Unlock()
}
func dns() {
	c, e := net.ListenPacket("udp", ":53")
	if e != nil {
		log.Fatal(e)
	}
	for {
		b := make([]byte, 1500)
		n, addr, e := c.ReadFrom(b)
		if e != nil {
			continue
		}
		if n < 17 {
			continue
		}
		b = b[:n]
		p := 12
		labels := []string{}
		for p < n && b[p] != 0 {
			l := int(b[p])
			p++
			if l > 63 || p+l > n {
				p = n
				break
			}
			labels = append(labels, string(b[p:p+l]))
			p += l
		}
		p++
		if p+4 > n {
			continue
		}
		typ := binary.BigEndian.Uint16(b[p : p+2])
		p += 4
		name := strings.ToLower(strings.Join(labels, ".")) + "."
		mu.RLock()
		ok := names[name]
		mu.RUnlock()
		out := append([]byte(nil), b[:p]...)
		out[2] = 0x84 | (b[2] & 1)
		out[3] = 0
		binary.BigEndian.PutUint16(out[4:6], 1)
		for i := 6; i < 12; i++ {
			out[i] = 0
		}
		if !ok {
			out[3] = 3
		} else if typ == 1 {
			out[7] = 1
			out = append(out, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, ttl, 0, 4, 10, 99, 0, targets[targetCounter.Add(1)%uint64(len(targets))])
		}
		c.WriteTo(out, addr)
	}
}
func main() {
	if v := os.Getenv("DNS_TARGETS"); v != "" {
		targets = nil
		for _, x := range strings.Split(v, ",") {
			n, e := strconv.Atoi(x)
			if e != nil || n < 11 || n > 13 {
				log.Fatal("invalid target")
			}
			targets = append(targets, byte(n))
		}
	}
	if v := os.Getenv("DNS_TTL"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 0 || n > 255 {
			log.Fatal("invalid TTL")
		}
		ttl = byte(n)
	}
	if f, e := os.Open("/home/isucon/webapp/pdns/u.isucon.dev.zone"); e == nil {
		scan := bufio.NewScanner(f)
		for scan.Scan() {
			v := strings.Fields(scan.Text())
			if len(v) >= 5 && v[3] == "A" {
				n := v[0] + ".u.isucon.dev."
				if v[0] == "@" {
					n = "u.isucon.dev."
				}
				baseNames[n] = true
			}
		}
		f.Close()
	}
	replace([]string{"pipe"})

	http.HandleFunc("/bcrypt", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Password string `json:"password"`
			Hash     string `json:"hash"`
		}
		if e := json.NewDecoder(r.Body).Decode(&v); e != nil {
			http.Error(w, "JSON", 400)
			return
		}
		if v.Hash == "" {
			b, e := bcrypt.GenerateFromPassword([]byte(v.Password), bcrypt.MinCost)
			if e != nil {
				http.Error(w, e.Error(), 400)
				return
			}
			w.Write(b)
		} else {
			if e := bcrypt.CompareHashAndPassword([]byte(v.Hash), []byte(v.Password)); e != nil {
				http.Error(w, "mismatch", 401)
			}
		}
	})
	http.HandleFunc("/names", func(w http.ResponseWriter, r *http.Request) {
		var v []string
		if e := json.NewDecoder(r.Body).Decode(&v); e != nil {
			http.Error(w, "JSON", 400)
			return
		}
		replace(v)
	})
	http.HandleFunc("/name", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1000))
		mu.Lock()
		names[strings.ToLower(string(b))+".u.isucon.dev."] = true
		namesRevision++
		mu.Unlock()
	})
	if os.Getenv("DNS") == "1" {
		go dns()
		go func() {
			client := &http.Client{Timeout: 2 * time.Second}
			for {
				source := os.Getenv("NAMES_SOURCE")
				if source == "" {
					source = "http://127.0.0.1:8080"
				}
				mu.RLock()
				revision := namesRevision
				mu.RUnlock()
				r, e := client.Get(source + "/internal/names")
				if e == nil {
					var v []string
					e = json.NewDecoder(r.Body).Decode(&v)
					r.Body.Close()
					if e == nil {
						replaceAt(v, &revision)
					}
				}
				time.Sleep(time.Second)
			}
		}()
	}
	log.Fatal(http.ListenAndServe(":8090", nil))
}
