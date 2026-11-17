package main

import (
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

var baselinePath = "/home/isucon/webapp/baseline.json.gz"
var weightsPath = "/home/isucon/webapp/collation.bin"

func saveBaseline() error {
	f, e := os.OpenFile(baselinePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	z := gzip.NewWriter(f)
	if e = json.NewEncoder(z).Encode(st); e != nil {
		return e
	}
	if e = z.Close(); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	b := make([]byte, 65536*2)
	for i, v := range mysqlWeights {
		binary.BigEndian.PutUint16(b[2*i:], uint16(v))
	}
	return os.WriteFile(weightsPath, b, 0600)
}
func loadBaseline() error {
	f, e := os.Open(baselinePath)
	if e != nil {
		return e
	}
	defer f.Close()
	z, e := gzip.NewReader(f)
	if e != nil {
		return e
	}
	defer z.Close()
	var v state
	if e = json.NewDecoder(z).Decode(&v); e != nil {
		return e
	}
	if _, e = io.Copy(io.Discard, z); e != nil {
		return e
	}
	st = &v
	return nil
}
func loadSavedWeights() error {
	b, e := os.ReadFile(weightsPath)
	if e != nil {
		return e
	}
	if len(b) != 65536*2 {
		return fmt.Errorf("invalid collation file")
	}
	for i := range mysqlWeights {
		mysqlWeights[i] = rune(binary.BigEndian.Uint16(b[2*i:]))
	}
	useWeights = true
	return nil
}
