package main

import (
	"crypto/tls"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envUint32(k string, def uint32) uint32 {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.ParseUint(v, 10, 32); err == nil {
			return uint32(n)
		}
	}
	return def
}

func main() {
	exportSnap := flag.Bool("export-snapshot", false, "export initial snapshot from MySQL and exit")
	snapPath := flag.String("snapshot", "/home/isucon/isupipe_data/initial_snapshot.gob", "path to initial snapshot")
	dataDir := flag.String("data-dir", "/home/isucon/isupipe_data", "path to data/WAL dir")
	flag.Parse()

	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	if sk := os.Getenv("ISUCON13_SESSION_SECRETKEY"); sk != "" {
		sessionSecret = []byte(sk)
	}

	if *exportSnap {
		if err := os.MkdirAll(filepath.Dir(*snapPath), 0755); err != nil {
			log.Fatal(err)
		}
		dsn := envOr("EXPORT_DSN", "isucon:isucon@tcp(127.0.0.1:3306)/isupipe?parseTime=true")
		if err := ExportSnapshotFromMySQL(dsn, "../pdns/u.isucon.dev.zone", "../img/NoImage.jpg", *snapPath); err != nil {
			log.Fatalf("export snapshot failed: %v", err)
		}
		return
	}

	t0 := time.Now()
	snap, err := LoadSnapshot(*snapPath)
	if err != nil {
		log.Fatalf("failed to load snapshot %s: %v", *snapPath, err)
	}
	log.Printf("Loaded snapshot in %v (users=%d, livestreams=%d, dns=%d)",
		time.Since(t0), len(snap.Users), len(snap.Livestreams), len(snap.DNSNames))

	role := envOr("ISU_ROLE", "core")
	internalAddr := envOr("ISU_INTERNAL_ADDR", ":8081")
	httpsAddr := envOr("ISU_HTTPS_ADDR", ":443")
	httpAddr := envOr("ISU_HTTP_ADDR", ":80")
	enableTLS := envOr("ISU_ENABLE_TLS", "true") == "true"

	app := &AppServer{isCore: role == "core"}
	if envOr("ISU_PROFILE", "false") == "true" {
		app.metrics = NewMetrics()
		if envOr("ISU_MUTEX_PROFILE", "false") == "true" {
			runtime.SetMutexProfileFraction(1)
		}
	}
	var wal *WAL

	if app.isCore {
		wal, err = NewWAL(*dataDir)
		if err != nil {
			log.Fatalf("failed to init WAL: %v", err)
		}

		var dns *DNSServer
		if envOr("ISU_ENABLE_DNS", "true") == "true" {
			pipeIPs := envOr("ISU_DNS_PIPE_IPS", "10.99.0.12,10.99.0.13")
			subIPs := envOr("ISU_DNS_SUB_IPS", "10.99.0.11")
			apexIP := envOr("ISU_DNS_APEX_IP", "10.99.0.11")
			pipeTTL := envUint32("ISU_DNS_PIPE_TTL", 0)
			subTTL := envUint32("ISU_DNS_SUB_TTL", 3600)
			dns = NewDNSServer(pipeIPs, subIPs, apexIP, pipeTTL, subTTL)
			dns.dropUnknown = envOr("ISU_DNS_DROP_UNKNOWN", "false") == "true"
			dns.nxBurst = float64(envUint32("ISU_DNS_NX_BURST", 15))
			dns.nxRate = float64(envUint32("ISU_DNS_NX_RATE", 2))
			dns.nxTokens = dns.nxBurst
			dns.nxLast = time.Now()
		}

		store := NewStore(snap, wal, dns)
		replayed, err := store.ReplayWAL(*dataDir)
		if err != nil {
			log.Printf("WARNING: WAL replay error after %d records: %v", replayed, err)
		} else {
			log.Printf("Replayed %d WAL records in %v", replayed, time.Since(t0))
		}

		peers := NewPeerBroadcaster(envOr("ISU_EDGE_PEERS", "10.99.0.12:8081,10.99.0.13:8081"))
		store.onRegister = peers.BroadcastUser
		store.onSetIcon = peers.BroadcastIcon

		app.store = store
		app.peers = peers

		if dns != nil {
			if err := dns.Start(envOr("ISU_DNS_ADDR", ":53"), 8); err != nil {
				log.Fatalf("failed to start DNS server: %v", err)
			}
		}
	} else {
		coreAddr := envOr("ISU_CORE_ADDR", "10.99.0.11:8081")
		app.edge = NewEdgeCache(snap, coreAddr)
		go app.edge.SyncFromCore()
	}

	// Internal HTTP server (:8081)
	internalMux := http.NewServeMux()
	registerPprof(internalMux)
	internalMux.HandleFunc("/internal/metrics", func(w http.ResponseWriter, r *http.Request) {
		if app.metrics == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		app.metrics.ServeText(w, r)
	})
	internalMux.Handle("/", app)
	internalSrv := &http.Server{
		Addr:              internalAddr,
		Handler:           internalMux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("Internal HTTP listening on %s (role=%s)", internalAddr, role)
		if err := internalSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("internal http error: %v", err)
		}
	}()

	// Optional HTTP :80 (returns 404 like default nginx)
	if enableTLS && httpAddr != "" {
		go func() {
			s80 := &http.Server{
				Addr:    httpAddr,
				Handler: http.NotFoundHandler(),
			}
			_ = s80.ListenAndServe()
		}()
	}

	// Public HTTPS server (:443) or HTTP (:8080 if TLS disabled)
	if enableTLS {
		tlsDir := envOr("ISU_TLS_DIR", "/home/isucon/tls")
		uCert, err := tls.LoadX509KeyPair(filepath.Join(tlsDir, "_.u.isucon.dev.crt"), filepath.Join(tlsDir, "_.u.isucon.dev.key"))
		if err != nil {
			log.Fatalf("failed to load u.isucon.dev TLS cert: %v", err)
		}
		tCert, err := tls.LoadX509KeyPair(filepath.Join(tlsDir, "_.t.isucon.dev.crt"), filepath.Join(tlsDir, "_.t.isucon.dev.key"))
		if err != nil {
			log.Fatalf("failed to load t.isucon.dev TLS cert: %v", err)
		}

		tlsCfg := &tls.Config{
			MinVersion: tls.VersionTLS12,
			GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
				sn := strings.ToLower(hello.ServerName)
				if sn == "u.isucon.dev" || strings.HasSuffix(sn, ".u.isucon.dev") {
					return &uCert, nil
				}
				return &tCert, nil
			},
			NextProtos: []string{"h2", "http/1.1"},
		}
		if envOr("ISU_DISABLE_H2", "false") == "true" {
			tlsCfg.NextProtos = []string{"http/1.1"}
		}

		httpsSrv := &http.Server{
			Addr:              httpsAddr,
			Handler:           app,
			TLSConfig:         tlsCfg,
			ReadHeaderTimeout: 10 * time.Second,
		}
		if envOr("ISU_DISABLE_H2", "false") == "true" {
			httpsSrv.TLSNextProto = make(map[string]func(*http.Server, *tls.Conn, http.Handler))
		}

		ln, err := net.Listen("tcp", httpsAddr)
		if err != nil {
			log.Fatalf("failed to listen on %s: %v", httpsAddr, err)
		}
		tlsLn := tls.NewListener(ln, tlsCfg)
		go func() {
			log.Printf("Public HTTPS listening on %s (role=%s)", httpsAddr, role)
			if err := httpsSrv.Serve(tlsLn); err != nil && err != http.ErrServerClosed {
				log.Fatalf("https serve error: %v", err)
			}
		}()
	} else {
		unixSock := envOr("ISU_UNIX_SOCK", "")
		httpSrv := &http.Server{
			Handler:           app,
			ReadHeaderTimeout: 10 * time.Second,
		}
		if unixSock != "" {
			_ = os.MkdirAll(filepath.Dir(unixSock), 0777)
			_ = os.Remove(unixSock)
			ln, err := net.Listen("unix", unixSock)
			if err != nil {
				log.Fatalf("failed to listen unix %s: %v", unixSock, err)
			}
			_ = os.Chmod(unixSock, 0777)
			go func() {
				log.Printf("Public HTTP listening on unix:%s (role=%s)", unixSock, role)
				if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
					log.Fatalf("unix http serve error: %v", err)
				}
			}()
		} else {
			httpSrv.Addr = ":8080"
			go func() {
				log.Printf("Public HTTP listening on :8080 (role=%s)", role)
				if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					log.Fatalf("http serve error: %v", err)
				}
			}()
		}
	}

	// Graceful shutdown: flush WAL on SIGTERM/SIGINT/SIGQUIT
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	sig := <-sigCh
	log.Printf("Received signal %v, shutting down cleanly...", sig)
	if wal != nil {
		wal.Close()
		log.Printf("WAL flushed and synced cleanly")
	}
}
