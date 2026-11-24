package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo/v4"
	echolog "github.com/labstack/gommon/log"
	"golang.org/x/net/http2"
)

const (
	listenPort                     = 8080
	powerDNSSubdomainAddressEnvKey = "ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS"
)

var (
	powerDNSSubdomainAddress string
	dbConn                   *sqlx.DB
	secret                   = []byte("isucon13_session_cookiestore_defaultsecret")
)

func init() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	if secretKey, ok := os.LookupEnv("ISUCON13_SESSION_SECRETKEY"); ok {
		secret = []byte(secretKey)
	}
}

type InitializeResponse struct {
	Language string `json:"language"`
}

func connectDB(logger echo.Logger) (*sqlx.DB, error) {
	const (
		networkTypeEnvKey = "ISUCON13_MYSQL_DIALCONFIG_NET"
		addrEnvKey        = "ISUCON13_MYSQL_DIALCONFIG_ADDRESS"
		portEnvKey        = "ISUCON13_MYSQL_DIALCONFIG_PORT"
		userEnvKey        = "ISUCON13_MYSQL_DIALCONFIG_USER"
		passwordEnvKey    = "ISUCON13_MYSQL_DIALCONFIG_PASSWORD"
		dbNameEnvKey      = "ISUCON13_MYSQL_DIALCONFIG_DATABASE"
		parseTimeEnvKey   = "ISUCON13_MYSQL_DIALCONFIG_PARSETIME"
	)

	conf := mysql.NewConfig()

	conf.Net = "tcp"
	conf.Addr = net.JoinHostPort("127.0.0.1", "3306")
	conf.User = "isucon"
	conf.Passwd = "isucon"
	conf.DBName = "isupipe"
	conf.ParseTime = true
	conf.InterpolateParams = true

	if v, ok := os.LookupEnv(networkTypeEnvKey); ok {
		conf.Net = v
	}
	if addr, ok := os.LookupEnv(addrEnvKey); ok {
		if port, ok2 := os.LookupEnv(portEnvKey); ok2 {
			conf.Addr = net.JoinHostPort(addr, port)
		} else {
			conf.Addr = net.JoinHostPort(addr, "3306")
		}
	}
	if v, ok := os.LookupEnv(userEnvKey); ok {
		conf.User = v
	}
	if v, ok := os.LookupEnv(passwordEnvKey); ok {
		conf.Passwd = v
	}
	if v, ok := os.LookupEnv(dbNameEnvKey); ok {
		conf.DBName = v
	}
	if v, ok := os.LookupEnv(parseTimeEnvKey); ok {
		parseTime, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("failed to parse environment variable '%s' as bool: %+v", parseTimeEnvKey, err)
		}
		conf.ParseTime = parseTime
	}

	db, err := sqlx.Open("mysql", conf.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(64)
	db.SetMaxIdleConns(64)
	db.SetConnMaxLifetime(5 * time.Minute)

	var pingErr error
	for i := 0; i < 60; i++ {
		pingErr = db.Ping()
		if pingErr == nil {
			return db, nil
		}
		log.Printf("waiting for mysql (%s): %v", conf.Addr, pingErr)
		time.Sleep(500 * time.Millisecond)
	}
	return nil, pingErr
}

func initializeHandler(c echo.Context) error {
	ctx := c.Request().Context()
	resetEPMetrics()

	flushAllNodesSync()

	if out, err := exec.Command("../sql/init.sh").CombinedOutput(); err != nil {
		c.Logger().Warnf("init.sh failed with err=%s", string(out))
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to initialize: "+err.Error())
	}

	if err := reloadAllCaches(ctx); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to reload caches: "+err.Error())
	}
	if !isClustered() {
		if err := loadDNSFromZoneAndDB(ctx, "../pdns/u.isucon.dev.zone", dbConn); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to reload dns: "+err.Error())
		}
	} else {
		if err := reloadOtherNodesSync(); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to reload peer nodes: "+err.Error())
		}
	}

	c.Request().Header.Add("Content-Type", "application/json;charset=utf-8")
	return c.JSON(http.StatusOK, InitializeResponse{
		Language: "golang",
	})
}

