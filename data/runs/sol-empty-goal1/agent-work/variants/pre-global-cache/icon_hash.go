package main

import (
 "crypto/sha256"
 "fmt"
 "os"
 "sync"
)

var defaultIconOnce sync.Once
var defaultIconDigest string
var defaultIconError error

func defaultIconHash() (string, error) {
 defaultIconOnce.Do(func() {
  var image []byte
  image, defaultIconError = os.ReadFile(fallbackImage)
  if defaultIconError == nil { defaultIconDigest = fmt.Sprintf("%x", sha256.Sum256(image)) }
 })
 return defaultIconDigest, defaultIconError
}

