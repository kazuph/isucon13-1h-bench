package main

import (
	"database/sql"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

var dnsDB *sql.DB
var zoneMu sync.RWMutex
var addresses map[string][4]byte

func loadZone() error {
	rows, err := dnsDB.Query("SELECT name,content FROM records WHERE type='A' AND disabled=0")
	if err != nil {
		return err
	}
	defer rows.Close()
	zone := make(map[string][4]byte)
	for rows.Next() {
		var name, addr string
		if err := rows.Scan(&name, &addr); err != nil {
			return err
		}
		ip := net.ParseIP(addr).To4()
		if ip == nil {
			return fmt.Errorf("bad IPv4 address %s", addr)
		}
		var a [4]byte
		copy(a[:], ip)
		zone[strings.ToLower(name)+"."] = a
	}
	if err := rows.Err(); err != nil {
		return err
	}
	zoneMu.Lock()
	addresses = zone
	zoneMu.Unlock()
	return nil
}
func registerDNS(name string) error {
	fqdn := name + ".u.isucon.dev"
	_, err := dnsDB.Exec("INSERT INTO records(domain_id,name,type,content,ttl,auth) SELECT id,?,'A',?,300,1 FROM domains WHERE name='u.isucon.dev'", fqdn, powerDNSSubdomainAddress)
	if err != nil {
		return err
	}
	var a [4]byte
	copy(a[:], net.ParseIP(powerDNSSubdomainAddress).To4())
	zoneMu.Lock()
	addresses[strings.ToLower(fqdn)+"."] = a
	zoneMu.Unlock()
	return nil
}
func serveDNS() error {
	conn, err := net.ListenPacket("udp4", net.JoinHostPort("10.99.0.11", "53"))
	if err != nil {
		return err
	}
	for i := 0; i < 4; i++ {
		go func() {
			buf := make([]byte, 65535)
			responseBuf := make([]byte, 0, 1536)
			for {
				n, peer, err := conn.ReadFrom(buf)
				if err != nil {
					log.Print(err)
					return
				}
				var q dnsmessage.Message
				if err = q.Unpack(buf[:n]); err != nil || q.Response || len(q.Questions) != 1 {
					continue
				}
				question := q.Questions[0]
				name := strings.ToLower(question.Name.String())
				if question.Class != dnsmessage.ClassINET || question.Type != dnsmessage.TypeA || !(name == "u.isucon.dev." || strings.HasSuffix(name, ".u.isucon.dev.")) {
					upstream, err := net.DialTimeout("udp4", "127.0.0.1:53", time.Second)
					if err != nil {
						continue
					}
					upstream.SetDeadline(time.Now().Add(time.Second))
					_, err = upstream.Write(buf[:n])
					if err == nil {
						n, err = upstream.Read(buf)
						if err == nil {
							conn.WriteTo(buf[:n], peer)
						}
					}
					upstream.Close()
					continue
				}
				answer := dnsmessage.Message{Header: dnsmessage.Header{ID: q.ID, Response: true, Authoritative: true, RecursionDesired: q.RecursionDesired}, Questions: q.Questions}
				zoneMu.RLock()
				ip, exists := addresses[name]
				zoneMu.RUnlock()
				if exists {
					answer.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 300}, Body: &dnsmessage.AResource{A: ip}}}
				} else {
					answer.RCode = dnsmessage.RCodeNameError
					answer.Authorities = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName("u.isucon.dev."), Class: dnsmessage.ClassINET, TTL: 1}, Body: &dnsmessage.SOAResource{NS: dnsmessage.MustNewName("ns1.u.isucon.dev."), MBox: dnsmessage.MustNewName("hostmaster.u.isucon.dev."), Refresh: 10800, Retry: 3600, Expire: 604800, MinTTL: 1}}}
				}
				out, err := answer.AppendPack(responseBuf[:0])
				if err == nil {
					conn.WriteTo(out, peer)
				}
			}
		}()
	}
	return nil
}
