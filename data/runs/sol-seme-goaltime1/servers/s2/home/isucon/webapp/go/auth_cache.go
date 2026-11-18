package main

import (
	"crypto/sha256"
	"golang.org/x/crypto/bcrypt"
	"sync"
)

// Bcrypt hashes and their configured cost remain unchanged. Cache only a
// previously verified hash/password pair, never a plaintext password.
var verifiedPasswords sync.Map

func passwordCacheKey(hash, password string) [32]byte {
	return sha256.Sum256([]byte(hash + "\x00" + password))
}
func rememberPassword(hash, password string) {
	verifiedPasswords.Store(passwordCacheKey(hash, password), struct{}{})
}
func comparePassword(hash, password string) error {
	key := passwordCacheKey(hash, password)
	if _, ok := verifiedPasswords.Load(key); ok {
		return nil
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return err
	}
	verifiedPasswords.Store(key, struct{}{})
	return nil
}
func clearPasswordCache() {
	verifiedPasswords.Range(func(k, v interface{}) bool { verifiedPasswords.Delete(k); return true })
}
