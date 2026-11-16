package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/labstack/echo/v4"
	"io"
	"sort"
)

func hashRows[T any](w io.Writer, rows map[int64]T) error {
	ids := make([]int64, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	e := json.NewEncoder(w)
	for _, id := range ids {
		if err := e.Encode(id); err != nil {
			return err
		}
		if err := e.Encode(rows[id]); err != nil {
			return err
		}
	}
	return nil
}
func memoryDigest(c echo.Context) error {
	stateMu.RLock()
	defer stateMu.RUnlock()
	h := sha256.New()
	for _, err := range []error{hashRows(h, state.Users), hashRows(h, state.Themes), hashRows(h, state.Icons), hashRows(h, state.Streams), hashRows(h, state.Tags), hashRows(h, state.StreamTags), hashRows(h, state.Comments), hashRows(h, state.Reactions), hashRows(h, state.Reports), hashRows(h, state.Words), hashRows(h, state.Viewers)} {
		if err != nil {
			return err
		}
	}
	e := json.NewEncoder(h)
	if err := e.Encode(state.Slots); err != nil {
		return err
	}
	if err := e.Encode(state.Next); err != nil {
		return err
	}
	return c.JSON(200, map[string]interface{}{"sha256": fmt.Sprintf("%x", h.Sum(nil)), "users": len(state.Users), "streams": len(state.Streams), "comments": len(state.Comments), "reactions": len(state.Reactions), "icons": len(state.Icons), "reports": len(state.Reports), "words": len(state.Words)})
}
