package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"golang.org/x/net/dns/dnsmessage"
)

const zoneName = "u.isucon.dev."

type recordRow struct {
	Name    string `db:"name"`
	Type    string `db:"type"`
	Content string `db:"content"`
	TTL     uint32 `db:"ttl"`
}
type zoneStore struct {
	sync.RWMutex
	names   map[string][]dnsmessage.Resource
	soa     dnsmessage.Resource
	update  sync.Mutex
	db      *sqlx.DB
	queries atomic.Uint64
	answers atomic.Uint64
}

func canonical(name string) string { return strings.ToLower(strings.TrimSuffix(name, ".")) + "." }
func inZone(name string) bool      { return name == zoneName || strings.HasSuffix(name, "."+zoneName) }
func parseRecord(row recordRow) (dnsmessage.Resource, error) {
	name, err := dnsmessage.NewName(canonical(row.Name))
	if err != nil {
		return dnsmessage.Resource{}, err
	}
	resource := dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: name, Class: dnsmessage.ClassINET, TTL: row.TTL}}
	switch row.Type {
	case "A":
		ip := net.ParseIP(row.Content).To4()
		if ip == nil {
			return resource, fmt.Errorf("invalid A: %q", row.Content)
		}
		body := &dnsmessage.AResource{}
		copy(body.A[:], ip)
		resource.Body = body
		resource.Header.Type = dnsmessage.TypeA
	case "NS":
		target, err := dnsmessage.NewName(canonical(row.Content))
		if err != nil {
			return resource, err
		}
		resource.Body = &dnsmessage.NSResource{NS: target}
		resource.Header.Type = dnsmessage.TypeNS
	case "SOA":
		fields := strings.Fields(row.Content)
		if len(fields) != 7 {
			return resource, fmt.Errorf("invalid SOA: %q", row.Content)
		}
		ns, err := dnsmessage.NewName(canonical(fields[0]))
		if err != nil {
			return resource, err
		}
		mailbox, err := dnsmessage.NewName(canonical(fields[1]))
		if err != nil {
			return resource, err
		}
		nums := [5]uint32{}
		for i := range nums {
			v, err := strconv.ParseUint(fields[i+2], 10, 32)
			if err != nil {
				return resource, err
			}
			nums[i] = uint32(v)
		}
		resource.Body = &dnsmessage.SOAResource{NS: ns, MBox: mailbox, Serial: nums[0], Refresh: nums[1], Retry: nums[2], Expire: nums[3], MinTTL: nums[4]}
		resource.Header.Type = dnsmessage.TypeSOA
	default:
		return resource, fmt.Errorf("unsupported record type %q", row.Type)
	}
	return resource, nil
}
func addParents(names map[string][]dnsmessage.Resource, name string) {
	for name != zoneName {
		idx := strings.IndexByte(name, '.')
		if idx < 0 {
			return
		}
		name = name[idx+1:]
		if !inZone(name) {
			return
		}
		if _, ok := names[name]; !ok {
			names[name] = nil
		}
	}
}
func buildZone(rows []recordRow) (map[string][]dnsmessage.Resource, dnsmessage.Resource, error) {
	names := make(map[string][]dnsmessage.Resource)
	var soa dnsmessage.Resource
	for _, row := range rows {
		name := canonical(row.Name)
		if !inZone(name) {
			return nil, soa, fmt.Errorf("out of zone record: %s", name)
		}
		resource, err := parseRecord(row)
		if err != nil {
			return nil, soa, err
		}
		names[name] = append(names[name], resource)
		addParents(names, name)
		if name == zoneName && row.Type == "SOA" {
			soa = resource
		}
	}
	if soa.Body == nil {
		return nil, soa, fmt.Errorf("zone SOA missing")
	}
	return names, soa, nil
}

const recordsQuery = `SELECT r.name,r.type,r.content,COALESCE(r.ttl,0) ttl FROM records r JOIN domains d ON d.id=r.domain_id WHERE r.disabled=0 AND d.name='u.isucon.dev'`

