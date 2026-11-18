package main
import("encoding/json";"os";"io";"log";"net";"net/http";"sync";"strings";"golang.org/x/net/dns/dnsmessage")
var mu sync.RWMutex
var names=map[string]bool{"pipe":true}
type update struct {Replace bool;Names []string}
var journal *os.File
func apply(v update){if v.Replace{names=map[string]bool{"pipe":true}};for _,s:=range v.Names{names[strings.ToLower(s)]=true}}
func main(){
 var err error
 journal,err=os.OpenFile("/home/isucon/webapp/dns-names.jsonl",os.O_CREATE|os.O_RDWR|os.O_APPEND,0644);if err!=nil{log.Fatal(err)}
 dec:=json.NewDecoder(journal);for{var v update;e:=dec.Decode(&v);if e==io.EOF{break};if e!=nil{log.Fatal(e)};apply(v)}

 http.HandleFunc("/names",func(w http.ResponseWriter,r *http.Request){var list []string;if err:=json.NewDecoder(r.Body).Decode(&list);err!=nil{http.Error(w,err.Error(),400);return};mu.Lock();defer mu.Unlock();v:=update{Replace:r.Method=="PUT",Names:list};if err:=json.NewEncoder(journal).Encode(v);err!=nil{http.Error(w,err.Error(),500);return};if err:=journal.Sync();err!=nil{http.Error(w,err.Error(),500);return};apply(v)})
 go func(){log.Fatal(http.ListenAndServe(":8053",nil))}()
 conn,err:=net.ListenPacket("udp",":53");if err!=nil{log.Fatal(err)}
 for i:=0;i<4;i++{go serve(conn)};select{}
}
func serve(conn net.PacketConn){buf:=make([]byte,4096);for{n,addr,e:=conn.ReadFrom(buf);if e!=nil{continue};var p dnsmessage.Parser;h,e:=p.Start(buf[:n]);if e!=nil{continue};q,e:=p.Question();if e!=nil{continue};name:=strings.ToLower(q.Name.String());valid:=false
 if strings.HasSuffix(name,".u.isucon.dev."){prefix:=strings.TrimSuffix(name,".u.isucon.dev.");mu.RLock();valid=names[prefix];mu.RUnlock()}else if name=="u.isucon.dev."{valid=true}
 h.Response=true;h.Authoritative=true;h.RecursionAvailable=false;h.RCode=dnsmessage.RCodeSuccess;if !valid{h.RCode=dnsmessage.RCodeNameError}
 b:=dnsmessage.NewBuilder(nil,h);b.EnableCompression();b.StartQuestions();b.Question(q)
 if valid&&q.Type==dnsmessage.TypeA {b.StartAnswers();b.AResource(dnsmessage.ResourceHeader{Name:q.Name,Type:dnsmessage.TypeA,Class:dnsmessage.ClassINET,TTL:60},dnsmessage.AResource{A:[4]byte{10,99,0,11}})}
 out,e:=b.Finish();if e==nil{conn.WriteTo(out,addr)}
}}
