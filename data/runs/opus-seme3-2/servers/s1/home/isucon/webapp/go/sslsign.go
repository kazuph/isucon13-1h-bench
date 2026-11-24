package main

/*
#cgo LDFLAGS: -lcrypto
#include <stdlib.h>
#include <openssl/evp.h>
#include <openssl/pem.h>
#include <openssl/rsa.h>

static EVP_PKEY* load_key(const char* pem, int len) {
	BIO* b = BIO_new_mem_buf(pem, len);
	EVP_PKEY* k = PEM_read_bio_PrivateKey(b, NULL, NULL, NULL);
	BIO_free(b);
	return k;
}

static int do_sign(EVP_PKEY* k, int pss, int hashid, const unsigned char* dg, size_t dglen, unsigned char* out, size_t* outlen) {
	EVP_PKEY_CTX* c = EVP_PKEY_CTX_new(k, NULL);
	if (!c) return -1;
	int r = -1;
	const EVP_MD* md = NULL;
	if (EVP_PKEY_sign_init(c) <= 0) goto end;
	if (pss) {
		if (EVP_PKEY_CTX_set_rsa_padding(c, RSA_PKCS1_PSS_PADDING) <= 0) goto end;
		if (EVP_PKEY_CTX_set_rsa_pss_saltlen(c, RSA_PSS_SALTLEN_DIGEST) <= 0) goto end;
	} else {
		if (EVP_PKEY_CTX_set_rsa_padding(c, RSA_PKCS1_PADDING) <= 0) goto end;
	}
	switch (hashid) {
	case 1: md = EVP_sha1(); break;
	case 256: md = EVP_sha256(); break;
	case 384: md = EVP_sha384(); break;
	case 512: md = EVP_sha512(); break;
	}
	if (md && EVP_PKEY_CTX_set_signature_md(c, md) <= 0) goto end;
	if (EVP_PKEY_sign(c, out, outlen, dg, dglen) <= 0) goto end;
	r = 0;
end:
	EVP_PKEY_CTX_free(c);
	return r;
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
	"unsafe"
)

type sslSigner struct {
	key *C.EVP_PKEY
	pub crypto.PublicKey
	sz  int
}

func (s *sslSigner) Public() crypto.PublicKey { return s.pub }

func (s *sslSigner) Sign(_ io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	pss := 0
	if _, ok := opts.(*rsa.PSSOptions); ok {
		pss = 1
	}
	hid := 0
	switch opts.HashFunc() {
	case crypto.SHA1:
		hid = 1
	case crypto.SHA256:
		hid = 256
	case crypto.SHA384:
		hid = 384
	case crypto.SHA512:
		hid = 512
	default:
		return nil, errors.New("unsupported hash")
	}
	out := make([]byte, s.sz)
	outlen := C.size_t(len(out))
	r := C.do_sign(s.key, C.int(pss), C.int(hid), (*C.uchar)(unsafe.Pointer(&digest[0])), C.size_t(len(digest)),
		(*C.uchar)(unsafe.Pointer(&out[0])), &outlen)
	if r != 0 {
		return nil, errors.New("openssl sign failed")
	}
	return out[:outlen], nil
}

// loadCertOpenSSL returns a certificate whose private key operations are done by OpenSSL.
func loadCertOpenSSL(certFile, keyFile string) (tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return cert, err
	}
	rk, ok := cert.PrivateKey.(*rsa.PrivateKey)
	if !ok {
		return cert, nil
	}
	pem, err := os.ReadFile(keyFile)
	if err != nil {
		return cert, err
	}
	cs := C.CString(string(pem))
	defer C.free(unsafe.Pointer(cs))
	k := C.load_key(cs, C.int(len(pem)))
	if k == nil {
		return cert, errors.New("openssl load key failed")
	}
	cert.PrivateKey = &sslSigner{key: k, pub: &rk.PublicKey, sz: rk.Size()}
	return cert, nil
}
