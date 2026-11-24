package main

import (
	"sort"
	"sync"
)

var reservationSlots struct {
	sync.RWMutex
	rows []ReservationSlotModel
}

// Slot boundaries are fixed until initialize. Capacity only decreases after a committed booking.
func matchingSlots(start, end int64) ([]int, bool) {
	reservationSlots.RLock()
	defer reservationSlots.RUnlock()
	rows := reservationSlots.rows
	first := sort.Search(len(rows), func(i int) bool { return rows[i].StartAt >= start })
	var indexes []int
	for i := first; i < len(rows) && rows[i].StartAt < end; i++ {
		if rows[i].EndAt > end {
			continue
		}
		if rows[i].Slot == 0 {
			return nil, false
		}
		indexes = append(indexes, i)
	}
	return indexes, true
}
func bookedSlots(indexes []int) {
	reservationSlots.Lock()
	defer reservationSlots.Unlock()
	for _, i := range indexes {
		reservationSlots.rows[i].Slot--
	}
}
