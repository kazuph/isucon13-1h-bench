package main

import "sync"

var readMu sync.RWMutex
var committedState *State

func copyMap[K comparable, V any](src map[K]V) map[K]V {
	dst := make(map[K]V, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
func copyLists[K comparable, V any](src map[K][]V) map[K][]V {
	dst := make(map[K][]V, len(src))
	for k, v := range src {
		dst[k] = append([]V{}, v...)
	}
	return dst
}
func cloneState(src *State) *State {
	s := *src
	s.Users = copyMap(src.Users)
	s.Names = copyMap(src.Names)
	s.Themes = copyMap(src.Themes)
	// Image bytes and strings are immutable. Only their index entries change.
	s.Icons = copyMap(src.Icons)
	s.Hashes = copyMap(src.Hashes)
	s.Streams = copyMap(src.Streams)
	s.StreamTags = copyLists(src.StreamTags)
	s.Tags = append([]Tag{}, src.Tags...)
	s.Comments = copyMap(src.Comments)
	s.CommentIDs = copyLists(src.CommentIDs)
	s.Reactions = copyMap(src.Reactions)
	s.ReactionIDs = copyLists(src.ReactionIDs)
	s.Reports = copyLists(src.Reports)
	s.ReportComments = copyMap(src.ReportComments)
	s.Words = copyLists(src.Words)
	s.Viewers = make(map[int64]map[int64]int64, len(src.Viewers))
	for k, v := range src.Viewers {
		s.Viewers[k] = copyMap(v)
	}
	s.Slots = append([]ReservationSlotModel{}, src.Slots...)
	s.Stats = make(map[int64]*StreamStat, len(src.Stats))
	for k, v := range src.Stats {
		z := *v
		z.Emoji = copyMap(v.Emoji)
		s.Stats[k] = &z
	}
	s.Next = copyMap(src.Next)
	s.DNS = copyMap(src.DNS)
	s.buildIndexes()
	return &s
}
