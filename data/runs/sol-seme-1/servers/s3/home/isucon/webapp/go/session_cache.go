package main

import (
	"github.com/gorilla/sessions"
	"net/http"
	"sync"
	"time"
)

type cachedSession struct {
	values  map[interface{}]interface{}
	options sessions.Options
	until   time.Time
}
type cachedCookieStore struct {
	base    *sessions.CookieStore
	mu      sync.RWMutex
	entries map[string]cachedSession
}

func newCachedStore(base *sessions.CookieStore) *cachedCookieStore {
	return &cachedCookieStore{base: base, entries: make(map[string]cachedSession)}
}
func copyValues(src map[interface{}]interface{}) map[interface{}]interface{} {
	dst := make(map[interface{}]interface{}, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
func (s *cachedCookieStore) Get(r *http.Request, name string) (*sessions.Session, error) {
	return sessions.GetRegistry(r).Get(s, name)
}
func (s *cachedCookieStore) New(r *http.Request, name string) (*sessions.Session, error) {
	cookie, err := r.Cookie(name)
	if err != nil {
		return s.base.New(r, name)
	}
	key := name + "|" + cookie.Value
	s.mu.RLock()
	entry, ok := s.entries[key]
	s.mu.RUnlock()
	if ok && time.Now().Before(entry.until) {
		sess := sessions.NewSession(s, name)
		sess.Values = copyValues(entry.values)
		opts := entry.options
		sess.Options = &opts
		sess.IsNew = false
		return sess, nil
	}
	sess, err := s.base.New(r, name)
	if err == nil && !sess.IsNew {
		entry = cachedSession{values: copyValues(sess.Values), options: *sess.Options, until: time.Now().Add(30 * time.Second)}
		s.mu.Lock()
		if len(s.entries) > 20000 {
			now := time.Now()
			for k, v := range s.entries {
				if now.After(v.until) {
					delete(s.entries, k)
				}
			}
		}
		s.entries[key] = entry
		s.mu.Unlock()
	}
	return sess, err
}
func (s *cachedCookieStore) Save(r *http.Request, w http.ResponseWriter, sess *sessions.Session) error {
	return s.base.Save(r, w, sess)
}
