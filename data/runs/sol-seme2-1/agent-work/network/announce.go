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
	if len(os.Args) != 2 {
		panic("usage: arp-announce OWN_IPV4")
	}
	ip := net.ParseIP(os.Args[1]).To4()
	if ip == nil {
		panic("invalid IPv4")
	}
	var iface *net.Interface
	for attempt := 0; attempt < 120; attempt++ {
		ifs, e := net.Interfaces()
		if e != nil {
			panic(e)
		}
		for _, i := range ifs {
			addrs, _ := i.Addrs()
			for _, a := range addrs {
				v, _, _ := net.ParseCIDR(a.String())
				if v != nil && v.Equal(ip) && len(i.HardwareAddr) == 6 {
					x := i
					iface = &x
					break
				}
			}
			if iface != nil {
				break
			}
		}
		if iface != nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if iface == nil {
		panic("address is not assigned to this server")
	}
	fd, e := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, 0x0608)
	if e != nil {
		panic(e)
	}
	defer syscall.Close(fd)
	frame := make([]byte, 42)
	for i := 0; i < 6; i++ {
		frame[i] = 255
	}
	copy(frame[6:12], iface.HardwareAddr)
	binary.BigEndian.PutUint16(frame[12:14], 0x0806)
	binary.BigEndian.PutUint16(frame[14:16], 1)
	binary.BigEndian.PutUint16(frame[16:18], 0x0800)
	frame[18] = 6
	frame[19] = 4
	copy(frame[22:28], iface.HardwareAddr)
	copy(frame[28:32], ip)
	copy(frame[38:42], ip)
	dest := &syscall.SockaddrLinklayer{Ifindex: iface.Index, Protocol: 0x0608, Halen: 6}
	for i := 0; i < 6; i++ {
		dest.Addr[i] = 255
	}
	for i := 0; i < 6; i++ {
		binary.BigEndian.PutUint16(frame[20:22], uint16(1+i%2))
		for j := 32; j < 38; j++ {
			if i%2 == 0 {
				frame[j] = 0
			} else {
				frame[j] = 255
			}
		}
		if e := syscall.Sendto(fd, frame, 0, dest); e != nil {
			panic(e)
		}
		time.Sleep(250 * time.Millisecond)
	}
	fmt.Printf("announced own address %s on %s (%s)\n", ip, iface.Name, iface.HardwareAddr)
}
