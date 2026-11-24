package main

import (
	"context"
	"crypto/tls"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envUint32(key string, def uint32) uint32 {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.ParseUint(v, 10, 32); err == nil {
			return uint32(n)
		}
	}
	return def
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	initSessionSecret()

	role := envOr("ISU_ROLE", "leader")
	seedPath := envOr("ISU_SEED_PATH", "/home/isucon/webapp/seed.gob")
	fallbackImgPath := envOr("ISU_FALLBACK_IMG", "/home/isucon/webapp/img/NoImage.jpg")
	walPath := envOr("ISU_WAL_PATH", "/home/isucon/data/wal.bin")
	httpsAddr := envOr("ISU_HTTPS_ADDR", ":443")
	httpAddr := envOr("ISU_HTTP_ADDR", "")
	rpcListen := envOr("ISU_RPC_LISTEN", ":9000")
	leaderAddr := envOr("ISU_LEADER_ADDR", "10.99.0.11:9000")
	pushListen := envOr("ISU_PUSH_LISTEN", ":9001")
	peersEnv := envOr("ISU_PEERS", "")

	seed, err := LoadSeed(seedPath)
	if err != nil {
		log.Fatalf("failed to load seed %s: %v", seedPath, err)
	}
	fallbackImg, fallbackHash, err := LoadFallbackIcon(fallbackImgPath)
	if err != nil {
		log.Fatalf("failed to load fallback image %s: %v", fallbackImgPath, err)
	}

	var backend Backend
	var store *Store
	var rpcSrv *RPCServer
	var dnsSrv *DNSServer
	var dnsConn interface{ Close() error }
	var sv *Server

	if role == "leader" {
		store, err = NewStore(seed, fallbackImg, fallbackHash, walPath)
		if err != nil {
			log.Fatalf("failed to init store: %v", err)
		}
		var peerHub *PeerHub
		if peersEnv != "" {
			peerHub = NewPeerHub(strings.Split(peersEnv, ","))
			store.onIconUpdated = peerHub.PushIconAsync
			store.onResetAll = peerHub.PushResetSync
		}

		if envOr("ISU_DNS_ENABLE", "false") == "true" {
			apexIP := envOr("ISU_DNS_APEX_IP", "10.99.0.11")
			pipeIPs := envOr("ISU_DNS_PIPE_IPS", "10.99.0.11,10.99.0.12,10.99.0.13")
			subIPs := envOr("ISU_DNS_SUB_IPS", "10.99.0.11,10.99.0.12,10.99.0.13")
			pipeTTL := envUint32("ISU_DNS_PIPE_TTL", 0)
			subTTL := envUint32("ISU_DNS_SUB_TTL", 300)
			dnsSrv = NewDNSServer(apexIP, pipeIPs, subIPs, pipeTTL, subTTL)
			dnsSrv.Reset(store.AllDNSNames())
			store.onDNSReset = dnsSrv.Reset
			store.onDNSAdd = dnsSrv.Add
			c, err := dnsSrv.Start(envOr("ISU_DNS_ADDR", ":53"))
			if err != nil {
				log.Fatalf("failed to start DNS server: %v", err)
			}
			dnsConn = c
		}

		if rpcListen != "" {
			rpcSrv, err = StartRPCServer(rpcListen, store)
			if err != nil {
				log.Fatalf("failed to start RPC server: %v", err)
			}
		}
		backend = store
		sv = NewServer(backend)
	} else {
		tagsJSON := BuildTagsJSON(seed.Tags)
		var rb *RemoteBackend
		rb, err = NewRemoteBackend(leaderAddr, tagsJSON, pushListen, func() {
			if sv != nil {
				sv.ClearVerifiedCache()
			}
		})
		if err != nil {
			log.Fatalf("failed to start remote backend: %v", err)
		}
		backend = rb
		sv = NewServer(backend)
	}

	if pprofAddr := envOr("ISU_PPROF_ADDR", ""); pprofAddr != "" {
		go func() {
			log.Printf("pprof listening on %s", pprofAddr)
			_ = http.ListenAndServe(pprofAddr, nil)
		}()
	}

	var httpsServer *http.Server
	if httpsAddr != "" {
		certPath := envOr("ISU_TLS_CERT", "/home/isucon/tls/_.u.isucon.dev.crt")
		keyPath := envOr("ISU_TLS_KEY", "/home/isucon/tls/_.u.isucon.dev.key")
		fbCertPath := envOr("ISU_TLS_FALLBACK_CERT", "/home/isucon/tls/_.t.isucon.dev.crt")
		fbKeyPath := envOr("ISU_TLS_FALLBACK_KEY", "/home/isucon/tls/_.t.isucon.dev.key")

		mainCert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			log.Fatalf("failed to load main TLS cert: %v", err)
		}
		fbCert, err := tls.LoadX509KeyPair(fbCertPath, fbKeyPath)
		if err != nil {
			log.Fatalf("failed to load fallback TLS cert: %v", err)
		}
		tlsCfg := &tls.Config{
			MinVersion: tls.VersionTLS12,
			GetCertificate: func(chi *tls.ClientHelloInfo) (*tls.Certificate, error) {
				sn := strings.ToLower(chi.ServerName)
				if sn == "u.isucon.dev" || strings.HasSuffix(sn, ".u.isucon.dev") {
					return &mainCert, nil
				}
				return &fbCert, nil
			},
		}
		httpsServer = &http.Server{
			Addr:         httpsAddr,
			Handler:      sv,
			TLSConfig:    tlsCfg,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
			IdleTimeout:  120 * time.Second,
		}
		go func() {
			log.Printf("HTTPS server listening on %s (role=%s)", httpsAddr, role)
			ln, err := net.Listen("tcp4", httpsAddr)
			if err != nil {
				log.Fatalf("net.Listen failed: %v", err)
			}
			if err := httpsServer.ServeTLS(ln, "", ""); err != nil && err != http.ErrServerClosed {
				log.Fatalf("https ServeTLS error: %v", err)
			}
		}()
	}

	var httpServer *http.Server
	if httpAddr != "" {
		httpServer = &http.Server{
			Addr:         httpAddr,
			Handler:      sv,
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
			IdleTimeout:  120 * time.Second,
		}
		go func() {
			log.Printf("HTTP server listening on %s (role=%s)", httpAddr, role)
			if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("http Serve error: %v", err)
			}
		}()
	}

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("Received signal %v, shutting down gracefully...", sig)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if httpsServer != nil {
		_ = httpsServer.Shutdown(ctx)
	}
	if httpServer != nil {
		_ = httpServer.Shutdown(ctx)
	}
	if dnsConn != nil {
		_ = dnsConn.Close()
	}
	if rpcSrv != nil {
		rpcSrv.Close()
	}
	if store != nil {
		store.Close()
	}
	log.Printf("Shutdown complete")
}
