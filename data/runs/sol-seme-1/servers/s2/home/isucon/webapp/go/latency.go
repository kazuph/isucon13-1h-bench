package main

import (
	"encoding/json"
	"github.com/labstack/echo/v4"
	"net/http"
	"sync"
	"time"
)

type requestTiming struct {
	Count   int64    `json:"count"`
	TotalUS int64    `json:"total_us"`
	MaxUS   int64    `json:"max_us"`
	Buckets [9]int64 `json:"buckets"`
}

var timings = struct {
	sync.Mutex
	routes map[string]requestTiming
}{routes: make(map[string]requestTiming)}
var timingLimits = [8]int64{100, 500, 1000, 2000, 5000, 10000, 50000, 100000}

func timingMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		start := time.Now()
		err := next(c)
		elapsed := time.Since(start).Microseconds()
		key := c.Request().Method + " " + c.Path()
		timings.Lock()
		t := timings.routes[key]
		t.Count++
		t.TotalUS += elapsed
		if elapsed > t.MaxUS {
			t.MaxUS = elapsed
		}
		bucket := 8
		for i, limit := range timingLimits {
			if elapsed < limit {
				bucket = i
				break
			}
		}
		t.Buckets[bucket]++
		timings.routes[key] = t
		timings.Unlock()
		return err
	}
}
func latencyHandler(w http.ResponseWriter, r *http.Request) {
	timings.Lock()
	defer timings.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"routes": timings.routes, "database": dbConn.Stats(), "bucket_upper_us": timingLimits})
}
func resetTimings() {
	timings.Lock()
	timings.routes = make(map[string]requestTiming)
	timings.Unlock()
}
