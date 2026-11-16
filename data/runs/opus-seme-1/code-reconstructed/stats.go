package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

type pstat struct {
	n   int64
	sz  int64
	dur time.Duration
	st  map[int]int64
}

var (
	pstMu sync.Mutex
	pst   = map[string]*pstat{}
)

func statsMW(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		t := time.Now()
		err := next(c)
		k := c.Request().Method + " " + c.Path()
		d := time.Since(t)
		pstMu.Lock()
		p := pst[k]
		if p == nil {
			p = &pstat{st: map[int]int64{}}
			pst[k] = p
		}
		p.n++
		p.sz += c.Response().Size
		p.dur += d
		p.st[c.Response().Status]++
		pstMu.Unlock()
		return err
	}
}

func statsHandler(c echo.Context) error {
	pstMu.Lock()
	defer pstMu.Unlock()
	keys := make([]string, 0, len(pst))
	for k := range pst {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return pst[keys[i]].dur > pst[keys[j]].dur })
	var sb strings.Builder
	for _, k := range keys {
		p := pst[k]
		fmt.Fprintf(&sb, "%8d %8dB %10.1fms avg=%7.3fms %v %s\n", p.n, p.sz/p.n, float64(p.dur.Microseconds())/1000, float64(p.dur.Microseconds())/1000/float64(p.n), p.st, k)
	}
	n := 0
	for k, v := range searchQ {
		if n++; n < 15 {
			fmt.Fprintf(&sb, "search %q %d\n", k, v)
		}
	}
	if c.QueryParam("reset") == "1" {
		searchQ = map[string]int{}
		pst = map[string]*pstat{}
	}
	return c.String(200, sb.String())
}
