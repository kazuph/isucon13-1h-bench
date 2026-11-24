// Announce only the server's own assigned IPv4 address after boot (RFC 5227).
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
		panic("assigned IPv4 address required")
	}
	address := os.Args[1]
	if address != "10.99.0.11" && address != "10.99.0.12" && address != "10.99.0.13" {
		panic("not an assigned server address")
	}
	var iface *net.Interface
	var err error
	found := false
	for n := 0; n < 60; n++ {
		iface, err = net.InterfaceByName("eth0")
		if err == nil {
			addresses, e := iface.Addrs()
			if e != nil {
				panic(e)
			}
			for _, a := range addresses {
				ip, _, e := net.ParseCIDR(a.String())
				if e == nil && ip.String() == address {
					found = true
					break
				}
			}
		}
		if found {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !found {
		panic("assigned address is not present")
	}
	mac := iface.HardwareAddr
	if len(mac) != 6 {
		panic("unexpected MAC length")
	}
	ip := net.ParseIP(address).To4()
	packet := make([]byte, 42)
	for i := 0; i < 6; i++ {
		packet[i] = 255
	}
	copy(packet[6:12], mac)
	binary.BigEndian.PutUint16(packet[12:14], 0x0806)
	binary.BigEndian.PutUint16(packet[14:16], 1)
	binary.BigEndian.PutUint16(packet[16:18], 0x0800)
	packet[18] = 6
	packet[19] = 4
	binary.BigEndian.PutUint16(packet[20:22], 1)
	copy(packet[22:28], mac)
	copy(packet[28:32], ip)
	copy(packet[38:42], ip)
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, 0x0608)
	if err != nil {
		panic(err)
	}
	defer syscall.Close(fd)
	dst := &syscall.SockaddrLinklayer{Ifindex: iface.Index, Protocol: 0x0608, Halen: 6}
	for i := 0; i < 6; i++ {
		dst.Addr[i] = 255
	}
	for n := 0; n < 3; n++ {
		if err = syscall.Sendto(fd, packet, 0, dst); err != nil {
			panic(err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	fmt.Println("Announced", address, "with MAC", mac.String())
}
