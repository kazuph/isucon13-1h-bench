package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

type insertResult struct {
	id  int64
	err error
}
type insertJob struct {
	args   []any
	result chan insertResult
}
type insertQueue struct {
	prefix, tuple string
	pending       chan insertJob
	batch         int
	increment     int64
}

var commentInserts = insertQueue{prefix: "INSERT INTO livecomments (user_id,livestream_id,comment,tip,created_at) VALUES ", tuple: "(?,?,?,?,?)"}
var reactionInserts = insertQueue{prefix: "INSERT INTO reactions (user_id,livestream_id,emoji_name,created_at) VALUES ", tuple: "(?,?,?,?)"}

func startInsertQueues() error {
	batch := 32
	if raw := os.Getenv("ISUPIPE_WRITE_BATCH"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 128 {
			return fmt.Errorf("invalid write batch size %q", raw)
		}
		batch = n
	}
	var increment int64
	if err := dbConn.Get(&increment, "SELECT @@SESSION.auto_increment_increment"); err != nil {
		return err
	}
	for _, q := range []*insertQueue{&commentInserts, &reactionInserts} {
		q.batch = batch
		q.increment = increment
		q.pending = make(chan insertJob, 256)
		if batch > 1 {
			go q.run()
		}
	}
	return nil
}
func insertedID(r sql.Result, err error) (int64, error) {
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}
func (q *insertQueue) insert(ctx context.Context, args ...any) (int64, error) {
	if q.batch == 1 {
		return insertedID(dbConn.ExecContext(ctx, q.prefix+q.tuple, args...))
	}
	job := insertJob{args, make(chan insertResult, 1)}
	select {
	case q.pending <- job:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	// After acceptance the caller must publish every durably committed event, even on disconnect.
	result := <-job.result
	return result.id, result.err
}
func (q *insertQueue) run() {
	for first := range q.pending {
		jobs := []insertJob{first}
	drain:
		for len(jobs) < q.batch {
			select {
			case job := <-q.pending:
				jobs = append(jobs, job)
			default:
				break drain
			}
		}
		q.flush(jobs)
	}
}
func (q *insertQueue) flush(jobs []insertJob) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	params := make([]any, 0, len(jobs)*len(jobs[0].args))
	for _, job := range jobs {
		params = append(params, job.args...)
	}
	query := q.prefix + q.tuple + strings.Repeat(","+q.tuple, len(jobs)-1)
	first, err := insertedID(dbConn.ExecContext(ctx, query, params...))
	var mysqlErr *mysql.MySQLError
	if err != nil && len(jobs) > 1 && errors.As(err, &mysqlErr) {
		switch mysqlErr.Number {
		case 1048, 1264, 1366, 1406, 3819:
			// InnoDB rejected the whole statement. Isolate the invalid row.
			for _, job := range jobs {
				id, oneErr := insertedID(dbConn.ExecContext(ctx, q.prefix+q.tuple, job.args...))
				job.result <- insertResult{id, oneErr}
			}
			return
		}
	}
	// A plain, fixed-size multi-VALUES insert reserves a contiguous auto-increment block.
	for i, job := range jobs {
		job.result <- insertResult{first + int64(i)*q.increment, err}
	}
}
