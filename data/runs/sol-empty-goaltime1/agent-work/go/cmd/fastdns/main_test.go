package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"golang.org/x/net/dns/dnsmessage"
)

func fixture(t *testing.T) *zoneStore {
	t.Helper()
	rows := []recordRow{
		{Name: "u.isucon.dev", Type: "SOA", Content: "ns1.u.isucon.dev hostmaster.u.isucon.dev 7 10800 3600 604800 3600", TTL: 3600},
		{Name: "u.isucon.dev", Type: "NS", Content: "ns1.u.isucon.dev", TTL: 0},
		{Name: "u.isucon.dev", Type: "A", Content: "10.99.0.11", TTL: 60},
		{Name: "alice.u.isucon.dev", Type: "A", Content: "10.99.0.11", TTL: 60},
		{Name: "child.parent.u.isucon.dev", Type: "A", Content: "10.99.0.12", TTL: 30},
	}
	names, soa, err := buildZone(rows)
	if err != nil {
		t.Fatal(err)
	}
	return &zoneStore{names: names, soa: soa}
}
func questionPacket(t *testing.T, name string, kind dnsmessage.Type) []byte {
	t.Helper()
	n, err := dnsmessage.NewName(name)
	if err != nil {
		t.Fatal(err)
	}
	q := dnsmessage.Message{Header: dnsmessage.Header{ID: 1234, RecursionDesired: true}, Questions: []dnsmessage.Question{{Name: n, Type: kind, Class: dnsmessage.ClassINET}}}
	data, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func lookup(t *testing.T, s *zoneStore, name string, kind dnsmessage.Type) dnsmessage.Message {
	t.Helper()
	data, err := s.response(questionPacket(t, name, kind))
	if err != nil {
		t.Fatal(err)
	}
	var result dnsmessage.Message
	if err := result.Unpack(data); err != nil {
		t.Fatal(err)
	}
	if result.ID != 1234 || !result.Response || !result.RecursionDesired || result.RecursionAvailable {
		t.Fatalf("bad flags: %#v", result.Header)
	}
	return result
}
func TestAuthoritativeAnswers(t *testing.T) {
	s := fixture(t)
	for _, name := range []string{"alice.u.isucon.dev.", "ALICE.U.ISUCON.DEV."} {
		result := lookup(t, s, name, dnsmessage.TypeA)
		if result.RCode != dnsmessage.RCodeSuccess || !result.Authoritative || len(result.Answers) != 1 {
			t.Fatalf("A: %#v", result)
		}
		a, ok := result.Answers[0].Body.(*dnsmessage.AResource)
		if !ok || a.A != [4]byte{10, 99, 0, 11} || result.Answers[0].Header.TTL != 60 {
			t.Fatalf("wrong A: %#v", result.Answers)
		}
	}
	missing := lookup(t, s, "never-registered.u.isucon.dev.", dnsmessage.TypeA)
	if missing.RCode != dnsmessage.RCodeNameError || !missing.Authoritative || len(missing.Answers) != 0 || len(missing.Authorities) != 1 {
		t.Fatalf("NX: %#v", missing)
	}
	for _, name := range []string{"alice.u.isucon.dev.", "parent.u.isucon.dev."} {
		empty := lookup(t, s, name, dnsmessage.TypeAAAA)
		if empty.RCode != dnsmessage.RCodeSuccess || len(empty.Answers) != 0 || len(empty.Authorities) != 1 {
			t.Fatalf("NODATA: %#v", empty)
		}
	}
	ns := lookup(t, s, zoneName, dnsmessage.TypeNS)
	if len(ns.Answers) != 1 || ns.Answers[0].Header.Type != dnsmessage.TypeNS {
		t.Fatalf("NS: %#v", ns)
	}
	soa := lookup(t, s, zoneName, dnsmessage.TypeSOA)
	if len(soa.Answers) != 1 || soa.Answers[0].Body.(*dnsmessage.SOAResource).Serial != 7 {
		t.Fatalf("SOA: %#v", soa)
	}
	outside := lookup(t, s, "example.org.", dnsmessage.TypeA)
	if outside.RCode != dnsmessage.RCodeRefused || outside.Authoritative {
		t.Fatalf("outside: %#v", outside)
	}
	any := lookup(t, s, zoneName, dnsmessage.TypeALL)
	if len(any.Answers) != 3 {
		t.Fatalf("ANY: %#v", any)
	}
}
func TestMalformedAndMultipleQuestions(t *testing.T) {
	s := fixture(t)
	for size := 0; size < 12; size++ {
		if _, err := s.response(make([]byte, size)); err == nil {
			t.Fatalf("accepted short packet %d", size)
		}
	}
	malformed := []byte{0, 1, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0xc0, 0x0c, 0, 1, 0, 1}
	data, err := s.response(malformed)
	if err != nil {
		t.Fatal(err)
	}
	var result dnsmessage.Message
	if err := result.Unpack(data); err != nil || result.RCode != dnsmessage.RCodeFormatError {
		t.Fatalf("bad compression pointer response: %#v %v", result, err)
	}
	var query dnsmessage.Message
	if err := query.Unpack(questionPacket(t, "alice.u.isucon.dev.", dnsmessage.TypeA)); err != nil {
		t.Fatal(err)
	}
	query.Questions = append(query.Questions, query.Questions[0])
	packet, err := query.Pack()
	if err != nil {
		t.Fatal(err)
	}
	data, err = s.response(packet)
	if err != nil {
		t.Fatal(err)
	}
	if err := result.Unpack(data); err != nil || result.RCode != dnsmessage.RCodeFormatError {
		t.Fatalf("multiple questions: %#v %v", result, err)
	}
}
func TestFlushAccessControl(t *testing.T) {
	s := fixture(t)
	for _, test := range []struct {
		address, key, method, query string
		status                      int
	}{
		{"10.99.0.200:1234", "isudns", "PUT", "?domain=alice.u.isucon.dev.", 403},
		{"127.0.0.1:1234", "wrong", "PUT", "?domain=alice.u.isucon.dev.", 403},
		{"127.0.0.1:1234", "isudns", "POST", "?domain=alice.u.isucon.dev.", 405},
		{"127.0.0.1:1234", "isudns", "PUT", "", 400},
	} {
		req := httptest.NewRequest(test.method, "http://localhost/api/v1/servers/localhost/cache/flush"+test.query, nil)
		req.RemoteAddr = test.address
		req.Header.Set("X-API-Key", test.key)
		rec := httptest.NewRecorder()
		s.flushHandler(rec, req)
		if rec.Code != test.status {
			t.Fatalf("%#v got%d", test, rec.Code)
		}
	}
}
func TestDatabaseRefresh(t *testing.T) {
	if os.Getenv("FASTDNS_INTEGRATION") != "1" {
		t.Skip("requires contest DNS DB")
	}
	db, err := sqlx.Open("mysql", "isudns:isudns@tcp(10.99.0.12:3306)/isudns?interpolateParams=true")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	name := fmt.Sprintf("codex-fastdns-%d.u.isucon.dev", time.Now().UnixNano())
	defer db.Exec("DELETE FROM records WHERE name=?", name)
	store := &zoneStore{db: db}
	ctx := context.Background()
	if err := store.reload(ctx); err != nil {
		t.Fatal(err)
	}
	if r := lookup(t, store, name+".", dnsmessage.TypeA); r.RCode != dnsmessage.RCodeNameError {
		t.Fatal("test name already exists")
	}
	if _, err := db.Exec("INSERT INTO records(domain_id,name,type,content,ttl,prio,disabled,auth) SELECT id,?,'A','10.99.0.11',60,0,0,1 FROM domains WHERE name='u.isucon.dev'", name); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "http://localhost/api/v1/servers/localhost/cache/flush?domain="+name+".", nil)
	req.RemoteAddr = "10.99.0.12:1234"
	req.Header.Set("X-API-Key", "isudns")
	recorder := httptest.NewRecorder()
	store.flushHandler(recorder, req)
	if recorder.Code != 200 {
		t.Fatalf("flush%d: %s", recorder.Code, recorder.Body.String())
	}
	if r := lookup(t, store, strings.ToUpper(name)+".", dnsmessage.TypeA); r.RCode != dnsmessage.RCodeSuccess || len(r.Answers) != 1 {
		t.Fatal("new record not visible")
	}
	if _, err := db.Exec("UPDATE records SET content='10.99.0.12' WHERE name=?", name); err != nil {
		t.Fatal(err)
	}
	if err := store.refresh(ctx, name+"."); err != nil {
		t.Fatal(err)
	}
	if r := lookup(t, store, name+".", dnsmessage.TypeA); r.Answers[0].Body.(*dnsmessage.AResource).A != [4]byte{10, 99, 0, 12} {
		t.Fatal("updated record not visible")
	}
	if _, err := db.Exec("DELETE FROM records WHERE name=?", name); err != nil {
		t.Fatal(err)
	}
	if err := store.refresh(ctx, "u.isucon.dev$"); err != nil {
		t.Fatal(err)
	}
	if r := lookup(t, store, name+".", dnsmessage.TypeA); r.RCode != dnsmessage.RCodeNameError {
		t.Fatal("full reset retained deleted record")
	}
}
func FuzzDNSPacket(f *testing.F) {
	names, soa, err := buildZone([]recordRow{{Name: "u.isucon.dev", Type: "SOA", Content: "ns1.u.isucon.dev hostmaster.u.isucon.dev 0 1 1 1 1", TTL: 1}})
	if err != nil {
		f.Fatal(err)
	}
	store := &zoneStore{names: names, soa: soa}
	f.Add([]byte{0, 1, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0xc0, 0x0c, 0, 1, 0, 1})
	f.Fuzz(func(t *testing.T, packet []byte) { _, _ = store.response(packet) })
}

