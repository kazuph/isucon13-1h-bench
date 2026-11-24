package main

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

type PaymentResult struct {
	TotalTip int64 `json:"total_tip"`
}

func GetPaymentResult(c echo.Context) error {
	var totalTip int64
	activityMu.RLock()
	for _, a := range activities {
		for _, m := range a.comments {
			totalTip += m.Tip
		}
	}
	activityMu.RUnlock()
	return c.JSON(http.StatusOK, &PaymentResult{
		TotalTip: totalTip,
	})
}
