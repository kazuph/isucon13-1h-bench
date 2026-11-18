package main
import (
 "bytes"
 "encoding/json"
 "fmt"
 "io"
 "net/http"
 "os"
 "time"
 "github.com/google/uuid"
 "github.com/gorilla/sessions"
 "github.com/labstack/echo/v4"
 "github.com/labstack/echo-contrib/session"
 "golang.org/x/crypto/bcrypt"
)
type authRequest struct { Password string;Hash string;Generate bool }
var authClient=&http.Client{Timeout:20*time.Second,Transport:&http.Transport{MaxIdleConns:128,MaxIdleConnsPerHost:128}}
func authCall(req authRequest)(string,error){
 b,_:=json.Marshal(req);resp,e:=authClient.Post("http://"+os.Getenv("AUTH_ADDRESS")+"/bcrypt","application/json",bytes.NewReader(b));if e!=nil{return "",e};defer resp.Body.Close();b,e=io.ReadAll(resp.Body);if e!=nil{return "",e};if resp.StatusCode!=200{return "",fmt.Errorf("invalid password")};return string(b),nil
}
func serveAuth(){http.HandleFunc("/bcrypt",func(w http.ResponseWriter,r *http.Request){var req authRequest;if json.NewDecoder(r.Body).Decode(&req)!=nil{http.Error(w,"bad request",400);return};if req.Generate{b,e:=bcrypt.GenerateFromPassword([]byte(req.Password),bcrypt.MinCost);if e!=nil{http.Error(w,e.Error(),500);return};w.Write(b)}else {if e:=bcrypt.CompareHashAndPassword([]byte(req.Hash),[]byte(req.Password));e!=nil{http.Error(w,"invalid password",401);return};w.Write([]byte("ok"))}});if e:=http.ListenAndServe(":8081",nil);e!=nil{panic(e)}}
func registerHandler(c echo.Context)error{
 var r PostUserRequest;if e:=decode(c,&r);e!=nil{return e};if r.Name=="pipe"{return bad("reserved username")}
 hash,e:=authCall(authRequest{Password:r.Password,Generate:true});if e!=nil{return e}
 mu.Lock();defer mu.Unlock();if _,ok:=st.Names[r.Name];ok{return bad("duplicate user")}
 u:=UserModel{ID:st.next("user"),Name:r.Name,DisplayName:r.DisplayName,Description:r.Description,HashedPassword:hash};t:=Theme{ID:st.next("theme"),DarkMode:r.Theme.DarkMode};if e:=persist(Event{Kind:"user",User:&u,Theme:t});e!=nil{return e};return c.JSON(201,st.user(u.ID))
}
func loginHandler(c echo.Context)error{
 var req LoginRequest;if e:=decode(c,&req);e!=nil{return e};mu.RLock();id,ok:=st.Names[req.Username];u:=st.Users[id];mu.RUnlock();if !ok{return echo.NewHTTPError(401,"invalid username or password")}
 if _,e:=authCall(authRequest{Password:req.Password,Hash:u.HashedPassword});e!=nil{return echo.NewHTTPError(401,"invalid username or password")}
 sess,e:=session.Get(defaultSessionIDKey,c);if e!=nil{return echo.NewHTTPError(401,"failed session")};sess.Options=&sessions.Options{Domain:"u.isucon.dev",MaxAge:60000,Path:"/"};sess.Values[defaultSessionIDKey]=uuid.NewString();sess.Values[defaultUserIDKey]=id;sess.Values[defaultUsernameKey]=u.Name;sess.Values[defaultSessionExpiresKey]=time.Now().Add(time.Hour).Unix();if e=sess.Save(c.Request(),c.Response());e!=nil{return e};return c.NoContent(200)
}