// Fast-path wrapper that handles GET /api/user/:username/icon directly without Echo overhead!
func wrapFastIconHandler(next http.Handler) http.Handler {
	const prefix = "/api/user/"
	const suffix = "/icon"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if r.Method == http.MethodGet && len(p) > len(prefix)+len(suffix) && strings.HasPrefix(p, prefix) && strings.HasSuffix(p, suffix) {
			username := p[len(prefix) : len(p)-len(suffix)]
			if strings.IndexByte(username, '/') < 0 {
				if s := timelineSlot(); s >= 0 {
					secReqs[s].Add(1)
				}
				u, ok := getUserByName(username)
				if !ok {
					w.Header().Set("Content-Type", "application/json; charset=UTF-8")
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"error":"code=404, message=not found user that has the given username"}`))
					return
				}
				iconHash := getIconHash(u.ID)
				if inm := r.Header.Get("If-None-Match"); inm != "" {
					trimmed := strings.TrimSpace(inm)
					if strings.HasPrefix(trimmed, "W/") {
						trimmed = strings.TrimPrefix(trimmed, "W/")
					}
					trimmed = strings.Trim(trimmed, "\"")
					if trimmed == iconHash {
						icon304Cnt.Add(1)
						w.WriteHeader(http.StatusNotModified)
						return
					}
				}
				img, actualHash, err := getIconImage(r.Context(), u.ID)
				if err != nil {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				icon200Cnt.Add(1)
				w.Header().Set("Content-Type", "image/jpeg")
				w.Header().Set("ETag", `"`+actualHash+`"`)
				w.Header().Set("Content-Length", strconv.Itoa(len(img)))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(img)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func main() {
	if os.Getenv("ISUCON13_BCRYPT_WORKER_ONLY") == "1" {
		addr := os.Getenv("ISUCON13_BCRYPT_LISTEN")
		if addr == "" {
			addr = ":8082"
		}
		startBcryptWorkerServer(addr)
		return
	}

	initClusterConfig()
	initCryptoPeers()
	if err := initFallbackImage(); err != nil {
		log.Fatalf("failed to load fallback image: %v", err)
	}

	startDebugServer()

	e := echo.New()
	e.Debug = false
	e.HideBanner = true
	e.Logger.SetLevel(echolog.ERROR)
	e.Use(metricsMiddleware)

	// 初期化
	e.POST("/api/initialize", initializeHandler)

	// top
	e.GET("/api/tag", getTagHandler)
	e.GET("/api/user/:username/theme", getStreamerThemeHandler)

	// livestream
	e.POST("/api/livestream/reservation", reserveLivestreamHandler)
	e.GET("/api/livestream/search", searchLivestreamsHandler)
	e.GET("/api/livestream", getMyLivestreamsHandler)
	e.GET("/api/user/:username/livestream", getUserLivestreamsHandler)
	e.GET("/api/livestream/:livestream_id", getLivestreamHandler)
	e.GET("/api/livestream/:livestream_id/livecomment", getLivecommentsHandler)
	e.POST("/api/livestream/:livestream_id/livecomment", postLivecommentHandler)
	e.POST("/api/livestream/:livestream_id/reaction", postReactionHandler)
	e.GET("/api/livestream/:livestream_id/reaction", getReactionsHandler)

	e.GET("/api/livestream/:livestream_id/report", getLivecommentReportsHandler)
	e.GET("/api/livestream/:livestream_id/ngwords", getNgwords)
	e.POST("/api/livestream/:livestream_id/livecomment/:livecomment_id/report", reportLivecommentHandler)
	e.POST("/api/livestream/:livestream_id/moderate", moderateHandler)

	e.POST("/api/livestream/:livestream_id/enter", enterLivestreamHandler)
	e.DELETE("/api/livestream/:livestream_id/exit", exitLivestreamHandler)

	// user
	e.POST("/api/register", registerHandler)
	e.POST("/api/login", loginHandler)
	e.GET("/api/user/me", getMeHandler)
	e.GET("/api/user/:username", getUserHandler)
	e.GET("/api/user/:username/statistics", getUserStatisticsHandler)
	e.GET("/api/user/:username/icon", getIconHandler)
	e.POST("/api/icon", postIconHandler)

	// stats
	e.GET("/api/livestream/:livestream_id/statistics", getLivestreamStatisticsHandler)

	// 課金情報
	e.GET("/api/payment", GetPaymentResult)

	e.HTTPErrorHandler = errorResponseHandler

	conn, err := connectDB(e.Logger)
	if err != nil {
		e.Logger.Errorf("failed to connect db: %v", err)
		os.Exit(1)
	}
	defer conn.Close()
	dbConn = conn

	subdomainAddr, ok := os.LookupEnv(powerDNSSubdomainAddressEnvKey)
	if !ok {
		e.Logger.Errorf("environ %s must be provided", powerDNSSubdomainAddressEnvKey)
		os.Exit(1)
	}
	powerDNSSubdomainAddress = subdomainAddr

	if err := reloadAllCaches(context.Background()); err != nil {
		log.Fatalf("failed to load initial caches: %v", err)
	}

	startDBFlusher()
	startInternalServer()

	dnsListen := os.Getenv("ISUCON13_DNS_LISTEN")
	if dnsListen == "" {
		dnsListen = ":53"
	}
	if dnsListen != "disabled" {
		dnsAddrs := os.Getenv("ISUCON13_DNS_TARGET_IPS")
		if dnsAddrs == "" {
			dnsAddrs = powerDNSSubdomainAddress
		}
		if err := initDNSIPs(dnsAddrs); err != nil {
			log.Fatalf("failed to init dns ips: %v", err)
		}
		if err := loadDNSFromZoneAndDB(context.Background(), "../pdns/u.isucon.dev.zone", dbConn); err != nil {
			log.Fatalf("failed to load initial dns zone: %v", err)
		}
		if err := startDNSServer(dnsListen); err != nil {
			log.Fatalf("failed to start DNS server on %s: %v", dnsListen, err)
		}
	}

	handler := wrapFastIconHandler(e)
	var srv *http.Server

	if os.Getenv("ISUCON13_DIRECT_TLS") == "1" {
		uCert, err := tls.LoadX509KeyPair("/home/isucon/webapp/tls/_.u.isucon.dev.crt", "/home/isucon/webapp/tls/_.u.isucon.dev.key")
		if err != nil {
			log.Fatalf("load u.isucon.dev cert: %v", err)
		}
		if wrapped, err := wrapTLSPrivateKey(uCert.PrivateKey); err == nil {
			uCert.PrivateKey = wrapped
		}
		tCert, err := tls.LoadX509KeyPair("/home/isucon/webapp/tls/_.t.isucon.dev.crt", "/home/isucon/webapp/tls/_.t.isucon.dev.key")
		if err != nil {
			log.Fatalf("load t.isucon.dev cert: %v", err)
		}
		nextProtos := []string{"h2", "http/1.1"}
		if os.Getenv("ISUCON13_DISABLE_H2") == "1" {
			nextProtos = []string{"http/1.1"}
		}
		tlsCfg := &tls.Config{
			MinVersion:       tls.VersionTLS13,
			CurvePreferences: []tls.CurveID{tls.X25519},
			NextProtos:       nextProtos,
			GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
				sn := strings.ToLower(hello.ServerName)
				if sn == "u.isucon.dev" || strings.HasSuffix(sn, ".u.isucon.dev") {
					return &uCert, nil
				}
				return &tCert, nil
			},
		}
		srv = &http.Server{
			Addr:      ":443",
			Handler:   handler,
			TLSConfig: tlsCfg,
		}
		if os.Getenv("ISUCON13_DISABLE_H2") == "1" {
			srv.TLSNextProto = make(map[string]func(*http.Server, *tls.Conn, http.Handler))
		} else {
			_ = http2.ConfigureServer(srv, &http2.Server{
				MaxConcurrentStreams:         2048,
				MaxReadFrameSize:             1 << 20,
				MaxUploadBufferPerConnection: 4 << 20,
				MaxUploadBufferPerStream:     1 << 20,
			})
		}
	} else {
		srv = &http.Server{
			Handler: handler,
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		sig := <-sigCh
		log.Printf("received signal %v, shutting down gracefully and flushing DB queue...", sig)
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		stopDBFlusher()
		log.Printf("graceful shutdown complete")
		os.Exit(0)
	}()

	if os.Getenv("ISUCON13_DIRECT_TLS") == "1" {
		log.Printf("starting direct HTTPS (TLS 1.3 + HTTP/2) server on :443")
		ln, err := net.Listen("tcp4", ":443")
		if err != nil {
			log.Fatalf("listen :443 failed: %v", err)
		}
		tlsLn := tls.NewListener(ln, srv.TLSConfig)
		if err := srv.Serve(tlsLn); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTPS server error: %v", err)
			stopDBFlusher()
			os.Exit(1)
		}
	} else if unixSock := os.Getenv("ISUCON13_UNIX_SOCK"); unixSock != "" {
		_ = os.Remove(unixSock)
		ln, err := net.Listen("unix", unixSock)
		if err != nil {
			log.Fatalf("failed to listen unix socket %s: %v", unixSock, err)
		}
		_ = os.Chmod(unixSock, 0777)
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("Unix server error: %v", err)
			stopDBFlusher()
			os.Exit(1)
		}
	} else {
		srv.Addr = net.JoinHostPort("", strconv.Itoa(listenPort))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
			stopDBFlusher()
			os.Exit(1)
		}
	}
}

type ErrorResponse struct {
	Error string `json:"error"`
}

func errorResponseHandler(err error, c echo.Context) {
	if he, ok := err.(*echo.HTTPError); ok {
		if he.Code >= 500 {
			c.Logger().Errorf("error at %s: %+v", c.Path(), err)
		}
		if e := c.JSON(he.Code, &ErrorResponse{Error: err.Error()}); e != nil {
			c.Logger().Errorf("%+v", e)
		}
		return
	}

	c.Logger().Errorf("error at %s: %+v", c.Path(), err)
	if e := c.JSON(http.StatusInternalServerError, &ErrorResponse{Error: err.Error()}); e != nil {
		c.Logger().Errorf("%+v", e)
	}
}
