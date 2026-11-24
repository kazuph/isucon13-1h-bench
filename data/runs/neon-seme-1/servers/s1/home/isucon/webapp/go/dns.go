package main

import (
	"encoding/binary"
	"log"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
)

type DNSServer struct {
	mu      sync.RWMutex
	domains map[string]struct{}

	apexIP  [4]byte
	pipeIPs [][4]byte
	subIPs  [][4]byte
	pipeTTL uint32
	subTTL  uint32
	rr      uint64
}

func parseIPList(s string, fallback [4]byte) [][4]byte {
	var res [][4]byte
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		ip := net.ParseIP(part).To4()
		if ip != nil {
			var b [4]byte
			copy(b[:], ip)
			res = append(res, b)
		}
	}
	if len(res) == 0 {
		res = append(res, fallback)
	}
	return res
}

func NewDNSServer(apexIPStr, pipeIPsStr, subIPsStr string, pipeTTL, subTTL uint32) *DNSServer {
	var apex [4]byte
	if ip := net.ParseIP(strings.TrimSpace(apexIPStr)).To4(); ip != nil {
		copy(apex[:], ip)
	} else {
		apex = [4]byte{10, 99, 0, 11}
	}
	return &DNSServer{
		domains: make(map[string]struct{}, 4096),
		apexIP:  apex,
		pipeIPs: parseIPList(pipeIPsStr, apex),
		subIPs:  parseIPList(subIPsStr, apex),
		pipeTTL: pipeTTL,
		subTTL:  subTTL,
	}
}

func (d *DNSServer) Reset(names []string) {
	m := make(map[string]struct{}, len(names)+1024)
	for _, n := range names {
		m[strings.ToLower(n)] = struct{}{}
	}
	d.mu.Lock()
	d.domains = m
	d.mu.Unlock()
}

func (d *DNSServer) Add(name string) {
	lower := strings.ToLower(name)
	d.mu.Lock()
	d.domains[lower] = struct{}{}
	d.mu.Unlock()
}

func (d *DNSServer) Has(lower string) bool {
	d.mu.RLock()
	_, ok := d.domains[lower]
	d.mu.RUnlock()
	return ok
}

