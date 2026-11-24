package main

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

type PaymentResult struct {
	TotalTip int64 `json:"total_tip"`
}

func GetPaymentResult(c echo.Context) error {
	metrics.RLock()
	var totalTip int64
	for _, m := range metrics.streams {
		totalTip += m.comments.Tips
	}
	metrics.RUnlock()
	return c.JSON(http.StatusOK, &PaymentResult{
		TotalTip: totalTip,
	})
}
