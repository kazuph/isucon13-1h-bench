package main

import (
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime"
)

func init() {
	if os.Getenv("PROFILE") == "1" {
		runtime.SetMutexProfileFraction(10)
		runtime.SetBlockProfileRate(1000000)
		go http.ListenAndServe("0.0.0.0:6060", nil)
	}
}
