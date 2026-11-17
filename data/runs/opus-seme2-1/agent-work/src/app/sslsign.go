package main

/*
#cgo LDFLAGS: -lcrypto
#include <stdlib.h>
#include <openssl/evp.h>
#include <openssl/pem.h>
#include <openssl/rsa.h>
#include <openssl/bio.h>
#include <openssl/objects.h>

static EVP_PKEY* load_key(const char* pem, int len) {
	BIO* b = BIO_new_mem_buf(pem, len);
	if (!b) return NULL;
	EVP_PKEY* k = PEM_read_bio_PrivateKey(b, NULL, NULL, NULL);
	BIO_free(b);
	return k;
}

static EVP_PKEY_CTX* new_ctx(EVP_PKEY* k, int pss, int hashnid) {
	EVP_PKEY_CTX* ctx = EVP_PKEY_CTX_new(k, NULL);
	if (!ctx) return NULL;
	if (EVP_PKEY_sign_init(ctx) <= 0) goto bad;
	if (pss) {
		if (EVP_PKEY_CTX_set_rsa_padding(ctx, RSA_PKCS1_PSS_PADDING) <= 0) goto bad;
		if (EVP_PKEY_CTX_set_rsa_pss_saltlen(ctx, RSA_PSS_SALTLEN_DIGEST) <= 0) goto bad;
	} else {
		if (EVP_PKEY_CTX_set_rsa_padding(ctx, RSA_PKCS1_PADDING) <= 0) goto bad;
	}
	if (EVP_PKEY_CTX_set_signature_md(ctx, EVP_get_digestbynid(hashnid)) <= 0) goto bad;
	return ctx;
bad:
	EVP_PKEY_CTX_free(ctx);
	return NULL;
}

static int ctx_sign(EVP_PKEY_CTX* ctx, const unsigned char* dgst, size_t dlen, unsigned char* sig, size_t* siglen) {
	return EVP_PKEY_sign(ctx, sig, siglen, dgst, dlen) > 0;
}

static int do_sign(EVP_PKEY* k, int pss, int hashnid, const unsigned char* dgst, size_t dlen, unsigned char* sig, size_t* siglen) {
	EVP_PKEY_CTX* ctx = EVP_PKEY_CTX_new(k, NULL);
	if (!ctx) return 0;
	int ok = 0;
	if (EVP_PKEY_sign_init(ctx) <= 0) goto end;
	if (pss) {
		if (EVP_PKEY_CTX_set_rsa_padding(ctx, RSA_PKCS1_PSS_PADDING) <= 0) goto end;
		if (EVP_PKEY_CTX_set_rsa_pss_saltlen(ctx, RSA_PSS_SALTLEN_DIGEST) <= 0) goto end;
	} else {
		if (EVP_PKEY_CTX_set_rsa_padding(ctx, RSA_PKCS1_PADDING) <= 0) goto end;
	}
	if (EVP_PKEY_CTX_set_signature_md(ctx, EVP_get_digestbynid(hashnid)) <= 0) goto end;
	if (EVP_PKEY_sign(ctx, sig, siglen, dgst, dlen) <= 0) goto end;
	ok = 1;
end:
	EVP_PKEY_CTX_free(ctx);
	return ok;
}
*/
import "C"

import (
	"crypto"
	"crypto/rsa"
	"crypto/tls"
	"errors"
	"io"
	"os"
	"sync"
	"unsafe"
)

type sslSigner struct {
	key   *C.EVP_PKEY
	pub   crypto.PublicKey
	pools sync.Map // ctxKey -> chan *C.EVP_PKEY_CTX
}

type ctxKey struct {
	pss int
	nid C.int
}

func (s *sslSigner) Public() crypto.PublicKey { return s.pub }

func (s *sslSigner) Sign(_ io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	var nid C.int
	switch opts.HashFunc() {
	case crypto.SHA256:
		nid = C.NID_sha256
	case crypto.SHA384:
		nid = C.NID_sha384
	case crypto.SHA512:
		nid = C.NID_sha512
	case crypto.SHA1:
		nid = C.NID_sha1
	default:
		return nil, errors.New("unsupported hash")
	}
	pss := 0
	if _, ok := opts.(*rsa.PSSOptions); ok {
		pss = 1
	}
	sig := make([]byte, 1024)
	siglen := C.size_t(len(sig))
	k := ctxKey{pss, nid}
	pv, _ := s.pools.LoadOrStore(k, make(chan *C.EVP_PKEY_CTX, 64))
	pool := pv.(chan *C.EVP_PKEY_CTX)
	var ctx *C.EVP_PKEY_CTX
	select {
	case ctx = <-pool:
	default:
		ctx = C.new_ctx(s.key, C.int(pss), nid)
	}
	if ctx == nil {
		if C.do_sign(s.key, C.int(pss), nid, (*C.uchar)(unsafe.Pointer(&digest[0])), C.size_t(len(digest)),
			(*C.uchar)(unsafe.Pointer(&sig[0])), &siglen) != 1 {
			return nil, errors.New("openssl sign failed")
		}
		return sig[:siglen], nil
	}
	ok := C.ctx_sign(ctx, (*C.uchar)(unsafe.Pointer(&digest[0])), C.size_t(len(digest)), (*C.uchar)(unsafe.Pointer(&sig[0])), &siglen)
	select {
	case pool <- ctx:
	default:
		C.EVP_PKEY_CTX_free(ctx)
	}
	if ok != 1 {
		return nil, errors.New("openssl sign failed")
	}
	return sig[:siglen], nil
}

// loadCertOpenSSL returns a tls.Certificate whose private key operations run in OpenSSL.
func loadCertOpenSSL(certFile, keyFile string) (tls.Certificate, error) {
	c, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return c, err
	}
	if _, ok := c.PrivateKey.(*rsa.PrivateKey); !ok {
		return c, nil
	}
	pem, err := os.ReadFile(keyFile)
	if err != nil {
		return c, err
	}
	cs := C.CString(string(pem))
	defer C.free(unsafe.Pointer(cs))
	k := C.load_key(cs, C.int(len(pem)))
	if k == nil {
		return c, errors.New("openssl load key failed")
	}
	c.PrivateKey = &sslSigner{key: k, pub: c.PrivateKey.(*rsa.PrivateKey).Public()}
	return c, nil
}
