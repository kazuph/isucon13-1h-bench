package main

import (
 "bytes"
 "encoding/gob"
 "testing"
)
func TestEventReplay(t *testing.T) {
 s:=freshStore();u:=&memUser{Model:UserModel{ID:1,Name:"CaseUser"},Response:User{ID:1,Name:"CaseUser"}}
 s.apply(memEvent{Kind:"user",User:u});s.apply(memEvent{Kind:"stream",Stream:&LivestreamModel{ID:1,UserID:1}})
 events:=[]memEvent{
  {Kind:"comment",Comment:&LivecommentModel{ID:1,UserID:1,LivestreamID:1,Comment:"normal",Tip:10}},
  {Kind:"comment",Comment:&LivecommentModel{ID:2,UserID:1,LivestreamID:1,Comment:"spam",Tip:20}},
  {Kind:"reaction",Reaction:&ReactionModel{ID:1,UserID:1,LivestreamID:1,EmojiName:"smile"}},
  {Kind:"enter",Viewer:&LivestreamViewerModel{UserID:1,LivestreamID:1}},
  {Kind:"enter",Viewer:&LivestreamViewerModel{UserID:1,LivestreamID:1}},
  {Kind:"exit",Viewer:&LivestreamViewerModel{UserID:1,LivestreamID:1}},
  {Kind:"word",Word:&NGWord{ID:1,UserID:1,LivestreamID:1,Word:"spam"}},
 }
 var b bytes.Buffer;if err:=gob.NewEncoder(&b).Encode(s);err!=nil {t.Fatal(err)}
 restored:=freshStore();if err:=gob.NewDecoder(&b).Decode(restored);err!=nil {t.Fatal(err)}
 for _,e:=range events {s.apply(e);restored.apply(e)}
 for _,m:=range []*memoryStore{s,restored} {l:=m.Streams[1];if m.TotalTip!=10||l.MaxTip!=10||l.CommentCount!=1||l.ReactionCount!=1||l.ViewerCount!=0||!m.DNSNames["caseuser"] {t.Fatalf("incorrect replay: %+v, total %d",l,m.TotalTip)};if m.next("comment")!=3 {t.Fatal("ID reuse")}}
}
func TestLike(t *testing.T) {
 for _,v:=range []struct{text,word string;want bool}{{"日本語spamです","spam",true},{"普通","spam",false},{"xabcdefy","a%f",true},{"abc","a_c",true},{"a_c",`a\_c`,true},{"abc",`a\_c`,false},{"a%b",`a\%b`,true},{"日本語","日_語",true}} {if got:=likeContains(v.text,v.word);got!=v.want {t.Errorf("%q LIKE %q: %v",v.text,v.word,got)}}
}
