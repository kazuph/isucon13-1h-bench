package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

type persistArg struct {
	Kind      string `json:"kind"`
	Text      string `json:"text,omitempty"`
	Integer   int64  `json:"integer,omitempty"`
	Bytes     []byte `json:"bytes,omitempty"`
	Reference int    `json:"reference,omitempty"`
}
type persistQuery struct {
	SQL  string       `json:"sql"`
	Args []persistArg `json:"args"`
}
type persistResult struct {
	ID       int64 `json:"id"`
	Affected int64 `json:"affected"`
}

func (p persistResult) LastInsertId() (int64, error) { return p.ID, nil }
func (p persistResult) RowsAffected() (int64, error) { return p.Affected, nil }

var persistHTTP = &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{MaxIdleConns: 128, MaxIdleConnsPerHost: 128, IdleConnTimeout: 90 * time.Second}}

func makePersistQuery(q string, args ...interface{}) persistQuery {
	out := persistQuery{SQL: q, Args: make([]persistArg, len(args))}
	for i, v := range args {
		switch x := v.(type) {
		case int64:
			out.Args[i] = persistArg{Kind: "int", Integer: x}
		case int:
			out.Args[i] = persistArg{Kind: "int", Integer: int64(x)}
		case bool:
			var n int64
			if x {
				n = 1
			}
			out.Args[i] = persistArg{Kind: "int", Integer: n}
		case []byte:
			out.Args[i] = persistArg{Kind: "bytes", Bytes: x}
		case string:
			out.Args[i] = persistArg{Kind: "string", Text: x}
		case persistArg:
			out.Args[i] = x
		case nil:
			out.Args[i] = persistArg{Kind: "nil"}
		default:
			panic(fmt.Sprintf("unsupported persistence argument %T", v))
		}
	}
	return out
}
func persistBatch(qs []persistQuery) ([]persistResult, error) {
	base := os.Getenv("ISUCON_PERSIST_URL")
	if base == "" {
		return nil, fmt.Errorf("persistence URL not configured")
	}
	data, err := json.Marshal(struct {
		Queries []persistQuery `json:"queries"`
	}{qs})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, base+"/write", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Persist-Token", "isupipe-durable-write-batches")
	req.Header.Set("Content-Type", "application/json")
	resp, err := persistHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("persistence HTTP %d", resp.StatusCode)
	}
	var reply struct {
		Results []persistResult `json:"results"`
		Error   string          `json:"error"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&reply); err != nil {
		return nil, err
	}
	if reply.Error != "" {
		return nil, fmt.Errorf("persistence: %s", reply.Error)
	}
	return reply.Results, nil
}

func persistTransaction(qs []persistQuery) ([]persistResult, error) {
	if os.Getenv("ISUCON_PERSIST_URL") != "" {
		return persistBatch(qs)
	}
	tx, err := dbConn.Beginx()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	out := make([]persistResult, 0, len(qs))
	for _, q := range qs {
		args := make([]interface{}, len(q.Args))
		for i, a := range q.Args {
			switch a.Kind {
			case "int":
				args[i] = a.Integer
			case "bytes":
				args[i] = a.Bytes
			case "nil":
				args[i] = nil
			case "ref":
				if a.Reference < 0 || a.Reference >= len(out) {
					return nil, fmt.Errorf("invalid persistence reference")
				}
				args[i] = out[a.Reference].ID
			default:
				args[i] = a.Text
			}
		}
		r, err := tx.Exec(q.SQL, args...)
		if err != nil {
			return nil, err
		}
		id, _ := r.LastInsertId()
		affected, _ := r.RowsAffected()
		out = append(out, persistResult{ID: id, Affected: affected})
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}
func memoryPersist(qs []persistQuery) ([]persistResult, error) {
	memMu.Unlock()
	defer memMu.Lock()
	return persistTransaction(qs)
}
