package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
)

func projectionDigestHandler(w http.ResponseWriter, r *http.Request) {
	state.RLock()
	defer state.RUnlock()
	reactions := make(map[int64][]ReactionModel)
	for id, rs := range state.reactions {
		rs = append([]ReactionModel{}, rs...)
		sort.Slice(rs, func(i, j int) bool { return rs[i].ID < rs[j].ID })
		reactions[id] = rs
	}
	reports := make(map[int64][]LivecommentReportModel)
	for id, rs := range state.reports {
		rs = append([]LivecommentReportModel{}, rs...)
		sort.Slice(rs, func(i, j int) bool { return rs[i].ID < rs[j].ID })
		reports[id] = rs
	}
	words := make(map[int64][]NGWord)
	for id, rs := range state.words {
		rs = append([]NGWord{}, rs...)
		sort.Slice(rs, func(i, j int) bool { return rs[i].ID < rs[j].ID })
		words[id] = rs
	}
	views := make(map[int64]map[int64]int64)
	for id, vs := range state.viewers {
		if len(vs) > 0 {
			views[id] = vs
		}
	}
	comments := make(map[int64]map[int64]LivecommentModel)
	for id, cs := range state.comments {
		if len(cs) > 0 {
			comments[id] = cs
		}
	}
	scores := make(map[int64]int64)
	for id, score := range state.userScore {
		if score != 0 {
			scores[id] = score
		}
	}
	fields := map[string]interface{}{"users": state.users, "streams": state.streams, "tags": state.tags, "comments": comments, "reactions": reactions, "reports": reports, "words": words, "viewers": views, "stream_scores": state.streamScore, "user_scores": scores, "total_tip": state.totalTip}
	out := make(map[string]string)
	for key, value := range fields {
		h := sha256.New()
		if err := json.NewEncoder(h).Encode(value); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		out[key] = fmt.Sprintf("%x", h.Sum(nil))
	}
	for id, img := range state.icons {
		if state.users[id].IconHash != fmt.Sprintf("%x", sha256.Sum256(img)) {
			out["icon_error"] = fmt.Sprint(id)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}
