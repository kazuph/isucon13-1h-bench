package main

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var (
	cryptoPeers        []string
	cryptoRR           atomic.Uint64
	activeLocalCrypto  atomic.Int32
	bcryptOffloadCnt   atomic.Int64
	bcryptLocalCnt     atomic.Int64
	rsaOffloadCnt      atomic.Int64
	rsaLocalCnt        atomic.Int64

	cryptoHTTP = &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        256,
			MaxIdleConnsPerHost: 128,
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  true,
		},
	}
)

func initCryptoPeers() {
	v := os.Getenv("ISUCON13_BCRYPT_PEERS")
	if v == "" {
		return
	}
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			cryptoPeers = append(cryptoPeers, p)
		}
	}
}

type bcryptHashReq struct {
	Password string `json:"p"`
}

type bcryptCompareReq struct {
	Hash     string `json:"h"`
	Password string `json:"p"`
}

type rsaSignReq struct {
	Hash   uint   `json:"h"`
	Digest []byte `json:"d"`
	PSS    bool   `json:"pss"`
	Salt   int    `json:"s"`
}

// offloadRSASigner wraps *rsa.PrivateKey and offloads overflow signing to s2/s3 when s1 is busy.
type offloadRSASigner struct {
	priv *rsa.PrivateKey
}

func (s *offloadRSASigner) Public() crypto.PublicKey {
	return s.priv.Public()
}

func (s *offloadRSASigner) Sign(r io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	if len(cryptoPeers) > 0 && activeLocalCrypto.Load() >= 2 {
		req := rsaSignReq{
			Hash:   uint(opts.HashFunc()),
			Digest: digest,
		}
		if pssOpts, ok := opts.(*rsa.PSSOptions); ok {
			req.PSS = true
			req.Salt = pssOpts.SaltLength
		}
		b, _ := json.Marshal(req)
		peer := cryptoPeers[cryptoRR.Add(1)%uint64(len(cryptoPeers))]
		resp, err := cryptoHTTP.Post(peer+"/rsa/sign", "application/json", bytes.NewReader(b))
		if err == nil {
			sig, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr == nil && resp.StatusCode == http.StatusOK && len(sig) > 0 {
				rsaOffloadCnt.Add(1)
				return sig, nil
			}
		}
	}
	activeLocalCrypto.Add(1)
	defer activeLocalCrypto.Add(-1)
	rsaLocalCnt.Add(1)
	return s.priv.Sign(r, digest, opts)
}

func startBcryptWorkerServer(listenAddr string) {
	var rsaPriv *rsa.PrivateKey
	if cert, err := tls.LoadX509KeyPair("/home/isucon/webapp/tls/_.u.isucon.dev.crt", "/home/isucon/webapp/tls/_.u.isucon.dev.key"); err == nil {
		if p, ok := cert.PrivateKey.(*rsa.PrivateKey); ok {
			rsaPriv = p
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/bcrypt/hash", func(w http.ResponseWriter, r *http.Request) {
		var req bcryptHashReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		h, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcryptDefaultCost)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(h)
	})
	mux.HandleFunc("/bcrypt/compare", func(w http.ResponseWriter, r *http.Request) {
		var req bcryptCompareReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		err := bcrypt.CompareHashAndPassword([]byte(req.Hash), []byte(req.Password))
		if err == nil {
			w.WriteHeader(http.StatusOK)
			return
		}
		if err == bcrypt.ErrMismatchedHashAndPassword {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/rsa/sign", func(w http.ResponseWriter, r *http.Request) {
		if rsaPriv == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var req rsaSignReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var sig []byte
		var err error
		if req.PSS {
			sig, err = rsa.SignPSS(rand.Reader, rsaPriv, crypto.Hash(req.Hash), req.Digest, &rsa.PSSOptions{
				SaltLength: req.Salt,
				Hash:       crypto.Hash(req.Hash),
			})
		} else {
			sig, err = rsa.SignPKCS1v15(rand.Reader, rsaPriv, crypto.Hash(req.Hash), req.Digest)
		}
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(sig)
	})
	log.Printf("crypto worker listening on %s (rsa=%v)", listenAddr, rsaPriv != nil)
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		log.Fatalf("crypto worker failed: %v", err)
	}
}

func generateBcryptHash(password string) ([]byte, error) {
	if len(cryptoPeers) > 0 && activeLocalCrypto.Load() >= 2 {
		peer := cryptoPeers[cryptoRR.Add(1)%uint64(len(cryptoPeers))]
		b, _ := json.Marshal(bcryptHashReq{Password: password})
		resp, err := cryptoHTTP.Post(peer+"/bcrypt/hash", "application/json", bytes.NewReader(b))
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr == nil && resp.StatusCode == http.StatusOK && len(body) > 0 {
				bcryptOffloadCnt.Add(1)
				return body, nil
			}
		}
	}
	activeLocalCrypto.Add(1)
	defer activeLocalCrypto.Add(-1)
	bcryptLocalCnt.Add(1)
	return bcrypt.GenerateFromPassword([]byte(password), bcryptDefaultCost)
}

func compareBcryptHash(hashed, password string) error {
	if len(cryptoPeers) > 0 && activeLocalCrypto.Load() >= 2 {
		peer := cryptoPeers[cryptoRR.Add(1)%uint64(len(cryptoPeers))]
		b, _ := json.Marshal(bcryptCompareReq{Hash: hashed, Password: password})
		resp, err := cryptoHTTP.Post(peer+"/bcrypt/compare", "application/json", bytes.NewReader(b))
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusOK:
				bcryptOffloadCnt.Add(1)
				return nil
			case http.StatusUnauthorized:
				bcryptOffloadCnt.Add(1)
				return bcrypt.ErrMismatchedHashAndPassword
			}
		}
	}
	activeLocalCrypto.Add(1)
	defer activeLocalCrypto.Add(-1)
	bcryptLocalCnt.Add(1)
	return bcrypt.CompareHashAndPassword([]byte(hashed), []byte(password))
}

func wrapTLSPrivateKey(priv crypto.PrivateKey) (crypto.PrivateKey, error) {
	p, ok := priv.(*rsa.PrivateKey)
	if !ok {
		return priv, errors.New("not rsa key")
	}
	return &offloadRSASigner{priv: p}, nil
}
