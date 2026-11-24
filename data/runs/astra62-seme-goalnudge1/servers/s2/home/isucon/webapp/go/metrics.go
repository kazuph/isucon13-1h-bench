package main

import (
	"expvar"
	"math/bits"
	"sync"
	"sync/atomic"
	"time"

	"github.com/labstack/echo/v4"
)

type latencyHistogram struct{ buckets [32]atomic.Uint64 }

func (h *latencyHistogram) observe(t time.Duration) {
	bucket := bits.Len64(uint64(t.Microseconds()))
	if bucket >= len(h.buckets) {
		bucket = len(h.buckets) - 1
	}
	h.buckets[bucket].Add(1)
}
func (h *latencyHistogram) counts() []uint64 {
	out := make([]uint64, len(h.buckets))
	for i := range out {
		out[i] = h.buckets[i].Load()
	}
	return out
}

var requestTiming sync.Map
var syncTiming latencyHistogram
var syncBatches, committedEvents atomic.Uint64

func init() {
	expvar.Publish("request_microsecond_buckets", expvar.Func(func() interface{} {
		out := make(map[string][]uint64)
		requestTiming.Range(func(k, v interface{}) bool { out[k.(string)] = v.(*latencyHistogram).counts(); return true })
		return out
	}))
	expvar.Publish("journal_sync_microsecond_buckets", expvar.Func(func() interface{} { return syncTiming.counts() }))
	expvar.Publish("journal_batches", expvar.Func(func() interface{} { return syncBatches.Load() }))
	expvar.Publish("journal_events", expvar.Func(func() interface{} { return committedEvents.Load() }))
}
func timeRequest(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		key := c.Request().Method + " " + c.Path()
		v, ok := requestTiming.Load(key)
		if !ok {
			v, _ = requestTiming.LoadOrStore(key, new(latencyHistogram))
		}
		start := time.Now()
		err := next(c)
		v.(*latencyHistogram).observe(time.Since(start))
		return err
	}
}
