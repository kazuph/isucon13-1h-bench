package main

import (
 "crypto/sha256"
 "fmt"
 "net/http"
 "os"
 "os/exec"
 "sort"
 "strconv"
 "strings"
 "sync"
 "time"
 "github.com/labstack/echo/v4"
 "github.com/labstack/echo-contrib/session"
)

type memoryState struct {
 users map[int64]User
 names map[string]int64
 images map[int64][]byte
 streams map[int64]Livestream
 comments map[int64]LivecommentModel
 reactions map[int64]ReactionModel
 reports map[int64]LivecommentReportModel
 words map[int64][]NGWord
 viewers []LivestreamViewerModel
 slots []ReservationSlotModel
 tags []Tag
}
var mem memoryState
var memMu sync.RWMutex
func loadMemory() error {
 m:=memoryState{users:map[int64]User{},names:map[string]int64{},images:map[int64][]byte{},streams:map[int64]Livestream{},comments:map[int64]LivecommentModel{},reactions:map[int64]ReactionModel{},reports:map[int64]LivecommentReportModel{},words:map[int64][]NGWord{}}
 var users []UserModel; if e:=dbConn.Select(&users,"SELECT * FROM users");e!=nil{return e}
 var themes []ThemeModel; if e:=dbConn.Select(&themes,"SELECT * FROM themes");e!=nil{return e}; tm:=map[int64]Theme{};for _,t:=range themes{tm[t.UserID]=Theme{ID:t.ID,DarkMode:t.DarkMode}}
 var icons []struct{ID int64 `db:"id"`;UserID int64 `db:"user_id"`;Image []byte `db:"image"`};if e:=dbConn.Select(&icons,"SELECT * FROM icons");e!=nil{return e};for _,i:=range icons{m.images[i.UserID]=i.Image}
 fallback,e:=os.ReadFile(fallbackImage);if e!=nil{return e}
 for _,u:=range users{im:=m.images[u.ID];if im==nil{im=fallback};m.images[u.ID]=im;m.users[u.ID]=User{ID:u.ID,Name:u.Name,DisplayName:u.DisplayName,Description:u.Description,Theme:tm[u.ID],IconHash:fmt.Sprintf("%x",sha256.Sum256(im))};m.names[u.Name]=u.ID}
 if e:=dbConn.Select(&m.tags,"SELECT * FROM tags");e!=nil{return e};tags:=map[int64]Tag{};for _,t:=range m.tags{tags[t.ID]=t}
 var links []LivestreamTagModel;if e:=dbConn.Select(&links,"SELECT * FROM livestream_tags ORDER BY id");e!=nil{return e};lt:=map[int64][]Tag{};for _,l:=range links{lt[l.LivestreamID]=append(lt[l.LivestreamID],tags[l.TagID])}
 var streams []LivestreamModel;if e:=dbConn.Select(&streams,"SELECT * FROM livestreams");e!=nil{return e};for _,l:=range streams{ts:=lt[l.ID];if ts==nil{ts=[]Tag{}};m.streams[l.ID]=Livestream{ID:l.ID,Owner:m.users[l.UserID],Title:l.Title,Description:l.Description,PlaylistUrl:l.PlaylistUrl,ThumbnailUrl:l.ThumbnailUrl,StartAt:l.StartAt,EndAt:l.EndAt,Tags:ts}}
 var cs []LivecommentModel;if e:=dbConn.Select(&cs,"SELECT * FROM livecomments");e!=nil{return e};for _,c:=range cs{m.comments[c.ID]=c}
 var rs []ReactionModel;if e:=dbConn.Select(&rs,"SELECT * FROM reactions");e!=nil{return e};for _,r:=range rs{m.reactions[r.ID]=r}
 var reports []LivecommentReportModel;if e:=dbConn.Select(&reports,"SELECT * FROM livecomment_reports");e!=nil{return e};for _,r:=range reports{m.reports[r.ID]=r}
 var words []NGWord;if e:=dbConn.Select(&words,"SELECT * FROM ng_words ORDER BY id");e!=nil{return e};for _,w:=range words{m.words[w.LivestreamID]=append(m.words[w.LivestreamID],w)}
 if e:=dbConn.Select(&m.viewers,"SELECT user_id,livestream_id,created_at FROM livestream_viewers_history");e!=nil{return e}
 if e:=dbConn.Select(&m.slots,"SELECT * FROM reservation_slots");e!=nil{return e}
 mem=m;return nil
}
func memoryInitialize(c echo.Context) error {memMu.Lock();defer memMu.Unlock();if out,e:=exec.Command("../sql/init.sh").CombinedOutput();e!=nil{return fmt.Errorf("initialize: %s %w",out,e)};if e:=loadMemory();e!=nil{return e};return c.JSON(200,map[string]string{"language":"golang","lang":"golang"})}
func streamResponse(id int64) Livestream {s:=mem.streams[id];s.Owner=mem.users[s.Owner.ID];return s}
func commentResponse(m LivecommentModel) Livecomment{return Livecomment{ID:m.ID,User:mem.users[m.UserID],Livestream:streamResponse(m.LivestreamID),Comment:m.Comment,Tip:m.Tip,CreatedAt:m.CreatedAt}}
func reactionResponse(m ReactionModel) Reaction{return Reaction{ID:m.ID,User:mem.users[m.UserID],Livestream:streamResponse(m.LivestreamID),EmojiName:m.EmojiName,CreatedAt:m.CreatedAt}}
func insertMemory(query string,args ...interface{})(int64,error){r,e:=dbConn.Exec(query,args...);if e!=nil{return 0,e};return r.LastInsertId()}
func memoryHandler(c echo.Context) error {
 write:=c.Request().Method!="GET";if write{memMu.Lock();defer memMu.Unlock()}else{memMu.RLock();defer memMu.RUnlock()}
 path:=c.Path();method:=c.Request().Method
 public:=path=="/api/tag"||path=="/api/livestream/search"||path=="/api/user/:username/icon"||path=="/api/payment"
 var uid int64
 if !public{if e:=verifyUserSession(c);e!=nil{return e};s,_:=session.Get(defaultSessionIDKey,c);uid=s.Values[defaultUserIDKey].(int64)}
 id,_:=strconv.ParseInt(c.Param("livestream_id"),10,64);username:=c.Param("username");user:=mem.users[mem.names[username]]
 limit:=int(^uint(0)>>1);if v:=c.QueryParam("limit");v!=""{n,e:=strconv.Atoi(v);if e!=nil||n<0{return echo.NewHTTPError(400,"invalid limit")};limit=n}
 switch path {
 case "/api/tag":return c.JSON(200,struct{Tags []Tag `json:"tags"`}{mem.tags})
 case "/api/user/me":return c.JSON(200,mem.users[uid])
 case "/api/user/:username":if user.ID==0{return echo.NewHTTPError(404)};return c.JSON(200,user)
 case "/api/user/:username/theme":if user.ID==0{return echo.NewHTTPError(404)};return c.JSON(200,user.Theme)
 case "/api/user/:username/icon":if user.ID==0{return echo.NewHTTPError(404)};if c.Request().Header.Get("If-None-Match")=="\""+user.IconHash+"\""{return c.NoContent(304)};c.Response().Header().Set("ETag","\""+user.IconHash+"\"");return c.Blob(200,"image/jpeg",mem.images[user.ID])
 case "/api/icon":var req PostIconRequest;if e:=c.Bind(&req);e!=nil{return e};tx,e:=dbConn.Beginx();if e!=nil{return e};defer tx.Rollback();if _,e=tx.Exec("DELETE FROM icons WHERE user_id=?",uid);e!=nil{return e};r,e:=tx.Exec("INSERT INTO icons(user_id,image) VALUES(?,?)",uid,req.Image);if e!=nil{return e};iid,_:=r.LastInsertId();if e=tx.Commit();e!=nil{return e};mem.images[uid]=req.Image;u:=mem.users[uid];u.IconHash=fmt.Sprintf("%x",sha256.Sum256(req.Image));mem.users[uid]=u;return c.JSON(201,PostIconResponse{ID:iid})
 case "/api/livestream/search","/api/livestream","/api/user/:username/livestream":
  out:=[]Livestream{};for sid,s:=range mem.streams{if path=="/api/livestream"&&s.Owner.ID!=uid{continue};if path=="/api/user/:username/livestream"&&s.Owner.ID!=user.ID{continue};tag:=c.QueryParam("tag");if tag!=""{found:=false;for _,t:=range s.Tags{if t.Name==tag{found=true}};if !found{continue}};out=append(out,streamResponse(sid))};sort.Slice(out,func(i,j int)bool{return out[i].ID>out[j].ID});if path=="/api/livestream/search"&&c.QueryParam("tag")==""&&len(out)>limit{out=out[:limit]};return c.JSON(200,out)
 case "/api/livestream/:livestream_id":if mem.streams[id].ID==0{return echo.NewHTTPError(404)};return c.JSON(200,streamResponse(id))
 case "/api/payment":var total int64;for _,c:=range mem.comments{total+=c.Tip};return c.JSON(200,PaymentResult{TotalTip:total})
 }
 return memoryActivity(c,uid,id,limit,method)
}
func memoryActivity(c echo.Context,uid,id int64,limit int,method string) error {
 now:=time.Now().Unix();path:=c.Path()
 switch path{
 case "/api/livestream/:livestream_id/livecomment":
 if method=="GET"{out:=[]Livecomment{};for _,v:=range mem.comments{if v.LivestreamID==id{out=append(out,commentResponse(v))}};sort.Slice(out,func(i,j int)bool{if out[i].CreatedAt==out[j].CreatedAt{return out[i].ID>out[j].ID};return out[i].CreatedAt>out[j].CreatedAt});if len(out)>limit{out=out[:limit]};return c.JSON(200,out)}
 var req PostLivecommentRequest;if e:=c.Bind(&req);e!=nil{return e};if mem.streams[id].ID==0{return echo.NewHTTPError(404)};for _,w:=range mem.words[id]{if strings.Contains(req.Comment,w.Word){return echo.NewHTTPError(400,"spam")}}
 cid,e:=insertMemory("INSERT INTO livecomments(user_id,livestream_id,comment,tip,created_at) VALUES(?,?,?,?,?)",uid,id,req.Comment,req.Tip,now);if e!=nil{return e};m:=LivecommentModel{ID:cid,UserID:uid,LivestreamID:id,Comment:req.Comment,Tip:req.Tip,CreatedAt:now};mem.comments[cid]=m;return c.JSON(201,commentResponse(m))
 case "/api/livestream/:livestream_id/reaction":
 if method=="GET"{out:=[]Reaction{};for _,v:=range mem.reactions{if v.LivestreamID==id{out=append(out,reactionResponse(v))}};sort.Slice(out,func(i,j int)bool{if out[i].CreatedAt==out[j].CreatedAt{return out[i].ID>out[j].ID};return out[i].CreatedAt>out[j].CreatedAt});if len(out)>limit{out=out[:limit]};return c.JSON(200,out)}
 var req PostReactionRequest;if e:=c.Bind(&req);e!=nil{return e};rid,e:=insertMemory("INSERT INTO reactions(user_id,livestream_id,emoji_name,created_at) VALUES(?,?,?,?)",uid,id,req.EmojiName,now);if e!=nil{return e};m:=ReactionModel{ID:rid,UserID:uid,LivestreamID:id,EmojiName:req.EmojiName,CreatedAt:now};mem.reactions[rid]=m;return c.JSON(201,reactionResponse(m))
 case "/api/livestream/:livestream_id/ngwords":out:=[]NGWord{};for _,w:=range mem.words[id]{if w.UserID==uid{out=append(out,w)}};sort.Slice(out,func(i,j int)bool{return out[i].CreatedAt>out[j].CreatedAt});return c.JSON(200,out)
 case "/api/livestream/:livestream_id/moderate":
 if mem.streams[id].Owner.ID!=uid{return echo.NewHTTPError(400)};var req ModerateRequest;if e:=c.Bind(&req);e!=nil{return e}
 tx,e:=dbConn.Beginx();if e!=nil{return e};defer tx.Rollback();r,e:=tx.Exec("INSERT INTO ng_words(user_id,livestream_id,word,created_at) VALUES(?,?,?,?)",uid,id,req.NGWord,now);if e!=nil{return e};wid,_:=r.LastInsertId();if _,e=tx.Exec("DELETE FROM livecomments WHERE livestream_id=? AND comment LIKE CONCAT('%',?,'%')",id,req.NGWord);e!=nil{return e};if e=tx.Commit();e!=nil{return e};mem.words[id]=append(mem.words[id],NGWord{ID:wid,UserID:uid,LivestreamID:id,Word:req.NGWord,CreatedAt:now});for cid,v:=range mem.comments{if v.LivestreamID==id&&strings.Contains(v.Comment,req.NGWord){delete(mem.comments,cid)}};return c.JSON(201,map[string]int64{"word_id":wid})
 case "/api/livestream/:livestream_id/enter":_,e:=insertMemory("INSERT INTO livestream_viewers_history(user_id,livestream_id,created_at) VALUES(?,?,?)",uid,id,now);if e!=nil{return e};mem.viewers=append(mem.viewers,LivestreamViewerModel{UserID:uid,LivestreamID:id,CreatedAt:now});return c.NoContent(200)
 case "/api/livestream/:livestream_id/exit":if _,e:=dbConn.Exec("DELETE FROM livestream_viewers_history WHERE user_id=? AND livestream_id=?",uid,id);e!=nil{return e};out:=mem.viewers[:0];for _,v:=range mem.viewers{if v.UserID!=uid||v.LivestreamID!=id{out=append(out,v)}};mem.viewers=out;return c.NoContent(200)
 case "/api/livestream/:livestream_id/livecomment/:livecomment_id/report":cid,_:=strconv.ParseInt(c.Param("livecomment_id"),10,64);v,ok:=mem.comments[cid];if !ok||mem.streams[id].ID==0{return echo.NewHTTPError(404)};rid,e:=insertMemory("INSERT INTO livecomment_reports(user_id,livestream_id,livecomment_id,created_at) VALUES(?,?,?,?)",uid,id,cid,now);if e!=nil{return e};mem.reports[rid]=LivecommentReportModel{ID:rid,UserID:uid,LivestreamID:id,LivecommentID:cid,CreatedAt:now};return c.JSON(201,LivecommentReport{ID:rid,Reporter:mem.users[uid],Livecomment:commentResponse(v),CreatedAt:now})
 case "/api/livestream/:livestream_id/report":if mem.streams[id].Owner.ID!=uid{return echo.NewHTTPError(403)};out:=[]LivecommentReport{};for _,r:=range mem.reports{if r.LivestreamID==id{v,ok:=mem.comments[r.LivecommentID];if !ok{continue};out=append(out,LivecommentReport{ID:r.ID,Reporter:mem.users[r.UserID],Livecomment:commentResponse(v),CreatedAt:r.CreatedAt})}};sort.Slice(out,func(i,j int)bool{return out[i].ID<out[j].ID});return c.JSON(200,out)
 case "/api/livestream/reservation":return memoryReserve(c,uid)
 case "/api/user/:username/statistics","/api/livestream/:livestream_id/statistics":return memoryStats(c,id)
 }
 return echo.NewHTTPError(http.StatusNotFound)
}
func memoryReserve(c echo.Context,uid int64) error {
 var req ReserveLivestreamRequest;if e:=c.Bind(&req);e!=nil{return e};if req.StartAt>=1732496400||req.EndAt<=1700874000{return echo.NewHTTPError(400)}
 for _,s:=range mem.slots{if s.StartAt>=req.StartAt&&s.EndAt<=req.EndAt&&s.Slot<1{return echo.NewHTTPError(400)}}
 tx,e:=dbConn.Beginx();if e!=nil{return e};defer tx.Rollback();if _,e=tx.Exec("UPDATE reservation_slots SET slot=slot-1 WHERE start_at>=? AND end_at<=?",req.StartAt,req.EndAt);e!=nil{return e}
 r,e:=tx.Exec("INSERT INTO livestreams(user_id,title,description,playlist_url,thumbnail_url,start_at,end_at) VALUES(?,?,?,?,?,?,?)",uid,req.Title,req.Description,req.PlaylistUrl,req.ThumbnailUrl,req.StartAt,req.EndAt);if e!=nil{return e};id,_:=r.LastInsertId();tags:=[]Tag{};for _,tid:=range req.Tags{if _,e=tx.Exec("INSERT INTO livestream_tags(livestream_id,tag_id) VALUES(?,?)",id,tid);e!=nil{return e};for _,t:=range mem.tags{if t.ID==tid{tags=append(tags,t)}}};if e=tx.Commit();e!=nil{return e};for i,s:=range mem.slots{if s.StartAt>=req.StartAt&&s.EndAt<=req.EndAt{mem.slots[i].Slot--}};s:=Livestream{ID:id,Owner:mem.users[uid],Title:req.Title,Description:req.Description,PlaylistUrl:req.PlaylistUrl,ThumbnailUrl:req.ThumbnailUrl,StartAt:req.StartAt,EndAt:req.EndAt,Tags:tags};mem.streams[id]=s;return c.JSON(201,s)
}
func memoryStats(c echo.Context,id int64) error {
 scores:=map[int64]int64{};uscores:=map[int64]int64{};reacts:=map[int64]int64{};tips:=map[int64]int64{};counts:=map[int64]int64{};max:=map[int64]int64{}
 for _,r:=range mem.reactions{scores[r.LivestreamID]++;reacts[r.LivestreamID]++}
 for _,v:=range mem.comments{scores[v.LivestreamID]+=v.Tip;tips[v.LivestreamID]+=v.Tip;counts[v.LivestreamID]++;if v.Tip>max[v.LivestreamID]{max[v.LivestreamID]=v.Tip}}
 for sid,s:=range mem.streams{uscores[s.Owner.ID]+=scores[sid]}
 if c.Path()=="/api/livestream/:livestream_id/statistics"{if mem.streams[id].ID==0{return echo.NewHTTPError(400)};out:=LivestreamStatistics{Rank:1,TotalReactions:reacts[id],MaxTip:max[id]};for sid:=range mem.streams{if scores[sid]>scores[id]||(scores[sid]==scores[id]&&sid>id){out.Rank++}};for _,v:=range mem.viewers{if v.LivestreamID==id{out.ViewersCount++}};for _,r:=range mem.reports{if r.LivestreamID==id{out.TotalReports++}};return c.JSON(200,out)}
 uid:=mem.names[c.Param("username")];if uid==0{return echo.NewHTTPError(400)};out:=UserStatistics{Rank:1};for other,u:=range mem.users{if uscores[other]>uscores[uid]||(uscores[other]==uscores[uid]&&u.Name>mem.users[uid].Name){out.Rank++}};for sid,s:=range mem.streams{if s.Owner.ID==uid{out.TotalReactions+=reacts[sid];out.TotalTip+=tips[sid];out.TotalLivecomments+=counts[sid]}};for _,v:=range mem.viewers{if mem.streams[v.LivestreamID].Owner.ID==uid{out.ViewersCount++}};emoji:=map[string]int64{};for _,r:=range mem.reactions{if mem.streams[r.LivestreamID].Owner.ID==uid{emoji[r.EmojiName]++}};var best int64;for em,n:=range emoji{if n>best||(n==best&&em>out.FavoriteEmoji){best=n;out.FavoriteEmoji=em}};return c.JSON(200,out)
}
