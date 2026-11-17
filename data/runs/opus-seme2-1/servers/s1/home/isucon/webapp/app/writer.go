package main

import (
	"database/sql"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
)

type op struct {
	q    string
	args []any
	prep func([]any)
}

var (
	db       *sql.DB
	wq       = make(chan op, 1<<20)
	writerMu sync.Mutex
)

func getenv(k, d string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return d
}

func connectDB() *sql.DB {
	conf := mysql.NewConfig()
	conf.Net = "tcp"
	conf.Addr = net.JoinHostPort(getenv("ISUCON13_MYSQL_DIALCONFIG_ADDRESS", "127.0.0.1"), getenv("ISUCON13_MYSQL_DIALCONFIG_PORT", "3306"))
	conf.User = getenv("ISUCON13_MYSQL_DIALCONFIG_USER", "isucon")
	conf.Passwd = getenv("ISUCON13_MYSQL_DIALCONFIG_PASSWORD", "isucon")
	conf.DBName = getenv("ISUCON13_MYSQL_DIALCONFIG_DATABASE", "isupipe")
	conf.ParseTime = true
	conf.InterpolateParams = true
	for {
		d, err := sql.Open("mysql", conf.FormatDSN())
		if err == nil {
			err = d.Ping()
			if err == nil {
				d.SetMaxOpenConns(8)
				d.SetMaxIdleConns(8)
				return d
			}
			d.Close()
		}
		log.Printf("waiting for db: %v", err)
		time.Sleep(time.Second)
	}
}

// enqueue must be called while holding mu (write lock) so ordering is preserved.
func enqueue(q string, args ...any) {
	wq <- op{q: q, args: args}
}

func writer() {
	batch := make([]op, 0, 1024)
	for o := range wq {
		writerMu.Lock()
		batch = append(batch[:0], o)
	drain:
		for len(batch) < 1000 {
			select {
			case o2 := <-wq:
				batch = append(batch, o2)
			default:
				break drain
			}
		}
		execBatch(batch)
		writerMu.Unlock()
	}
}

func execBatch(batch []op) {
	for attempt := 0; attempt < 5; attempt++ {
		tx, err := db.Begin()
		if err != nil {
			log.Printf("writer begin: %v", err)
			time.Sleep(200 * time.Millisecond)
			continue
		}
		ok := true
		for i := range batch {
			o := &batch[i]
			if o.prep != nil {
				o.prep(o.args)
				o.prep = nil
			}
			if _, err := tx.Exec(o.q, o.args...); err != nil {
				log.Printf("writer exec %q: %v", o.q, err)
			}
		}
		if err := tx.Commit(); err != nil {
			log.Printf("writer commit: %v", err)
			ok = false
		}
		if ok {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// drainQueue discards pending ops (used at initialize). Caller holds mu.
func drainQueue() {
	for {
		select {
		case <-wq:
		default:
			return
		}
	}
}

// flushQueue waits until all queued ops are written. Caller holds mu.
func flushQueue() {
	for len(wq) > 0 {
		time.Sleep(5 * time.Millisecond)
	}
	writerMu.Lock()
	writerMu.Unlock()
}
