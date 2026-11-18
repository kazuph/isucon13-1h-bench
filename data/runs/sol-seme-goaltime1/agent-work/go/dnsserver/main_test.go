package main

import (
 "testing"
 "golang.org/x/net/dns/dnsmessage"
)
func TestAuthoritativeAnswers(t *testing.T){
 current.Store(&zoneData{A:map[string][4]byte{"pipe.u.isucon.dev.":{10,99,0,11}},Known:map[string]bool{zone:true,"pipe.u.isucon.dev.":true}})
 for _,tt:=range []struct{name string;kind dnsmessage.Type;rcode dnsmessage.RCode;answers int}{{"pipe.u.isucon.dev.",dnsmessage.TypeA,0,1},{"PIPE.u.isucon.dev.",dnsmessage.TypeA,0,1},{"missing.u.isucon.dev.",dnsmessage.TypeA,dnsmessage.RCodeNameError,0},{"pipe.u.isucon.dev.",dnsmessage.TypeAAAA,0,0},{zone,dnsmessage.TypeSOA,0,1},{"example.org.",dnsmessage.TypeA,dnsmessage.RCodeRefused,0}} {
  n,_:=dnsmessage.NewName(tt.name);msg:=dnsmessage.Message{Header:dnsmessage.Header{ID:123,RecursionDesired:true},Questions:[]dnsmessage.Question{{Name:n,Type:tt.kind,Class:dnsmessage.ClassINET}}};packet,e:=msg.Pack();if e!=nil{t.Fatal(e)};out,e:=answer(packet);if e!=nil{t.Fatal(e)};var response dnsmessage.Message;if e=response.Unpack(out);e!=nil{t.Fatal(e)};if response.Header.ID!=123||!response.Header.Response||response.Header.RCode!=tt.rcode||len(response.Answers)!=tt.answers{t.Fatalf("%s type %v: %+v",tt.name,tt.kind,response)}
 }
}
