package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"bufio"
	"crypto/tls"
	"path/filepath"
	"runtime/pprof"
	_ "net/http/pprof"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	json "github.com/goccy/go-json"
)

var (
	secret = []byte("isucon13_session_cookiestore_defaultsecret_x")
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	if v := os.Getenv("ISUCON13_SESSION_SECRETKEY"); v != "" {
		secret = []byte(v)
	}
	loadFallback()
	loadZoneNames()
	if os.Getenv("DNS_DISABLED") == "" {
		go startDNS()
	}
	initDB()
	for {
		if err := loadAll(); err != nil {
			log.Printf("load failed: %v", err)
			time.Sleep(time.Second)
			continue
		}
		break
	}
	go persistWorker()
	if os.Getenv("PPROF") != "" {
		go http.ListenAndServe("127.0.0.1:6060", nil)
	}
	if addr := os.Getenv("TLS_LISTEN"); addr != "" {
		go func() {
			var cert tls.Certificate
			var err error
			if os.Getenv("TLS_OPENSSL") != "" {
				cert, err = loadCertOpenSSL(os.Getenv("TLS_CERT"), os.Getenv("TLS_KEY"))
			} else {
				cert, err = tls.LoadX509KeyPair(os.Getenv("TLS_CERT"), os.Getenv("TLS_KEY"))
			}
			if err != nil {
				log.Fatal(err)
			}
			tsrv := &http.Server{
				Addr:    addr,
				Handler: http.HandlerFunc(topHandler),
				TLSConfig: &tls.Config{
					Certificates: []tls.Certificate{cert},
					MinVersion:   tls.VersionTLS12,
				},
				IdleTimeout: 120 * time.Second,
			}
			if os.Getenv("TLS_H2") == "" {
				tsrv.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){}
			}
			log.Fatal(tsrv.ListenAndServeTLS("", ""))
		}()
	}
	srv := &http.Server{
		Addr:    ":8080",
		Handler: http.HandlerFunc(topHandler),
	}
	log.Fatal(srv.ListenAndServe())
}

var staticDir = "/home/isucon/webapp/public"

var accessLog = os.Getenv("ACCESS_LOG") != ""
var alogCh = make(chan string, 1<<16)

func init() {
	if accessLog {
		go func() {
			f, _ := os.Create("/tmp/app_access.log")
			bw := bufio.NewWriterSize(f, 1<<20)
			t := time.NewTicker(time.Second)
			for {
				select {
				case l := <-alogCh:
					bw.WriteString(l)
				case <-t.C:
					bw.Flush()
				}
			}
		}()
	}
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }

func topHandler(w http.ResponseWriter, r *http.Request) {
	if accessLog {
		st := time.Now()
		sw := &statusWriter{w, 200}
		topHandler2(sw, r)
		uid, _ := getSession(r)
		select {
		case alogCh <- fmt.Sprintf("%d %d %s %s %s %d %d %s\n", st.UnixMicro(), time.Since(st).Microseconds(), r.RemoteAddr, r.Method, r.URL.RequestURI(), sw.code, uid, r.Host):
		default:
		}
		return
	}
	topHandler2(w, r)
}

func topHandler2(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api") {
		route(w, r)
		return
	}
	p := filepath.Clean("/" + r.URL.Path)
	fp := filepath.Join(staticDir, p)
	if st, err := os.Stat(fp); err == nil && !st.IsDir() {
		http.ServeFile(w, r, fp)
		return
	}
	http.ServeFile(w, r, filepath.Join(staticDir, "index.html"))
}

// ---------- helpers ----------

type apiErr struct {
	code int
	msg  string
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		w.WriteHeader(500)
		return
	}
	h := w.Header()
	h["Content-Type"] = []string{"application/json; charset=UTF-8"}
	w.WriteHeader(code)
	w.Write(b)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// ---------- session ----------

func sign(s string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(s))
	return hex.EncodeToString(m.Sum(nil)[:16])
}

func makeSession(userID int64, exp int64) string {
	p := strconv.FormatInt(userID, 10) + "." + strconv.FormatInt(exp, 10)
	return p + "." + sign(p)
}

