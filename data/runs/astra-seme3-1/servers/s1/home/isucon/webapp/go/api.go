package main
import (
 "encoding/json"
 "net/http"
 "sort"
 "strconv"
 "time"
 "github.com/labstack/echo/v4"
 "github.com/labstack/echo-contrib/session"
)
func uid(c echo.Context) int64 {v,_:=session.Get(defaultSessionIDKey,c);id,_:=v.Values[defaultUserIDKey].(int64);return id}
func bad(s string) error {return echo.NewHTTPError(400,s)}
func missing(s string) error {return echo.NewHTTPError(404,s)}
func parseID(c echo.Context) (int64,error) {id,e:=strconv.ParseInt(c.Param("livestream_id"),10,64);if e!=nil{return 0,bad("invalid livestream_id")};return id,nil}
func limit(c echo.Context,n int)(int,error){if c.QueryParam("limit")=="" {return n,nil};v,e:=strconv.Atoi(c.QueryParam("limit"));if e!=nil || v<0{return 0,bad("invalid limit")};if v<n{return v,nil};return n,nil}
func decode(c echo.Context,v interface{})error {if e:=json.NewDecoder(c.Request().Body).Decode(v);e!=nil{return bad("invalid JSON")};return nil}
func readAPI(c echo.Context) error {
 path:=c.Path()
 if path!="/api/livestream/search" && path!="/api/tag" && path!="/api/payment" && path!="/api/user/:username/icon" {if e:=verifyUserSession(c);e!=nil{return e}}
 mu.RLock();defer mu.RUnlock()
 switch path {
 case "/api/tag":return c.JSON(200,map[string]interface{}{"tags":st.Tags})
 case "/api/payment":var tip int64;for _,v:=range st.Stats{tip+=v.Tips};return c.JSON(200,PaymentResult{TotalTip:tip})
 case "/api/user/me":return c.JSON(200,st.user(uid(c)))
 case "/api/user/:username", "/api/user/:username/theme", "/api/user/:username/icon", "/api/user/:username/livestream", "/api/user/:username/statistics":
  id,ok:=st.Names[c.Param("username")];if !ok{return missing("user not found")}
  switch path {
  case "/api/user/:username":return c.JSON(200,st.user(id))
  case "/api/user/:username/theme":return c.JSON(200,st.Themes[id])
  case "/api/user/:username/icon":h:=`"`+st.Hashes[id]+`"`;c.Response().Header().Set("ETag",h);if c.Request().Header.Get("If-None-Match")==h{return c.NoContent(304)};b:=st.Icons[id];if b==nil {b=fallback};return c.Blob(200,"image/jpeg",b)
  case "/api/user/:username/livestream":return streamList(c,id,false)
  case "/api/user/:username/statistics":return userStats(c,id)
  }
 case "/api/livestream":return streamList(c,uid(c),false)
 case "/api/livestream/search":return streamList(c,0,true)
 }
 id,e:=parseID(c);if e!=nil{return e}
 switch path {
 case "/api/livestream/:livestream_id":if _,ok:=st.Streams[id];!ok{return missing("livestream not found")};return c.JSON(200,st.stream(id))
 case "/api/livestream/:livestream_id/livecomment":ids:=st.CommentIDs[id];n,e:=limit(c,len(ids));if e!=nil{return e};v:=make([]Livecomment,0,n);for i:=len(ids)-1;i>=len(ids)-n;i-- {v=append(v,st.comment(st.Comments[ids[i]]))};return c.JSON(200,v)
 case "/api/livestream/:livestream_id/reaction":ids:=st.ReactionIDs[id];n,e:=limit(c,len(ids));if e!=nil{return e};v:=make([]Reaction,0,n);for i:=len(ids)-1;i>=len(ids)-n;i-- {v=append(v,st.reaction(st.Reactions[ids[i]]))};return c.JSON(200,v)
 case "/api/livestream/:livestream_id/ngwords":v:=make([]NGWord,0);for _,w:=range st.Words[id] {if w.UserID==uid(c){v=append(v,w)}};sort.SliceStable(v,func(i,j int)bool{return v[i].CreatedAt>v[j].CreatedAt});return c.JSON(200,v)
 case "/api/livestream/:livestream_id/report":if st.Streams[id].UserID!=uid(c){return echo.NewHTTPError(403,"not owner")};v:=make([]LivecommentReport,0,len(st.Reports[id]));for _,r:=range st.Reports[id]{v=append(v,st.report(r))};return c.JSON(200,v)
 case "/api/livestream/:livestream_id/statistics":if _,ok:=st.Streams[id];!ok{return bad("livestream not found")};z:=st.Stats[id];rank:=int64(1);score:=z.Reactions+z.Tips;for lid,v:=range st.Stats {if _,ok:=st.Streams[lid];ok && (v.Reactions+v.Tips>score || (v.Reactions+v.Tips==score && lid>id)){rank++}};return c.JSON(200,LivestreamStatistics{Rank:rank,ViewersCount:z.Viewers,TotalReactions:z.Reactions,TotalReports:z.Reports,MaxTip:z.MaxTip})
 }
 return missing("route")
}
func streamList(c echo.Context,owner int64,search bool)error {
 ids:=descIDs(st.Streams);v:=make([]Livestream,0);tag:=c.QueryParam("tag")
 n:=len(ids);if search && tag=="" {var e error;n,e=limit(c,n);if e!=nil{return e}}
 for _,id:=range ids {s:=st.Streams[id];if !search && s.UserID!=owner{continue};if tag!="" && search {for _,t:=range st.StreamTags[id]{if t.Name==tag {v=append(v,st.stream(id))}};continue};if len(v)>=n{break};v=append(v,st.stream(id))}
 if !search {sort.Slice(v,func(i,j int)bool{return v[i].ID<v[j].ID})};return c.JSON(200,v)
}
func userStats(c echo.Context,id int64)error {
 scores:=map[int64]int64{};var out UserStatistics;emojis:=map[string]int64{}
 for lid,z:=range st.Stats {owner:=st.Streams[lid].UserID;scores[owner]+=z.Tips+z.Reactions;if owner==id{out.ViewersCount+=z.Viewers;out.TotalReactions+=z.Reactions;out.TotalLivecomments+=z.Comments;out.TotalTip+=z.Tips;for e,n:=range z.Emoji{emojis[e]+=n}}}
 out.Rank=1;name:=st.Users[id].Name;for uid,u:=range st.Users {if scores[uid]>scores[id] || (scores[uid]==scores[id] && u.Name>name){out.Rank++}}
 var best int64;for e,n:=range emojis {if n>best || (n==best && e>out.FavoriteEmoji){out.FavoriteEmoji=e;best=n}};return c.JSON(200,out)
}
func writeAPI(c echo.Context) error {
 if e:=verifyUserSession(c);e!=nil{return e}
 userID:=uid(c);path:=c.Path();id:=int64(0);var err error
 if path!="/api/icon" && path!="/api/livestream/reservation" {id,err=parseID(c);if err!=nil{return err}}
 // Decode before taking the state lock.
 var icon PostIconRequest;var reserve ReserveLivestreamRequest;var comment PostLivecommentRequest;var reaction PostReactionRequest;var moderate ModerateRequest
 switch path {case "/api/icon":err=decode(c,&icon);case "/api/livestream/reservation":err=decode(c,&reserve);case "/api/livestream/:livestream_id/livecomment":err=decode(c,&comment);case "/api/livestream/:livestream_id/reaction":err=decode(c,&reaction);case "/api/livestream/:livestream_id/moderate":err=decode(c,&moderate)};if err!=nil{return err}
 mu.Lock();defer mu.Unlock();now:=time.Now().Unix()
 switch path {
 case "/api/icon":id:=st.next("icon");if e:=persist(Event{Kind:"icon",UserID:userID,ID:id,Image:icon.Image});e!=nil{return e};return c.JSON(201,PostIconResponse{ID:id})
 case "/api/livestream/reservation":
  r:=reserve;if r.StartAt>=1732496400 || r.EndAt<=1700874000{return bad("bad reservation time range")};for _,slot:=range st.Slots {if slot.StartAt>=r.StartAt && slot.EndAt<=r.EndAt && slot.Slot<1{return bad("no reservation slots")}}
  for _,tag:=range r.Tags{found:=false;for _,t:=range st.Tags{if t.ID==tag{found=true}};if !found{return bad("invalid tag")}}
  v:=LivestreamModel{ID:st.next("stream"),UserID:userID,Title:r.Title,Description:r.Description,PlaylistUrl:r.PlaylistUrl,ThumbnailUrl:r.ThumbnailUrl,StartAt:r.StartAt,EndAt:r.EndAt};if e:=persist(Event{Kind:"stream",Stream:&v,Tags:r.Tags});e!=nil{return e};return c.JSON(201,st.stream(v.ID))
 case "/api/livestream/:livestream_id/livecomment":
  if _,ok:=st.Streams[id];!ok{return missing("livestream not found")};for _,w:=range st.Words[id]{if like(comment.Comment,w.Word){return bad("このコメントがスパム判定されました")}}
  v:=LivecommentModel{ID:st.next("comment"),UserID:userID,LivestreamID:id,Comment:comment.Comment,Tip:comment.Tip,CreatedAt:now};if e:=persist(Event{Kind:"comment",Comment:&v});e!=nil{return e};return c.JSON(201,st.comment(v))
 case "/api/livestream/:livestream_id/reaction":
  if _,ok:=st.Streams[id];!ok{return missing("livestream not found")};v:=ReactionModel{ID:st.next("reaction"),UserID:userID,LivestreamID:id,EmojiName:reaction.EmojiName,CreatedAt:now};if e:=persist(Event{Kind:"reaction",Reaction:&v});e!=nil{return e};return c.JSON(201,st.reaction(v))
 case "/api/livestream/:livestream_id/livecomment/:livecomment_id/report":
  cid,e:=strconv.ParseInt(c.Param("livecomment_id"),10,64);if e!=nil{return bad("invalid comment ID")};if _,ok:=st.Streams[id];!ok{return missing("stream not found")};if _,ok:=st.Comments[cid];!ok{return missing("comment not found")};v:=LivecommentReportModel{ID:st.next("report"),UserID:userID,LivestreamID:id,LivecommentID:cid,CreatedAt:now};if e:=persist(Event{Kind:"report",Report:&v});e!=nil{return e};return c.JSON(201,st.report(v))
 case "/api/livestream/:livestream_id/moderate":
  if st.Streams[id].UserID!=userID{return bad("not owner")};v:=NGWord{ID:st.next("word"),UserID:userID,LivestreamID:id,Word:moderate.NGWord,CreatedAt:now};if e:=persist(Event{Kind:"word",Word:&v});e!=nil{return e};return c.JSON(201,map[string]int64{"word_id":v.ID})
 case "/api/livestream/:livestream_id/enter":if e:=persist(Event{Kind:"enter",StreamID:id,UserID:userID});e!=nil{return e};return c.NoContent(http.StatusOK)
 case "/api/livestream/:livestream_id/exit":if e:=persist(Event{Kind:"exit",StreamID:id,UserID:userID});e!=nil{return e};return c.NoContent(http.StatusOK)
 }
 return missing("route")
}
