package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

func clusterTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.MaxIdleConns = 256
	t.MaxIdleConnsPerHost = 128
	t.IdleConnTimeout = 90 * time.Second
	t.DisableCompression = true
	return t
}
func edgeIcon(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	name, ok := strings.CutPrefix(r.URL.Path, "/api/user/")
	if !ok {
		return false
	}
	name, ok = strings.CutSuffix(name, "/icon")
	if !ok || strings.ContainsRune(name, '/') {
		return false
	}
	data.RLock()
	id, ok := data.names[name]
	u := data.users[id]
	img, custom := data.images[id]
	if !custom {
		img = data.fallback
	}
	data.RUnlock()
	if !ok {
		return false
	}
	tag := strconv.Quote(u.IconHash)
	w.Header().Set("ETag", tag)
	if r.Header.Get("If-None-Match") == tag {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(img)))
	_, _ = w.Write(img)
	return true
}
func nativeIngress(e *echo.Echo, addr string) (*http.Server, net.Listener, error) {
	folder := os.Getenv("ISUPIPE_TLS_FILES")
	if folder == "" {
		folder = os.Getenv("CREDENTIALS_DIRECTORY")
	}
	leaf, err := tls.LoadX509KeyPair(filepath.Join(folder, "pipe.crt"), filepath.Join(folder, "pipe.key"))
	if err != nil {
		return nil, nil, err
	}
	fallback, err := tls.LoadX509KeyPair(filepath.Join(folder, "default.crt"), filepath.Join(folder, "default.key"))
	if err != nil {
		return nil, nil, err
	}
	upstream, err := url.Parse(os.Getenv("ISUPIPE_LOGIN_URL"))
	if err != nil || upstream.Host == "" {
		return nil, nil, fmt.Errorf("invalid login origin")
	}
	login := httputil.NewSingleHostReverseProxy(upstream)
	login.Transport = clusterTransport()
	// Preserve the original virtual hosts and static-file handling in nginx.
	staticOrigin, _ := url.Parse("http://127.0.0.1:8085")
	static := httputil.NewSingleHostReverseProxy(staticOrigin)
	static.Transport = clusterTransport()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.TrimSuffix(strings.ToLower(host), ".")
		if host != "u.isucon.dev" && !strings.HasSuffix(host, ".u.isucon.dev") {
			static.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api/login" && r.Method == "POST" {
			login.ServeHTTP(w, r)
			return
		}
		if edgeIcon(w, r) {
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			e.ServeHTTP(w, r)
			return
		}
		static.ServeHTTP(w, r)
	})
	conf := &tls.Config{MinVersion: tls.VersionTLS13, GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		name := strings.ToLower(hello.ServerName)
		if name == "u.isucon.dev" || strings.HasSuffix(name, ".u.isucon.dev") {
			return &leaf, nil
		}
		return &fallback, nil
	}}
	server := &http.Server{Handler: handler, TLSConfig: conf, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 75 * time.Second, MaxHeaderBytes: 1 << 20}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, err
	}
	return server, ln, nil
}
