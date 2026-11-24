package main

import (
	"context"
	"encoding/binary"
	"log"
	"net"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const zoneSuffix = "u.isucon.dev"

var dnsTTL uint32 = 3600

var dnsIPs [][4]byte
var subdomainIP [4]byte

func dnsIPFor(name string) [4]byte {
	if len(dnsIPs) == 0 {
		return subdomainIP
	}
	var h uint32 = 2166136261
	for i := 0; i < len(name); i++ {
		h ^= uint32(name[i])
		h *= 16777619
	}
	return dnsIPs[h%uint32(len(dnsIPs))]
}

func startDNS(addr string) {
	lc := net.ListenConfig{Control: func(network, address string, c syscall.RawConn) error {
		var serr error
		c.Control(func(fd uintptr) {
			serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
			unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
			unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUF, 8<<20)
		})
		return serr
	}}
	n := runtime.NumCPU()
	for i := 0; i < n; i++ {
		pc, err := lc.ListenPacket(context.Background(), "udp4", addr)
		if err != nil {
			log.Printf("dns listen: %v", err)
			return
		}
		go serveDNS(pc.(*net.UDPConn))
	}
	log.Printf("dns listening on %s x%d", addr, n)
}

func serveDNS(conn *net.UDPConn) {
	buf := make([]byte, 1500)
	out := make([]byte, 0, 512)
	for {
		n, raddr, err := conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			continue
		}
		resp := handleDNS(buf[:n], out[:0])
		if resp != nil {
			conn.WriteToUDPAddrPort(resp, raddr)
		}
	}
}

// handleDNS builds a response into out.
func handleDNS(q []byte, out []byte) []byte {
	if len(q) < 12 {
		return nil
	}
	flags := binary.BigEndian.Uint16(q[2:4])
	if flags&0x8000 != 0 { // response
		return nil
	}
	qd := binary.BigEndian.Uint16(q[4:6])
	opcode := (flags >> 11) & 0xf
	rd := flags & 0x0100
	if qd != 1 || opcode != 0 {
		out = append(out, q[0], q[1])
		out = binary.BigEndian.AppendUint16(out, 0x8000|(opcode<<11)|rd|4) // NOTIMP
		out = append(out, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
		return out
	}
	// parse qname
	i := 12
	var sb [256]byte
	nameLen := 0
	for {
		if i >= len(q) {
			return nil
		}
		l := int(q[i])
		if l == 0 {
			i++
			break
		}
		if l&0xc0 != 0 || i+1+l > len(q) || nameLen+l+1 > 255 {
			return nil
		}
		if nameLen > 0 {
			sb[nameLen] = '.'
			nameLen++
		}
		for k := 0; k < l; k++ {
			c := q[i+1+k]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			sb[nameLen] = c
			nameLen++
		}
		i += 1 + l
	}
	if i+4 > len(q) {
		return nil
	}
	qtype := binary.BigEndian.Uint16(q[i : i+2])
	qend := i + 4
	name := string(sb[:nameLen])

	rcode := uint16(0)
	var label string
	inZone := false
	found := false
	if name == zoneSuffix {
		inZone, found = true, true
		label = ""
	} else if strings.HasSuffix(name, "."+zoneSuffix) {
		inZone = true
		label = name[:len(name)-len(zoneSuffix)-1]
		_, found = dnsNames.Load(label)
	}
	if !inZone {
		rcode = 5 // REFUSED
	} else if !found {
		rcode = 3 // NXDOMAIN
	}
	answer := found && (qtype == 1 || qtype == 255)

	out = append(out, q[0], q[1])
	f := uint16(0x8000) | (opcode << 11) | rd | rcode
	if inZone {
		f |= 0x0400 // AA
	}
	out = binary.BigEndian.AppendUint16(out, f)
	out = append(out, 0, 1) // qd
	if answer {
		out = append(out, 0, 1)
	} else {
		out = append(out, 0, 0)
	}
	if inZone && !answer {
		out = append(out, 0, 1) // ns (SOA)
	} else {
		out = append(out, 0, 0)
	}
	out = append(out, 0, 0)
	out = append(out, q[12:qend]...)
	if answer {
		ip := dnsIPFor(label)
		out = append(out, 0xc0, 0x0c, 0, 1, 0, 1)
		out = binary.BigEndian.AppendUint32(out, dnsTTL)
		out = append(out, 0, 4, ip[0], ip[1], ip[2], ip[3])
	} else if inZone {
		// SOA for zone apex
		out = append(out, soaRR...)
	}
	return out
}

var soaRR = func() []byte {
	var b []byte
	appendName := func(b []byte, n string) []byte {
		for _, p := range strings.Split(n, ".") {
			b = append(b, byte(len(p)))
			b = append(b, p...)
		}
		return append(b, 0)
	}
	b = appendName(b, zoneSuffix)
	b = append(b, 0, 6, 0, 1)
	b = binary.BigEndian.AppendUint32(b, 3600)
	var rd []byte
	rd = appendName(rd, "ns1."+zoneSuffix)
	rd = appendName(rd, "hostmaster."+zoneSuffix)
	rd = binary.BigEndian.AppendUint32(rd, 0)
	rd = binary.BigEndian.AppendUint32(rd, 10800)
	rd = binary.BigEndian.AppendUint32(rd, 3600)
	rd = binary.BigEndian.AppendUint32(rd, 604800)
	rd = binary.BigEndian.AppendUint32(rd, 3600)
	b = binary.BigEndian.AppendUint16(b, uint16(len(rd)))
	return append(b, rd...)
}()
