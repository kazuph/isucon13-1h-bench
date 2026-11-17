package main

import (
	"encoding/binary"
	"log"
	"net"
	"syscall"
	"time"
)

func main() {
	nic, err := net.InterfaceByName("eth0")
	if err != nil {
		log.Fatal(err)
	}
	addrs, err := nic.Addrs()
	if err != nil {
		log.Fatal(err)
	}
	var ip net.IP
	for _, a := range addrs {
		v, _, e := net.ParseCIDR(a.String())
		if e == nil && v.To4() != nil && v.To4()[0] == 10 && v.To4()[1] == 99 && v.To4()[2] == 0 {
			ip = v.To4()
			break
		}
	}
	if len(ip) != 4 || len(nic.HardwareAddr) != 6 {
		log.Fatal("missing IPv4 address or hardware address")
	}
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, 0x0608)
	if err != nil {
		log.Fatal(err)
	}
	defer syscall.Close(fd)
	frame := make([]byte, 42)
	for i := 0; i < 6; i++ {
		frame[i] = 255
	}
	copy(frame[6:12], nic.HardwareAddr)
	binary.BigEndian.PutUint16(frame[12:14], 0x0806)
	binary.BigEndian.PutUint16(frame[14:16], 1)
	binary.BigEndian.PutUint16(frame[16:18], 0x0800)
	frame[18] = 6
	frame[19] = 4
	binary.BigEndian.PutUint16(frame[20:22], 1)
	copy(frame[22:28], nic.HardwareAddr)
	copy(frame[28:32], ip)
	copy(frame[38:42], ip)
	dst := &syscall.SockaddrLinklayer{Ifindex: nic.Index, Halen: 6, Protocol: 0x0608}
	for i := 0; i < 6; i++ {
		dst.Addr[i] = 255
	}
	for i := 0; i < 3; i++ {
		if err := syscall.Sendto(fd, frame, 0, dst); err != nil {
			log.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	log.Printf("announced own address %s on %s", ip, nic.Name)
}