func (s *zoneStore) reload(ctx context.Context) error {
	s.update.Lock()
	defer s.update.Unlock()
	var rows []recordRow
	if err := s.db.SelectContext(ctx, &rows, recordsQuery); err != nil {
		return err
	}
	names, soa, err := buildZone(rows)
	if err != nil {
		return err
	}
	s.Lock()
	s.names = names
	s.soa = soa
	s.Unlock()
	return nil
}
func (s *zoneStore) refresh(ctx context.Context, name string) error {
	if strings.HasSuffix(name, "$") || canonical(name) == zoneName {
		return s.reload(ctx)
	}
	name = canonical(name)
	if !inZone(name) {
		return fmt.Errorf("name outside zone")
	}
	s.update.Lock()
	defer s.update.Unlock()
	rows := []recordRow{}
	if err := s.db.SelectContext(ctx, &rows, recordsQuery+" AND r.name=?", strings.TrimSuffix(name, ".")); err != nil {
		return err
	}
	records := make([]dnsmessage.Resource, 0, len(rows))
	for _, row := range rows {
		r, err := parseRecord(row)
		if err != nil {
			return err
		}
		records = append(records, r)
	}
	s.Lock()
	defer s.Unlock()
	if len(records) == 0 {
		delete(s.names, name)
	} else {
		s.names[name] = records
		addParents(s.names, name)
	}
	return nil
}
func (s *zoneStore) response(packet []byte) ([]byte, error) {
	s.queries.Add(1)
	var parser dnsmessage.Parser
	header, err := parser.Start(packet)
	if err != nil {
		return nil, err
	}
	if header.Response {
		return nil, fmt.Errorf("not a question")
	}
	reply := dnsmessage.Message{Header: dnsmessage.Header{ID: header.ID, Response: true, OpCode: header.OpCode, RecursionDesired: header.RecursionDesired, CheckingDisabled: header.CheckingDisabled}}
	question, err := parser.Question()
	if err != nil {
		reply.RCode = dnsmessage.RCodeFormatError
		return reply.Pack()
	}
	reply.Questions = []dnsmessage.Question{question}
	if _, err := parser.Question(); err != dnsmessage.ErrSectionDone {
		reply.RCode = dnsmessage.RCodeFormatError
		return reply.Pack()
	}
	if header.OpCode != 0 {
		reply.RCode = dnsmessage.RCodeNotImplemented
		return reply.Pack()
	}
	name := canonical(question.Name.String())
	if !inZone(name) || (question.Class != dnsmessage.ClassINET && question.Class != dnsmessage.ClassANY) {
		reply.RCode = dnsmessage.RCodeRefused
		return reply.Pack()
	}
	reply.Authoritative = true
	s.RLock()
	records, exists := s.names[name]
	soa := s.soa
	for _, resource := range records {
		if question.Type == resource.Header.Type || question.Type == dnsmessage.TypeALL {
			reply.Answers = append(reply.Answers, resource)
		}
	}
	s.RUnlock()
	if !exists {
		reply.RCode = dnsmessage.RCodeNameError
	}
	if len(reply.Answers) == 0 {
		if body, ok := soa.Body.(*dnsmessage.SOAResource); ok && body.MinTTL < soa.Header.TTL {
			soa.Header.TTL = body.MinTTL
		}
		reply.Authorities = []dnsmessage.Resource{soa}
	}
	data, err := reply.Pack()
	if err != nil {
		return nil, err
	}
	// All ordinary A/NS/SOA replies fit 512 bytes. Bound exceptional ANY replies.
	if len(data) > 512 {
		reply.Truncated = true
		reply.Answers = nil
		reply.Authorities = nil
		data, err = reply.Pack()
	}
	s.answers.Add(1)
	return data, err
}
func (s *zoneStore) flushHandler(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || (host != "127.0.0.1" && host != "::1" && host != "10.99.0.11" && host != "10.99.0.12") {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Header.Get("X-API-Key") != "isudns" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPut {
		http.Error(w, "method", http.StatusMethodNotAllowed)
		return
	}
	name := r.URL.Query().Get("domain")
	if name == "" {
		http.Error(w, "missing domain", http.StatusBadRequest)
		return
	}
	if err := s.refresh(r.Context(), name); err != nil {
		log.Printf("refresh %q: %v", name, err)
		http.Error(w, "refresh failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"count": 1, "result": "Flushed cache."})
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func main() {
	config := mysql.NewConfig()
	config.Net = "tcp"
	config.Addr = net.JoinHostPort(env("ISUCON13_DNS_DB_ADDRESS", "10.99.0.12"), "3306")
	config.User = "isudns"
	config.Passwd = "isudns"
	config.DBName = "isudns"
	config.InterpolateParams = true
	config.Timeout = 3 * time.Second
	db, err := sqlx.Open("mysql", config.FormatDSN())
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	store := &zoneStore{db: db}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = store.reload(ctx)
	cancel()
	if err != nil {
		log.Fatal(err)
	}
	address, err := net.ResolveUDPAddr("udp4", env("FASTDNS_ADDR", "127.0.0.1:5354"))
	if err != nil {
		log.Fatal(err)
	}
	conn, err := net.ListenUDP("udp4", address)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetReadBuffer(4 << 20); err != nil {
		log.Print(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/servers/localhost/cache/flush", store.flushHandler)
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]uint64{"queries": store.queries.Load(), "answers": store.answers.Load()})
	})
	server := &http.Server{Addr: env("FASTDNS_API_ADDR", "127.0.0.1:8082"), Handler: mux, ReadHeaderTimeout: 3 * time.Second}
	go func() { log.Fatal(server.ListenAndServe()) }()
	log.Printf("loaded %d names; DNS %s; API %s", len(store.names), conn.LocalAddr(), server.Addr)
	for worker := 0; worker < 4; worker++ {
		go func() {
			buffer := make([]byte, 4096)
			for {
				n, remote, err := conn.ReadFromUDP(buffer)
				if err != nil {
					log.Print(err)
					return
				}
				response, err := store.response(buffer[:n])
				if err == nil {
					if _, err := conn.WriteToUDP(response, remote); err != nil {
						log.Print(err)
					}
				}
			}
		}()
	}
	select {}
}
