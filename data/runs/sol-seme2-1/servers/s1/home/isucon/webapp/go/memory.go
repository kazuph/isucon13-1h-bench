package main

import (
 "sync"
 "sort"
 "strconv"
 "net/http"
 "fmt"
 "crypto/sha256"
 "encoding/json"
 "strings"
 "github.com/labstack/echo/v4"
 "github.com/labstack/echo-contrib/session"
)

type streamTotals struct { reactions,comments,tips,viewers,reports int64 }
type readState struct {
 sync.RWMutex
 ready bool
 users map[int64]User
 names map[string]int64
 models map[int64]UserModel
 icons map[int64][]byte
 streams map[int64]LivestreamModel
 streamTags map[int64][]Tag
 userStreams map[int64][]int64
 streamIDs []int64
 tags []Tag
 comments map[int64]LivecommentModel
 commentIDs map[int64][]int64
 reactions map[int64]ReactionModel
 reactionIDs map[int64][]int64
 words map[int64][]NGWord
 reports map[int64][]LivecommentReportModel
 viewers map[int64]map[int64]int64
 totals map[int64]*streamTotals
}
var mem = &readState{}
var streamLocks sync.Map
func lockStream(id int64) func(){ v,_:=streamLocks.LoadOrStore(id,&sync.Mutex{});m:=v.(*sync.Mutex);m.Lock();return m.Unlock }

