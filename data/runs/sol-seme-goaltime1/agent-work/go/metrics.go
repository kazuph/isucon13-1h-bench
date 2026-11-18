package main

import (
	"encoding/json"
	"github.com/labstack/echo/v4"
	"net/http"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type requestMetric struct {
	Count   atomic.Uint64
	Nanos   atomic.Uint64
	Max     atomic.Uint64
	Errors  atomic.Uint64
	Buckets [9]atomic.Uint64
}

var requestMetrics sync.Map
var metricBounds = [8]uint64{100000, 500000, 1000000, 2000000, 5000000, 10000000, 50000000, 100000000}

func metricMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		start := time.Now()
		err := next(c)
		n := uint64(time.Since(start))
		key := c.Request().Method + " " + c.Path()
		v, _ := requestMetrics.LoadOrStore(key, &requestMetric{})
		m := v.(*requestMetric)
		m.Count.Add(1)
		m.Nanos.Add(n)
		if err != nil {
			m.Errors.Add(1)
		}
		for old := m.Max.Load(); n > old; old = m.Max.Load() {
			if m.Max.CompareAndSwap(old, n) {
				break
			}
		}
		bucket := len(metricBounds)
		for i, b := range metricBounds {
			if n <= b {
				bucket = i
				break
			}
		}
		m.Buckets[bucket].Add(1)
		return err
	}
}
func init() {
	http.HandleFunc("/debug/lockprofile", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("enable") == "1" {
			runtime.SetBlockProfileRate(1000000)
			runtime.SetMutexProfileFraction(10)
		} else {
			runtime.SetBlockProfileRate(0)
			runtime.SetMutexProfileFraction(0)
		}
		w.WriteHeader(204)
	})
	http.HandleFunc("/debug/appmetrics", func(w http.ResponseWriter, r *http.Request) {
		type item struct {
			Route   string   `json:"route"`
			Count   uint64   `json:"count"`
			MeanMs  float64  `json:"mean_ms"`
			MaxMs   float64  `json:"max_ms"`
			Errors  uint64   `json:"errors"`
			Buckets []uint64 `json:"buckets"`
		}
		out := []item{}
		requestMetrics.Range(func(k, v interface{}) bool {
			m := v.(*requestMetric)
			count := m.Count.Load()
			a := item{Route: k.(string), Count: count, MaxMs: float64(m.Max.Load()) / 1e6, Errors: m.Errors.Load(), Buckets: make([]uint64, 9)}
			if count > 0 {
				a.MeanMs = float64(m.Nanos.Load()) / float64(count) / 1e6
			}
			for i := range a.Buckets {
				a.Buckets[i] = m.Buckets[i].Load()
			}
			out = append(out, a)
			return true
		})
		sort.Slice(out, func(i, j int) bool { return out[i].MeanMs*float64(out[i].Count) > out[j].MeanMs*float64(out[j].Count) })
		json.NewEncoder(w).Encode(map[string]interface{}{"routes": out, "db": dbConn.Stats(), "bounds_ns": metricBounds})
	})
}