func fnv32a(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

func (d *DNSServer) Start(addr string) (*net.UDPConn, error) {
	udpAddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp4", udpAddr)
	if err != nil {
		return nil, err
	}
	_ = conn.SetReadBuffer(16 * 1024 * 1024)
	_ = conn.SetWriteBuffer(16 * 1024 * 1024)

	workers := runtime.NumCPU() * 2
	if workers < 4 {
		workers = 4
	}
	for i := 0; i < workers; i++ {
		go d.serveLoop(conn)
	}
	log.Printf("DNS server listening on %s (workers=%d, pipeIPs=%d, subIPs=%d, pipeTTL=%d, subTTL=%d)",
		addr, workers, len(d.pipeIPs), len(d.subIPs), d.pipeTTL, d.subTTL)
	return conn, nil
}

func (d *DNSServer) serveLoop(conn *net.UDPConn) {
	var reqBuf [512]byte
	var respBuf [512]byte
	for {
		n, remote, err := conn.ReadFromUDP(reqBuf[:])
		if err != nil {
			if strings.Contains(err.Error(), "closed") {
				return
			}
			continue
		}
		rn := d.handlePacket(reqBuf[:n], respBuf[:])
		if rn > 0 {
			_, _ = conn.WriteToUDP(respBuf[:rn], remote)
		}
	}
}

// handlePacket parses a DNS query and writes the response into resp.
// Returns response length, or 0 to drop malformed packets.
func (d *DNSServer) handlePacket(req, resp []byte) int {
	if len(req) < 12 {
		return 0
	}
	qdCount := binary.BigEndian.Uint16(req[4:6])
	if qdCount == 0 {
		return 0
	}

	// Parse first QNAME starting at offset 12
	var labels [8]string
	var lowerBuf [256]byte
	lowerPos := 0
	labelCount := 0
	pos := 12
	for pos < len(req) {
		l := int(req[pos])
		pos++
		if l == 0 {
			break
		}
		if l&0xC0 != 0 || pos+l > len(req) || lowerPos+l > len(lowerBuf) {
			return 0
		}
		start := lowerPos
		for i := 0; i < l; i++ {
			c := req[pos+i]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			lowerBuf[lowerPos] = c
			lowerPos++
		}
		if labelCount < len(labels) {
			labels[labelCount] = string(lowerBuf[start:lowerPos])
		}
		labelCount++
		pos += l
	}
	if pos+4 > len(req) {
		return 0
	}
	qtype := binary.BigEndian.Uint16(req[pos : pos+2])
	qclass := binary.BigEndian.Uint16(req[pos+2 : pos+4])
	qEnd := pos + 4

	// Prepare response header + copied question
	copy(resp[:qEnd], req[:qEnd])
	// Flags: QR=1, AA=1, copy RD from request
	rd := req[2] & 0x01
	resp[2] = 0x84 | rd // QR=1 (0x80), AA=1 (0x04), RD copied
	resp[3] = 0x00      // RA=0, RCODE=0 (NOERROR)
	binary.BigEndian.PutUint16(resp[4:6], 1)
	binary.BigEndian.PutUint16(resp[6:8], 0)
	binary.BigEndian.PutUint16(resp[8:10], 0)
	binary.BigEndian.PutUint16(resp[10:12], 0)

	// Check zone suffix: u.isucon.dev (3 labels) or <sub >.u.isucon.dev (4 labels)
	if qclass != 1 && qclass != 255 {
		resp[3] = 3 // NXDOMAIN
		return qEnd
	}

	// Verify suffix "u", "isucon", "dev"
	if labelCount < 3 || labelCount > 4 {
		resp[3] = 3 // NXDOMAIN
		return qEnd
	}
	off := labelCount - 3
	if labels[off] != "u" || labels[off+1] != "isucon" || labels[off+2] != "dev" {
		resp[3] = 3 // NXDOMAIN
		return qEnd
	}

	if labelCount == 3 {
		// Apex: u.isucon.dev
		switch qtype {
		case 1: // A
			return writeARecord(resp, qEnd, d.apexIP, d.pipeTTL)
		case 2: // NS -> ns1.u.isucon.dev
			return writeNSRecord(resp, qEnd, 300)
		case 6: // SOA
			return writeSOARecord(resp, qEnd, 300)
		default:
			return qEnd // NOERROR, 0 answers
		}
	}

	// 4 labels: <sub >.u.isucon.dev
	sub := labels[0]
	switch sub {
	case "pipe":
		if qtype == 1 {
			idx := atomic.AddUint64(&d.rr, 1) % uint64(len(d.pipeIPs))
			return writeARecord(resp, qEnd, d.pipeIPs[idx], d.pipeTTL)
		}
		return qEnd
	case "ns1":
		if qtype == 1 {
			return writeARecord(resp, qEnd, d.apexIP, 300)
		}
		return qEnd
	default:
		if !d.Has(sub) {
			resp[3] = 3 // NXDOMAIN
			return qEnd
		}
		if qtype == 1 {
			idx := fnv32a(sub) % uint32(len(d.subIPs))
			return writeARecord(resp, qEnd, d.subIPs[idx], d.subTTL)
		}
		return qEnd
	}
}

func writeARecord(resp []byte, offset int, ip [4]byte, ttl uint32) int {
	binary.BigEndian.PutUint16(resp[6:8], 1) // ANCOUNT = 1
	// Pointer to QNAME at offset 12 (0xC00C)
	resp[offset] = 0xC0
	resp[offset+1] = 0x0C
	binary.BigEndian.PutUint16(resp[offset+2:offset+4], 1) // TYPE A
	binary.BigEndian.PutUint16(resp[offset+4:offset+6], 1) // CLASS IN
	binary.BigEndian.PutUint32(resp[offset+6:offset+10], ttl)
	binary.BigEndian.PutUint16(resp[offset+10:offset+12], 4) // RDLENGTH
	copy(resp[offset+12:offset+16], ip[:])
	return offset + 16
}

func writeNSRecord(resp []byte, offset int, ttl uint32) int {
	binary.BigEndian.PutUint16(resp[6:8], 1)
	resp[offset] = 0xC0
	resp[offset+1] = 0x0C
	binary.BigEndian.PutUint16(resp[offset+2:offset+4], 2) // TYPE NS
	binary.BigEndian.PutUint16(resp[offset+4:offset+6], 1) // CLASS IN
	binary.BigEndian.PutUint32(resp[offset+6:offset+10], ttl)
	// ns1 + pointer to u.isucon.dev (offset 12)
	rdata := []byte{3, 'n', 's', '1', 0xC0, 0x0C}
	binary.BigEndian.PutUint16(resp[offset+10:offset+12], uint16(len(rdata)))
	copy(resp[offset+12:], rdata)
	return offset + 12 + len(rdata)
}

func writeSOARecord(resp []byte, offset int, ttl uint32) int {
	binary.BigEndian.PutUint16(resp[6:8], 1)
	resp[offset] = 0xC0
	resp[offset+1] = 0x0C
	binary.BigEndian.PutUint16(resp[offset+2:offset+4], 6) // TYPE SOA
	binary.BigEndian.PutUint16(resp[offset+4:offset+6], 1) // CLASS IN
	binary.BigEndian.PutUint32(resp[offset+6:offset+10], ttl)
	pos := offset + 12
	// MNAME: ns1.u.isucon.dev
	copy(resp[pos:], []byte{3, 'n', 's', '1', 0xC0, 0x0C})
	pos += 6
	// RNAME: hostmaster.u.isucon.dev
	copy(resp[pos:], []byte{10, 'h', 'o', 's', 't', 'm', 'a', 's', 't', 'e', 'r', 0xC0, 0x0C})
	pos += 13
	binary.BigEndian.PutUint32(resp[pos:pos+4], 1)      // serial
	binary.BigEndian.PutUint32(resp[pos+4:pos+8], 10800) // refresh
	binary.BigEndian.PutUint32(resp[pos+8:pos+12], 3600) // retry
	binary.BigEndian.PutUint32(resp[pos+12:pos+16], 604800)
	binary.BigEndian.PutUint32(resp[pos+16:pos+20], 3600)
	pos += 20
	binary.BigEndian.PutUint16(resp[offset+10:offset+12], uint16(pos-(offset+12)))
	return pos
}
