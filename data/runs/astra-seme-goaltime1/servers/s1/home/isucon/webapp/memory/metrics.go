package main

import (
	"encoding/json"
	"github.com/labstack/echo/v4"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

type endpointMetric struct {
	Count, TotalNS, Bytes atomic.Int64
	Buckets               [7]atomic.Int64
}

var endpointMetrics sync.Map

func measureRequests(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		start := time.Now()
		err := next(c)
		key := c.Request().Method + " " + c.Path()
		v, _ := endpointMetrics.LoadOrStore(key, &endpointMetric{})
		m := v.(*endpointMetric)
		elapsed := time.Since(start)
		m.Count.Add(1)
		m.TotalNS.Add(int64(elapsed))
		m.Bytes.Add(c.Response().Size)
		b := 0
		for _, d := range []time.Duration{time.Millisecond, 5 * time.Millisecond, 10 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, time.Second} {
			if elapsed >= d {
				b++
			}
		}
		m.Buckets[b].Add(1)
		return err
	}
}
func init() {
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		out := map[string]interface{}{}
		endpointMetrics.Range(func(k, v interface{}) bool {
			m := v.(*endpointMetric)
			bs := []int64{}
			for i := range m.Buckets {
				bs = append(bs, m.Buckets[i].Load())
			}
			out[k.(string)] = map[string]interface{}{"count": m.Count.Load(), "total_ns": m.TotalNS.Load(), "bytes": m.Bytes.Load(), "buckets": bs}
			return true
		})
		json.NewEncoder(w).Encode(out)
	})
}