func wireLookup(t *testing.T, conn net.Conn, name string, kind dnsmessage.Type) dnsmessage.Message {
	t.Helper()
	packet := questionPacket(t, name, kind)
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(packet); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 4096)
	n, err := conn.Read(data)
	if err != nil {
		t.Fatal(err)
	}
	var msg dnsmessage.Message
	if err := msg.Unpack(data[:n]); err != nil {
		t.Fatal(err)
	}
	if msg.ID != 1234 || len(msg.Questions) != 1 || !strings.EqualFold(msg.Questions[0].Name.String(), name) {
		t.Fatalf("bad response identity: %#v", msg)
	}
	return msg
}
func normalizedRecords(records []dnsmessage.Resource) []string {
	result := []string{}
	for _, record := range records {
		var content string
		switch body := record.Body.(type) {
		case *dnsmessage.AResource:
			content = fmt.Sprint(body.A)
		case *dnsmessage.NSResource:
			content = canonical(body.NS.String())
		case *dnsmessage.SOAResource:
			content = fmt.Sprintf("%s %s %d %d %d %d %d", canonical(body.NS.String()), canonical(body.MBox.String()), body.Serial, body.Refresh, body.Retry, body.Expire, body.MinTTL)
		default:
			content = fmt.Sprintf("%#v", body)
		}
		result = append(result, fmt.Sprintf("%s %d %d %s", canonical(record.Header.Name.String()), record.Header.Type, record.Header.Class, content))
	}
	sort.Strings(result)
	return result
}
func TestLivePowerDNSComparison(t *testing.T) {
	if os.Getenv("FASTDNS_COMPARE") != "1" {
		t.Skip("requires live PowerDNS/prototype")
	}
	db, err := sqlx.Open("mysql", "isudns:isudns@tcp(10.99.0.12:3306)/isudns?interpolateParams=true")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var rows []recordRow
	if err := db.Select(&rows, recordsQuery); err != nil {
		t.Fatal(err)
	}
	native, err := net.Dial("udp4", "127.0.0.1:53")
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	candidate, err := net.Dial("udp4", "127.0.0.1:5354")
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.Close()
	compare := func(name string, kind dnsmessage.Type) {
		expected := wireLookup(t, native, name, kind)
		actual := wireLookup(t, candidate, name, kind)
		if expected.RCode != actual.RCode || expected.Authoritative != actual.Authoritative || !reflect.DeepEqual(normalizedRecords(expected.Answers), normalizedRecords(actual.Answers)) || !reflect.DeepEqual(normalizedRecords(expected.Authorities), normalizedRecords(actual.Authorities)) {
			t.Fatalf("%s %v: expected%#v actual%#v", name, kind, expected, actual)
		}
	}
	for _, row := range rows {
		if row.Type == "A" {
			compare(canonical(row.Name), dnsmessage.TypeA)
		}
	}
	for _, kind := range []dnsmessage.Type{dnsmessage.TypeA, dnsmessage.TypeAAAA, dnsmessage.TypeNS, dnsmessage.TypeSOA} {
		compare(zoneName, kind)
		compare("PIPE.U.ISUCON.DEV.", kind)
	}
	for i := 0; i < 200; i++ {
		compare(fmt.Sprintf("codex-nx-%d-%d.u.isucon.dev.", time.Now().UnixNano(), i), dnsmessage.TypeA)
	}
	t.Logf("compared %d records, metadata and 200 absent names", len(rows))
}

