package main

import (
	"encoding/gob"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gorilla/sessions"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

const (
	defaultSessionIDKey      = "SESSIONID"
	defaultSessionExpiresKey = "EXPIRES"
	defaultUserIDKey         = "USERID"
	defaultUsernameKey       = "USERNAME"
)

var secret = []byte("isucon13_session_cookiestore_defaultsecret")

func verifyUserSession(c echo.Context) error {
	s, err := session.Get(defaultSessionIDKey, c)
	if err != nil {
		return echo.NewHTTPError(401, "failed to get session")
	}
	ex, ok := s.Values[defaultSessionExpiresKey].(int64)
	if !ok {
		return echo.NewHTTPError(403, "missing session expiry")
	}
	if _, ok = s.Values[defaultUserIDKey].(int64); !ok {
		return echo.NewHTTPError(401, "missing user")
	}
	if time.Now().Unix() > ex {
		return echo.NewHTTPError(401, "session expired")
	}
	return nil
}
func sessionUID(c echo.Context) int64 {
	s, _ := session.Get(defaultSessionIDKey, c)
	id, _ := s.Values[defaultUserIDKey].(int64)
	return id
}
func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "bcrypt":
			runBcrypt()
			return
		case "dns":
			runDNS()
			return
		case "import":
			var err error
			defaultImage, err = os.ReadFile("../img/NoImage.jpg")
			if err != nil {
				log.Fatal(err)
			}
			defaultHash = hashImage(defaultImage)
			s, err := importDB()
			if err != nil {
				log.Fatal(err)
			}
			dataDir = "../data/baseline"
			if err = os.MkdirAll(dataDir, 0755); err != nil {
				log.Fatal(err)
			}
			if err = saveSnapshot(s); err != nil {
				log.Fatal(err)
			}
			log.Printf("imported %d users %d streams", len(s.Users), len(s.Streams))
			return
		}
	}
	if v := os.Getenv("ISUCON13_SESSION_SECRETKEY"); v != "" {
		secret = []byte(v)
	}
	dataDir = "../data/current"
	if _, err := os.Stat(dataDir); os.IsNotExist(err) {
		if err = resetStore(); err != nil {
			log.Fatal(err)
		}
	} else {
		if err = loadStore(); err != nil {
			log.Fatal(err)
		}
	}
	go func() { log.Print(http.ListenAndServe(":6060", nil)) }()
	names := make([]string, 0, len(state.Names))
	for n := range state.Names {
		names = append(names, n)
	}
	for attempts := 0; attempts < 30; attempts++ {
		if err := dnsUpdate(names, true); err == nil {
			break
		} else if attempts == 29 {
			log.Fatal(err)
		}
		time.Sleep(time.Second)
	}
	e := echo.New()
	e.HideBanner = true
	cs := sessions.NewCookieStore(secret)
	e.Use(session.Middleware(cs))
	e.GET("/internal/durability", func(c echo.Context) error {
		return c.JSON(200, map[string]int64{"sync_calls": syncCalls.Load(), "sync_requests": syncRequests.Load(), "sync_ns": syncNanos.Load()})
	})
	e.POST("/api/initialize", initialize)
	e.POST("/api/register", register)
	e.POST("/api/login", login)
	e.GET("/api/user/:username/icon", icon)
	e.GET("/internal/names", func(c echo.Context) error {
		mu.RLock()
		names := make([]string, 0, len(state.Names))
		for n := range state.Names {
			names = append(names, n)
		}
		mu.RUnlock()
		return c.JSON(200, names)
	})
	routes := []struct{ m, p string }{
		{"GET", "/api/tag"}, {"GET", "/api/user/:username/theme"}, {"POST", "/api/livestream/reservation"}, {"GET", "/api/livestream/search"}, {"GET", "/api/livestream"}, {"GET", "/api/user/:username/livestream"}, {"GET", "/api/livestream/:livestream_id"}, {"GET", "/api/livestream/:livestream_id/livecomment"}, {"POST", "/api/livestream/:livestream_id/livecomment"}, {"GET", "/api/livestream/:livestream_id/reaction"}, {"POST", "/api/livestream/:livestream_id/reaction"}, {"GET", "/api/livestream/:livestream_id/report"}, {"GET", "/api/livestream/:livestream_id/ngwords"}, {"POST", "/api/livestream/:livestream_id/livecomment/:livecomment_id/report"}, {"POST", "/api/livestream/:livestream_id/moderate"}, {"POST", "/api/livestream/:livestream_id/enter"}, {"DELETE", "/api/livestream/:livestream_id/exit"}, {"GET", "/api/user/me"}, {"GET", "/api/user/:username"}, {"GET", "/api/user/:username/statistics"}, {"POST", "/api/icon"}, {"GET", "/api/livestream/:livestream_id/statistics"}, {"GET", "/api/payment"}}
	for _, r := range routes {
		e.Add(r.m, r.p, handle)
	}
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		code := 500
		if he, ok := err.(*echo.HTTPError); ok {
			code = he.Code
		}
		if code >= 500 {
			log.Printf("%s: %v", c.Path(), err)
		}
		_ = c.JSON(code, map[string]string{"error": err.Error()})
	}
	log.Fatal(e.Start(":8080"))
}
func resetStore() error {
	f, err := os.Open("../data/baseline/snapshot.gob")
	if err != nil {
		return err
	}
	s := newState()
	err = gob.NewDecoder(f).Decode(s)
	f.Close()
	if err != nil {
		return err
	}
	dir := fmt.Sprintf("../data/gen-%d", time.Now().UnixNano())
	if err = os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	old := dataDir
	dataDir = dir
	if err = saveSnapshot(s); err != nil {
		dataDir = old
		return err
	}
	w, err := os.OpenFile(filepath.Join(dir, "wal.jsonl"), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
	if err != nil {
		dataDir = old
		return err
	}
	if err = w.Sync(); err != nil {
		return err
	}
	generationDir, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = generationDir.Sync()
	generationDir.Close()
	if err != nil {
		return err
	}
	link := "../data/current.new"
	os.Remove(link)
	if err = os.Symlink(filepath.Base(dir), link); err != nil {
		return err
	}
	if err = os.Rename(link, "../data/current"); err != nil {
		return err
	}
	d, err := os.Open("../data")
	if err != nil {
		return err
	}
	err = d.Sync()
	d.Close()
	if err != nil {
		return err
	}
	if wal != nil {
		if durability != nil {
			if err = durability.close(); err != nil {
				return err
			}
		}
		wal.Close()
	}
	wal = w
	durability = newDurableWAL(wal)
	state = s
	dataDir = "../data/current"
	if defaultImage == nil {
		defaultImage, err = os.ReadFile("../img/NoImage.jpg")
		defaultHash = hashImage(defaultImage)
	}
	return err
}
func initialize(c echo.Context) error {
	mu.Lock()
	defer mu.Unlock()
	if err := resetStore(); err != nil {
		return err
	}
	names := make([]string, 0, len(state.Names))
	for n := range state.Names {
		names = append(names, n)
	}
	if err := dnsUpdate(names, true); err != nil {
		return err
	}
	return c.JSON(200, map[string]string{"language": "golang", "lang": "golang"})
}
func handle(c echo.Context) error {
	p := c.Path()
	if p != "/api/tag" && p != "/api/livestream/search" && p != "/api/payment" {
		if err := verifyUserSession(c); err != nil {
			return err
		}
	}
	var sid int64
	var err error
	if c.Param("livestream_id") != "" {
		sid, err = strconv.ParseInt(c.Param("livestream_id"), 10, 64)
		if err != nil {
			return echo.NewHTTPError(400, "invalid livestream id")
		}
	}
	limit := int(^uint(0) >> 1)
	if q := c.QueryParam("limit"); q != "" {
		limit, err = strconv.Atoi(q)
		if err != nil || limit < 0 {
			return echo.NewHTTPError(400, "invalid limit")
		}
	}
	var obj any
	var code int
	if c.Request().Method == http.MethodGet {
		mu.RLock()
		obj, code, err = readAPI(c, sid, limit)
		mu.RUnlock()
	} else {
		if err := prepareRequest(c); err != nil {
			return err
		}
		mu.Lock()
		obj, code, err = writeAPI(c, sid)
		var synced <-chan error
		if err == nil {
			synced = durability.barrier()
		}
		mu.Unlock()
		if synced != nil {
			err = <-synced
		}
	}
	if err != nil {
		return err
	}
	if obj == nil {
		return c.NoContent(code)
	}
	return c.JSON(code, obj)
}
