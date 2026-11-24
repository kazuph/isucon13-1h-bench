package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

const zoneName = "u.isucon.dev."

var apex = dnsmessage.MustNewName(zoneName)

type aRecord struct {
	ip  [4]byte
	ttl uint32
}
type zoneIndex struct {
	addresses map[string][]aRecord
	special   map[string]bool
	soa       dnsmessage.SOAResource
	soaHeader dnsmessage.ResourceHeader
	complete  bool
}
type authority struct {
 reloadMu sync.Mutex
 dirty bool
	sync.RWMutex
	data      *zoneIndex
	backend   string
	localAddr net.IP
}

var fastDNS *authority

func safeOwner(name string) bool {
	for i := 0; i < len(name); i++ {
		ch := name[i]
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' && ch != '_' && ch != '.' {
			return false
		}
	}
	return true
}

func (a *authority) load() error {
	// Read the exact records served by the authoritative backend, including TTL and SOA.
	raw, err := exec.Command("pdnsutil", "list-zone", strings.TrimSuffix(zoneName, ".")).Output()
	if err != nil {
		return err
	}
	index := &zoneIndex{addresses: make(map[string][]aRecord), special: make(map[string]bool), complete: true}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		f := strings.Fields(scanner.Text())
		if len(f) == 0 || f[0] == "$ORIGIN" {
			continue
		}
		if len(f) < 5 || f[2] != "IN" {
			index.complete = false
			continue
		}
		name := strings.ToLower(strings.TrimSuffix(f[0], ".") + ".")
		ttl, err := strconv.ParseUint(f[1], 10, 32)
		if err != nil {
			return err
		}
		if !safeOwner(name) {
			index.complete = false
			continue
		}
		if !strings.HasSuffix(name, "."+zoneName) && name != zoneName {
			index.complete = false
			continue
		}
		// Empty non-terminals and delegation points require the full resolver.
		for parent := name; parent != zoneName; {
			i := strings.IndexByte(parent, '.')
			if i < 0 {
				break
			}
			parent = parent[i+1:]
			index.special[parent] = true
		}
		if f[3] != "A" {
			index.special[name] = true
			if f[3] == "NS" && name != zoneName {
				index.complete = false
			}
			continue
		}
		addr, err := netip.ParseAddr(f[4])
		if err != nil || !addr.Is4() {
			return fmt.Errorf("invalid A record for %s", name)
		}
		index.addresses[name] = append(index.addresses[name], aRecord{addr.As4(), uint32(ttl)})
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	msg := dnsmessage.Message{Header: dnsmessage.Header{ID: 1}, Questions: []dnsmessage.Question{{Name: apex, Type: dnsmessage.TypeSOA, Class: dnsmessage.ClassINET}}}
	wire, err := msg.Pack()
	if err != nil {
		return err
	}
	answer, err := a.relay(wire,true)
	if err != nil {
		return err
	}
	var response dnsmessage.Message
	if err := response.Unpack(answer); err != nil {
		return err
	}
	if len(response.Answers) != 1 {
		return fmt.Errorf("expected a zone SOA")
	}
	soa, ok := response.Answers[0].Body.(*dnsmessage.SOAResource)
	if !ok {
		return fmt.Errorf("invalid SOA")
	}
	index.soa = *soa
	index.soaHeader = response.Answers[0].Header
	if index.soaHeader.TTL > soa.MinTTL {
		index.soaHeader.TTL = soa.MinTTL
	}
	a.Lock()
	a.data = index
	a.Unlock()
	return nil
}
func (a *authority) added(encoded, address string) {
	var raw strings.Builder
	for i := 0; i+3 < len(encoded); i += 4 {
		n, err := strconv.ParseUint(encoded[i+1:i+4], 10, 8)
		if err != nil {
			return
		}
		raw.WriteByte(byte(n))
	}
	label := strings.ToLower(raw.String())
	a.Lock()
	defer a.Unlock()
	if a.data == nil {
		return
	}
	if !safeOwner(label) || strings.Contains(label, ".") {
		a.data.complete = false
		return
	}
	ip, err := netip.ParseAddr(address)
	if err != nil {
		return
	}
	name := label + "." + zoneName
	for _, r := range a.data.addresses[name] {
		if r.ip == ip.As4() {
			return
		}
	}
	a.data.addresses[name] = append(a.data.addresses[name], aRecord{ip.As4(), 30})
}

