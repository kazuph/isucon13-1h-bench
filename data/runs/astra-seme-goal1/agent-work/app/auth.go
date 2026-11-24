package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/gorilla/sessions"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
	"io"
	"log"
	"net/http"
	"time"
)

var internalClient = &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{MaxIdleConns: 128, MaxIdleConnsPerHost: 128}}

type BcryptRequest struct{ Password, Hash string }

func runBcrypt() {
	http.HandleFunc("/hash", func(w http.ResponseWriter, r *http.Request) {
		var q BcryptRequest
		if json.NewDecoder(r.Body).Decode(&q) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		h, e := bcrypt.GenerateFromPassword([]byte(q.Password), bcrypt.MinCost)
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		w.Write(h)
	})
	http.HandleFunc("/verify", func(w http.ResponseWriter, r *http.Request) {
		var q BcryptRequest
		if json.NewDecoder(r.Body).Decode(&q) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		if e := bcrypt.CompareHashAndPassword([]byte(q.Hash), []byte(q.Password)); e != nil {
			http.Error(w, "invalid password", 401)
			return
		}
		w.WriteHeader(200)
	})
	log.Fatal(http.ListenAndServe(":8090", nil))
}
func bcryptCall(path string, q BcryptRequest) (string, error) {
	b, _ := json.Marshal(q)
	r, e := internalClient.Post("http://10.99.0.13:8090/"+path, "application/json", bytes.NewReader(b))
	if e != nil {
		return "", e
	}
	defer r.Body.Close()
	b, e = io.ReadAll(r.Body)
	if e != nil {
		return "", e
	}
	if r.StatusCode == 401 {
		return "", echo.NewHTTPError(401, "invalid username or password")
	}
	if r.StatusCode != 200 {
		return "", fmt.Errorf("bcrypt service: %s", b)
	}
	return string(b), nil
}
func register(c echo.Context) error {
	var r PostUserRequest
	if err := decode(c, &r); err != nil {
		return err
	}
	if r.Name == "pipe" {
		return echo.NewHTTPError(400, "the username 'pipe' is reserved")
	}
	hash, err := bcryptCall("hash", BcryptRequest{Password: r.Password})
	if err != nil {
		return err
	}
	mu.Lock()
	if _, ok := state.Names[r.Name]; ok {
		mu.Unlock()
		return echo.NewHTTPError(500, "duplicate username")
	}
	u := &StoredUser{Model: UserModel{ID: state.NextUser + 1, Name: r.Name, DisplayName: r.DisplayName, Description: r.Description, HashedPassword: hash}, Theme: Theme{ID: state.NextTheme + 1, DarkMode: r.Theme.DarkMode}, Hash: defaultHash}
	if err = commit(Event{Kind: "user", User: u}); err != nil {
		mu.Unlock()
		return err
	}
	out := state.user(u.Model.ID)
	synced := durability.barrier()
	mu.Unlock()
	if err = <-synced; err != nil {
		return err
	}
	if err = dnsUpdate([]string{r.Name}, false); err != nil {
		return err
	}
	return c.JSON(201, out)
}
func login(c echo.Context) error {
	var r LoginRequest
	if err := decode(c, &r); err != nil {
		return err
	}
	mu.RLock()
	id, ok := state.Names[r.Username]
	var hash string
	if ok {
		hash = state.Users[id].Model.HashedPassword
	}
	mu.RUnlock()
	if !ok {
		return echo.NewHTTPError(401, "invalid username or password")
	}
	if _, err := bcryptCall("verify", BcryptRequest{Password: r.Password, Hash: hash}); err != nil {
		return err
	}
	sess, err := session.Get(defaultSessionIDKey, c)
	if err != nil {
		return echo.NewHTTPError(401, "failed to get session")
	}
	sess.Options = &sessions.Options{Domain: "u.isucon.dev", MaxAge: 60000, Path: "/"}
	sess.Values[defaultSessionIDKey] = uuid.NewString()
	sess.Values[defaultUserIDKey] = id
	sess.Values[defaultUsernameKey] = r.Username
	sess.Values[defaultSessionExpiresKey] = time.Now().Add(time.Hour).Unix()
	if err = sess.Save(c.Request(), c.Response()); err != nil {
		return err
	}
	return c.NoContent(200)
}
func icon(c echo.Context) error {
	mu.RLock()
	id, ok := state.Names[c.Param("username")]
	if !ok {
		mu.RUnlock()
		return echo.NewHTTPError(404, "user not found")
	}
	u := state.Users[id]
	hash := u.Hash
	img := u.Image
	if img == nil && hash == defaultHash {
		img = defaultImage
	}
	mu.RUnlock()
	etag := `"` + hash + `"`
	c.Response().Header().Set("ETag", etag)
	// nginx treats integer-second expiry as inclusive; expire at this second
	// boundary to leave margin under the two-second icon freshness contract.
	c.Response().Header().Set("X-Accel-Expires", fmt.Sprintf("@%d", time.Now().Unix()))
	if c.Request().Header.Get("If-None-Match") == etag {
		return c.NoContent(304)
	}
	return c.Blob(200, "image/jpeg", img)
}
