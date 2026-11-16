package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/gorilla/sessions"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/net/dns/dnsmessage"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	_ "net/http/pprof"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultSessionIDKey = "SESSIONID"
const defaultSessionExpiresKey = "EXPIRES"
const defaultUserIDKey = "USERID"
const defaultUsernameKey = "USERNAME"

var dnsNames = map[string]bool{}
var secret = []byte("isucon13_session_cookiestore_defaultsecret")
var authClient = &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{MaxIdleConns: 128, MaxIdleConnsPerHost: 128}}

type AuthRequest struct {
	Password string
	Hash     string
	Generate bool
}

func authWork(req AuthRequest) (string, error) {
	b, _ := json.Marshal(req)
	r, e := authClient.Post("http://10.99.0.13:8090/bcrypt", "application/json", bytes.NewReader(b))
	if e != nil {
		return "", e
	}
	defer r.Body.Close()
	data, e := io.ReadAll(r.Body)
	if e != nil {
		return "", e
	}
	if r.StatusCode != 200 {
		return "", fmt.Errorf("bcrypt: %s", data)
	}
	return string(data), nil
}
func authServer() {
	http.HandleFunc("/bcrypt", func(w http.ResponseWriter, r *http.Request) {
		var req AuthRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		if req.Generate {
			h, e := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.MinCost)
			if e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			w.Write(h)
		} else {
			if e := bcrypt.CompareHashAndPassword([]byte(req.Hash), []byte(req.Password)); e != nil {
				http.Error(w, e.Error(), 401)
				return
			}
			w.Write([]byte("ok"))
		}
	})
	log.Fatal(http.ListenAndServe(":8090", nil))
}

type cachedSession struct{ ID, Expiry int64 }

var sessionCache sync.Map

func verifyUserSession(c echo.Context) error {
	cookie, cookieErr := c.Cookie(defaultSessionIDKey)
	if cookieErr == nil {
		if v, ok := sessionCache.Load(cookie.Value); ok {
			a := v.(cachedSession)
			if time.Now().Unix() > a.Expiry {
				return echo.NewHTTPError(401, "expired session")
			}
			c.Set("userID", a.ID)
			return nil
		}
	}

	s, e := session.Get(defaultSessionIDKey, c)
	if e != nil {
		return echo.NewHTTPError(401, "invalid session")
	}
	expiry, ok := s.Values[defaultSessionExpiresKey].(int64)
	if !ok {
		return echo.NewHTTPError(403, "missing session")
	}
	if _, ok = s.Values[defaultUserIDKey].(int64); !ok {
		return echo.NewHTTPError(401, "missing user")
	}
	if time.Now().Unix() > expiry {
		return echo.NewHTTPError(401, "expired session")
	}
	id := s.Values[defaultUserIDKey].(int64)
	c.Set("userID", id)
	if cookieErr == nil {
		sessionCache.Store(cookie.Value, cachedSession{ID: id, Expiry: expiry})
	}
	return nil
}
func uid(c echo.Context) int64 {
	if id, ok := c.Get("userID").(int64); ok {
		return id
	}

	s, _ := session.Get(defaultSessionIDKey, c)
	id, _ := s.Values[defaultUserIDKey].(int64)
	return id
}
func num(c echo.Context, key string) (int64, error) {
	n, e := strconv.ParseInt(c.Param(key), 10, 64)
	if e != nil {
		return 0, echo.NewHTTPError(400, "invalid integer")
	}
	return n, nil
}
func bind(c echo.Context, v interface{}) error {
	if e := json.NewDecoder(c.Request().Body).Decode(v); e != nil {
		return echo.NewHTTPError(400, "invalid JSON")
	}
	return nil
}
func limit(c echo.Context) (int, error) {
	if c.QueryParam("limit") == "" {
		return int(^uint(0) >> 1), nil
	}
	n, e := strconv.Atoi(c.QueryParam("limit"))
	if e != nil || n < 0 {
		return 0, echo.NewHTTPError(400, "invalid limit")
	}
	return n, nil
}
func read(f echo.HandlerFunc, auth bool) echo.HandlerFunc {
	return func(c echo.Context) error {
		if auth {
			if e := verifyUserSession(c); e != nil {
				return e
			}
		}
		mu.RLock()
		defer mu.RUnlock()
		return f(c)
	}
}
func write(f echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if e := verifyUserSession(c); e != nil {
			return e
		}
		mu.Lock()
		defer mu.Unlock()
		return f(c)
	}
}