func loadMemory() error {
 var users []UserModel; var themes []ThemeModel
 var icons []struct { UserID int64 `db:"user_id"`; Image []byte `db:"image"` }
 var streams []LivestreamModel;var tags []TagModel;var links []LivestreamTagModel
 var comments []LivecommentModel;var reactions []ReactionModel;var words []NGWord;var reports []LivecommentReportModel
 var viewers []struct {UserID int64 `db:"user_id"`;LivestreamID int64 `db:"livestream_id"`}
 for _,q:=range []struct{dst interface{};sql string}{
 {&users,"SELECT * FROM users"},{&themes,"SELECT * FROM themes"},{&icons,"SELECT user_id,image FROM icons"},
 {&streams,"SELECT * FROM livestreams ORDER BY id"},{&tags,"SELECT * FROM tags ORDER BY id"},{&links,"SELECT * FROM livestream_tags ORDER BY id"},
 {&comments,"SELECT * FROM livecomments ORDER BY id"},{&reactions,"SELECT * FROM reactions ORDER BY id"},{&words,"SELECT * FROM ng_words ORDER BY id"},
 {&reports,"SELECT * FROM livecomment_reports ORDER BY id"},{&viewers,"SELECT user_id,livestream_id FROM livestream_viewers_history"},
 } { if err:=dbConn.Select(q.dst,q.sql);err!=nil{return err} }
 mem.Lock();defer mem.Unlock()
 mem.users=map[int64]User{};mem.names=map[string]int64{};mem.models=map[int64]UserModel{};mem.icons=map[int64][]byte{}
 mem.streams=map[int64]LivestreamModel{};mem.streamTags=map[int64][]Tag{};mem.userStreams=map[int64][]int64{};mem.streamIDs=nil;mem.tags=[]Tag{}
 mem.comments=map[int64]LivecommentModel{};mem.commentIDs=map[int64][]int64{};mem.reactions=map[int64]ReactionModel{};mem.reactionIDs=map[int64][]int64{}
 mem.words=map[int64][]NGWord{};mem.reports=map[int64][]LivecommentReportModel{};mem.viewers=map[int64]map[int64]int64{};mem.totals=map[int64]*streamTotals{}
 _,fallback:=defaultIcon()
 for _,m:=range users {mem.models[m.ID]=m;mem.names[m.Name]=m.ID;mem.users[m.ID]=User{ID:m.ID,Name:m.Name,DisplayName:m.DisplayName,Description:m.Description,IconHash:fallback}}
 for _,t:=range themes {u:=mem.users[t.UserID];u.Theme=Theme{ID:t.ID,DarkMode:t.DarkMode};mem.users[t.UserID]=u}
 for _,i:=range icons {mem.icons[i.UserID]=i.Image;u:=mem.users[i.UserID];u.IconHash=fmt.Sprintf("%x",sha256.Sum256(i.Image));mem.users[i.UserID]=u}
 for _,s:=range streams {mem.streams[s.ID]=s;mem.streamIDs=append(mem.streamIDs,s.ID);mem.userStreams[s.UserID]=append(mem.userStreams[s.UserID],s.ID);mem.totals[s.ID]=&streamTotals{};mem.streamTags[s.ID]=[]Tag{}}
 tagMap:=map[int64]Tag{}
 for _,t:=range tags {v:=Tag{ID:t.ID,Name:t.Name};tagMap[t.ID]=v;mem.tags=append(mem.tags,v)}
 for _,l:=range links {mem.streamTags[l.LivestreamID]=append(mem.streamTags[l.LivestreamID],tagMap[l.TagID])}
 for _,c:=range comments {mem.comments[c.ID]=c;mem.commentIDs[c.LivestreamID]=append(mem.commentIDs[c.LivestreamID],c.ID);t:=mem.total(c.LivestreamID);t.comments++;t.tips+=c.Tip}
 for _,r:=range reactions {mem.reactions[r.ID]=r;mem.reactionIDs[r.LivestreamID]=append(mem.reactionIDs[r.LivestreamID],r.ID);mem.total(r.LivestreamID).reactions++}
 for _,w:=range words {mem.words[w.LivestreamID]=append(mem.words[w.LivestreamID],w)}
 for _,r:=range reports {mem.reports[r.LivestreamID]=append(mem.reports[r.LivestreamID],r);mem.total(r.LivestreamID).reports++}
 for _,v:=range viewers {if mem.viewers[v.LivestreamID]==nil{mem.viewers[v.LivestreamID]=map[int64]int64{}};mem.viewers[v.LivestreamID][v.UserID]++;mem.total(v.LivestreamID).viewers++}
 mem.ready=true
 return nil
}
func (s *readState) total(id int64)*streamTotals {t:=s.totals[id];if t==nil{t=&streamTotals{};s.totals[id]=t};return t}
func (s *readState) stream(id int64)Livestream {m:=s.streams[id];tags:=s.streamTags[id];if tags==nil{tags=[]Tag{}};return Livestream{ID:m.ID,Owner:s.users[m.UserID],Title:m.Title,Description:m.Description,PlaylistUrl:m.PlaylistUrl,ThumbnailUrl:m.ThumbnailUrl,StartAt:m.StartAt,EndAt:m.EndAt,Tags:tags}}
func (s *readState) comment(id int64)Livecomment {m:=s.comments[id];return Livecomment{ID:m.ID,User:s.users[m.UserID],Livestream:s.stream(m.LivestreamID),Comment:m.Comment,Tip:m.Tip,CreatedAt:m.CreatedAt}}
func (s *readState) reaction(id int64)Reaction {m:=s.reactions[id];return Reaction{ID:m.ID,User:s.users[m.UserID],Livestream:s.stream(m.LivestreamID),EmojiName:m.EmojiName,CreatedAt:m.CreatedAt}}
func memAddUser(m UserModel,u User){mem.Lock();defer mem.Unlock();mem.users[m.ID]=u;mem.names[m.Name]=m.ID;mem.models[m.ID]=m}
func memIcon(id int64,img []byte){mem.Lock();defer mem.Unlock();mem.icons[id]=img;u:=mem.users[id];u.IconHash=fmt.Sprintf("%x",sha256.Sum256(img));mem.users[id]=u}
func memAddStream(m LivestreamModel,tags []int64){mem.Lock();defer mem.Unlock();mem.streams[m.ID]=m;mem.streamIDs=append(mem.streamIDs,m.ID);mem.userStreams[m.UserID]=append(mem.userStreams[m.UserID],m.ID);mem.totals[m.ID]=&streamTotals{};out:=[]Tag{};for _,id:=range tags{for _,t:=range mem.tags{if t.ID==id{out=append(out,t);break}}};mem.streamTags[m.ID]=out}
func memAddComment(m LivecommentModel){mem.Lock();defer mem.Unlock();mem.comments[m.ID]=m;mem.commentIDs[m.LivestreamID]=append(mem.commentIDs[m.LivestreamID],m.ID);t:=mem.total(m.LivestreamID);t.comments++;t.tips+=m.Tip}
func memAddReaction(m ReactionModel){mem.Lock();defer mem.Unlock();mem.reactions[m.ID]=m;mem.reactionIDs[m.LivestreamID]=append(mem.reactionIDs[m.LivestreamID],m.ID);mem.total(m.LivestreamID).reactions++}
func memAddReport(m LivecommentReportModel){mem.Lock();defer mem.Unlock();mem.reports[m.LivestreamID]=append(mem.reports[m.LivestreamID],m);mem.total(m.LivestreamID).reports++}
func memEnter(uid,lid int64){mem.Lock();defer mem.Unlock();if mem.viewers[lid]==nil{mem.viewers[lid]=map[int64]int64{}};mem.viewers[lid][uid]++;mem.total(lid).viewers++}
func memExit(uid,lid int64){mem.Lock();defer mem.Unlock();mem.total(lid).viewers-=mem.viewers[lid][uid];delete(mem.viewers[lid],uid)}
// MySQL LIKE with the default backslash escape and binary collation.
func likeWord(text,word string)bool {
 p:=[]rune("%"+word+"%");v:=[]rune(text)
 type token struct{r rune;kind int}; ts:=[]token{}
 for i:=0;i<len(p);i++ {if p[i]=='\\' && i+1<len(p){i++;ts=append(ts,token{p[i],0})}else if p[i]=='%'{ts=append(ts,token{0,2})}else if p[i]=='_'{ts=append(ts,token{0,1})}else{ts=append(ts,token{p[i],0})}}
 prev:=make([]bool,len(v)+1);prev[0]=true
 for _,t:=range ts {next:=make([]bool,len(v)+1);if t.kind==2{next[0]=prev[0]};for j:=1;j<=len(v);j++{if t.kind==2{next[j]=prev[j]||next[j-1]}else{next[j]=prev[j-1]&&(t.kind==1||t.r==v[j-1])}};prev=next};return prev[len(v)]
}
func memModerate(w NGWord){mem.Lock();defer mem.Unlock();mem.words[w.LivestreamID]=append(mem.words[w.LivestreamID],w);out:=make([]int64,0,len(mem.commentIDs[w.LivestreamID]));t:=mem.total(w.LivestreamID);for _,id:=range mem.commentIDs[w.LivestreamID]{c:=mem.comments[id];if likeWord(c.Comment,w.Word){t.comments--;t.tips-=c.Tip}else{out=append(out,id)}};mem.commentIDs[w.LivestreamID]=out}

