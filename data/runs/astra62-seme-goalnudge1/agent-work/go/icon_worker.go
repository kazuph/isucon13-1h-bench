package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"

	"github.com/labstack/echo/v4"
)

type storedIcon struct {
	ID   int64  `json:"id"`
	Hash string `json:"hash"`
}

func storeIcon(ctx context.Context, userID int64, img []byte) (storedIcon, error) {
	hash := fmt.Sprintf("%x", sha256.Sum256(img))
	if _, known := knownIconObjects.Load(hash); !known {
		if _, err := dbConn.ExecContext(ctx, "INSERT IGNORE INTO icon_objects(hash,image) VALUES(?,?)", hash, img); err != nil {
			return storedIcon{}, err
		}
		knownIconObjects.Store(hash, true)
	}
	res, err := dbConn.ExecContext(ctx, "REPLACE INTO icon_refs(user_id,hash) VALUES(?,?)", userID, hash)
	if err != nil {
		return storedIcon{}, err
	}
	id, err := res.LastInsertId()
	return storedIcon{ID: id, Hash: hash}, err
}
func workerUpload(c echo.Context) error {
	if subtle.ConstantTimeCompare([]byte(c.Request().Header.Get("Authorization")), []byte("Bearer "+os.Getenv("ISUPIPE_ZONE_TOKEN"))) != 1 {
		return echo.NewHTTPError(403, "forbidden")
	}
	id, err := strconv.ParseInt(c.Param("user"), 10, 64)
	if err != nil || id <= 0 {
		return echo.NewHTTPError(400, "invalid user")
	}
	img, err := decodeIcon(c)
	if err != nil {
		return echo.NewHTTPError(400, "invalid icon upload")
	}
	ref, err := storeIcon(c.Request().Context(), id, img)
	if err != nil {
		return err
	}
	return c.JSON(201, ref)
}
func receiveIcon(c echo.Context, userID int64) (storedIcon, []byte, error) {
	ctx := c.Request().Context()
	worker := os.Getenv("ISUPIPE_ICON_WORKER")
	if worker == "" {
		img, err := decodeIcon(c)
		if err != nil {
			return storedIcon{}, nil, echo.NewHTTPError(400, "invalid icon upload")
		}
		ref, err := storeIcon(ctx, userID, img)
		return ref, img, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", worker+"/store-icon/"+strconv.FormatInt(userID, 10), c.Request().Body)
	if err != nil {
		return storedIcon{}, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("ISUPIPE_ZONE_TOKEN"))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = c.Request().ContentLength
	resp, err := zoneClient.Do(req)
	if err != nil {
		return storedIcon{}, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		return storedIcon{}, nil, echo.NewHTTPError(resp.StatusCode, "icon upload failed")
	}
	var ref storedIcon
	if err = json.NewDecoder(resp.Body).Decode(&ref); err != nil {
		return ref, nil, err
	}
	data.RLock()
	img, found := data.objects[ref.Hash]
	data.RUnlock()
	if !found {
		err = dbConn.GetContext(ctx, &img, "SELECT image FROM icon_objects WHERE hash=?", ref.Hash)
	}
	return ref, img, err
}
