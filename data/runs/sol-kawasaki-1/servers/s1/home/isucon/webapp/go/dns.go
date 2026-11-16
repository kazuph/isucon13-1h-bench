package main

import (
	"fmt"
	"github.com/jmoiron/sqlx"
	"golang.org/x/net/dns/dnsmessage"
	"net"
	"os"
	"strings"
	"sync"
)

var dnsDB *sqlx.DB
var dnsReloadMu sync.Mutex
var dnsRecords struct {
	sync.RWMutex
	names map[string][]dnsmessage.Resource
}

func reloadDNS() error {
	if dnsDB == nil {
		return nil
	}
	dnsReloadMu.Lock()
	defer dnsReloadMu.Unlock()
	var rows []struct {
		Name    string `db:"name"`
		Type    string `db:"type"`
		Content string `db:"content"`
		TTL     uint32 `db:"ttl"`
	}
	if err := dnsDB.Select(&rows, `SELECT name,type,content,ttl FROM records WHERE disabled=0`); err != nil {
		return err
	}
	records := make(map[string][]dnsmessage.Resource, len(rows))
	for _, row := range rows {
		key := strings.ToLower(strings.TrimSuffix(row.Name, ".")) + "."
		name, err := dnsmessage.NewName(key)
		if err != nil {
			return err
		}
		r := dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: name, Class: dnsmessage.ClassINET, TTL: 60}}
		switch row.Type {
		case "A":
			ip := net.ParseIP(row.Content).To4()
			if ip == nil {
				return fmt.Errorf("invalid A record %s", row.Name)
			}
			r.Header.Type = dnsmessage.TypeA
			r.Body = &dnsmessage.AResource{A: [4]byte{ip[0], ip[1], ip[2], ip[3]}}
		case "NS":
			ns, err := dnsmessage.NewName(strings.TrimSuffix(row.Content, ".") + ".")
			if err != nil {
				return err
			}
			r.Header.Type = dnsmessage.TypeNS
			r.Body = &dnsmessage.NSResource{NS: ns}
		case "SOA":
			fields := strings.Fields(row.Content)
			if len(fields) != 7 {
				return fmt.Errorf("invalid SOA")
			}
			ns, _ := dnsmessage.NewName(strings.TrimSuffix(fields[0], ".") + ".")
			mb, _ := dnsmessage.NewName(strings.TrimSuffix(fields[1], ".") + ".")
			var serial, refresh, retry, expire, min uint32
			if _, err := fmt.Sscanf(strings.Join(fields[2:], " "), "%d %d %d %d %d", &serial, &refresh, &retry, &expire, &min); err != nil {
				return err
			}
			r.Header.Type = dnsmessage.TypeSOA
			r.Body = &dnsmessage.SOAResource{NS: ns, MBox: mb, Serial: serial, Refresh: refresh, Retry: retry, Expire: expire, MinTTL: min}
		default:
			continue
		}
		records[key] = append(records[key], r)
	}
	dnsRecords.Lock()
	dnsRecords.names = records
	dnsRecords.Unlock()
	return nil
}

func startDNS() error {
	addr := os.Getenv("ISUCON_DNS_LISTEN")
	if addr == "" {
		return nil
	}
	var err error
	dnsDB, err = sqlx.Open("mysql", "isudns:isudns@tcp(127.0.0.1:3306)/isudns")
	if err != nil {
		return err
	}
	dnsDB.SetMaxOpenConns(8)
	dnsDB.SetMaxIdleConns(8)
	if err = reloadDNS(); err != nil {
		return err
	}
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return err
	}
	conn.SetReadBuffer(4 << 20)
	conn.SetWriteBuffer(4 << 20)
	for i := 0; i < 2; i++ {
		go func() {
			buf := make([]byte, 4096)
			out := make([]byte, 0, 4096)
			for {
				n, remote, err := conn.ReadFromUDPAddrPort(buf)
				if err != nil {
					continue
				}
				packet, err := answerDNS(buf[:n], out)
				if err != nil {
					continue
				}
				conn.WriteToUDPAddrPort(packet, remote)
			}
		}()
	}
	return nil
}

