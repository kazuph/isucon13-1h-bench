package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"testing"
)

func BenchmarkSign(b *testing.B) {
	c, err := loadCertOpenSSL("/home/isucon/tls/cert.crt", "/home/isucon/tls/cert.key")
	if err != nil {
		b.Fatal(err)
	}
	s := c.PrivateKey.(crypto.Signer)
	d := sha256.Sum256([]byte("x"))
	opts := &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash, Hash: crypto.SHA256}
	for i := 0; i < b.N; i++ {
		sig, err := s.Sign(rand.Reader, d[:], opts)
		if err != nil {
			b.Fatal(err)
		}
		if i == 0 {
			if err := rsa.VerifyPSS(s.Public().(*rsa.PublicKey), crypto.SHA256, d[:], sig, opts); err != nil {
				b.Fatal(err)
			}
		}
	}
}
