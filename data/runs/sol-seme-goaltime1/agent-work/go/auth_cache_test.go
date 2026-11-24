package main

import (
	"errors"
	"golang.org/x/crypto/bcrypt"
	"testing"
)

func TestPasswordCacheRetainsBcryptVerification(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcryptDefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	cost, err := bcrypt.Cost(hash)
	if err != nil || cost != bcryptDefaultCost {
		t.Fatal("bcrypt cost changed")
	}
	if err = comparePassword(string(hash), "correct-password"); err != nil {
		t.Fatal(err)
	}
	if err = comparePassword(string(hash), "incorrect-password"); !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		t.Fatalf("wrong password accepted: %v", err)
	}
	if err = comparePassword(string(hash), "correct-password"); err != nil {
		t.Fatal(err)
	}
	other, _ := bcrypt.GenerateFromPassword([]byte("other"), bcryptDefaultCost)
	if err = comparePassword(string(other), "correct-password"); !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		t.Fatalf("cache crossed hash boundary: %v", err)
	}
}
