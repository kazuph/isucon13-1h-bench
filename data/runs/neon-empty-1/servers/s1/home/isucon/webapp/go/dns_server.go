package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/jmoiron/sqlx"
)

const (
	dnsZoneDomain = "u.isucon.dev"
	dnsDefaultTTL = 3600
)

var (
	dnsMu         sync.RWMutex
	dnsRecords    = make(map[string]struct{})
	dnsIPs        [][4]byte
	dnsPipeIP     [4]byte
	dnsHasPipeIP  bool
	dnsShard0IP   [4]byte
	dnsShard1IP   [4]byte
	dnsSharded    bool
	dnsRRIdx      uint64
	dnsSOAWire    []byte
	dnsNSWire     []byte
)

func initDNSIPs(addrsCSV string) error {
	if pipeStr := os.Getenv("ISUCON13_DNS_PIPE_IP"); pipeStr != "" {
		if ip := net.ParseIP(strings.TrimSpace(pipeStr)).To4(); ip != nil {
			copy(dnsPipeIP[:], ip)
			dnsHasPipeIP = true
		}
	}
	if s0Str := os.Getenv("ISUCON13_DNS_SHARD0_IP"); s0Str != "" {
		if s1Str := os.Getenv("ISUCON13_DNS_SHARD1_IP"); s1Str != "" {
			ip0 := net.ParseIP(strings.TrimSpace(s0Str)).To4()
			ip1 := net.ParseIP(strings.TrimSpace(s1Str)).To4()
			if ip0 != nil && ip1 != nil {
				copy(dnsShard0IP[:], ip0)
				copy(dnsShard1IP[:], ip1)
				dnsSharded = true
			}
		}
	}
	parts := strings.Split(addrsCSV, ",")
	var ips [][4]byte
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		ip := net.ParseIP(p).To4()
		if ip == nil {
			return fmt.Errorf("invalid IPv4 address for DNS: %q", p)
		}
		var b [4]byte
		copy(b[:], ip)
		ips = append(ips, b)
	}
	if len(ips) == 0 {
		return fmt.Errorf("no valid IPv4 addresses provided for DNS")
	}
	dnsIPs = ips
	dnsNSWire = encodeDNSName("ns1.u.isucon.dev")
	// SOA RDATA: MNAME ns1.u.isucon.dev, RNAME hostmaster.u.isucon.dev, serial=0, refresh=10800, retry=3600, expire=604800, min=3600
	var soa []byte
	soa = append(soa, encodeDNSName("ns1.u.isucon.dev")...)
	soa = append(soa, encodeDNSName("hostmaster.u.isucon.dev")...)
	var nums [20]byte
	binary.BigEndian.PutUint32(nums[0:4], 0)
	binary.BigEndian.PutUint32(nums[4:8], 10800)
	binary.BigEndian.PutUint32(nums[8:12], 3600)
	binary.BigEndian.PutUint32(nums[12:16], 604800)
	binary.BigEndian.PutUint32(nums[16:20], 3600)
	soa = append(soa, nums[:]...)
	dnsSOAWire = soa
	return nil
}

func encodeDNSName(name string) []byte {
	var out []byte
	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	out = append(out, 0)
	return out
}

func loadDNSFromZoneAndDB(ctx context.Context, zonePath string, db *sqlx.DB) error {
	f, err := os.Open(zonePath)
	if err != nil {
		return fmt.Errorf("open zone file: %w", err)
	}
	defer f.Close()

	newRecs := make(map[string]struct{}, 4096)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "$") {
			continue
		}
		fields := strings.Fields(line)
		// e.g. "pipe 0 IN A <ISUCON_SUBDOMAIN_ADDRESS>"
		if len(fields) >= 5 && strings.EqualFold(fields[2], "IN") && strings.EqualFold(fields[3], "A") {
			sub := strings.ToLower(fields[0])
			newRecs[sub] = struct{}{}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan zone file: %w", err)
	}

	var names []string
	if err := db.SelectContext(ctx, &names, "SELECT name FROM users"); err != nil {
		return fmt.Errorf("select users for dns: %w", err)
	}
	for _, n := range names {
		newRecs[strings.ToLower(n)] = struct{}{}
	}
	newRecs["@"] = struct{}{}
	newRecs["ns1"] = struct{}{}
	newRecs["pipe"] = struct{}{}

	dnsMu.Lock()
	dnsRecords = newRecs
	dnsMu.Unlock()
	return nil
}

