package main

import (
	"crypto/tls"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func startDirectFrontend(api http.Handler) {
	if !replicaMode || os.Getenv("ISUPIPE_DIRECT_TLS") != "1" {
		return
	}
	target, _ := url.Parse("http://10.99.0.12:8080")
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &http.Transport{MaxIdleConns: 128, MaxIdleConnsPerHost: 128}
	files := http.FileServer(http.Dir("../public"))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api") {
			if r.Method == "GET" || r.Method == "HEAD" {
				api.ServeHTTP(w, r)
			} else {
				proxy.ServeHTTP(w, r)
			}
			return
		}
		filename := filepath.Join("../public", strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/"))
		if info, err := os.Stat(filename); err == nil && !info.IsDir() {
			files.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, "../public/index.html")
	})
	server := &http.Server{Addr: "10.99.0.11:443", Handler: handler, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13}}
	go func() { log.Fatal(server.ListenAndServeTLS("../frontend.crt", "../frontend.key")) }()
}
