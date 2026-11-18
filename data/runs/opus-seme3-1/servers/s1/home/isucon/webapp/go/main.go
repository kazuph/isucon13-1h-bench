package main

import (
	"crypto/tls"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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
)

var (
	dbConn *sqlx.DB
	secret = []byte("isucon13_session_cookiestore_defaultsecret")
)

func connectDB() *sqlx.DB {
	conf := mysql.NewConfig()
	conf.Net = "tcp"
	conf.Addr = net.JoinHostPort("127.0.0.1", "3306")
	conf.User = "isucon"
	conf.Passwd = "isucon"
	conf.DBName = "isupipe"
	conf.ParseTime = true
	conf.InterpolateParams = true
	if v, ok := os.LookupEnv("ISUCON13_MYSQL_DIALCONFIG_ADDRESS"); ok {
		port := os.Getenv("ISUCON13_MYSQL_DIALCONFIG_PORT")
		if port == "" {
			port = "3306"
		}
		conf.Addr = net.JoinHostPort(v, port)
	}
	if v, ok := os.LookupEnv("ISUCON13_MYSQL_DIALCONFIG_PASSWORD"); ok {
		conf.Passwd = v
	}
	for {
		db, err := sqlx.Open("mysql", conf.FormatDSN())
		if err == nil {
			if err = db.Ping(); err == nil {
				db.SetMaxOpenConns(8)
				db.SetMaxIdleConns(8)
				return db
			}
		}
		log.Printf("db connect retry: %v", err)
		time.Sleep(time.Second)
	}
}

// ---------- write-behind persistence ----------

type dbOp struct {
	q    string
	args []any
	done chan struct{}
}

var opCh = make(chan dbOp, 100000)

func persist(q string, args ...any) {
	opCh <- dbOp{q: q, args: args}
}

func flushDB() {
	d := make(chan struct{})
	opCh <- dbOp{done: d}
	<-d
}

func writer() {
	batch := make([]dbOp, 0, 1024)
	for {
		op := <-opCh
		batch = append(batch[:0], op)
	drain:
		for len(batch) < 1000 {
			select {
			case o := <-opCh:
				batch = append(batch, o)
			default:
				break drain
			}
		}
		for {
			err := execBatch(batch)
			if err == nil {
				break
			}
			log.Printf("persist error: %v", err)
			time.Sleep(200 * time.Millisecond)
		}
		for _, o := range batch {
			if o.done != nil {
				close(o.done)
			}
		}
	}
}

func execBatch(batch []dbOp) error {
	tx, err := dbConn.Begin()
	if err != nil {
		return err
	}
	for _, o := range batch {
		if o.q == "" {
			continue
		}
		if _, err := tx.Exec(o.q, o.args...); err != nil {
			log.Printf("persist exec error (skipped): %v q=%s", err, o.q)
		}
	}
	return tx.Commit()
}

// ---------- session ----------

const cookieName = "SESSIONID"

func signSession(uid int64, exp int64) string {
	payload := strconv.FormatInt(uid, 10) + "." + strconv.FormatInt(exp, 10)
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(payload))
	return payload + "." + hex.EncodeToString(m.Sum(nil))[:32]
}

// returns userID or http error
func getSession(c echo.Context) (int64, error) {
	ck, err := c.Cookie(cookieName)
	if err != nil || ck.Value == "" {
		return 0, echo.NewHTTPError(http.StatusForbidden, "failed to get EXPIRES value from session")
	}
	parts := strings.Split(ck.Value, ".")
	if len(parts) != 3 {
		return 0, echo.NewHTTPError(http.StatusUnauthorized, "failed to get session")
	}
	uid, err1 := strconv.ParseInt(parts[0], 10, 64)
	exp, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil || signSession(uid, exp) != ck.Value {
		return 0, echo.NewHTTPError(http.StatusUnauthorized, "failed to get session")
	}
	if time.Now().Unix() > exp {
		return 0, echo.NewHTTPError(http.StatusUnauthorized, "session has expired")
	}
	return uid, nil
}

// ---------- initialize ----------

type InitializeResponse struct {
	Language string `json:"language"`
}

func initializeHandler(c echo.Context) error {
	flushDB()
	if out, err := exec.Command("../sql/init_fast.sh").CombinedOutput(); err != nil {
		log.Printf("init failed: %s", string(out))
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to initialize: "+err.Error())
	}
	loadAll(dbConn)
	return c.JSON(http.StatusOK, InitializeResponse{Language: "golang"})
}

func main() {
	loadFallback()
	if v := os.Getenv("ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS"); v != "" {
		dnsAddr = v
	}
	dbConn = connectDB()
	loadAll(dbConn)
	go writer()
	go startDNS()

	e := echo.New()
	e.HTTPErrorHandler = errorResponseHandler

	e.POST("/api/initialize", initializeHandler)
	e.GET("/api/tag", getTagHandler)
	e.GET("/api/user/:username/theme", getStreamerThemeHandler)
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
	e.POST("/api/register", registerHandler)
	e.POST("/api/login", loginHandler)
	e.GET("/api/user/me", getMeHandler)
	e.GET("/api/user/:username", getUserHandler)
	e.GET("/api/user/:username/statistics", getUserStatisticsHandler)
	e.GET("/api/user/:username/icon", getIconHandler)
	e.POST("/api/icon", postIconHandler)
	e.GET("/api/livestream/:livestream_id/statistics", getLivestreamStatisticsHandler)
	e.GET("/api/payment", getPaymentHandler)

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT)
		<-sig
		flushDB()
		os.Exit(0)
	}()

	listen := ":8080"
	if v := os.Getenv("LISTEN"); v != "" {
		listen = v
	}
	e.HideBanner = true
	if tl := os.Getenv("LISTEN_TLS"); tl != "" {
		cert, err := tls.LoadX509KeyPair(os.Getenv("TLS_CERT"), os.Getenv("TLS_KEY"))
		if err != nil {
			log.Fatal(err)
		}
		srv := &http.Server{
			Addr:        tl,
			Handler:     e,
			IdleTimeout: 120 * time.Second,
			TLSConfig:   &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		}
		if os.Getenv("HTTP2") != "1" {
			srv.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){}
		}
		go func() {
			if err := srv.ListenAndServeTLS("", ""); err != nil {
				log.Fatal(err)
			}
		}()
	}
	if err := e.Start(listen); err != nil {
		log.Fatal(err)
	}
}

type ErrorResponse struct {
	Error string `json:"error"`
}

func errorResponseHandler(err error, c echo.Context) {
	if he, ok := err.(*echo.HTTPError); ok {
		c.JSON(he.Code, &ErrorResponse{Error: fmt.Sprint(he.Message)})
		return
	}
	c.JSON(http.StatusInternalServerError, &ErrorResponse{Error: err.Error()})
}