func addDNSRecord(name string) error {
	recordName := strings.ToLower(strings.TrimSuffix(name, ".")) + ".u.isucon.dev"
	result, err := dnsDB.Exec(`INSERT INTO records (domain_id,name,type,content,ttl,prio,disabled,auth) SELECT id,?,'A',?,60,0,0,1 FROM domains WHERE name='u.isucon.dev'`, recordName, powerDNSSubdomainAddress)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("DNS zone not found")
	}
	return nil
}
func publishDNSRecord(name string) {
	dnsReloadMu.Lock()
	defer dnsReloadMu.Unlock()
	key := strings.ToLower(strings.TrimSuffix(name, ".")) + ".u.isucon.dev."
	dnsName, err := dnsmessage.NewName(key)
	if err != nil {
		return
	}
	ip := net.ParseIP(powerDNSSubdomainAddress).To4()
	if ip == nil {
		return
	}
	r := dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: dnsName, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60}, Body: &dnsmessage.AResource{A: [4]byte{ip[0], ip[1], ip[2], ip[3]}}}
	dnsRecords.Lock()
	dnsRecords.names[key] = []dnsmessage.Resource{r}
	dnsRecords.Unlock()
}

func answerDNS(query, out []byte) ([]byte, error) {
	var parser dnsmessage.Parser
	h, err := parser.Start(query)
	if err != nil {
		return nil, err
	}
	if h.Response || len(query) < 12 || query[4] != 0 || query[5] != 1 {
		return nil, fmt.Errorf("unsupported DNS question")
	}
	question, err := parser.Question()
	if err != nil {
		return nil, err
	}
	key := strings.ToLower(question.Name.String())
	header := dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true, RecursionDesired: h.RecursionDesired}
	var resources, zone []dnsmessage.Resource
	if key != "u.isucon.dev." && !strings.HasSuffix(key, ".u.isucon.dev.") {
		header.Authoritative = false
		header.RCode = dnsmessage.RCodeRefused
	} else {
		dnsRecords.RLock()
		var exists bool
		resources, exists = dnsRecords.names[key]
		zone = dnsRecords.names["u.isucon.dev."]
		dnsRecords.RUnlock()
		if !exists {
			header.RCode = dnsmessage.RCodeNameError
		}
	}
	builder := dnsmessage.NewBuilder(out[:0], header)
	if len(key) > 100 {
		builder.EnableCompression()
	}
	if err := builder.StartQuestions(); err != nil {
		return nil, err
	}
	if err := builder.Question(question); err != nil {
		return nil, err
	}
	if err := builder.StartAnswers(); err != nil {
		return nil, err
	}
	answered := false
	for _, r := range resources {
		if question.Type == r.Header.Type || question.Type == dnsmessage.TypeALL {
			if err := appendDNSResource(&builder, r); err != nil {
				return nil, err
			}
			answered = true
		}
	}
	if !answered {
		if err := builder.StartAuthorities(); err != nil {
			return nil, err
		}
		for _, r := range zone {
			if r.Header.Type == dnsmessage.TypeSOA {
				if err := appendDNSResource(&builder, r); err != nil {
					return nil, err
				}
			}
		}
	}
	return builder.Finish()
}
func appendDNSResource(b *dnsmessage.Builder, r dnsmessage.Resource) error {
	switch body := r.Body.(type) {
	case *dnsmessage.AResource:
		return b.AResource(r.Header, *body)
	case *dnsmessage.NSResource:
		return b.NSResource(r.Header, *body)
	case *dnsmessage.SOAResource:
		return b.SOAResource(r.Header, *body)
	}
	return fmt.Errorf("unsupported resource")
}
