package main
import("net";"strings";"golang.org/x/net/dns/dnsmessage")
func serveDNS(){
 conn,e:=net.ListenPacket("udp",":53");if e!=nil{panic(e)}
 for i:=0;i<4;i++{go func(){b:=make([]byte,4096);for{n,addr,e:=conn.ReadFrom(b);if e!=nil{return};var p dnsmessage.Parser;h,e:=p.Start(b[:n]);if e!=nil{continue};q,e:=p.Question();if e!=nil{continue};name:=strings.ToLower(q.Name.String());mu.RLock();ok:=st!=nil && st.DNS[name];mu.RUnlock();h.Response=true;h.Authoritative=true;h.RecursionAvailable=false;h.RCode=dnsmessage.RCodeSuccess;if !ok{h.RCode=dnsmessage.RCodeNameError};builder:=dnsmessage.NewBuilder(nil,h);builder.EnableCompression();builder.StartQuestions();builder.Question(q);builder.StartAnswers();if ok && q.Type==dnsmessage.TypeA{builder.AResource(dnsmessage.ResourceHeader{Name:q.Name,Type:dnsmessage.TypeA,Class:dnsmessage.ClassINET,TTL:60},dnsmessage.AResource{A:[4]byte{10,99,0,12}})};out,e:=builder.Finish();if e==nil{conn.WriteTo(out,addr)}}}()}
}
