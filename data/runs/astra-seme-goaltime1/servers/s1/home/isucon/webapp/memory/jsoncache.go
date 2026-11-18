package main

import "encoding/json"

var commentTexts map[int64][]byte
var userResponses map[int64]User
var streamResponses map[int64]Livestream

func (u User) MarshalJSON() ([]byte, error) {
	if u.rawJSON != nil {
		return u.rawJSON, nil
	}
	type plain User
	return json.Marshal(plain(u))
}
func (s Livestream) MarshalJSON() ([]byte, error) {
	if s.rawJSON != nil {
		return s.rawJSON, nil
	}
	type plain Livestream
	return json.Marshal(plain(s))
}
func refreshUser(id int64) {
	u := buildUserResponse(id)
	type plain User
	u.rawJSON, _ = json.Marshal(plain(u))
	userResponses[id] = u
}
func refreshStream(id int64) {
	s := buildStreamResponse(id)
	type plain Livestream
	s.rawJSON, _ = json.Marshal(plain(s))
	streamResponses[id] = s
}
func rebuildJSONCaches() {
	commentTexts = map[int64][]byte{}
	for id, c := range state.Comments {
		commentTexts[id], _ = json.Marshal(c.Comment)
	}
	for id, c := range state.ArchivedComments {
		commentTexts[id], _ = json.Marshal(c.Comment)
	}
	userResponses = make(map[int64]User, len(state.Users))
	streamResponses = make(map[int64]Livestream, len(state.Streams))
	for id := range state.Users {
		refreshUser(id)
	}
	for id := range state.Streams {
		refreshStream(id)
	}
}
func userResponse(id int64) User {
	if u, ok := userResponses[id]; ok {
		return u
	}
	return buildUserResponse(id)
}
func streamResponse(id int64) Livestream {
	if s, ok := streamResponses[id]; ok {
		return s
	}
	return buildStreamResponse(id)
}
