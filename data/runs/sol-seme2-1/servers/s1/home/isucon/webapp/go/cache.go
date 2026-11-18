package main

import (
 "context"
 "sync"
 "time"
 "os"
 "crypto/sha256"
 "fmt"
 "database/sql"
 "errors"
 "github.com/jmoiron/sqlx"
)

type cachedUser struct { user User; expires time.Time }
var userCache sync.Map
var modelCache sync.Map
var tagsCache sync.Map
var streamModelCache sync.Map
var fallbackOnce sync.Once
var fallbackHash string
var fallbackBytes []byte
func defaultIcon() ([]byte,string) {
 fallbackOnce.Do(func(){ fallbackBytes,_=os.ReadFile(fallbackImage); h:=sha256.Sum256(fallbackBytes); fallbackHash=fmt.Sprintf("%x",h) })
 return fallbackBytes,fallbackHash
}
func resetCaches() {
 userCache.Range(func(k,v interface{})bool{userCache.Delete(k);return true})
 modelCache.Range(func(k,v interface{})bool{modelCache.Delete(k);return true})
 tagsCache.Range(func(k,v interface{})bool{tagsCache.Delete(k);return true})
 streamModelCache.Range(func(k,v interface{})bool{streamModelCache.Delete(k);return true})
}
func getUserModel(ctx context.Context,tx *sqlx.Tx,id int64) (UserModel,error) {
 if v,ok:=modelCache.Load(id);ok{return v.(UserModel),nil}
 var m UserModel
 err:=tx.GetContext(ctx,&m,"SELECT * FROM users WHERE id = ?",id)
 if err==nil {modelCache.Store(id,m)}
 return m,err
}
func getStreamModel(ctx context.Context,tx *sqlx.Tx,id int64) (LivestreamModel,error) {
 if v,ok:=streamModelCache.Load(id);ok{return v.(LivestreamModel),nil}
 var m LivestreamModel
 err:=tx.GetContext(ctx,&m,"SELECT * FROM livestreams WHERE id = ?",id)
 if err==nil {streamModelCache.Store(id,m)}
 return m,err
}
func cachedFillUser(ctx context.Context,tx *sqlx.Tx,m UserModel)(User,error){
 if v,ok:=userCache.Load(m.ID);ok {c:=v.(cachedUser);if time.Now().Before(c.expires){return c.user,nil}}
 var t ThemeModel
 if err:=tx.GetContext(ctx,&t,"SELECT * FROM themes WHERE user_id = ?",m.ID);err!=nil{return User{},err}
 var hash string
 if err:=tx.GetContext(ctx,&hash,"SELECT LOWER(SHA2(image,256)) FROM icons WHERE user_id = ?",m.ID);err!=nil{
  if !errors.Is(err,sql.ErrNoRows){return User{},err};_,hash=defaultIcon()
 }
 u:=User{ID:m.ID,Name:m.Name,DisplayName:m.DisplayName,Description:m.Description,Theme:Theme{ID:t.ID,DarkMode:t.DarkMode},IconHash:hash}
 userCache.Store(m.ID,cachedUser{u,time.Now().Add(700*time.Millisecond)})
 return u,nil
}