func TestConcurrentResponsesAndReset(t *testing.T) {
	store := fixture(t)
	replacement := fixture(t)
	packet := questionPacket(t, "alice.u.isucon.dev.", dnsmessage.TypeA)
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for j := 0; j < 1000; j++ {
				if _, err := store.response(packet); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	for i := 0; i < 1000; i++ {
		store.Lock()
		store.names = replacement.names
		store.soa = replacement.soa
		store.Unlock()
	}
	group.Wait()
}
func TestLiveHTTPRefresh(t *testing.T) {
	if os.Getenv("FASTDNS_COMPARE") != "1" {
		t.Skip("requires running prototype")
	}
	db, err := sqlx.Open("mysql", "isudns:isudns@tcp(10.99.0.12:3306)/isudns?interpolateParams=true")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := net.Dial("udp4", "127.0.0.1:5354")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	flush := func(name string) error {
		req, err := http.NewRequest(http.MethodPut, "http://127.0.0.1:8082/api/v1/servers/localhost/cache/flush?domain="+name, nil)
		if err != nil {
			return err
		}
		req.Header.Set("X-API-Key", "isudns")
		client := &http.Client{Timeout: 3 * time.Second}
		res, err := client.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		io.Copy(io.Discard, res.Body)
		if res.StatusCode != 200 {
			return fmt.Errorf("HTTP %d", res.StatusCode)
		}
		return nil
	}
	name := fmt.Sprintf("codex-live-%d.u.isucon.dev", time.Now().UnixNano())
	defer func() {
		db.Exec("DELETE FROM records WHERE name=?", name)
		if err := flush("u.isucon.dev$"); err != nil {
			t.Error(err)
		}
	}()
	if result := wireLookup(t, conn, name+".", dnsmessage.TypeA); result.RCode != dnsmessage.RCodeNameError {
		t.Fatal("name unexpectedly exists")
	}
	if _, err := db.Exec("INSERT INTO records(domain_id,name,type,content,ttl,prio,disabled,auth) SELECT id,?,'A','10.99.0.11',60,0,0,1 FROM domains WHERE name='u.isucon.dev'", name); err != nil {
		t.Fatal(err)
	}
	if err := flush(name + "."); err != nil {
		t.Fatal(err)
	}
	if result := wireLookup(t, conn, name+".", dnsmessage.TypeA); result.RCode != dnsmessage.RCodeSuccess || len(result.Answers) != 1 {
		t.Fatal("HTTP update not reflected on UDP")
	}
	if _, err := db.Exec("DELETE FROM records WHERE name=?", name); err != nil {
		t.Fatal(err)
	}
	if err := flush("u.isucon.dev$"); err != nil {
		t.Fatal(err)
	}
	if result := wireLookup(t, conn, name+".", dnsmessage.TypeA); result.RCode != dnsmessage.RCodeNameError {
		t.Fatal("HTTP full reset not reflected on UDP")
	}
}

func BenchmarkAbsentQueries(b *testing.B) {
	rows := []recordRow{{Name: "u.isucon.dev", Type: "SOA", Content: "ns1.u.isucon.dev hostmaster.u.isucon.dev 0 10800 3600 604800 3600", TTL: 3600}}
	for i := 0; i < 4000; i++ {
		rows = append(rows, recordRow{Name: fmt.Sprintf("streamer%d.u.isucon.dev", i), Type: "A", Content: "10.99.0.11", TTL: 60})
	}
	names, soa, err := buildZone(rows)
	if err != nil {
		b.Fatal(err)
	}
	store := &zoneStore{names: names, soa: soa}
	packets := make([][]byte, 1024)
	for i := range packets {
		name, _ := dnsmessage.NewName(fmt.Sprintf("unknownname%d.u.isucon.dev.", i))
		msg := dnsmessage.Message{Header: dnsmessage.Header{ID: 1}, Questions: []dnsmessage.Question{{Name: name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}}
		packets[i], err = msg.Pack()
		if err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if _, err := store.response(packets[i&1023]); err != nil {
				b.Fatal(err)
			}
			i++
		}
	})
}

