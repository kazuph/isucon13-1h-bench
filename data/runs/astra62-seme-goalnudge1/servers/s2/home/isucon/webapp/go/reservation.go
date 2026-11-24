package main

import (
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

var inventory struct {
	sync.Mutex
	slots []ReservationSlotModel
	next  int64
}

func loadInventory() error {
	inventory.slots = nil
	if err := dbConn.Select(&inventory.slots, "SELECT * FROM reservation_slots ORDER BY start_at"); err != nil {
		return err
	}
	return dbConn.Get(&inventory.next, "SELECT IFNULL(MAX(id),0) FROM livestreams")
}
func slotWindow(from, to int64) (int, int) {
	start := sort.Search(len(inventory.slots), func(i int) bool { return inventory.slots[i].StartAt >= from })
	end := sort.Search(len(inventory.slots), func(i int) bool { return inventory.slots[i].StartAt >= to })
	if end < start {
		return start, start
	}
	return start, end
}
func reserve(c echo.Context) error {
	if err := verifyUserSession(c); err != nil {
		return err
	}
	uid := c.Get(defaultUserIDKey).(int64)
	var req ReserveLivestreamRequest
	if err := parseBody(c, &req); err != nil {
		return err
	}
	first := time.Date(2023, 11, 25, 1, 0, 0, 0, time.UTC).Unix()
	last := time.Date(2024, 11, 25, 1, 0, 0, 0, time.UTC).Unix()
	if req.StartAt >= last || req.EndAt <= first || req.StartAt >= req.EndAt {
		return echo.NewHTTPError(400, "bad reservation time range")
	}
	data.RLock()
	owner, ok := data.users[uid]
	tags := make([]Tag, 0, len(req.Tags))
	for _, id := range req.Tags {
		t, found := data.tags[id]
		if !found {
			data.RUnlock()
			return echo.NewHTTPError(400, "unknown tag")
		}
		tags = append(tags, t)
	}
	data.RUnlock()
	if !ok {
		return echo.NewHTTPError(404, "owner not found")
	}
	inventory.Lock()
	start, end := slotWindow(req.StartAt, req.EndAt)
	for i := start; i < end; i++ {
		v := inventory.slots[i]
		if v.EndAt <= req.EndAt && v.Slot < 1 {
			inventory.Unlock()
			return echo.NewHTTPError(400, "no available slot")
		}
	}
	id := inventory.next + 1
	stream := Livestream{ID: id, Owner: owner, Title: req.Title, Description: req.Description, PlaylistUrl: req.PlaylistUrl, ThumbnailUrl: req.ThumbnailUrl, Tags: tags, StartAt: req.StartAt, EndAt: req.EndAt}
	payload, err := json.Marshal(stream)
	if err == nil {
		_, err = commitEvent(activityEvent{Kind: "reserve", Stream: id, User: uid, Text: string(payload)})
	}
	inventory.Unlock()
	if err != nil {
		return err
	}
	return c.JSON(201, stream)
}
func restoreReservation(e activityEvent) error {
	var stream Livestream
	if err := json.Unmarshal([]byte(e.Text), &stream); err != nil {
		return err
	}
	start, end := slotWindow(stream.StartAt, stream.EndAt)
	for i := start; i < end; i++ {
		if inventory.slots[i].EndAt <= stream.EndAt {
			inventory.slots[i].Slot--
		}
	}
	if stream.ID > inventory.next {
		inventory.next = stream.ID
	}
	data.Lock()
	stream.Owner = data.users[e.User]
	data.streams[stream.ID] = stream
	data.indexStream(stream)
	data.Unlock()
	return nil
}
