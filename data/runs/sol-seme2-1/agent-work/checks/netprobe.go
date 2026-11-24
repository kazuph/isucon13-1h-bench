package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"syscall"
	"time"
)

func main() {
	f, e := os.OpenFile("/home/isucon/netprobe.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if e != nil {
		panic(e)
	}
	defer f.Close()
	boot, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	fmt.Fprintf(f, "BOOT %s %s\n", time.Now().Format(time.RFC3339Nano), boot)
	iface, e := net.InterfaceByName("eth0")
	if e != nil {
		panic(e)
	}
	fd, e := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int((3 << 8)))
	if e != nil {
		panic(e)
	}
	defer syscall.Close(fd)
	if e = syscall.Bind(fd, &syscall.SockaddrLinklayer{Protocol: 3 << 8, Ifindex: iface.Index}); e != nil {
		panic(e)
	}
	b := make([]byte, 65536)
	count := 0
	for count < 300 {
		n, sa, e := syscall.Recvfrom(fd, b, 0)
		if e != nil {
			continue
		}
		if n < 42 {
			continue
		}
		direction := "in"
		if sa.(*syscall.SockaddrLinklayer).Pkttype == syscall.PACKET_OTHERHOST {
			direction = "otherhost"
		}
		if sa.(*syscall.SockaddrLinklayer).Pkttype == syscall.PACKET_OUTGOING {
			direction = "out"
		}
		proto := binary.BigEndian.Uint16(b[12:14])
		if proto == 0x0806 {
			src, dst := net.IP(b[28:32]).String(), net.IP(b[38:42]).String()
			if src == "10.99.0.200" || dst == "10.99.0.200" {
				fmt.Fprintf(f, "%s ARP %s %s -> %s op=%d\n", time.Now().Format(time.RFC3339Nano), direction, src, dst, binary.BigEndian.Uint16(b[20:22]))
				f.Sync()
			}
			continue
		}
		if proto != 0x0800 || b[23] != 17 {
			continue
		}
		o := 14 + int(b[14]&15)*4
		if n < o+20 {
			continue
		}
		src, dst := net.IP(b[26:30]).String(), net.IP(b[30:34]).String()
		if src != "10.99.0.200" && dst != "10.99.0.200" {
			continue
		}
		sp, dp := binary.BigEndian.Uint16(b[o:o+2]), binary.BigEndian.Uint16(b[o+2:o+4])
		if sp != 53 && dp != 53 {
			continue
		}
		count++
		end := o + 52
		if end > n {
			end = n
		}
		fmt.Fprintf(f, "%s UDP %s %s:%d -> %s:%d dns=%x\n", time.Now().Format(time.RFC3339Nano), direction+fmt.Sprintf(" dstmac=%x srcmac=%x", b[:6], b[6:12]), src, sp, dst, dp, b[o+8:end])
	}
}
