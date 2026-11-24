package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/ipv4"
)

// PowerDNS retains the complete authoritative zone. The UDP front end serves
// address and negative answers from its copy, forwarding other query types.
var zone struct {
	sync.RWMutex
	records  map[string][4]byte
	wire     map[string][4]byte
	db       *sqlx.DB
	accounts *sqlx.DB
}

func loadZone() error {
	if zone.db == nil {
		return nil
	}
	var rows []struct {
		Name    string
		Content string
	}
	if err := zone.db.Select(&rows, "SELECT name,content FROM records WHERE type='A' AND disabled=0"); err != nil {
		return err
	}
	records := make(map[string][4]byte, len(rows))
	for _, r := range rows {
		if ip := net.ParseIP(r.Content).To4(); ip != nil {
			records[strings.ToLower(r.Name)+"."] = [4]byte(ip)
		}
	}
	if zone.accounts != nil {
		var names []string
		if err := zone.accounts.Select(&names, "SELECT name FROM registered_domains"); err != nil {
			return err
		}
		ip := [4]byte(net.ParseIP(powerDNSSubdomainAddress).To4())
		for _, name := range names {
			records[strings.ToLower(name)+".u.isucon.dev."] = ip
		}
	}
	zone.Lock()
	zone.records = records
	zone.wire = make(map[string][4]byte, len(records))
	for name, ip := range records {
		zone.wire[wireName(name)] = ip
	}
	zone.Unlock()
	return nil
}
func addAddress(name string) error {
	fqdn := strings.ToLower(name) + ".u.isucon.dev"
	if zone.accounts == nil {
		_, err := zone.db.Exec("INSERT INTO records(domain_id,name,type,content,ttl,prio,disabled,auth) SELECT id,?,'A',?,60,0,0,1 FROM domains WHERE name='u.isucon.dev'", fqdn, powerDNSSubdomainAddress)
		if err != nil {
			return err
		}
	}
	zone.Lock()
	ip := [4]byte(net.ParseIP(powerDNSSubdomainAddress).To4())
	zone.records[fqdn+"."] = ip
	zone.wire[wireName(fqdn)] = ip
	zone.Unlock()
	return nil
}
func runDNS() error {
	db, err := sqlx.Connect("mysql", os.Getenv("ISUPIPE_ZONE_DSN"))
	if err != nil {
		return err
	}
	zone.db = db
	if dsn := os.Getenv("ISUPIPE_DOMAIN_DB"); dsn != "" {
		zone.accounts, err = sqlx.Connect("mysql", dsn)
		if err != nil {
			return err
		}
	}
	if err = loadZone(); err != nil {
		return err
	}
	listen := os.Getenv("ISUPIPE_DNS_ADDR")
	if listen == "" {
		listen = ":53"
	}
	addr, err := net.ResolveUDPAddr("udp", listen)
	if err != nil {
		return err
	}
	sock, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	tcp, err := net.Listen("tcp", listen)
	if err != nil {
		sock.Close()
		return err
	}
	go func() {
		for {
			client, err := tcp.Accept()
			if err != nil {
				return
			}
			go func() {
				defer client.Close()
				var length [2]byte
				for {
					client.SetDeadline(time.Now().Add(30 * time.Second))
					if _, err := io.ReadFull(client, length[:]); err != nil {
						return
					}
					n := binary.BigEndian.Uint16(length[:])
					if n == 0 {
						return
					}
					q := make([]byte, n)
					if _, err := io.ReadFull(client, q); err != nil {
						return
					}
					ans := dnsAnswer(q, nil)
					if len(ans) == 0 {
						return
					}
					binary.BigEndian.PutUint16(length[:], uint16(len(ans)))
					packet := net.Buffers{length[:], ans}
					if _, err := packet.WriteTo(client); err != nil {
						return
					}
				}

			}()
		}
	}()
	packetConn := ipv4.NewPacketConn(sock)
	workers, _ := strconv.Atoi(os.Getenv("ISUPIPE_DNS_WORKERS"))
	if workers < 1 || workers > 8 {
		workers = 2
	}
	batch, _ := strconv.Atoi(os.Getenv("ISUPIPE_DNS_BATCH"))
	if batch < 1 || batch > 128 {
		batch = 16
	}
	for i := 0; i < workers; i++ {
		go serveDNSBatch(packetConn, batch)
	}
	return nil
}
func serveDNSBatch(conn *ipv4.PacketConn, batchSize int) {
	reads := make([]ipv4.Message, batchSize)
	writes := make([]ipv4.Message, batchSize)
	for i := range reads {
		reads[i].Buffers = [][]byte{make([]byte, 4096)}
		writes[i].Buffers = make([][]byte, 1)
	}
	for {
		n, err := conn.ReadBatch(reads[:], 0)
		if err != nil {
			log.Print(err)
			return
		}
		count := 0
		for i := 0; i < n; i++ {
			packet := dnsAnswer(reads[i].Buffers[0][:reads[i].N], writes[count].Buffers[0][:0])
			if len(packet) == 0 {
				continue
			}
			writes[count].Buffers[0] = packet
			writes[count].Addr = reads[i].Addr
			count++
		}
		for sent := 0; sent < count; {
			n, err := conn.WriteBatch(writes[sent:count], 0)
			if err != nil || n == 0 {
				break
			}
			sent += n
		}
	}
}

