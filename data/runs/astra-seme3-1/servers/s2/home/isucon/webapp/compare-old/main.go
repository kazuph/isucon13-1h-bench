package main

import (
	"fmt"
	"github.com/go-sql-driver/mysql"
	"github.com/gorilla/sessions"
	"github.com/jmoiron/sqlx"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
)

var dbConn *sqlx.DB

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

	// 環境変数がセットされていなかった場合でも一旦動かせるように、デフォルト値を入れておく
	// この挙動を変更して、エラーを出すようにしてもいいかもしれない
	conf.Net = "tcp"
	conf.Addr = net.JoinHostPort("127.0.0.1", "3306")
	conf.User = "isucon"
	conf.Passwd = "isucon"
	conf.DBName = "isupipe"
	conf.ParseTime = true

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
	db.SetMaxOpenConns(10)

	if err := db.Ping(); err != nil {
		return nil, err
	}

	return db, nil
}

func initializeHandler(c echo.Context) error {
	mu.Lock()
	defer mu.Unlock()
	if out, e := exec.Command("../sql/init.sh").CombinedOutput(); e != nil {
		return fmt.Errorf("init: %s: %w", out, e)
	}
	s, e := loadDB(dbConn)
	if e != nil {
		return e
	}
	// Initialization replaces all old data. Snapshot rename precedes opening the new journal.
	if journal != nil {
		journal.Close()
	}
	if e = os.Remove(dataDir + "/journal.jsonl"); e != nil && !os.IsNotExist(e) {
		return e
	}
	if e = saveSnapshot(s); e != nil {
		return e
	}
	journal, e = os.OpenFile(dataDir+"/journal.jsonl", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if e != nil {
		return e
	}
	st = s
	if e = pushDNS(st.DNS, true); e != nil {
		return e
	}
	return c.JSON(200, map[string]string{"language": "golang", "lang": "golang"})
}
func main() {
	if os.Getenv("DNS_ONLY") == "1" {
		dnsMain()
		return
	}
	if os.Getenv("AUTH_ONLY") == "1" {
		serveAuth()
		return
	}
	var err error
	fallback, err = os.ReadFile("../img/NoImage.jpg")
	if err != nil {
		panic(err)
	}
	fallbackHash = hashImage(fallback)
	os.MkdirAll(dataDir, 0755)
	e := echo.New()
	e.HideBanner = true
	secret := []byte("isucon13_session_cookiestore_defaultsecret")
	if v := os.Getenv("ISUCON13_SESSION_SECRETKEY"); v != "" {
		secret = []byte(v)
	}
	cookieStore := sessions.NewCookieStore(secret)
	cookieStore.Options.Domain = "*.u.isucon.dev"
	e.Use(session.Middleware(cookieStore))
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		code := 500
		if h, ok := err.(*echo.HTTPError); ok {
			code = h.Code
		}
		if code >= 500 {
			log.Print(err)
		}
		c.JSON(code, map[string]string{"error": err.Error()})
	}
	dbConn, err = connectDB(e.Logger)
	if err != nil {
		panic(err)
	}
	if err = loadState(); err != nil {
		if !os.IsNotExist(err) {
			panic(err)
		}
		st, err = loadDB(dbConn)
		if err != nil {
			panic(err)
		}
		if err = saveSnapshot(st); err != nil {
			panic(err)
		}
		journal, err = os.OpenFile(dataDir+"/journal.jsonl", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			panic(err)
		}
	}
	if err = pushDNS(st.DNS, true); err != nil {
		log.Print(err)
	}
	e.GET("/internal/dns", func(c echo.Context) error { mu.RLock(); defer mu.RUnlock(); return c.JSON(200, st.DNS) })
	e.GET("/internal/checksum", checksumHandler)
	e.POST("/api/initialize", initializeHandler)
	e.POST("/api/register", registerHandler)
	e.POST("/api/login", loginHandler)
	for _, p := range []string{"/api/tag", "/api/payment", "/api/user/me", "/api/user/:username", "/api/user/:username/theme", "/api/user/:username/icon", "/api/user/:username/livestream", "/api/user/:username/statistics", "/api/livestream", "/api/livestream/search", "/api/livestream/:livestream_id", "/api/livestream/:livestream_id/livecomment", "/api/livestream/:livestream_id/reaction", "/api/livestream/:livestream_id/ngwords", "/api/livestream/:livestream_id/report", "/api/livestream/:livestream_id/statistics"} {
		e.GET(p, readAPI)
	}
	for _, p := range []string{"/api/icon", "/api/livestream/reservation", "/api/livestream/:livestream_id/livecomment", "/api/livestream/:livestream_id/reaction", "/api/livestream/:livestream_id/moderate", "/api/livestream/:livestream_id/livecomment/:livecomment_id/report", "/api/livestream/:livestream_id/enter"} {
		e.POST(p, writeAPI)
	}
	e.DELETE("/api/livestream/:livestream_id/exit", writeAPI)
	log.Fatal(e.Start(":8080"))
}
