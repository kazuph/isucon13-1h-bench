package main

import (
	"context"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
)

func listTimeline(ctx context.Context, dest interface{}, query string, id int, rawLimit string) error {
	args := []interface{}{id}
	if rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil || limit < 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid limit")
		}
		query += " LIMIT ?"
		args = append(args, limit)
	}
	return dbConn.SelectContext(ctx, dest, query, args...)
}
