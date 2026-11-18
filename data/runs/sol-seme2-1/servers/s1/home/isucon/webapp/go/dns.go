package main

import (
 "net"
 "net/http"
 "os"
 "log"
 "sync"
 "strings"
 "time"
 "io"
 "golang.org/x/net/dns/dnsmessage"
)
var dnsNames struct {sync.RWMutex; names map[string][4]byte}
func reloadDNS() error {
 var rows []struct{Name string `db:"name"`;Content string `db:"content"`}
 if err:=dbConn.Select(&rows,"SELECT name,content FROM isudns.records WHERE type='A' AND disabled=0");err!=nil{return err}
 m:=map[string][4]byte{}
 for _,r:=range rows {ip:=net.ParseIP(r.Content).To4();if ip==nil{continue};m[strings.ToLower(strings.TrimSuffix(r.Name,"."))+"."]=[4]byte{ip[0],ip[1],ip[2],ip[3]}}
 dnsNames.Lock();dnsNames.names=m;dnsNames.Unlock();return nil
}
func runDNS(){
 if err:=reloadDNS();err!=nil{log.Fatal(err)}
 mux:=http.NewServeMux()
 mux.HandleFunc("/reload",func(w http.ResponseWriter,r *http.Request){if err:=reloadDNS();err!=nil{http.Error(w,err.Error(),500)}})
 mux.HandleFunc("/add",func(w http.ResponseWriter,r *http.Request){ip:=net.ParseIP(r.URL.Query().Get("ip")).To4();if ip==nil{http.Error(w,"bad ip",400);return};name:=strings.ToLower(r.URL.Query().Get("name"))+".u.isucon.dev.";dnsNames.Lock();dnsNames.names[name]=[4]byte{ip[0],ip[1],ip[2],ip[3]};dnsNames.Unlock()})
 go func(){log.Fatal(http.ListenAndServe(":8053",mux))}()
 c,err:=net.ListenPacket("udp",":53");if err!=nil{log.Fatal(err)}
 buf:=make([]byte,4096)
 for{n,addr,err:=c.ReadFrom(buf);if err!=nil{continue};var p dnsmessage.Parser;h,err:=p.Start(buf[:n]);if err!=nil{continue};q,err:=p.Question();if err!=nil{continue};if h.Response{continue}
  name:=strings.ToLower(q.Name.String());dnsNames.RLock();ip,ok:=dnsNames.names[name];dnsNames.RUnlock()
  rh:=dnsmessage.Header{ID:h.ID,Response:true,Authoritative:true,RecursionDesired:h.RecursionDesired};if !ok{rh.RCode=dnsmessage.RCodeNameError}
  b:=dnsmessage.NewBuilder(make([]byte,0,512),rh);b.EnableCompression();b.StartQuestions();b.Question(q)
  if ok&&q.Type==dnsmessage.TypeA {b.StartAnswers();b.AResource(dnsmessage.ResourceHeader{Name:q.Name,Class:dnsmessage.ClassINET,TTL:60},dnsmessage.AResource{A:ip})}
  out,e:=b.Finish();if e==nil{c.WriteTo(out,addr)}
 }
}
var dnsHTTP = &http.Client{Timeout:5*time.Second}
func notifyDNS(path string)error{host:=os.Getenv("DNS_CONTROL");if host==""{return nil};r,e:=dnsHTTP.Get(host+path);if e!=nil{return e};io.Copy(io.Discard,r.Body);r.Body.Close();if r.StatusCode!=200{return &dnsNotifyError{}};return nil}
type dnsNotifyError struct{}
func (*dnsNotifyError)Error()string{return "DNS update failed"}
