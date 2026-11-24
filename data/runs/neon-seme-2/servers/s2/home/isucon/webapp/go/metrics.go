package main

import (
	"fmt"
	"net/http"
	"net/http/pprof"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type epStat struct {
	count   atomic.Int64
	totalNs atomic.Int64
	maxNs   atomic.Int64
	bytes   atomic.Int64
	status304 atomic.Int64
}

type Metrics struct {
	mu    sync.RWMutex
	stats map[string]*epStat
}

func NewMetrics() *Metrics {
	return &Metrics{stats: make(map[string]*epStat, 32)}
}

func (m *Metrics) Reset() {
	m.mu.Lock()
	m.stats = make(map[string]*epStat, 32)
	m.mu.Unlock()
}

func (m *Metrics) Record(name string, dur time.Duration, status int) {
	if m == nil {
		return
	}
	m.mu.RLock()
	st, ok := m.stats[name]
	m.mu.RUnlock()
	if !ok {
		m.mu.Lock()
		st, ok = m.stats[name]
		if !ok {
			st = &epStat{}
			m.stats[name] = st
		}
		m.mu.Unlock()
	}
	ns := dur.Nanoseconds()
	st.count.Add(1)
	st.totalNs.Add(ns)
	if status == http.StatusNotModified {
		st.status304.Add(1)
	}
	for {
		old := st.maxNs.Load()
		if ns <= old || st.maxNs.CompareAndSwap(old, ns) {
			break
		}
	}
}

func (m *Metrics) ServeText(w http.ResponseWriter, r *http.Request) {
	m.mu.RLock()
	type row struct {
		name  string
		cnt   int64
		totMs float64
		avgMs float64
		maxMs float64
		n304  int64
	}
	rows := make([]row, 0, len(m.stats))
	var totCnt int64
	for k, st := range m.stats {
		c := st.count.Load()
		totCnt += c
		t := float64(st.totalNs.Load()) / 1e6
		avg := 0.0
		if c > 0 {
			avg = t / float64(c)
		}
		rows = append(rows, row{
			name:  k,
			cnt:   c,
			totMs: t,
			avgMs: avg,
			maxMs: float64(st.maxNs.Load()) / 1e6,
			n304:  st.status304.Load(),
		})
	}
	m.mu.RUnlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].totMs > rows[j].totMs })
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "TOTAL_REQUESTS: %d\n", totCnt)
	fmt.Fprintf(w, "%-38s %8s %10s %8s %8s %8s\n", "ENDPOINT", "COUNT", "TOTAL_MS", "AVG_MS", "MAX_MS", "304s")
	for _, r := range rows {
		fmt.Fprintf(w, "%-38s %8d %10.1f %8.3f %8.3f %8d\n", r.name, r.cnt, r.totMs, r.avgMs, r.maxMs, r.n304)
	}
}

func registerPprof(mux *http.ServeMux) {
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
}
