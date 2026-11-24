package main

import (
	"crypto/tls"
	"github.com/labstack/echo/v4"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

func startNativeTLS(e *echo.Echo) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			e.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/internal/") {
			http.NotFound(w, r)
			return
		}
		clean := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		file := filepath.Join("../public", clean)
		if info, err := os.Stat(file); err != nil || info.IsDir() {
			file = "../public/index.html"
		}
		http.ServeFile(w, r, file)
	})
	server := &http.Server{Addr: ":443", Handler: handler, ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13}}
	go func() {
		log.Fatal(server.ListenAndServeTLS("/etc/nginx/tls/_.u.isucon.dev.crt", "/home/isucon/webapp/tls.key"))
	}()
}
