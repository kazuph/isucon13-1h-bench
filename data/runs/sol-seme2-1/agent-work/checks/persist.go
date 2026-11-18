package main
import (
 "database/sql"
 "net/http"
 "net/http/httptest"
 "io"
 "os"
 "fmt"
 "time"
 "encoding/json"
 "crypto/sha256"
 "github.com/gorilla/sessions"
 _ "github.com/go-sql-driver/mysql"
)
type Probe struct {Path string;UID int64;Name string;Hash string}
func get(p Probe)(string,error){
 req,_:=http.NewRequest("GET","http://127.0.0.1:8080"+p.Path,nil)
 store:=sessions.NewCookieStore([]byte("isucon13_session_cookiestore_defaultsecret"));s:=sessions.NewSession(store,"SESSIONID");s.Options=&sessions.Options{Path:"/",Domain:"u.isucon.dev",MaxAge:60000};s.Values["USERID"]=p.UID;s.Values["USERNAME"]=p.Name;s.Values["EXPIRES"]=time.Now().Add(time.Hour).Unix();s.Values["SESSIONID"]="persistence-check"
 rec:=httptest.NewRecorder();if err:=store.Save(req,rec,s);err!=nil{return "",err};req.AddCookie(rec.Result().Cookies()[0])
 client:=http.Client{Timeout:30*time.Second};r,err:=client.Do(req);if err!=nil{return "",err};defer r.Body.Close();data,err:=io.ReadAll(r.Body);if err!=nil{return "",err};if r.StatusCode!=200{return "",fmt.Errorf("%s: %d %s",p.Path,r.StatusCode,data)};return fmt.Sprintf("%x",sha256.Sum256(data)),nil
}
func main(){if len(os.Args)!=3{panic("capture|verify snapshot.json")};var probes []Probe
 if os.Args[1]=="capture" {
 db,err:=sql.Open("mysql","isucon:isucon@tcp(10.99.0.13:3306)/isupipe?interpolateParams=true");if err!=nil{panic(err)};defer db.Close()
 probes=[]Probe{{Path:"/api/payment",UID:1,Name:"test001"},{Path:"/api/tag",UID:1,Name:"test001"},{Path:"/api/livestream/search?limit=30",UID:1,Name:"test001"}}
 rows,err:=db.Query("SELECT l.id,l.user_id,u.name FROM livestreams l JOIN users u ON u.id=l.user_id JOIN livecomments c ON c.livestream_id=l.id GROUP BY l.id,l.user_id,u.name ORDER BY SUM(c.tip) DESC,l.id DESC LIMIT 15");if err!=nil{panic(err)}
 for rows.Next(){var lid,uid int64;var name string;rows.Scan(&lid,&uid,&name);for _,suffix:=range []string{"","/statistics","/livecomment?limit=50","/reaction?limit=50","/ngwords","/report"}{probes=append(probes,Probe{Path:fmt.Sprintf("/api/livestream/%d%s",lid,suffix),UID:uid,Name:name})};for _,suffix:=range []string{"","/statistics","/icon","/livestream"}{probes=append(probes,Probe{Path:"/api/user/"+name+suffix,UID:uid,Name:name})}}
 rows.Close()
 }else{b,err:=os.ReadFile(os.Args[2]);if err!=nil{panic(err)};if err=json.Unmarshal(b,&probes);err!=nil{panic(err)}}
 for i,p:=range probes {h,err:=get(p);if err!=nil{panic(err)};if os.Args[1]=="verify"&&h!=p.Hash{panic(fmt.Sprintf("changed: %s before=%s after=%s",p.Path,p.Hash,h))};probes[i].Hash=h}
 if os.Args[1]=="capture"{b,_:=json.MarshalIndent(probes,"","  ");if err:=os.WriteFile(os.Args[2],b,0600);err!=nil{panic(err)}}
 fmt.Printf("%s: %d API responses verified\n",os.Args[1],len(probes))
}