func dnsAddRecord(name string) {
	key := strings.ToLower(name)
	dnsMu.Lock()
	dnsRecords[key] = struct{}{}
	dnsMu.Unlock()
}

func dnsHasRecord(sub string) bool {
	dnsMu.RLock()
	_, ok := dnsRecords[sub]
	dnsMu.RUnlock()
	return ok
}

func dnsPickIP(sub string) [4]byte {
	if dnsHasPipeIP && (sub == "pipe" || sub == "@" || sub == "ns1") {
		return dnsPipeIP
	}
	if dnsSharded {
		if shardForUsername(sub) == 0 {
			return dnsShard0IP
		}
		return dnsShard1IP
	}
	if len(dnsIPs) == 1 {
		return dnsIPs[0]
	}
	idx := atomic.AddUint64(&dnsRRIdx, 1) % uint64(len(dnsIPs))
	return dnsIPs[idx]
}

func startDNSServer(listenAddr string) error {
	udpAddr, err := net.ResolveUDPAddr("udp4", listenAddr)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp4", udpAddr)
	if err != nil {
		return err
	}
	_ = conn.SetReadBuffer(4 * 1024 * 1024)
	_ = conn.SetWriteBuffer(4 * 1024 * 1024)

	const numWorkers = 4
	for i := 0; i < numWorkers; i++ {
		go dnsWorker(conn)
	}
	log.Printf("DNS server listening on %s (IPs: %d)", listenAddr, len(dnsIPs))
	return nil
}

