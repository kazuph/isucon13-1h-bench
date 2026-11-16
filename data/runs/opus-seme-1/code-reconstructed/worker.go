package main

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// ---------------- worker side (runs on s2/s3) ----------------

func loadRSAKey(path string) *rsa.PrivateKey {
	b, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		log.Fatal("no pem")
	}
	if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
		return k
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		log.Fatal(err)
	}
	return k.(*rsa.PrivateKey)
}

func runWorker(addr string) {
	key := loadRSAKey(os.Getenv("WORKER_KEY"))
	mux := http.NewServeMux()
	// body: [1 byte hash id][1 byte pss?][digest]
	mux.HandleFunc("/sign", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if len(b) < 2 {
			http.Error(w, "bad", 400)
			return
		}
		h := crypto.Hash(b[0])
		var sig []byte
		var err error
		if b[1] == 1 {
			sig, err = rsa.SignPSS(rand.Reader, key, h, b[2:], &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: h})
		} else {
			sig, err = rsa.SignPKCS1v15(rand.Reader, key, h, b[2:])
		}
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Write(sig)
	})
	// body: password -> hash
	mux.HandleFunc("/bhash", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h, err := bcrypt.GenerateFromPassword(b, bcrypt.MinCost)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Write(h)
	})
	// body: [4 byte len hash][hash][password] -> "1"/"0"
	mux.HandleFunc("/bcmp", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if len(b) < 4 {
			http.Error(w, "bad", 400)
			return
		}
		n := binary.BigEndian.Uint32(b)
		if int(n)+4 > len(b) {
			http.Error(w, "bad", 400)
			return
		}
		if bcrypt.CompareHashAndPassword(b[4:4+n], b[4+n:]) == nil {
			w.Write([]byte("1"))
		} else {
			w.Write([]byte("0"))
		}
	})
	log.Printf("worker listening %s", addr)
	srv := &http.Server{Addr: addr, Handler: mux, IdleTimeout: 300 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

// ---------------- client side (s1) ----------------

var (
	workers   []string
	workerRR  uint64
	workerCli = &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        1024,
			MaxIdleConnsPerHost: 512,
			IdleConnTimeout:     300 * time.Second,
			DialContext:         (&net.Dialer{Timeout: time.Second, KeepAlive: 30 * time.Second}).DialContext,
		},
	}
)

func initWorkers() {
	if v := os.Getenv("WORKERS"); v != "" {
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				workers = append(workers, "http://"+s)
			}
		}
	}
}

func workerCall(path string, body []byte) ([]byte, error) {
	if len(workers) == 0 {
		return nil, errors.New("no workers")
	}
	start := atomic.AddUint64(&workerRR, 1)
	var lastErr error
	for i := 0; i < len(workers); i++ {
		u := workers[(start+uint64(i))%uint64(len(workers))] + path
		resp, err := workerCli.Post(u, "application/octet-stream", bytes.NewReader(body))
		if err != nil {
			lastErr = err
			continue
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 {
			lastErr = errors.New("worker status " + resp.Status)
			continue
		}
		return b, nil
	}
	return nil, lastErr
}

type remoteSigner struct {
	local *rsa.PrivateKey
}

func (s *remoteSigner) Public() crypto.PublicKey { return &s.local.PublicKey }

func (s *remoteSigner) Sign(r io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	pss := byte(0)
	if _, ok := opts.(*rsa.PSSOptions); ok {
		pss = 1
	}
	body := make([]byte, 0, 2+len(digest))
	body = append(body, byte(opts.HashFunc()), pss)
	body = append(body, digest...)
	if sig, err := workerCall("/sign", body); err == nil {
		return sig, nil
	}
	return s.local.Sign(r, digest, opts)
}

func tlsConfig(certPath, keyPath string) *tls.Config {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		log.Fatal(err)
	}
	if len(workers) > 0 {
		cert.PrivateKey = &remoteSigner{local: cert.PrivateKey.(*rsa.PrivateKey)}
	}
	np := []string{"h2", "http/1.1"}
	if os.Getenv("NO_H2") == "1" {
		np = []string{"http/1.1"}
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: np}
}

func bcryptHash(pw string) (string, error) {
	if b, err := workerCall("/bhash", []byte(pw)); err == nil {
		return string(b), nil
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	return string(h), err
}

func bcryptCompare(hash, pw string) bool {
	body := make([]byte, 4, 4+len(hash)+len(pw))
	binary.BigEndian.PutUint32(body, uint32(len(hash)))
	body = append(body, hash...)
	body = append(body, pw...)
	if b, err := workerCall("/bcmp", body); err == nil {
		return string(b) == "1"
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}
