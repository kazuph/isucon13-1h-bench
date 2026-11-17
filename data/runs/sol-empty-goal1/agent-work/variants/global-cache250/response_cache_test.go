package main

import (
 "context"
 "errors"
 "net/http"
 "net/http/httptest"
 "sync"
 "testing"
 "time"

 "github.com/labstack/echo/v4"
)

func cacheTestRequest(next echo.HandlerFunc) error {
 e := echo.New()
 c := e.NewContext(httptest.NewRequest(http.MethodGet,"/",nil),httptest.NewRecorder())
 return responseCacheMiddleware(next)(c)
}

func TestSharedCachePublishesOnlyAfterSuccessfulHandler(t *testing.T) {
 clearResponseCaches()
 err := cacheTestRequest(func(c echo.Context) error {
  cache := c.Request().Context().Value(responseCacheKey{}).(*responseCache)
  cache.users[1] = User{ID:1, Name:"committed"};cache.freshUsers[1]=true
  if _, ok := lookupUser(context.Background(),1);ok { t.Fatal("uncommitted response became visible") }
  return c.NoContent(http.StatusOK)
 })
 if err != nil { t.Fatal(err) }
 if user, ok := lookupUser(context.Background(),1); !ok || user.Name != "committed" { t.Fatal("committed response missing") }
 clearResponseCaches()
 _ = cacheTestRequest(func(c echo.Context) error {
  cache := c.Request().Context().Value(responseCacheKey{}).(*responseCache)
  cache.users[2]=User{ID:2};cache.freshUsers[2]=true
  return errors.New("commit failed")
 })
 if _, ok := lookupUser(context.Background(),2);ok { t.Fatal("failed transaction was cached") }
}

func TestSharedCacheDoesNotExtendOldEntriesOrSlowSnapshots(t *testing.T) {
 clearResponseCaches()
 expiry := time.Now().Add(responseCacheTTL)
 sharedResponses.Lock();sharedResponses.users[1]=cachedUser{User{ID:1},expiry};sharedResponses.Unlock()
 _ = cacheTestRequest(func(c echo.Context) error {
  if _, ok := lookupUser(c.Request().Context(),1);!ok { t.Fatal("expected cached user") }
  return c.NoContent(http.StatusOK)
 })
 sharedResponses.RLock();actual:=sharedResponses.users[1].expires;sharedResponses.RUnlock()
 if !actual.Equal(expiry) { t.Fatal("cache hit extended original expiry") }
 _ = cacheTestRequest(func(c echo.Context) error {
  cache := c.Request().Context().Value(responseCacheKey{}).(*responseCache)
  cache.started=time.Now().Add(-time.Second)
  cache.users[2]=User{ID:2};cache.freshUsers[2]=true
  return c.NoContent(http.StatusOK)
 })
 if _, ok := lookupUser(context.Background(),2);ok { t.Fatal("old snapshot refreshed shared cache") }
}

func TestSharedCacheConcurrentRequestsAndReset(t *testing.T) {
 clearResponseCaches()
 var workers sync.WaitGroup
 for i:=0;i<32;i++ {
  workers.Add(1)
  go func(id int64) {
   defer workers.Done()
   for j:=0;j<50;j++ {
    _=cacheTestRequest(func(c echo.Context) error {
     cache:=c.Request().Context().Value(responseCacheKey{}).(*responseCache)
     cache.users[id]=User{ID:id};cache.freshUsers[id]=true
     _,_=lookupUser(c.Request().Context(),id+1)
     return c.NoContent(http.StatusOK)
    })
    if j%10==0 { clearResponseCaches() }
   }
  }(int64(i))
 }
 workers.Wait()
}