func TestLiveDatabaseAnswers(t *testing.T) {
 if os.Getenv("FASTDNS_PRODUCTION")!="1" {t.Skip("requires production DNS")}
 db,err:=sqlx.Open("mysql","isudns:isudns@tcp(10.99.0.12:3306)/isudns?interpolateParams=true");if err!=nil{t.Fatal(err)};defer db.Close()
 var rows []recordRow;if err:=db.Select(&rows,recordsQuery);err!=nil{t.Fatal(err)}
 conn,err:=net.Dial("udp4","127.0.0.1:53");if err!=nil{t.Fatal(err)};defer conn.Close()
 total:=0
 for _,row:=range rows {
  if row.Type!="A"{continue}
  result:=wireLookup(t,conn,canonical(row.Name),dnsmessage.TypeA)
  if result.RCode!=dnsmessage.RCodeSuccess || len(result.Answers)==0 {t.Fatalf("missing DB name %s",row.Name)}
  found:=false
  for _,answer:=range result.Answers {
   body,ok:=answer.Body.(*dnsmessage.AResource);if !ok{t.Fatalf("non-A answer forA question: %s",row.Name)}
   if net.IP(body.A[:]).String()==row.Content && answer.Header.TTL==row.TTL{found=true}
  }
  if !found{t.Fatalf("DB address/TTL mismatch %s",row.Name)}
  total++
 }
 for i:=0;i<200;i++ {r:=wireLookup(t,conn,fmt.Sprintf("absent-%d-%d.u.isucon.dev.",time.Now().UnixNano(),i),dnsmessage.TypeA);if r.RCode!=dnsmessage.RCodeNameError{t.Fatal("fabricated non-DB name")}}
 t.Logf("verified %d DB A records and 200 absent names",total)
}
