package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
)

// Hash the complete state in bounded pieces. Encoding the entire state in one
// call would leave a large image buffer in encoding/json's shared buffer pool.
func stateDigest() (string, error) {
	h := sha256.New()
	enc := json.NewEncoder(h)
	v := reflect.ValueOf(st).Elem()
	typ := v.Type()
	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		if err := enc.Encode(typ.Field(i).Name); err != nil {
			return "", err
		}
		if field.Kind() != reflect.Map {
			if err := enc.Encode(field.Interface()); err != nil {
				return "", err
			}
			continue
		}
		keys := field.MapKeys()
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].Kind() == reflect.String {
				return keys[i].String() < keys[j].String()
			}
			return keys[i].Int() < keys[j].Int()
		})
		if err := enc.Encode(len(keys)); err != nil {
			return "", err
		}
		for _, key := range keys {
			if err := enc.Encode(key.Interface()); err != nil {
				return "", err
			}
			if err := enc.Encode(field.MapIndex(key).Interface()); err != nil {
				return "", err
			}
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
