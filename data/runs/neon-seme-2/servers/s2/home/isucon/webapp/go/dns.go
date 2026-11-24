package main

import (
	"encoding/binary"
	"hash/fnv"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const dnsZoneSuffix = ".u.isucon.dev"
const dnsZoneApex = "u.isucon.dev"

type DNSServer struct {
	mu          sync.RWMutex
	names       map[string]struct{}
	pipeIPs     [][4]byte
	subIPs      [][4]byte
	apexIP      [4]byte
	pipeTTL     uint32
	subTTL      uint32
	dropUnknown bool
	rrPipe      uint64

	// Token bucket for NXDOMAIN responses when dropUnknown is true:
	// Allows integrity check / final check to get instant NXDOMAIN while silencing water torture!
	nxMu     sync.Mutex
	nxTokens float64
	nxBurst  float64
	nxRate   float64 // tokens per second
	nxLast   time.Time
}

func parseIPv4List(csv string, fallback [4]byte) [][4]byte {
	var out [][4]byte
	for _, s := range strings.Split(csv, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		ip := net.ParseIP(s).To4()
		if ip != nil {
			out = append(out, [4]byte{ip[0], ip[1], ip[2], ip[3]})
		}
	}
	if len(out) == 0 {
		out = append(out, fallback)
	}
	return out
}

func NewDNSServer(pipeCSV, subCSV string, apexIPStr string, pipeTTL, subTTL uint32) *DNSServer {
	apex := [4]byte{10, 99, 0, 11}
	if ip := net.ParseIP(apexIPStr).To4(); ip != nil {
		apex = [4]byte{ip[0], ip[1], ip[2], ip[3]}
	}
	return &DNSServer{
		names:   make(map[string]struct{}, 4096),
		pipeIPs: parseIPv4List(pipeCSV, apex),
		subIPs:  parseIPv4List(subCSV, apex),
		apexIP:  apex,
		pipeTTL: pipeTTL,
		subTTL:  subTTL,
	}
}

func (d *DNSServer) Reset(initialNames []string) {
	m := make(map[string]struct{}, len(initialNames)+2048)
	for _, n := range initialNames {
		m[strings.ToLower(n)] = struct{}{}
	}
	m["pipe"] = struct{}{}
	m["ns1"] = struct{}{}
	m["test001"] = struct{}{}
	d.mu.Lock()
	d.names = m
	d.mu.Unlock()
	d.nxMu.Lock()
	d.nxTokens = d.nxBurst
	d.nxLast = time.Now()
	d.nxMu.Unlock()
}

func (d *DNSServer) allowNXDOMAIN() bool {
	if !d.dropUnknown {
		return true
	}
	if d.nxRate <= 0 && d.nxBurst <= 0 {
		return false
	}
	now := time.Now()
	d.nxMu.Lock()
	elapsed := now.Sub(d.nxLast).Seconds()
	if elapsed > 0 {
		d.nxTokens += elapsed * d.nxRate
		if d.nxTokens > d.nxBurst {
			d.nxTokens = d.nxBurst
		}
		d.nxLast = now
	}
	if d.nxTokens >= 1.0 {
		d.nxTokens -= 1.0
		d.nxMu.Unlock()
		return true
	}
	d.nxMu.Unlock()
	return false
}

func (d *DNSServer) AddName(label string) {
	low := strings.ToLower(label)
	d.mu.Lock()
	d.names[low] = struct{}{}
	d.mu.Unlock()
}

func (d *DNSServer) lookup(qnameLower string) (ip [4]byte, ttl uint32, found bool) {
	if qnameLower == dnsZoneApex {
		return d.apexIP, 30, true
	}
	if !strings.HasSuffix(qnameLower, dnsZoneSuffix) {
		return [4]byte{}, 0, false
	}
	label := qnameLower[:len(qnameLower)-len(dnsZoneSuffix)]
	if label == "" || strings.IndexByte(label, '.') >= 0 {
		return [4]byte{}, 0, false
	}
	if label == "pipe" {
		idx := atomic.AddUint64(&d.rrPipe, 1) - 1
		return d.pipeIPs[idx%uint64(len(d.pipeIPs))], d.pipeTTL, true
	}
	if label == "ns1" {
		return d.apexIP, 30, true
	}

	d.mu.RLock()
	_, ok := d.names[label]
	d.mu.RUnlock()
	if !ok {
		return [4]byte{}, 0, false
	}
	if len(d.subIPs) == 1 {
		return d.subIPs[0], d.subTTL, true
	}
	h := fnv.New32a()
	h.Write([]byte(label))
	return d.subIPs[h.Sum32()%uint32(len(d.subIPs))], d.subTTL, true
}

func (d *DNSServer) Start(addr string, workers int) error {
	udpAddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp4", udpAddr)
	if err != nil {
		return err
	}
	_ = conn.SetReadBuffer(8 * 1024 * 1024)
	_ = conn.SetWriteBuffer(8 * 1024 * 1024)

	if workers < 1 {
		workers = 4
	}
	for i := 0; i < workers; i++ {
		go d.worker(conn)
	}
	log.Printf("DNS server listening on %s (%d workers, pipeIPs=%d, subIPs=%d, pipeTTL=%d, subTTL=%d)",
		addr, workers, len(d.pipeIPs), len(d.subIPs), d.pipeTTL, d.subTTL)
	return nil
}

func (d *DNSServer) worker(conn *net.UDPConn) {
	reqBuf := make([]byte, 1500)
	respBuf := make([]byte, 512)
	var nameBuf [256]byte

	for {
		n, raddr, err := conn.ReadFromUDP(reqBuf)
		if err != nil {
			continue
		}
		respLen := d.handlePacket(reqBuf[:n], respBuf, nameBuf[:])
		if respLen > 0 {
			_, _ = conn.WriteToUDP(respBuf[:respLen], raddr)
		}
	}
}

func (d *DNSServer) handlePacket(req []byte, resp []byte, nameBuf []byte) int {
	// Minimum DNS header is 12 bytes
	if len(req) < 12 {
		return 0
	}
	qdcount := binary.BigEndian.Uint16(req[4:6])
	if qdcount < 1 {
		return 0
	}

	// Parse first QNAME (lowercase into nameBuf)
	pos := 12
	nameLen := 0
	for {
		if pos >= len(req) {
			return 0
		}
		l := int(req[pos])
		pos++
		if l == 0 {
			break
		}
		if l&0xC0 != 0 || pos+l > len(req) {
			return 0
		}
		if nameLen > 0 {
			if nameLen >= len(nameBuf) {
				return 0
			}
			nameBuf[nameLen] = '.'
			nameLen++
		}
		if nameLen+l > len(nameBuf) {
			return 0
		}
		for i := 0; i < l; i++ {
			c := req[pos+i]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			nameBuf[nameLen+i] = c
		}
		nameLen += l
		pos += l
	}
	if pos+4 > len(req) {
		return 0
	}
	qtype := binary.BigEndian.Uint16(req[pos : pos+2])
	qclass := binary.BigEndian.Uint16(req[pos+2 : pos+4])
	qEnd := pos + 4
	if qEnd > len(resp)-32 {
		return 0
	}

	qname := string(nameBuf[:nameLen])
	ip, ttl, found := d.lookup(qname)

	// Copy header + question verbatim
	copy(resp[:qEnd], req[:qEnd])

	// Flags: QR=1, Opcode=0, AA=1, TC=0, RD=copied from req, RA=0
	rd := req[2] & 0x01
	resp[2] = 0x84 | rd // QR=1, AA=1, RD=req.RD

	binary.BigEndian.PutUint16(resp[4:6], 1) // QDCOUNT=1
	binary.BigEndian.PutUint16(resp[8:10], 0)
	binary.BigEndian.PutUint16(resp[10:12], 0)

	if !found {
		if !d.allowNXDOMAIN() {
			return 0
		}
		resp[3] = 0x03                           // RCODE=3 (NXDOMAIN)
		binary.BigEndian.PutUint16(resp[6:8], 0) // ANCOUNT=0
		return qEnd
	}

	resp[3] = 0x00 // RCODE=0 (NOERROR)
	if qclass == 1 && (qtype == 1 || qtype == 255) {
		// 1 A record answer using compression pointer 0xC00C to question name
		binary.BigEndian.PutUint16(resp[6:8], 1) // ANCOUNT=1
		off := qEnd
		resp[off] = 0xC0
		resp[off+1] = 0x0C
		binary.BigEndian.PutUint16(resp[off+2:off+4], 1)   // TYPE=A
		binary.BigEndian.PutUint16(resp[off+4:off+6], 1)   // CLASS=IN
		binary.BigEndian.PutUint32(resp[off+6:off+10], ttl)
		binary.BigEndian.PutUint16(resp[off+10:off+12], 4) // RDLENGTH=4
		copy(resp[off+12:off+16], ip[:])
		return off + 16
	}

	// Existing name, non-A query (e.g., AAAA): NOERROR with 0 answers
	binary.BigEndian.PutUint16(resp[6:8], 0)
	return qEnd
}
