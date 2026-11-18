package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type rstat struct {
	n, total, max, bytes int64
}

var (
	statMu   sync.Mutex
	stats    = map[string]*rstat{}
	newConns int64
)

type cw struct {
	http.ResponseWriter
	n    int64
	code int
}

func (c *cw) Write(b []byte) (int, error) { c.n += int64(len(b)); return c.ResponseWriter.Write(b) }
func (c *cw) WriteHeader(code int)        { c.code = code; c.ResponseWriter.WriteHeader(code) }

func routeKey(r *http.Request) string {
	segs := strings.Split(r.URL.Path, "/")
	for i, s := range segs {
		if len(s) > 0 && s[0] >= '0' && s[0] <= '9' {
			segs[i] = "N"
		}
	}
	if len(segs) > 3 && segs[2] == "user" && segs[3] != "me" {
		segs[3] = "U"
	}
	k := r.Method + " " + strings.Join(segs, "/")
	if r.URL.RawQuery != "" {
		if strings.Contains(r.URL.RawQuery, "tag=") {
			k += "?tag"
		} else if strings.Contains(r.URL.RawQuery, "limit=") {
			k += "?limit"
		}
	}
	return k
}

type connInfo struct{ lastEnd time.Time }
type ciKey struct{}

var (
	gapHist  [12]int64
	protoCnt sync.Map
)

func connContext(ctx context.Context, c net.Conn) context.Context {
	return context.WithValue(ctx, ciKey{}, &connInfo{})
}

func instrument(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t := time.Now()
		if ci, ok := r.Context().Value(ciKey{}).(*connInfo); ok {
			if !ci.lastEnd.IsZero() {
				g := t.Sub(ci.lastEnd).Microseconds()
				b := 0
				for lim := int64(250); g > lim && b < 11; lim *= 2 {
					b++
				}
				atomic.AddInt64(&gapHist[b], 1)
			}
			defer func() { ci.lastEnd = time.Now() }()
		}
		v, _ := protoCnt.LoadOrStore(r.Proto, new(int64))
		atomic.AddInt64(v.(*int64), 1)
		c := &cw{ResponseWriter: w, code: 200}
		h.ServeHTTP(c, r)
		d := time.Since(t).Microseconds()
		k := routeKey(r) + " " + fmt.Sprint(c.code)
		statMu.Lock()
		s := stats[k]
		if s == nil {
			s = &rstat{}
			stats[k] = s
		}
		s.n++
		s.total += d
		if d > s.max {
			s.max = d
		}
		s.bytes += c.n
		statMu.Unlock()
	})
}

func connState(c net.Conn, st http.ConnState) {
	if st == http.StateNew {
		atomic.AddInt64(&newConns, 1)
	}
}

func statsHandler(w http.ResponseWriter, r *http.Request) {
	statMu.Lock()
	type kv struct {
		k string
		s rstat
	}
	var l []kv
	for k, s := range stats {
		l = append(l, kv{k, *s})
	}
	if r.URL.Query().Get("reset") != "" {
		stats = map[string]*rstat{}
		atomic.StoreInt64(&newConns, 0)
	}
	statMu.Unlock()
	sort.Slice(l, func(i, j int) bool { return l[i].s.total > l[j].s.total })
	fmt.Fprintf(w, "newConns=%d\n", atomic.LoadInt64(&newConns))
	protoCnt.Range(func(k, v any) bool { fmt.Fprintf(w, "proto %v=%d\n", k, atomic.LoadInt64(v.(*int64))); return true })
	lim := 250
	for i := range gapHist {
		fmt.Fprintf(w, "gap<=%dus: %d\n", lim, atomic.LoadInt64(&gapHist[i]))
		lim *= 2
		if r.URL.Query().Get("reset") != "" {
			atomic.StoreInt64(&gapHist[i], 0)
		}
	}
	fmt.Fprintf(w, "%8s %10s %8s %8s %10s  %s\n", "count", "total_ms", "avg_us", "max_ms", "avg_bytes", "route")
	for _, x := range l {
		fmt.Fprintf(w, "%8d %10d %8d %8d %10d  %s\n", x.s.n, x.s.total/1000, x.s.total/x.s.n, x.s.max/1000, x.s.bytes/x.s.n, x.k)
	}
}
