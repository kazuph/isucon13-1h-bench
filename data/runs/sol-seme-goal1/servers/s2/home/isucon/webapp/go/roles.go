package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/net/dns/dnsmessage"
)

var internalClient = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{MaxIdleConns: 128, MaxIdleConnsPerHost: 128, IdleConnTimeout: 90 * time.Second}}

type cryptoRequest struct {
	Password string
	Hash     string
	Generate bool
}
type cryptoResponse struct {
	Hash  string
	OK    bool
	Error string
}

func bcryptRPC(req cryptoRequest) (cryptoResponse, error) {
	var out cryptoResponse
	b, _ := json.Marshal(req)
	res, err := internalClient.Post("http://10.99.0.13:8091/crypto", "application/json", bytes.NewReader(b))
	if err != nil {
		return out, err
	}
	defer res.Body.Close()
	err = json.NewDecoder(res.Body).Decode(&out)
	if err == nil && out.Error != "" {
		err = fmt.Errorf("bcrypt: %s", out.Error)
	}
	return out, err
}
func cryptoService() {
	http.HandleFunc("/crypto", func(w http.ResponseWriter, r *http.Request) {
		var req cryptoRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		var out cryptoResponse
		if req.Generate {
			b, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcryptDefaultCost)
			out.Hash = string(b)
			out.OK = err == nil
			if err != nil {
				out.Error = err.Error()
			}
		} else {
			out.OK = bcrypt.CompareHashAndPassword([]byte(req.Hash), []byte(req.Password)) == nil
		}
		json.NewEncoder(w).Encode(out)
	})
	log.Fatal(http.ListenAndServe(":8091", nil))
}

var dnsNames = struct {
	sync.RWMutex
	M map[string]bool
}{M: map[string]bool{}}

func zoneNames() map[string]bool {
	out := map[string]bool{}
	f, err := os.Open("../pdns/u.isucon.dev.zone")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		p := strings.Fields(scanner.Text())
		if len(p) >= 5 && p[3] == "A" {
			name := p[0]
			if name == "@" {
				name = "u.isucon.dev."
			} else {
				name += ".u.isucon.dev."
			}
			out[strings.ToLower(name)] = true
		}
	}
	return out
}
func dnsService() {
	dnsNames.M = zoneNames()
	http.HandleFunc("/names", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Reset bool
			Names []string
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", 400)
			return
		}
		dnsNames.Lock()
		if req.Reset {
			dnsNames.M = zoneNames()
		}
		for _, name := range req.Names {
			dnsNames.M[strings.ToLower(name)+".u.isucon.dev."] = true
		}
		dnsNames.Unlock()
		w.WriteHeader(204)
	})
	go func() { log.Fatal(http.ListenAndServe(":8092", nil)) }()
	go func() {
		for {
			res, err := internalClient.Get("http://10.99.0.12:8080/internal/names")
			if err == nil {
				var names []string
				err = json.NewDecoder(res.Body).Decode(&names)
				res.Body.Close()
				if err == nil {
					dnsNames.Lock()
					for _, name := range names {
						dnsNames.M[strings.ToLower(name)+".u.isucon.dev."] = true
					}
					dnsNames.Unlock()
					return
				}
			}
			time.Sleep(time.Second)
		}
	}()
	conn, err := net.ListenPacket("udp", ":53")
	if err != nil {
		log.Fatal(err)
	}
	for {
		buf := make([]byte, 4096)
		n, addr, err := conn.ReadFrom(buf)
		if err != nil {
			continue
		}
		response := dnsReply(buf[:n])
		if response != nil {
			conn.WriteTo(response, addr)
		}
	}
}

func dnsReply(b []byte) []byte {
	var p dnsmessage.Parser
	h, err := p.Start(b)
	if err != nil {
		return nil
	}
	q, err := p.Question()
	if err != nil {
		return nil
	}
	name := strings.ToLower(q.Name.String())
	dnsNames.RLock()
	exists := dnsNames.M[name]
	dnsNames.RUnlock()
	rh := dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true, RecursionDesired: h.RecursionDesired}
	if !exists {
		rh.RCode = dnsmessage.RCodeNameError
	}
	builder := dnsmessage.NewBuilder(nil, rh)
	builder.EnableCompression()
	if builder.StartQuestions() != nil {
		return nil
	}
	if builder.Question(q) != nil {
		return nil
	}
	if exists && q.Type == dnsmessage.TypeA {
		if builder.StartAnswers() != nil {
			return nil
		}
		address := [4]byte{10, 99, 0, 11}
		ttl := uint32(60)
		err = builder.AResource(dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: ttl}, dnsmessage.AResource{A: address})
		if err != nil {
			return nil
		}
	}
	response, err := builder.Finish()
	if err != nil {
		return nil
	}
	return response
}
func sendDNS(names []string, reset bool) error {
	b, _ := json.Marshal(struct {
		Reset bool
		Names []string
	}{reset, names})
	r, err := internalClient.Post("http://10.99.0.11:8092/names", "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer r.Body.Close()
	io.Copy(io.Discard, r.Body)
	if r.StatusCode != 204 {
		return fmt.Errorf("DNS sync status %d", r.StatusCode)
	}
	return nil
}