// returns userID, status (0 ok)
func getSession(r *http.Request) (int64, int) {
	c, err := r.Cookie("SESSIONID")
	if err != nil || c.Value == "" {
		return 0, 403
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 3 {
		return 0, 401
	}
	p := parts[0] + "." + parts[1]
	if !hmac.Equal([]byte(sign(p)), []byte(parts[2])) {
		return 0, 401
	}
	uid, err1 := strconv.ParseInt(parts[0], 10, 64)
	exp, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil {
		return 0, 401
	}
	if time.Now().Unix() > exp {
		return 0, 401
	}
	return uid, 0
}

func auth(w http.ResponseWriter, r *http.Request) (int64, bool) {
	uid, st := getSession(r)
	if st != 0 {
		writeErr(w, st, "unauthorized")
		return 0, false
	}
	return uid, true
}

// ---------- router ----------

var debugDelay, _ = strconv.Atoi(os.Getenv("DEBUG_DELAY_US"))

func route(w http.ResponseWriter, r *http.Request) {
	if debugDelay > 0 {
		time.Sleep(time.Duration(debugDelay) * time.Microsecond)
	}
	path := r.URL.Path
	if !strings.HasPrefix(path, "/api/") {
		writeErr(w, 404, "not found")
		return
	}
	seg := strings.Split(path[5:], "/")
	m := r.Method
	switch seg[0] {
	case "initialize":
		if m == "POST" && len(seg) == 1 {
			initializeHandler(w, r)
			return
		}
	case "tag":
		if m == "GET" && len(seg) == 1 {
			getTagHandler(w, r)
			return
		}
	case "register":
		if m == "POST" && len(seg) == 1 {
			registerHandler(w, r)
			return
		}
	case "login":
		if m == "POST" && len(seg) == 1 {
			loginHandler(w, r)
			return
		}
	case "icon":
		if m == "POST" && len(seg) == 1 {
			postIconHandler(w, r)
			return
		}
	case "payment":
		if m == "GET" && len(seg) == 1 {
			paymentHandler(w, r)
			return
		}
	case "user":
		if len(seg) == 2 && m == "GET" {
			if seg[1] == "me" {
				getMeHandler(w, r)
			} else {
				getUserHandler(w, r, seg[1])
			}
			return
		}
		if len(seg) == 3 && m == "GET" {
			switch seg[2] {
			case "theme":
				getThemeHandler(w, r, seg[1])
				return
			case "livestream":
				getUserLivestreamsHandler(w, r, seg[1])
				return
			case "statistics":
				getUserStatisticsHandler(w, r, seg[1])
				return
			case "icon":
				getIconHandler(w, r, seg[1])
				return
			}
		}
	case "livestream":
		if len(seg) == 1 {
			if m == "GET" {
				getMyLivestreamsHandler(w, r)
				return
			}
			break
		}
		if len(seg) == 2 {
			switch {
			case seg[1] == "search" && m == "GET":
				searchHandler(w, r)
				return
			case seg[1] == "reservation" && m == "POST":
				reserveHandler(w, r)
				return
			case m == "GET":
				getLivestreamHandler(w, r, seg[1])
				return
			}
			break
		}
		if len(seg) == 3 {
			id := seg[1]
			switch seg[2] {
			case "livecomment":
				if m == "GET" {
					getLivecommentsHandler(w, r, id)
					return
				} else if m == "POST" {
					postLivecommentHandler(w, r, id)
					return
				}
			case "reaction":
				if m == "GET" {
					getReactionsHandler(w, r, id)
					return
				} else if m == "POST" {
					postReactionHandler(w, r, id)
					return
				}
			case "report":
				if m == "GET" {
					getReportsHandler(w, r, id)
					return
				}
			case "ngwords":
				if m == "GET" {
					getNgwordsHandler(w, r, id)
					return
				}
			case "moderate":
				if m == "POST" {
					moderateHandler(w, r, id)
					return
				}
			case "enter":
				if m == "POST" {
					enterHandler(w, r, id)
					return
				}
			case "exit":
				if m == "DELETE" {
					exitHandler(w, r, id)
					return
				}
			case "statistics":
				if m == "GET" {
					getLivestreamStatisticsHandler(w, r, id)
					return
				}
			}
			break
		}
		if len(seg) == 5 && seg[2] == "livecomment" && seg[4] == "report" && m == "POST" {
			reportHandler(w, r, seg[1], seg[3])
			return
		}
	}
	writeErr(w, 404, "not found")
}

// ---------- initialize ----------

func initializeHandler(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()
	flushPersist()
	if out, err := exec.Command("../sql/init_fast.sh").CombinedOutput(); err != nil {
		log.Printf("init failed: %s", out)
		writeErr(w, 500, "failed to initialize: "+err.Error())
		return
	}
	if err := loadAllLocked(); err != nil {
		writeErr(w, 500, fmt.Sprintf("load: %v", err))
		return
	}
	if os.Getenv("PPROF") != "" {
		go func() {
			time.Sleep(62 * time.Second)
			f, err := os.Create("/tmp/cpu.prof")
			if err != nil {
				return
			}
			pprof.StartCPUProfile(f)
			time.Sleep(50 * time.Second)
			pprof.StopCPUProfile()
			f.Close()
		}()
	}
	writeJSON(w, 200, map[string]string{"language": "golang"})
}

var _ = net.JoinHostPort
