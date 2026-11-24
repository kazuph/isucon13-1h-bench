package main

// Entries are contiguous for rank scans. The ID-to-position maps also handle
// sparse IDs from an older database; inserts and score changes are constant time.
type streamStanding struct{ id, owner, score int64 }
type userStanding struct {
	name  string
	score int64
}
type standings struct {
	streams        []streamStanding
	users          []userStanding
	streamPosition map[int64]int
	userPosition   map[int64]int
}

func (r *standings) load() {
	r.streams = make([]streamStanding, 0, len(data.streams))
	r.users = make([]userStanding, 0, len(data.users))
	r.streamPosition = make(map[int64]int, len(data.streams))
	r.userPosition = make(map[int64]int, len(data.users))
	for _, u := range data.users {
		r.addUser(u)
	}
	for _, id := range data.allStreams {
		r.addStream(data.streams[id])
	}
}
func (r *standings) addUser(u User) {
	if _, ok := r.userPosition[u.ID]; ok {
		return
	}
	r.userPosition[u.ID] = len(r.users)
	r.users = append(r.users, userStanding{name: u.Name})
}
func (r *standings) addStream(s Livestream) {
	r.streamPosition[s.ID] = len(r.streams)
	r.streams = append(r.streams, streamStanding{id: s.ID, owner: s.Owner.ID})
}
func (r *standings) change(id, delta int64) {
	if delta == 0 {
		return
	}
	pos, ok := r.streamPosition[id]
	if !ok {
		return
	}
	s := &r.streams[pos]
	s.score += delta
	if owner, ok := r.userPosition[s.owner]; ok {
		r.users[owner].score += delta
	}
}
func (r *standings) streamRank(id int64) int64 {
	mine := r.streams[r.streamPosition[id]]
	rank := int64(1)
	for _, s := range r.streams {
		if s.score > mine.score || s.score == mine.score && s.id > id {
			rank++
		}
	}
	return rank
}
func (r *standings) userRank(id int64) int64 {
	mine := r.users[r.userPosition[id]]
	rank := int64(1)
	for _, u := range r.users {
		if u.score > mine.score || u.score == mine.score && u.name > mine.name {
			rank++
		}
	}
	return rank
}