func dnsWorker(conn *net.UDPConn) {
	req := make([]byte, 1500)
	resp := make([]byte, 512)
	var nameBuf [256]byte

	for {
		n, remote, err := conn.ReadFromUDP(req)
		if err != nil {
			continue
		}
		if n < 12 {
			continue
		}

		// Parse header
		qdCount := binary.BigEndian.Uint16(req[4:6])
		if qdCount == 0 {
			continue
		}
		reqFlags := binary.BigEndian.Uint16(req[2:4])
		// Only handle standard queries (QR=0, Opcode=0)
		if (reqFlags & 0xF800) != 0 {
			continue
		}

		// Parse QNAME (lowercase into nameBuf)
		pos := 12
		nameLen := 0
		valid := true
		for pos < n {
			l := int(req[pos])
			pos++
			if l == 0 {
				break
			}
			if (l&0xC0) != 0 || pos+l > n || nameLen+l+1 >= len(nameBuf) {
				valid = false
				break
			}
			if nameLen > 0 {
				nameBuf[nameLen] = '.'
				nameLen++
			}
			for j := 0; j < l; j++ {
				ch := req[pos+j]
				if ch >= 'A' && ch <= 'Z' {
					ch += 'a' - 'A'
				}
				nameBuf[nameLen] = ch
				nameLen++
			}
			pos += l
		}
		if !valid || pos+4 > n {
			continue
		}

		qType := binary.BigEndian.Uint16(req[pos : pos+2])
		qClass := binary.BigEndian.Uint16(req[pos+2 : pos+4])
		qEnd := pos + 4
		if qEnd > 400 {
			continue
		}

		qname := string(nameBuf[:nameLen])

		// Determine subdomain relative to u.isucon.dev
		var sub string
		inZone := false
		if qname == dnsZoneDomain {
			sub = "@"
			inZone = true
		} else if strings.HasSuffix(qname, "."+dnsZoneDomain) {
			prefix := qname[:len(qname)-len(dnsZoneDomain)-1]
			if prefix != "" && !strings.ContainsRune(prefix, '.') {
				sub = prefix
				inZone = true
			} else {
				// Multi-level subdomain under u.isucon.dev -> NXDOMAIN
				inZone = true
				sub = ""
			}
		}

		// Build response header
		resp[0] = req[0]
		resp[1] = req[1]
		// QR=1 (0x8000), AA=1 (0x0400), preserve RD (0x0100)
		flags := uint16(0x8400) | (reqFlags & 0x0100)
		binary.BigEndian.PutUint16(resp[4:6], 1) // QDCOUNT = 1
		binary.BigEndian.PutUint16(resp[8:10], 0)
		binary.BigEndian.PutUint16(resp[10:12], 0)

		// Copy Question section verbatim
		copy(resp[12:qEnd], req[12:qEnd])
		outPos := qEnd
		var anCount uint16

		if !inZone {
			flags |= 5 // REFUSED
		} else if qClass != 1 && qClass != 255 {
			flags |= 3 // NXDOMAIN
		} else if sub == "" || !dnsHasRecord(sub) {
			dnsNXCnt.Add(1)
			flags |= 3 // NXDOMAIN
		} else {
			dnsHitCnt.Add(1)
			// Name exists!
			switch qType {
			case 1, 255: // A or ANY
				ip := dnsPickIP(sub)
				// Name pointer to offset 12 (0xC00C)
				resp[outPos] = 0xC0
				resp[outPos+1] = 0x0C
				binary.BigEndian.PutUint16(resp[outPos+2:outPos+4], 1) // TYPE A
				binary.BigEndian.PutUint16(resp[outPos+4:outPos+6], 1) // CLASS IN
				binary.BigEndian.PutUint32(resp[outPos+6:outPos+10], dnsDefaultTTL)
				binary.BigEndian.PutUint16(resp[outPos+10:outPos+12], 4) // RDLENGTH 4
				copy(resp[outPos+12:outPos+16], ip[:])
				outPos += 16
				anCount = 1
			case 2: // NS
				if sub == "@" && outPos+12+len(dnsNSWire) <= len(resp) {
					resp[outPos] = 0xC0
					resp[outPos+1] = 0x0C
					binary.BigEndian.PutUint16(resp[outPos+2:outPos+4], 2)
					binary.BigEndian.PutUint16(resp[outPos+4:outPos+6], 1)
					binary.BigEndian.PutUint32(resp[outPos+6:outPos+10], dnsDefaultTTL)
					binary.BigEndian.PutUint16(resp[outPos+10:outPos+12], uint16(len(dnsNSWire)))
					copy(resp[outPos+12:], dnsNSWire)
					outPos += 12 + len(dnsNSWire)
					anCount = 1
				}
			case 6: // SOA
				if sub == "@" && outPos+12+len(dnsSOAWire) <= len(resp) {
					resp[outPos] = 0xC0
					resp[outPos+1] = 0x0C
					binary.BigEndian.PutUint16(resp[outPos+2:outPos+4], 6)
					binary.BigEndian.PutUint16(resp[outPos+4:outPos+6], 1)
					binary.BigEndian.PutUint32(resp[outPos+6:outPos+10], 3600)
					binary.BigEndian.PutUint16(resp[outPos+10:outPos+12], uint16(len(dnsSOAWire)))
					copy(resp[outPos+12:], dnsSOAWire)
					outPos += 12 + len(dnsSOAWire)
					anCount = 1
				}
			default:
				// e.g. AAAA -> NOERROR, ANCOUNT=0 (NODATA)
			}
		}

		binary.BigEndian.PutUint16(resp[2:4], flags)
		binary.BigEndian.PutUint16(resp[6:8], anCount)
		_, _ = conn.WriteToUDP(resp[:outPos], remote)
	}
}
