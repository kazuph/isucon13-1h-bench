package main

import (
	"encoding/json"
	"github.com/labstack/echo/v4"
)

func checksumHandler(c echo.Context) error {
	mu.RLock()
	defer mu.RUnlock()
	b, e := json.Marshal(st)
	if e != nil {
		return e
	}
	return c.JSON(200, map[string]string{"sha256": hashImage(b)})
}
