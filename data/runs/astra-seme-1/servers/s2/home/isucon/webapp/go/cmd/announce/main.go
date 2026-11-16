package main

import (
	"encoding/binary"
	"golang.org/x/sys/unix"
	"log"
	"net"
	"time"
)

func main() {
	iface, e := net.InterfaceByName("eth0")
	if e != nil {
		log.Fatal(e)
	}
	address := ""
	for i := 0; i < 60; i++ {
		addrs, e := iface.Addrs()
		if e != nil {
			log.Fatal(e)
		}
		for _, a := range addrs {
			ip, _, e := net.ParseCIDR(a.String())
			if e != nil {
				continue
			}
			v := ip.String()
			if v == "10.99.0.11" || v == "10.99.0.12" || v == "10.99.0.13" {
				address = v
				break
			}
		}
		if address != "" {
			break
		}
		time.Sleep(time.Second)
	}
	if address == "" {
		log.Fatal("own assigned address unavailable")
	}
	fd, e := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW, 0x0608)
	if e != nil {
		log.Fatal(e)
	}
	defer unix.Close(fd)
	dest := &unix.SockaddrLinklayer{Ifindex: iface.Index, Protocol: 0x0608, Halen: 6, Addr: [8]uint8{255, 255, 255, 255, 255, 255}}
	for i := 0; i < 3; i++ {
		for _, op := range []uint16{1, 2} {
			frame := make([]byte, 60)
			copy(frame[:6], []byte{255, 255, 255, 255, 255, 255})
			copy(frame[6:12], iface.HardwareAddr)
			binary.BigEndian.PutUint16(frame[12:14], 0x0806)
			binary.BigEndian.PutUint16(frame[14:16], 1)
			binary.BigEndian.PutUint16(frame[16:18], 0x0800)
			frame[18] = 6
			frame[19] = 4
			binary.BigEndian.PutUint16(frame[20:22], op)
			copy(frame[22:28], iface.HardwareAddr)
			copy(frame[28:32], net.ParseIP(address).To4())
			copy(frame[38:42], net.ParseIP(address).To4())
			if e := unix.Sendto(fd, frame, 0, dest); e != nil {
				log.Fatal(e)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	log.Printf("Announced own assigned address %s with MAC %s", address, iface.HardwareAddr)
}
