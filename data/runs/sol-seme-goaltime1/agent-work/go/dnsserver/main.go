package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"golang.org/x/net/dns/dnsmessage"
)

const zone = "u.isucon.dev."
const updateToken = "isupipe-authoritative-zone-update"

type zoneData struct {
	A     map[string][4]byte
	Known map[string]bool
}

var current atomic.Pointer[zoneData]
var updateMu sync.Mutex
var store *sqlx.DB
var queries atomic.Uint64
var received atomic.Uint64
var invalid atomic.Uint64
var writeErrors atomic.Uint64
var found atomic.Uint64

func loadZone() error {
	var rows []struct {
		Name    string `db:"name"`
		Type    string `db:"type"`
		Content string `db:"content"`
	}
	if err := store.Select(&rows, "SELECT name,type,content FROM records WHERE disabled=0"); err != nil {
		return err
	}
	z := &zoneData{A: map[string][4]byte{}, Known: map[string]bool{zone: true}}
	for _, r := range rows {
		name := strings.ToLower(strings.TrimSuffix(r.Name, ".")) + "."
		z.Known[name] = true
		if r.Type == "A" {
			ip := net.ParseIP(r.Content).To4()
			if ip != nil {
				z.A[name] = [4]byte{ip[0], ip[1], ip[2], ip[3]}
			}
		}
	}
	current.Store(z)
	return nil
}
func zoneSOA() dnsmessage.Resource {
	name, _ := dnsmessage.NewName(zone)
	ns, _ := dnsmessage.NewName("ns1." + zone)
	mbox, _ := dnsmessage.NewName("hostmaster." + zone)
	return dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: name, Type: dnsmessage.TypeSOA, Class: dnsmessage.ClassINET, TTL: 0}, Body: &dnsmessage.SOAResource{NS: ns, MBox: mbox, Serial: 0, Refresh: 10800, Retry: 3600, Expire: 604800, MinTTL: 0}}
}
func answer(packet []byte) ([]byte, error) {
	var request dnsmessage.Message
	if err := request.Unpack(packet); err != nil {
		return nil, err
	}
	if request.Header.Response || len(request.Questions) != 1 {
		return nil, fmt.Errorf("invalid question")
	}
	q := request.Questions[0]
	name := strings.ToLower(q.Name.String())
	response := dnsmessage.Message{Header: dnsmessage.Header{ID: request.Header.ID, Response: true, Authoritative: true, RecursionDesired: request.Header.RecursionDesired}, Questions: request.Questions}
	z := current.Load()
	queries.Add(1)
	if !strings.HasSuffix(name, "."+zone) && name != zone {
		response.Header.Authoritative = false
		response.Header.RCode = dnsmessage.RCodeRefused
		return response.Pack()
	}
	if !z.Known[name] {
		response.Header.RCode = dnsmessage.RCodeNameError
		response.Authorities = []dnsmessage.Resource{zoneSOA()}
		return response.Pack()
	}
	if ip, ok := z.A[name]; ok && (q.Type == dnsmessage.TypeA || q.Type == dnsmessage.TypeALL) {
		response.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60}, Body: &dnsmessage.AResource{A: ip}}}
		found.Add(1)
	}
	if name == zone && (q.Type == dnsmessage.TypeSOA || q.Type == dnsmessage.TypeALL) {
		response.Answers = append(response.Answers, zoneSOA())
	}
	if name == zone && (q.Type == dnsmessage.TypeNS || q.Type == dnsmessage.TypeALL) {
		ns, _ := dnsmessage.NewName("ns1." + zone)
		response.Answers = append(response.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeNS, Class: dnsmessage.ClassINET, TTL: 60}, Body: &dnsmessage.NSResource{NS: ns}})
	}
	if len(response.Answers) == 0 {
		response.Authorities = []dnsmessage.Resource{zoneSOA()}
	}
	return response.Pack()
}
func serveUDP(conn *net.UDPConn) {
	buf := make([]byte, 4096)
	for {
		n, addr, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			log.Print(err)
			return
		}
		count := received.Add(1)
		out, err := answer(buf[:n])
		if err == nil {
			_, err = conn.WriteToUDPAddrPort(out, addr)
			if err != nil {
				writeErrors.Add(1)
			}
		} else {
			invalid.Add(1)
		}
		if count <= 12 {
			log.Printf("initial UDP packet source=%s bytes=%d response_bytes=%d error=%v", addr, n, len(out), err)
		}
	}
}
func main() {
	setupIconCache()
	runtime.GOMAXPROCS(2)
	var err error
	store, err = sqlx.Connect("mysql", "isudns:isudns@tcp(127.0.0.1:3306)/isudns")
	if err != nil {
		log.Fatal(err)
	}
	if err = loadZone(); err != nil {
		log.Fatal(err)
	}
	http.HandleFunc("/reload", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Zone-Token") != updateToken {
			http.Error(w, "forbidden", 403)
			return
		}
		updateMu.Lock()
		defer updateMu.Unlock()
		if err := loadZone(); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(204)
	})
	http.HandleFunc("/record", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Zone-Token") != updateToken {
			http.Error(w, "forbidden", 403)
			return
		}
		var input struct{ Name, Address string }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		ip := net.ParseIP(input.Address).To4()
		if ip == nil {
			http.Error(w, "invalid address", 400)
			return
		}
		name := strings.ToLower(strings.TrimSuffix(input.Name, ".")) + "."
		if !strings.HasSuffix(name, "."+zone) {
			http.Error(w, "invalid name", 400)
			return
		}
		updateMu.Lock()
		old := current.Load()
		z := &zoneData{A: make(map[string][4]byte, len(old.A)+1), Known: make(map[string]bool, len(old.Known)+1)}
		for k, v := range old.A {
			z.A[k] = v
		}
		for k, v := range old.Known {
			z.Known[k] = v
		}
		z.A[name] = [4]byte{ip[0], ip[1], ip[2], ip[3]}
		z.Known[name] = true
		current.Store(z)
		updateMu.Unlock()
		w.WriteHeader(204)
	})
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"records": len(current.Load().A), "queries": queries.Load(), "positive": found.Load(), "received": received.Load(), "invalid": invalid.Load(), "write_errors": writeErrors.Load()})
	})
	go func() { log.Fatal(http.ListenAndServe("10.99.0.11:8053", nil)) }()
	addr := os.Getenv("DNS_LISTEN")
	if addr == "" {
		addr = ":53"
	}
	ua, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		log.Fatal(err)
	}
	conn, err := net.ListenUDP("udp4", ua)
	if err != nil {
		log.Fatal(err)
	}
	conn.SetReadBuffer(4 << 20)
	conn.SetWriteBuffer(4 << 20)
	log.Printf("authoritative DNS ready on %s with %d records at %s", addr, len(current.Load().A), time.Now().UTC())
	go serveUDP(conn)
	serveUDP(conn)
}