func memID(c echo.Context)(int64,error){v,e:=strconv.ParseInt(c.Param("livestream_id"),10,64);if e!=nil{return 0,echo.NewHTTPError(400,"invalid livestream id")};return v,nil}
func memUID(c echo.Context)int64{s,_:=session.Get(defaultSessionIDKey,c);return s.Values[defaultUserIDKey].(int64)}
func memLimit(c echo.Context,n int)(int,error){if v:=c.QueryParam("limit");v!=""{i,e:=strconv.Atoi(v);if e!=nil||i<0{return 0,echo.NewHTTPError(400,"invalid limit")};if i<n{n=i}};return n,nil}
func memoryGet(c echo.Context)error {
 path:=c.Path()
 if path!="/api/tag"&&path!="/api/livestream/search"&&path!="/api/user/:username/icon"&&path!="/api/payment" {if err:=verifyUserSession(c);err!=nil{return err}}
 mem.RLock();locked:=true;defer func(){if locked{mem.RUnlock()}}()
 var out interface{}
 switch path {
 case "/api/payment": var total int64;for _,t:=range mem.totals{total+=t.tips};out=PaymentResult{TotalTip:total}
 case "/api/tag": out=struct{Tags []Tag `json:"tags"`}{mem.tags}
 case "/api/user/me","/api/user/:username","/api/user/:username/theme","/api/user/:username/icon":
  id:=mem.names[c.Param("username")];if path=="/api/user/me"{id=memUID(c)}
  u,ok:=mem.users[id];if !ok{return echo.NewHTTPError(404,"user not found")}
  if strings.HasSuffix(path,"/icon") {img:=mem.icons[id];if img==nil{img,_=defaultIcon()};etag:="\""+u.IconHash+"\"";c.Response().Header().Set("ETag",etag);if c.Request().Header.Get("If-None-Match")==etag{return c.NoContent(304)};return c.Blob(200,"image/jpeg",img)}
  if strings.HasSuffix(path,"/theme"){out=u.Theme}else{out=u}
 case "/api/livestream/search","/api/livestream","/api/user/:username/livestream":
  var ids []int64
  if path=="/api/livestream/search" {ids=make([]int64,0,len(mem.streamIDs));tag:=c.QueryParam("tag");for _,id:=range mem.streamIDs{if tag==""{ids=append(ids,id)}else{for _,t:=range mem.streamTags[id]{if t.Name==tag{ids=append(ids,id)}}}};sort.Slice(ids,func(i,j int)bool{return ids[i]>ids[j]});if tag==""{n,e:=memLimit(c,len(ids));if e!=nil{return e};ids=ids[:n]}}else{uid:=memUID(c);if path!="/api/livestream"{var ok bool;uid,ok=mem.names[c.Param("username")];if !ok{return echo.NewHTTPError(404,"user not found")}};ids=append([]int64{},mem.userStreams[uid]...);sort.Slice(ids,func(i,j int)bool{return ids[i]<ids[j]})}
  rows:=make([]Livestream,len(ids));for i,id:=range ids{rows[i]=mem.stream(id)};out=rows
 case "/api/user/:username/statistics":
  uid,ok:=mem.names[c.Param("username")];if !ok{return echo.NewHTTPError(400,"user not found")}
  stats:=UserStatistics{Rank:1};scores:=map[int64]int64{};emoji:=map[string]int64{}
  for id,m:=range mem.streams {t:=mem.totals[id];if t==nil{continue};scores[m.UserID]+=t.reactions+t.tips;if m.UserID==uid{stats.TotalReactions+=t.reactions;stats.TotalLivecomments+=t.comments;stats.TotalTip+=t.tips;stats.ViewersCount+=t.viewers;for _,rid:=range mem.reactionIDs[id]{emoji[mem.reactions[rid].EmojiName]++}}}
  score:=scores[uid];for id,u:=range mem.users{if scores[id]>score||(scores[id]==score&&u.Name>c.Param("username")){stats.Rank++}}
  var best int64;for e,n:=range emoji{if n>best||(n==best&&e>stats.FavoriteEmoji){best=n;stats.FavoriteEmoji=e}};out=stats
 default:
  lid,e:=memID(c);if e!=nil{return e};_,exists:=mem.streams[lid]
  switch path {
  case "/api/livestream/:livestream_id":if !exists{return echo.NewHTTPError(404,"livestream not found")};out=mem.stream(lid)
  case "/api/livestream/:livestream_id/livecomment":
   ids:=append([]int64{},mem.commentIDs[lid]...);sort.SliceStable(ids,func(i,j int)bool{return mem.comments[ids[i]].CreatedAt>mem.comments[ids[j]].CreatedAt});n,e:=memLimit(c,len(ids));if e!=nil{return e};rows:=make([]Livecomment,n);for i,id:=range ids[:n]{rows[i]=mem.comment(id)};out=rows
  case "/api/livestream/:livestream_id/reaction":
   ids:=append([]int64{},mem.reactionIDs[lid]...);sort.SliceStable(ids,func(i,j int)bool{return mem.reactions[ids[i]].CreatedAt>mem.reactions[ids[j]].CreatedAt});n,e:=memLimit(c,len(ids));if e!=nil{return e};rows:=make([]Reaction,n);for i,id:=range ids[:n]{rows[i]=mem.reaction(id)};out=rows
  case "/api/livestream/:livestream_id/ngwords":
   rows:=[]NGWord{};uid:=memUID(c);for _,w:=range mem.words[lid]{if w.UserID==uid{rows=append(rows,w)}};sort.SliceStable(rows,func(i,j int)bool{return rows[i].CreatedAt>rows[j].CreatedAt});out=rows
  case "/api/livestream/:livestream_id/report":
   if !exists{return echo.NewHTTPError(404,"livestream not found")};if mem.streams[lid].UserID!=memUID(c){return echo.NewHTTPError(403,"not owner")};rows:=[]LivecommentReport{};for _,r:=range mem.reports[lid]{rows=append(rows,LivecommentReport{ID:r.ID,Reporter:mem.users[r.UserID],Livecomment:mem.comment(r.LivecommentID),CreatedAt:r.CreatedAt})};out=rows
  case "/api/livestream/:livestream_id/statistics":
   if !exists{return echo.NewHTTPError(400,"livestream not found")};t:=mem.totals[lid];stats:=LivestreamStatistics{Rank:1,ViewersCount:t.viewers,TotalReactions:t.reactions,TotalReports:t.reports};score:=t.reactions+t.tips;for id,tot:=range mem.totals{if _,ok:=mem.streams[id];!ok{continue};if tot.reactions+tot.tips>score||(tot.reactions+tot.tips==score&&id>lid){stats.Rank++}};for _,id:=range mem.commentIDs[lid]{if mem.comments[id].Tip>stats.MaxTip{stats.MaxTip=mem.comments[id].Tip}};out=stats
  default:return echo.NewHTTPError(http.StatusNotFound)
  }
 }
 // Marshal under the read lock: all nested slices are immutable once published.
 data,err:=json.Marshal(out);mem.RUnlock();locked=false;if err!=nil{return err};return c.JSONBlob(http.StatusOK,append(data,'\n'))
}
