package main

import (
	"reflect"
	"testing"
)

func TestResolveArguments(t *testing.T) {
	args, err := resolveArgs([]argument{{Kind: "int", Integer: 9}, {Kind: "bytes", Bytes: []byte{0, 255, 1}}, {Kind: "ref", Reference: 0}, {Kind: "string", Text: "'\\\n日本語"}, {Kind: "nil"}}, []result{{ID: 42}})
	if err != nil {
		t.Fatal(err)
	}
	want := []interface{}{int64(9), []byte{0, 255, 1}, int64(42), "'\\\n日本語", nil}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("%#v != %#v", args, want)
	}
	if _, err = resolveArgs([]argument{{Kind: "ref", Reference: 1}}, []result{{ID: 42}}); err == nil {
		t.Fatal("out-of-range reference accepted")
	}
}
