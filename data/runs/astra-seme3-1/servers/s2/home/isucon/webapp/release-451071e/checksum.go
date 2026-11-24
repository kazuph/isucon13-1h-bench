package main

import (
	"encoding/json"
	"github.com/labstack/echo/v4"
)

func checksumHandler(c echo.Context) error {
	mu.RLock()
	b, err := json.Marshal(st)
	r := lastCommit
	mu.RUnlock()
	if err != nil {
		return err
	}
	if r != nil {
		<-r.done
		if r.err != nil {
			return r.err
		}
	}
	return c.JSON(200, map[string]string{"sha256": hashImage(b)})
}
