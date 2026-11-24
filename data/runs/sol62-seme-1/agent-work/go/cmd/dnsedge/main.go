package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/miekg/dns"
)

const (
	zoneName       = "u.isucon.dev."
	powerDNS       = "127.0.0.1:5300"
	refreshAddress = "10.99.0.11:8082"
)

type zoneSnapshot struct {
	addresses map[string][]dns.RR
	names     map[string]struct{}
	soa       *dns.SOA
	supported bool
}

type authoritativeServer struct {
	db       *sql.DB
	snapshot atomic.Pointer[zoneSnapshot]
	reloadMu sync.Mutex
}

func (server *authoritativeServer) reload(ctx context.Context) error {
	server.reloadMu.Lock()
	defer server.reloadMu.Unlock()

	rows, err := server.db.QueryContext(ctx, `
		SELECT r.name, r.type, r.content, r.ttl, r.auth
		FROM records r JOIN domains d ON d.id = r.domain_id
		WHERE d.name = 'u.isucon.dev' AND r.disabled = 0
		ORDER BY r.id`)
	if err != nil {
		return err
	}
	defer rows.Close()

	zone := &zoneSnapshot{
		addresses: make(map[string][]dns.RR),
		names:     make(map[string]struct{}),
		supported: true,
	}
	for rows.Next() {
		var name, rrtype, content string
		var ttl uint32
		var authoritative bool
		if err := rows.Scan(&name, &rrtype, &content, &ttl, &authoritative); err != nil {
			return err
		}
		name = dns.CanonicalName(name)
		if !authoritative || !dns.IsSubDomain(zoneName, name) {
			zone.supported = false
			continue
		}
		for _, label := range dns.Split(name) {
			ancestor := name[label:]
			zone.names[ancestor] = struct{}{}
			if ancestor == zoneName {
				break
			}
		}
		switch rrtype {
		case "A":
			ip := net.ParseIP(content).To4()
			if ip == nil || strings.HasPrefix(name, "*.") {
				zone.supported = false
				continue
			}
			zone.addresses[name] = append(zone.addresses[name], &dns.A{
				Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: ttl},
				A:   ip,
			})
		case "SOA":
			fields := strings.Fields(content)
			if name != zoneName || len(fields) != 7 {
				zone.supported = false
				continue
			}
			fields[0], fields[1] = dns.Fqdn(fields[0]), dns.Fqdn(fields[1])
			rr, err := dns.NewRR(fmt.Sprintf("%s %d IN SOA %s", name, ttl, strings.Join(fields, " ")))
			if err != nil {
				return err
			}
			zone.soa = rr.(*dns.SOA)
		case "NS":
			zone.supported = zone.supported && name == zoneName
		default:
			zone.supported = false
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if zone.soa == nil {
		return fmt.Errorf("%s has no SOA", zoneName)
	}
	server.snapshot.Store(zone)
	return nil
}

func (server *authoritativeServer) ServeDNS(w dns.ResponseWriter, request *dns.Msg) {
	if request.Opcode != dns.OpcodeQuery || request.IsTsig() != nil {
		_ = w.WriteMsg(new(dns.Msg).SetRcode(request, dns.RcodeRefused))
		return
	}
	if len(request.Question) != 1 {
		_ = w.WriteMsg(new(dns.Msg).SetRcode(request, dns.RcodeFormatError))
		return
	}
	question := request.Question[0]
	if question.Qtype == dns.TypeAXFR || question.Qtype == dns.TypeIXFR {
		_ = w.WriteMsg(new(dns.Msg).SetRcode(request, dns.RcodeRefused))
		return
	}

	zone := server.snapshot.Load()
	opt := request.IsEdns0()
	if zone == nil || !zone.supported ||
		(opt != nil && (opt.Version() != 0 || opt.Do() || len(opt.Option) != 0)) {
		forwardDNS(w, request)
		return
	}
	name := dns.CanonicalName(question.Name)
	if question.Qclass != dns.ClassINET || question.Qtype != dns.TypeA || !dns.IsSubDomain(zoneName, name) {
		forwardDNS(w, request)
		return
	}

	response := new(dns.Msg).SetReply(request)
	response.Authoritative = true
	response.Answer = zone.addresses[name]
	if len(response.Answer) == 0 {
		if _, exists := zone.names[name]; !exists {
			response.Rcode = dns.RcodeNameError
		}
		response.Ns = []dns.RR{zone.soa}
	}
	if opt != nil {
		response.SetEdns0(1232, false)
	}
	if _, udp := w.RemoteAddr().(*net.UDPAddr); udp {
		size := uint16(512)
		if opt != nil && opt.UDPSize() > size {
			size = min(opt.UDPSize(), 1232)
		}
		response.Truncate(int(size))
	}
	if err := w.WriteMsg(response); err != nil {
		log.Printf("write DNS response: %v", err)
	}
}

func forwardDNS(w dns.ResponseWriter, request *dns.Msg) {
	client := dns.Client{Timeout: time.Second}
	if _, tcp := w.RemoteAddr().(*net.TCPAddr); tcp {
		client.Net = "tcp"
	}
	response, _, err := client.Exchange(request, powerDNS)
	if err != nil {
		response = new(dns.Msg).SetRcode(request, dns.RcodeServerFailure)
	}
	_ = w.WriteMsg(response)
}

func main() {
	token := os.Getenv("ISUPIPE_DNS_REFRESH_TOKEN")
	if token == "" {
		log.Fatal("ISUPIPE_DNS_REFRESH_TOKEN is required")
	}
	conf := mysql.NewConfig()
	conf.Net = "unix"
	conf.Addr = "/run/mysqld/mysqld.sock"
	conf.DBName = "isudns"
	conf.User = "isupipe_dns_edge"
	conf.Passwd = os.Getenv("ISUPIPE_DNS_DB_PASSWORD")
	db, err := sql.Open("mysql", conf.FormatDSN())
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	server := &authoritativeServer{db: db}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err = server.reload(ctx)
	cancel()
	if err != nil {
		log.Fatal(err)
	}

	// Keep administrative changes made directly with pdnsutil visible.
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			if err := server.reload(ctx); err != nil {
				log.Printf("refresh zone: %v", err)
			}
			cancel()
		}
	}()

	refresh := http.NewServeMux()
	refresh.HandleFunc("/refresh", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := server.reload(ctx); err != nil {
			log.Printf("refresh zone: %v", err)
			http.Error(w, "zone reload failed", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	go func() {
		admin := &http.Server{
			Addr: refreshAddress, Handler: refresh, ReadHeaderTimeout: time.Second,
			ReadTimeout: 2 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second,
		}
		log.Fatal(admin.ListenAndServe())
	}()
	go func() {
		server := &dns.Server{Addr: ":53", Net: "tcp", Handler: server}
		log.Fatal(server.ListenAndServe())
	}()
	udp := &dns.Server{Addr: ":53", Net: "udp", Handler: server, UDPSize: 4096}
	log.Fatal(udp.ListenAndServe())
}