var dnsOrigin = dnsmessage.MustNewName("u.isucon.dev.")
var dnsAuthority = dnsmessage.SOAResource{NS: dnsmessage.MustNewName("ns1.u.isucon.dev."), MBox: dnsmessage.MustNewName("hostmaster.u.isucon.dev."), Refresh: 10800, Retry: 3600, Expire: 604800}

func dnsAnswer(query, dst []byte) []byte {
	if packet, ok := addressAnswer(query, dst); ok {
		return packet
	}
	var p dnsmessage.Parser
	h, err := p.Start(query)
	if err != nil || h.Response {
		return nil
	}
	q, err := p.Question()
	if err != nil {
		return nil
	}
	name := strings.ToLower(q.Name.String())
	if (name == "u.isucon.dev." && q.Type != dnsmessage.TypeA) || q.Class != dnsmessage.ClassINET || (name != "u.isucon.dev." && !strings.HasSuffix(name, ".u.isucon.dev.")) {
		packet, err := dnsBackend(query)
		if err != nil {
			return nil
		}
		return packet
	}
	zone.RLock()
	ip, found := zone.records[name]
	zone.RUnlock()
	header := dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true, RecursionDesired: h.RecursionDesired}
	if !found {
		header.RCode = dnsmessage.RCodeNameError
	}
	b := dnsmessage.NewBuilder(dst, header)
	b.EnableCompression()
	if b.StartQuestions() != nil || b.Question(q) != nil {
		return nil
	}
	if found && (q.Type == dnsmessage.TypeA || q.Type == dnsmessage.TypeALL) {
		if b.StartAnswers() != nil || b.AResource(dnsmessage.ResourceHeader{Name: q.Name, Class: q.Class, TTL: 60}, dnsmessage.AResource{A: ip}) != nil {
			return nil
		}
	} else if b.StartAuthorities() != nil || b.SOAResource(dnsmessage.ResourceHeader{Name: dnsOrigin, Class: q.Class, TTL: 0}, dnsAuthority) != nil {
		return nil
	}
	packet, err := b.Finish()
	if err != nil {
		return nil
	}
	return packet
}
func dnsBackend(query []byte) ([]byte, error) {
	conn, err := net.DialTimeout("udp", "127.0.0.1:5353", time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	if _, err = conn.Write(query); err != nil {
		return nil, err
	}
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("DNS backend: %w", err)
	}
	return buf[:n], nil
}