// Writes are synced before their successful response is exposed to the client.
func done(c echo.Context, code int, v interface{}) error {
	c.Set("dirty", true)
	if v == nil {
		return c.NoContent(code)
	}
	return c.JSON(code, v)
}
func initialize(c echo.Context) error {
	mu.Lock()
	defer mu.Unlock()
	if out, e := exec.Command("../sql/init.sh").CombinedOutput(); e != nil {
		return fmt.Errorf("initialize: %s: %w", out, e)
	}
	if e := loadSQL(); e != nil {
		return e
	}
	walMu.Lock()
	defer walMu.Unlock()
	if wal != nil {
		wal.Close()
	}
	if e := snapshot(); e != nil {
		return e
	}
	var e error
	wal, e = os.OpenFile("state.wal", os.O_CREATE|os.O_TRUNC|os.O_WRONLY|os.O_APPEND, 0600)
	if e != nil {
		return e
	}
	if e = wal.Sync(); e != nil {
		return e
	}
	if e = publishDNS(""); e != nil {
		return e
	}
	return c.JSON(200, map[string]string{"language": "golang", "lang": "golang"})
}
func login(c echo.Context) error {
	var req LoginRequest
	if e := bind(c, &req); e != nil {
		return e
	}
	mu.RLock()
	u := st.Users[st.Names[req.Username]]
	mu.RUnlock()
	if u == nil {
		return echo.NewHTTPError(401, "invalid username or password")
	}
	if _, e := authWork(AuthRequest{Hash: u.Model.HashedPassword, Password: req.Password}); e != nil {
		return echo.NewHTTPError(401, "invalid username or password")
	}
	s, _ := session.Get(defaultSessionIDKey, c)
	s.Options = &sessions.Options{Domain: "u.isucon.dev", MaxAge: 60000, Path: "/"}
	s.Values[defaultSessionIDKey] = fmt.Sprint(time.Now().UnixNano())
	s.Values[defaultUserIDKey] = u.Model.ID
	s.Values[defaultUsernameKey] = u.Model.Name
	s.Values[defaultSessionExpiresKey] = time.Now().Add(time.Hour).Unix()
	if e := s.Save(c.Request(), c.Response()); e != nil {
		return e
	}
	return c.NoContent(200)
}
func register(c echo.Context) error {
	var req PostUserRequest
	if e := bind(c, &req); e != nil {
		return e
	}
	if req.Name == "pipe" {
		return echo.NewHTTPError(400, "reserved name")
	}
	h, e := authWork(AuthRequest{Password: req.Password, Generate: true})
	if e != nil {
		return e
	}
	mu.Lock()
	defer mu.Unlock()
	if st.Names[req.Name] != 0 {
		return echo.NewHTTPError(500, "duplicate user")
	}
	id := next("user")
	u := &UserData{Model: UserModel{ID: id, Name: req.Name, DisplayName: req.DisplayName, Description: req.Description, HashedPassword: h}, Theme: Theme{ID: next("theme"), DarkMode: req.Theme.DarkMode}, Hash: defaultHash}
	if e = record(Event{Kind: "user", User: u}); e != nil {
		return e
	}
	if e = publishDNS(req.Name); e != nil {
		return e
	}
	return done(c, 201, user(id))
}
func dnsServer() {
	a, e := net.ResolveUDPAddr("udp", ":5353")
	if e != nil {
		log.Fatal(e)
	}
	conn, e := net.ListenUDP("udp", a)
	if e != nil {
		log.Fatal(e)
	}
	buf := make([]byte, 4096)
	for {
		n, addr, e := conn.ReadFromUDP(buf)
		if e != nil {
			continue
		}
		var p dnsmessage.Parser
		h, e := p.Start(buf[:n])
		if e != nil {
			continue
		}
		q, e := p.Question()
		if e != nil {
			continue
		}
		name := strings.ToLower(q.Name.String())
		known := dnsNames[name]
		if strings.HasSuffix(name, ".u.isucon.dev.") {
			sub := strings.TrimSuffix(name, ".u.isucon.dev.")
			mu.RLock()
			known = known || dnsUsers[sub]
			mu.RUnlock()
		}
		rh := dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true, RecursionDesired: h.RecursionDesired}
		if !known {
			rh.RCode = dnsmessage.RCodeNameError
		}
		b := dnsmessage.NewBuilder(nil, rh)
		b.EnableCompression()
		b.StartQuestions()
		b.Question(q)
		if known && q.Type == dnsmessage.TypeA {
			b.StartAnswers()
			b.AResource(dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.AResource{A: [4]byte{10, 99, 0, 11}})
		}
		out, e := b.Finish()
		if e == nil {
			conn.WriteToUDP(out, addr)
		}
	}
}
func responseBuffer(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		original := c.Response().Writer
		rec := httptest.NewRecorder()
		c.Response().Writer = rec
		err := next(c)
		c.Response().Writer = original
		if err != nil {
			c.Response().Committed = false
			return err
		}
		if c.Get("dirty") == true {
			if err = syncWAL(); err != nil {
				c.Response().Committed = false
				return err
			}
		}
		for k, vs := range rec.Header() {
			original.Header()[k] = vs
		}
		original.WriteHeader(rec.Code)
		_, err = original.Write(rec.Body.Bytes())
		return err
	}
}
func main() {
	if os.Getenv("DNS_FRONT") == "1" {
		dnsFront()
		return
	}
	if os.Getenv("AUTH_WORKER") == "1" {
		authServer()
		return
	}
	var e error
	defaultImage, e = os.ReadFile("../img/NoImage.jpg")
	if e != nil {
		log.Fatal(e)
	}
	defaultHash = hash(defaultImage)
	zone, err := os.ReadFile("../pdns/u.isucon.dev.zone")
	if err != nil {
		log.Fatal(err)
	}
	for _, line := range strings.Split(string(zone), "\n") {
		f := strings.Fields(line)
		if len(f) >= 5 && f[2] == "IN" && f[3] == "A" {
			name := f[0] + ".u.isucon.dev."
			if f[0] == "@" {
				name = "u.isucon.dev."
			}
			dnsNames[name] = true
		}
	}
	if e = recoverState(); e != nil {
		log.Fatal(e)
	}
	http.HandleFunc("/debug/state-digest", func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		defer mu.RUnlock()
		h := sha256.New()
		json.NewEncoder(h).Encode(st)
		fmt.Fprintf(w, "%x\n", h.Sum(nil))
	})
	runtime.SetMutexProfileFraction(10)
	runtime.SetBlockProfileRate(1000000)
	go dnsServer()
	go http.ListenAndServe(":6060", nil)
	app := echo.New()
	app.HideBanner = true
	app.Use(responseBuffer)
	store := sessions.NewCookieStore(secret)
	app.Use(session.Middleware(store))
	app.POST("/api/initialize", initialize)
	app.POST("/api/register", register)
	app.POST("/api/login", login)
	app.GET("/internal/dns-names", read(func(c echo.Context) error { return c.JSON(200, dnsUsers) }, false))
	routes(app)
	log.Fatal(app.Start(":8080"))
}
