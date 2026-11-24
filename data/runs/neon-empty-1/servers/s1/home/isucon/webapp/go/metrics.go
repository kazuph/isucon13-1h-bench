package main

import (
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/labstack/echo/v4"
)

type epMetric struct {
	count   atomic.Int64
	totalNs atomic.Int64
	pipeCnt atomic.Int64
	subCnt  atomic.Int64
}

var (
	epMetricsMu   sync.RWMutex
	epMetrics     = make(map[string]*epMetric)
	icon304Cnt    atomic.Int64
	icon200Cnt    atomic.Int64
	dnsHitCnt     atomic.Int64
	dnsNXCnt      atomic.Int64
	benchStartSec atomic.Int64

	secReqs [90]atomic.Int64
	secTips [90]atomic.Int64
	secReg  [90]atomic.Int64
	secLC   [90]atomic.Int64
)

func resetEPMetrics() {
	epMetricsMu.Lock()
	epMetrics = make(map[string]*epMetric)
	epMetricsMu.Unlock()
	icon304Cnt.Store(0)
	icon200Cnt.Store(0)
	dnsHitCnt.Store(0)
	dnsNXCnt.Store(0)
	bcryptOffloadCnt.Store(0)
	bcryptLocalCnt.Store(0)
	rsaOffloadCnt.Store(0)
	rsaLocalCnt.Store(0)
	benchStartSec.Store(time.Now().Unix())
	for i := range secReqs {
		secReqs[i].Store(0)
		secTips[i].Store(0)
		secReg[i].Store(0)
		secLC[i].Store(0)
	}
}

func timelineSlot() int {
	st := benchStartSec.Load()
	if st == 0 {
		return -1
	}
	d := int(time.Now().Unix() - st)
	if d < 0 || d >= len(secReqs) {
		return -1
	}
	return d
}

func recordTipMetric(tip int64) {
	if s := timelineSlot(); s >= 0 {
		secLC[s].Add(1)
		if tip > 0 {
			secTips[s].Add(tip)
		}
	}
}

func recordRegMetric() {
	if s := timelineSlot(); s >= 0 {
		secReg[s].Add(1)
	}
}

func getOrCreateEPMetric(key string) *epMetric {
	epMetricsMu.RLock()
	m := epMetrics[key]
	epMetricsMu.RUnlock()
	if m != nil {
		return m
	}
	epMetricsMu.Lock()
	m = epMetrics[key]
	if m == nil {
		m = &epMetric{}
		epMetrics[key] = m
	}
	epMetricsMu.Unlock()
	return m
}

func metricsMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		start := time.Now()
		err := next(c)
		dur := time.Since(start).Nanoseconds()
		if s := timelineSlot(); s >= 0 {
			secReqs[s].Add(1)
		}
		key := c.Request().Method + " " + c.Path()
		m := getOrCreateEPMetric(key)
		m.count.Add(1)
		m.totalNs.Add(dur)
		if strings.HasPrefix(c.Request().Host, "pipe.") {
			m.pipeCnt.Add(1)
		} else {
			m.subCnt.Add(1)
		}
		return err
	}
}

func startDebugServer() {
	http.HandleFunc("/debug/epstats", func(w http.ResponseWriter, r *http.Request) {
		type row struct {
			key     string
			cnt     int64
			totalMs float64
			avgMs   float64
			pipe    int64
			sub     int64
		}
		epMetricsMu.RLock()
		rows := make([]row, 0, len(epMetrics))
		for k, m := range epMetrics {
			cnt := m.count.Load()
			tot := float64(m.totalNs.Load()) / 1e6
			avg := 0.0
			if cnt > 0 {
				avg = tot / float64(cnt)
			}
			rows = append(rows, row{
				key:     k,
				cnt:     cnt,
				totalMs: tot,
				avgMs:   avg,
				pipe:    m.pipeCnt.Load(),
				sub:     m.subCnt.Load(),
			})
		}
		epMetricsMu.RUnlock()

		sort.Slice(rows, func(i, j int) bool {
			return rows[i].totalMs > rows[j].totalMs
		})
		fmt.Fprintf(w, "ICON_304=%d ICON_200=%d DNS_HIT=%d DNS_NX=%d BCRYPT_LOCAL=%d BCRYPT_OFFLOAD=%d RSA_LOCAL=%d RSA_OFFLOAD=%d\n",
			icon304Cnt.Load(), icon200Cnt.Load(), dnsHitCnt.Load(), dnsNXCnt.Load(),
			bcryptLocalCnt.Load(), bcryptOffloadCnt.Load(), rsaLocalCnt.Load(), rsaOffloadCnt.Load())
		fmt.Fprintf(w, "%-52s %8s %10s %8s %8s %8s\n", "ENDPOINT", "COUNT", "TOTAL_MS", "AVG_MS", "PIPE", "SUB")
		for _, r := range rows {
			fmt.Fprintf(w, "%-52s %8d %10.1f %8.3f %8d %8d\n", r.key, r.cnt, r.totalMs, r.avgMs, r.pipe, r.sub)
		}
		fmt.Fprintf(w, "\n=== TIMELINE (sec: reqs reg lc tips) ===\n")
		for i := 0; i < len(secReqs); i++ {
			rq := secReqs[i].Load()
			rg := secReg[i].Load()
			lc := secLC[i].Load()
			tp := secTips[i].Load()
			if rq > 0 || rg > 0 || lc > 0 || tp > 0 {
				fmt.Fprintf(w, "T+%02ds: reqs=%5d reg=%4d lc=%4d tips=%6d\n", i, rq, rg, lc, tp)
			}
		}
	})
	go func() {
		_ = http.ListenAndServe("127.0.0.1:6060", nil)
	}()
}