func (a *authority) cached(packet, buf []byte) ([]byte, bool) {
	if len(packet) < 12 || binary.BigEndian.Uint16(packet[4:6]) != 1 || binary.BigEndian.Uint32(packet[6:10]) != 0 {
		return nil, false
	}
	ar := binary.BigEndian.Uint16(packet[10:12])
	if ar > 1 {
		return nil, false
	}
	var parser dnsmessage.Parser
	h, err := parser.Start(packet)
	if err != nil || h.Response || h.OpCode != 0 || h.Truncated || h.RCode != 0 {
		return nil, false
	}
	q, err := parser.Question()
	if err != nil || q.Class != dnsmessage.ClassINET || q.Type != dnsmessage.TypeA {
		return nil, false
	}
	// Delegate EDNS extensions, DNSSEC, and nonstandard flag combinations unchanged.
	if ar != 0 {
		if err = parser.SkipAllQuestions(); err != nil {
			return nil, false
		}
		if err = parser.SkipAllAnswers(); err != nil {
			return nil, false
		}
		if err = parser.SkipAllAuthorities(); err != nil {
			return nil, false
		}
		opt, err := parser.AdditionalHeader()
		if err != nil || opt.Type != dnsmessage.TypeOPT || opt.TTL != 0 || opt.Length != 0 || opt.Name.String() != "." {
			return nil, false
		}
	}
	name := strings.ToLower(q.Name.String())
	if name != zoneName && !strings.HasSuffix(name, "."+zoneName) {
		return nil, false
	}
	if !safeOwner(name) {
		return nil, false
	}
	a.RLock()
	index := a.data
	if index == nil || !index.complete {
		a.RUnlock()
		return nil, false
	}
	rr := index.addresses[name]
	if len(rr) == 0 && index.special[name] {
		a.RUnlock()
		return nil, false
	}
	soa, soaHead := index.soa, index.soaHeader
	a.RUnlock()
	out := dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true, RecursionDesired: h.RecursionDesired}
	if len(rr) == 0 {
		out.RCode = dnsmessage.RCodeNameError
	}
	b := dnsmessage.NewBuilder(buf[:0], out)
	b.EnableCompression()
	if b.StartQuestions() != nil || b.Question(q) != nil {
		return nil, false
	}
	if len(rr) > 0 {
		if b.StartAnswers() != nil {
			return nil, false
		}
		for _, r := range rr {
			if b.AResource(dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: r.ttl}, dnsmessage.AResource{A: r.ip}) != nil {
				return nil, false
			}
		}
	} else {
		if b.StartAuthorities() != nil || b.SOAResource(soaHead, soa) != nil {
			return nil, false
		}
	}
	if ar != 0 {
		if b.StartAdditionals() != nil || b.OPTResource(dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName("."), Class: 1232}, dnsmessage.OPTResource{}) != nil {
			return nil, false
		}
	}
	answer, err := b.Finish()
	if err != nil || len(answer) > 512 {
		return nil, false
	}
	return answer, true
}

func (a *authority) ensureCurrent()error{
	a.reloadMu.Lock();defer a.reloadMu.Unlock()
	if !a.dirty{return nil}
	if err:=reloadZone();err!=nil{return err}
	a.dirty=false
	return nil
}

func (a *authority) forwardTCP(dst,src net.Conn){
	defer dst.(*net.TCPConn).CloseWrite()
	var prefix [2]byte
	payload:=make([]byte,65535)
	for {
		if _,err:=io.ReadFull(src,prefix[:]);err!=nil{return}
		n:=int(binary.BigEndian.Uint16(prefix[:]))
		if _,err:=io.ReadFull(src,payload[:n]);err!=nil{return}
		if err:=a.ensureCurrent();err!=nil{return}
		if _,err:=dst.Write(prefix[:]);err!=nil{return}
		if _,err:=dst.Write(payload[:n]);err!=nil{return}
	}
}

func (a *authority) relay(packet []byte,local bool) ([]byte, error) {
 if err:=a.ensureCurrent();err!=nil{return nil,err}
	ip:=a.localAddr;if local{ip=net.IPv4(127,0,0,1)}
 dial := net.Dialer{Timeout: time.Second, LocalAddr: &net.UDPAddr{IP: ip}}
	conn, err := dial.Dial("udp4", a.backend)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write(packet); err != nil {
		return nil, err
	}
	buf := make([]byte, 65535)
	n, err := conn.Read(buf)
	return buf[:n], err
}

func (a *authority) listen(addr string) error {
	udpAddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return err
	}
	sock, err := net.ListenUDP("udp4", udpAddr)
	if err != nil {
		return err
	}
	tcp, err := net.Listen("tcp4", addr)
	if err != nil {
		sock.Close()
		return err
	}
	go func() {
		for {
			c, err := tcp.Accept()
			if err != nil {
				log.Fatal(err)
			}
			go func(client net.Conn) {
				defer client.Close()
				ip:=a.localAddr;if client.RemoteAddr().(*net.TCPAddr).IP.IsLoopback(){ip=net.IPv4(127,0,0,1)}
 dial := net.Dialer{Timeout: 2 * time.Second, LocalAddr: &net.TCPAddr{IP: ip}}
				upstream, err := dial.Dial("tcp4", a.backend)
				if err != nil {
					return
				}
				defer upstream.Close()
				client.SetDeadline(time.Now().Add(2 * time.Minute))
				upstream.SetDeadline(time.Now().Add(2 * time.Minute))
				go a.forwardTCP(upstream,client)
				io.Copy(client, upstream)
			}(c)
		}
	}()
	for i := 0; i < 2; i++ {
		go func() {
			in := make([]byte, 65535)
			out := make([]byte, 0, 512)
			for {
				n, remote, err := sock.ReadFromUDPAddrPort(in)
				if err != nil {
					log.Fatal(err)
				}
				if reply, ok := a.cached(in[:n], out); ok {
					sock.WriteToUDPAddrPort(reply, remote)
					continue
				}
				if reply, err := a.relay(in[:n],remote.Addr().IsLoopback()); err == nil {
					sock.WriteToUDPAddrPort(reply, remote)
				}
			}
		}()
	}
	return nil
}

func startDNS() error {
	addr := os.Getenv("ZONE_DNS_LISTEN")
	if addr == "" {
		return nil
	}
	backend := os.Getenv("ZONE_DNS_BACKEND")
	if backend == "" {
		backend = "127.0.0.1:53"
	}
	ip := net.ParseIP(os.Getenv("ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS"))
	a := &authority{backend: backend, localAddr: ip}
	if err := a.load(); err != nil {
		return err
	}
	if err := a.listen(addr); err != nil {
		return err
	}
	fastDNS = a
	go func(){
		ticker:=time.NewTicker(200*time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {if err:=a.ensureCurrent();err!=nil{log.Print(err)}}
	}()
	return nil
}
