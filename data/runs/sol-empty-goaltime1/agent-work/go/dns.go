package main

import (
	"context"
	"fmt"
	"github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

var dnsConn *sqlx.DB

func connectDNS() (*sqlx.DB, error) {
	host := os.Getenv("ISUCON13_DNS_DB_ADDRESS")
	if host == "" {
		host = "127.0.0.1"
	}
	conf := mysql.NewConfig()
	conf.Net = "tcp"
	conf.Addr = net.JoinHostPort(host, "3306")
	conf.User = "isudns"
	conf.Passwd = "isudns"
	conf.DBName = "isudns"
	conf.InterpolateParams = true
	db, err := sqlx.Open("mysql", conf.FormatDSN())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
func addDNSRecord(ctx context.Context, username string) error {
	res, err := dnsConn.ExecContext(ctx, `INSERT INTO records (domain_id,name,type,content,ttl,prio,disabled,auth) SELECT id, ?, 'A', ?, 60, 0, 0, 1 FROM domains WHERE name='u.isucon.dev'`, username+".u.isucon.dev", powerDNSSubdomainAddress)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("DNS zone not found")
	}
	return flushDNSRecord(ctx, username+".u.isucon.dev.")
}

var dnsHTTP = &http.Client{Timeout: 2 * time.Second}

func flushDNSRecord(ctx context.Context, name string) error {
	base := os.Getenv("ISUCON13_DNS_API_URL")
	if base == "" {
		base = "http://127.0.0.1:8081"
	}
	endpoint := strings.TrimRight(base, "/") + "/api/v1/servers/localhost/cache/flush?domain=" + url.QueryEscape(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", "isudns")
	res, err := dnsHTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 4096))
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("DNS cache purge HTTP %d: %s", res.StatusCode, body)
	}
	return nil
}
