package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/go-sql-driver/mysql"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"
)

const token = "isupipe-durable-write-batches"

type argument struct {
	Kind      string `json:"kind"`
	Text      string `json:"text,omitempty"`
	Integer   int64  `json:"integer,omitempty"`
	Bytes     []byte `json:"bytes,omitempty"`
	Reference int    `json:"reference,omitempty"`
}
type query struct {
	SQL  string     `json:"sql"`
	Args []argument `json:"args"`
}
type request struct {
	Queries []query `json:"queries"`
	done    chan response
}
type result struct {
	ID       int64 `json:"id"`
	Affected int64 `json:"affected"`
}
type response struct {
	Results []result `json:"results"`
	Error   string   `json:"error,omitempty"`
}

var database *sql.DB
var queue = make(chan *request, 8192)
var count, batches, queries, failures atomic.Uint64
var batchDelay = 200 * time.Microsecond

func execute(batch []*request) {
	if len(batch) == 1 && len(batch[0].Queries) == 1 {
		executeSingle(batch[0])
		return
	}
	tx, err := database.Begin()
	if err != nil {
		for _, r := range batch {
			r.done <- response{Error: err.Error()}
		}
		return
	}
	replies := make([]response, len(batch))
	for i, r := range batch {
		replies[i].Results = make([]result, 0, len(r.Queries))
		for _, q := range r.Queries {
			args, argErr := resolveArgs(q.Args, replies[i].Results)
			if argErr != nil {
				err = argErr
			}
			if err != nil {
				break
			}
			var out sql.Result
			out, err = tx.Exec(q.SQL, args...)
			if err != nil {
				break
			}
			id, _ := out.LastInsertId()
			affected, _ := out.RowsAffected()
			replies[i].Results = append(replies[i].Results, result{ID: id, Affected: affected})
			queries.Add(1)
		}
		if err != nil {
			break
		}
	}
	rolledBack := err != nil
	if err == nil {
		err = tx.Commit()
	} else {
		tx.Rollback()
	}
	if err != nil {
		// A failing transaction must not make unrelated operations fail.
		if rolledBack && len(batch) > 1 {
			middle := len(batch) / 2
			execute(batch[:middle])
			execute(batch[middle:])
			return
		}
		failures.Add(1)
		for _, req := range batch {
			req.done <- response{Error: err.Error()}
		}
		return
	}
	batches.Add(1)
	count.Add(uint64(len(batch)))
	for i, r := range batch {
		r.done <- replies[i]
	}
}
func worker() {
	for first := range queue {
		batch := []*request{first}
		timer := time.NewTimer(batchDelay)
	gather:
		for len(batch) < 64 {
			select {
			case r := <-queue:
				batch = append(batch, r)
			case <-timer.C:
				break gather
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		execute(batch)
	}
}
func main() {
	conf := mysql.NewConfig()
	conf.Net = "unix"
	conf.Addr = "/var/run/mysqld/mysqld.sock"
	conf.User = "isucon"
	conf.Passwd = "isucon"
	conf.DBName = "isupipe"
	conf.InterpolateParams = true
	conf.Params = map[string]string{"transaction_isolation": "'READ-COMMITTED'"}
	var err error
	database, err = sql.Open("mysql", conf.FormatDSN())
	if err != nil {
		log.Fatal(err)
	}
	database.SetMaxOpenConns(16)
	database.SetMaxIdleConns(16)
	if err = database.Ping(); err != nil {
		log.Fatal(err)
	}
	if n, err := strconv.Atoi(os.Getenv("PERSIST_BATCH_US")); err == nil && n >= 0 {
		batchDelay = time.Duration(n) * time.Microsecond
	}
	workers := 2
	if n, err := strconv.Atoi(os.Getenv("PERSIST_WORKERS")); err == nil && n > 0 && n <= 64 {
		workers = n
	}
	for i := 0; i < workers; i++ {
		go worker()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/write", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Persist-Token") != token {
			http.Error(w, "forbidden", 403)
			return
		}
		req := new(request)
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if len(req.Queries) == 0 || len(req.Queries) > 200 {
			http.Error(w, "invalid batch", 400)
			return
		}
		req.done = make(chan response, 1)
		select {
		case queue <- req:
		case <-r.Context().Done():
			return
		}
		select {
		case reply := <-req.done:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(reply)
		case <-r.Context().Done():
			return
		}
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		json.NewEncoder(w).Encode(map[string]interface{}{"gc_count": memory.NumGC, "heap_alloc": memory.HeapAlloc, "total_alloc": memory.TotalAlloc, "gc_pause_ns": memory.PauseTotalNs, "requests": count.Load(), "batches": batches.Load(), "queries": queries.Load(), "failures": failures.Load(), "queued": len(queue), "db": database.Stats()})
	})
	go func() { log.Print(http.ListenAndServe("127.0.0.1:6061", nil)) }()
	log.Fatal(http.ListenAndServe("10.99.0.13:8090", mux))
}

func resolveArgs(values []argument, previous []result) ([]interface{}, error) {
	out := make([]interface{}, len(values))
	for i, a := range values {
		switch a.Kind {
		case "int":
			out[i] = a.Integer
		case "bytes":
			out[i] = a.Bytes
		case "nil":
			out[i] = nil
		case "ref":
			if a.Reference < 0 || a.Reference >= len(previous) {
				return nil, fmt.Errorf("invalid insert reference")
			}
			out[i] = previous[a.Reference].ID
		default:
			out[i] = a.Text
		}
	}
	return out, nil
}
func executeSingle(req *request) {
	q := req.Queries[0]
	args, err := resolveArgs(q.Args, nil)
	if err != nil {
		failures.Add(1)
		req.done <- response{Error: err.Error()}
		return
	}
	out, err := database.Exec(q.SQL, args...)
	if err != nil {
		failures.Add(1)
		req.done <- response{Error: err.Error()}
		return
	}
	id, _ := out.LastInsertId()
	affected, _ := out.RowsAffected()
	queries.Add(1)
	batches.Add(1)
	count.Add(1)
	req.done <- response{Results: []result{{ID: id, Affected: affected}}}
}
