package main
import (
 "bufio"
 "log"
 "net"
 "os"
 "strings"
 "golang.org/x/net/dns/dnsmessage"
)
func serveMemoryDNS() {
 names:=map[string]bool{}
 f,err:=os.Open("../pdns/u.isucon.dev.zone");if err!=nil {log.Fatal(err)}
 sc:=bufio.NewScanner(f);for sc.Scan() {a:=strings.Fields(sc.Text());if len(a)>=5&&a[2]=="IN"&&a[3]=="A" {name:=a[0];if name=="@" {name="u.isucon.dev."} else {name+=".u.isucon.dev."};names[name]=true}};f.Close()
 ip:=net.ParseIP(powerDNSSubdomainAddress).To4();if ip==nil {log.Fatal("invalid DNS address")};var address [4]byte;copy(address[:],ip)
 conn,err:=net.ListenPacket("udp4",":53");if err!=nil {log.Fatal(err)};defer conn.Close()
 buf:=make([]byte,4096)
 for {n,addr,err:=conn.ReadFrom(buf);if err!=nil {log.Print(err);continue};var p dnsmessage.Parser;h,err:=p.Start(buf[:n]);if err!=nil||h.Response {continue};q,err:=p.Question();if err!=nil{continue}
  name:=strings.ToLower(q.Name.String());exists:=names[name]
  if !exists&&strings.HasSuffix(name,".u.isucon.dev.") {username:=strings.TrimSuffix(name,".u.isucon.dev.");memMu.RLock();exists=mem.DNSNames[username];memMu.RUnlock()}
  rc:=dnsmessage.RCodeSuccess;if !exists {rc=dnsmessage.RCodeNameError}
  b:=dnsmessage.NewBuilder(nil,dnsmessage.Header{ID:h.ID,Response:true,Authoritative:true,RecursionDesired:h.RecursionDesired,RCode:rc});b.EnableCompression();b.StartQuestions();b.Question(q)
  if exists&&q.Class==dnsmessage.ClassINET&&q.Type==dnsmessage.TypeA {b.StartAnswers();b.AResource(dnsmessage.ResourceHeader{Name:q.Name,Type:dnsmessage.TypeA,Class:dnsmessage.ClassINET,TTL:60},dnsmessage.AResource{A:address})}
  out,err:=b.Finish();if err==nil {conn.WriteTo(out,addr)}
 }
}
