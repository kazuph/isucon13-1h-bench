package main

import (
	"crypto/subtle"
	"encoding/json"
	"io"

	"net/http"
	"os"

	"github.com/gorilla/sessions"
	"github.com/labstack/echo-contrib/session"
	"github.com/labstack/echo/v4"
)

func installUser(u User) {
	data.Lock()
	data.users[u.ID] = u
	data.names[u.Name] = u.ID
	data.refreshUser(u.ID)
	data.Unlock()
}

// Registration is forwarded while the primary holds its request epoch. The
// successful worker response publishes the profile before the caller sees it.
func registerAccount(c echo.Context) error {
	destination := os.Getenv("ISUPIPE_ACCOUNTS_URL")
	if destination == "" {
		return registerHandler(c)
	}
	req, err := http.NewRequestWithContext(c.Request().Context(), "POST", destination+"/api/register", c.Request().Body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+os.Getenv("ISUPIPE_ZONE_TOKEN"))
	resp, err := zoneClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return err
		}
		return c.JSONBlob(resp.StatusCode, body)
	}
	var user User
	if err = json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return err
	}
	installUser(user)
	return c.JSON(201, user)
}
func serveAccounts(addr string) error {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.HTTPErrorHandler = errorResponseHandler
	store := sessions.NewCookieStore(secret)
	store.Options.Domain = "*.u.isucon.dev"
	e.Use(session.Middleware(store))
	conn, err := connectDB(e.Logger)
	if err != nil {
		return err
	}
	defer conn.Close()
	dbConn = conn
	data.fallback, err = os.ReadFile(fallbackImage)
	if err != nil {
		return err
	}
	powerDNSSubdomainAddress = os.Getenv(powerDNSSubdomainAddressEnvKey)
	e.POST("/api/login", loginHandler)
	e.POST("/api/register", func(c echo.Context) error {
		if subtle.ConstantTimeCompare([]byte(c.Request().Header.Get("Authorization")), []byte("Bearer "+os.Getenv("ISUPIPE_ZONE_TOKEN"))) != 1 {
			return echo.NewHTTPError(403, "forbidden")
		}
		return registerHandler(c)
	})
	return e.Start(addr)
}
