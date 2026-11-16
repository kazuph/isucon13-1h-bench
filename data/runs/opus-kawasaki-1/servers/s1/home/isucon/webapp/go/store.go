package main

import (
	"strings"
	"context"
	"sort"
	"sync"
)

// In-memory store for hot per-livestream data. Single app instance assumed.
var (
	storeMu        sync.RWMutex
	commentsByLS   = map[int64][]LivecommentModel{}
	commentByID    = map[int64]LivecommentModel{}
	reactionsByLS  = map[int64][]ReactionModel{}
	ngwordsByLS    = map[int64][]*NGWord{}
	reportsByLS    = map[int64][]*LivecommentReportModel{}
	allLivestreams []*LivestreamModel // sorted by id asc
	lsByUser       = map[int64][]*LivestreamModel{}
)

func loadStore(ctx context.Context) error {
	var lcs []LivecommentModel
	if err := dbConn.SelectContext(ctx, &lcs, "SELECT * FROM livecomments ORDER BY id"); err != nil {
		return err
	}
	var rs []ReactionModel
	if err := dbConn.SelectContext(ctx, &rs, "SELECT * FROM reactions ORDER BY id"); err != nil {
		return err
	}
	var ngs []*NGWord
	if err := dbConn.SelectContext(ctx, &ngs, "SELECT * FROM ng_words ORDER BY id"); err != nil {
		return err
	}
	var reps []*LivecommentReportModel
	if err := dbConn.SelectContext(ctx, &reps, "SELECT * FROM livecomment_reports ORDER BY id"); err != nil {
		return err
	}
	var lss []*LivestreamModel
	if err := dbConn.SelectContext(ctx, &lss, "SELECT * FROM livestreams ORDER BY id"); err != nil {
		return err
	}

	cm := map[int64][]LivecommentModel{}
	cid := make(map[int64]LivecommentModel, len(lcs))
	for _, c := range lcs {
		cm[c.LivestreamID] = append(cm[c.LivestreamID], c)
		cid[c.ID] = c
	}
	rm := map[int64][]ReactionModel{}
	for _, r := range rs {
		rm[r.LivestreamID] = append(rm[r.LivestreamID], r)
	}
	nm := map[int64][]*NGWord{}
	for _, n := range ngs {
		nm[n.LivestreamID] = append(nm[n.LivestreamID], n)
	}
	pm := map[int64][]*LivecommentReportModel{}
	for _, p := range reps {
		pm[p.LivestreamID] = append(pm[p.LivestreamID], p)
	}
	um := map[int64][]*LivestreamModel{}
	for _, l := range lss {
		um[l.UserID] = append(um[l.UserID], l)
	}

	storeMu.Lock()
	commentsByLS = cm
	commentByID = cid
	reactionsByLS = rm
	ngwordsByLS = nm
	reportsByLS = pm
	allLivestreams = lss
	lsByUser = um
	storeMu.Unlock()

	cacheMu.Lock()
	for _, l := range lss {
		livestreamByID[l.ID] = l
	}
	cacheMu.Unlock()
	return nil
}

func storeAddLivestream(l *LivestreamModel) {
	storeMu.Lock()
	defer storeMu.Unlock()
	n := len(allLivestreams)
	if n == 0 || allLivestreams[n-1].ID < l.ID {
		allLivestreams = append(allLivestreams, l)
	} else {
		i := sort.Search(n, func(i int) bool { return allLivestreams[i].ID >= l.ID })
		allLivestreams = append(allLivestreams, nil)
		copy(allLivestreams[i+1:], allLivestreams[i:])
		allLivestreams[i] = l
	}
	us := lsByUser[l.UserID]
	us = append(us, l)
	sort.Slice(us, func(i, j int) bool { return us[i].ID < us[j].ID })
	lsByUser[l.UserID] = us
}

// latest livestreams, id desc
func storeLatestLivestreams(limit int) []*LivestreamModel {
	storeMu.RLock()
	defer storeMu.RUnlock()
	n := len(allLivestreams)
	if limit < 0 || limit > n {
		limit = n
	}
	res := make([]*LivestreamModel, 0, limit)
	for i := n - 1; i >= 0 && len(res) < limit; i-- {
		res = append(res, allLivestreams[i])
	}
	return res
}

func storeUserLivestreams(userID int64) []*LivestreamModel {
	storeMu.RLock()
	defer storeMu.RUnlock()
	src := lsByUser[userID]
	res := make([]*LivestreamModel, len(src))
	copy(res, src)
	return res
}

func insertSortedComment(list []LivecommentModel, c LivecommentModel) []LivecommentModel {
	n := len(list)
	if n == 0 || list[n-1].ID < c.ID {
		return append(list, c)
	}
	i := sort.Search(n, func(i int) bool { return list[i].ID >= c.ID })
	list = append(list, LivecommentModel{})
	copy(list[i+1:], list[i:])
	list[i] = c
	return list
}

func storeAddComment(c LivecommentModel) {
	storeMu.Lock()
	commentsByLS[c.LivestreamID] = insertSortedComment(commentsByLS[c.LivestreamID], c)
	commentByID[c.ID] = c
	storeMu.Unlock()
}

// order: created_at desc, id desc
func storeGetComments(lsID int64, limit int) []LivecommentModel {
	storeMu.RLock()
	defer storeMu.RUnlock()
	src := commentsByLS[lsID]
	n := len(src)
	res := make([]LivecommentModel, n)
	for i := 0; i < n; i++ {
		res[i] = src[n-1-i]
	}
	sort.SliceStable(res, func(i, j int) bool { return res[i].CreatedAt > res[j].CreatedAt })
	if limit >= 0 && limit < len(res) {
		res = res[:limit]
	}
	return res
}

func storeGetCommentByID(id int64) (LivecommentModel, bool) {
	storeMu.RLock()
	defer storeMu.RUnlock()
	c, ok := commentByID[id]
	return c, ok
}

func storeReplaceComments(lsID int64, list []LivecommentModel) {
	storeMu.Lock()
	defer storeMu.Unlock()
	keep := make(map[int64]struct{}, len(list))
	for _, c := range list {
		keep[c.ID] = struct{}{}
	}
	for _, c := range commentsByLS[lsID] {
		if _, ok := keep[c.ID]; !ok {
			delete(commentByID, c.ID)
		}
	}
	commentsByLS[lsID] = list
	for _, c := range list {
		commentByID[c.ID] = c
	}
}

func storeAddReaction(r ReactionModel) {
	storeMu.Lock()
	defer storeMu.Unlock()
	list := reactionsByLS[r.LivestreamID]
	n := len(list)
	if n == 0 || list[n-1].ID < r.ID {
		list = append(list, r)
	} else {
		i := sort.Search(n, func(i int) bool { return list[i].ID >= r.ID })
		list = append(list, ReactionModel{})
		copy(list[i+1:], list[i:])
		list[i] = r
	}
	reactionsByLS[r.LivestreamID] = list
}

func storeGetReactions(lsID int64, limit int) []ReactionModel {
	storeMu.RLock()
	defer storeMu.RUnlock()
	src := reactionsByLS[lsID]
	n := len(src)
	res := make([]ReactionModel, n)
	for i := 0; i < n; i++ {
		res[i] = src[n-1-i]
	}
	sort.SliceStable(res, func(i, j int) bool { return res[i].CreatedAt > res[j].CreatedAt })
	if limit >= 0 && limit < len(res) {
		res = res[:limit]
	}
	return res
}

func storeAddNGWord(w *NGWord) {
	storeMu.Lock()
	ngwordsByLS[w.LivestreamID] = append(ngwordsByLS[w.LivestreamID], w)
	storeMu.Unlock()
}

// count of ng words owned by userID on livestream
func storeNGWordCount(lsID, userID int64) int {
	storeMu.RLock()
	defer storeMu.RUnlock()
	cnt := 0
	for _, w := range ngwordsByLS[lsID] {
		if w.UserID == userID {
			cnt++
		}
	}
	return cnt
}

// order: created_at desc, id desc
func storeGetNGWords(lsID, userID int64) []*NGWord {
	storeMu.RLock()
	defer storeMu.RUnlock()
	src := ngwordsByLS[lsID]
	res := make([]*NGWord, 0, len(src))
	for i := len(src) - 1; i >= 0; i-- {
		if src[i].UserID == userID {
			res = append(res, src[i])
		}
	}
	sort.SliceStable(res, func(i, j int) bool { return res[i].CreatedAt > res[j].CreatedAt })
	return res
}

func storeAddReport(r *LivecommentReportModel) {
	storeMu.Lock()
	reportsByLS[r.LivestreamID] = append(reportsByLS[r.LivestreamID], r)
	storeMu.Unlock()
}

func storeGetReports(lsID int64) []*LivecommentReportModel {
	storeMu.RLock()
	defer storeMu.RUnlock()
	src := reportsByLS[lsID]
	res := make([]*LivecommentReportModel, len(src))
	copy(res, src)
	sort.Slice(res, func(i, j int) bool { return res[i].ID < res[j].ID })
	return res
}

func storeRemoveComments(lsID int64, ids []int64) {
	del := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		del[id] = struct{}{}
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	src := commentsByLS[lsID]
	out := make([]LivecommentModel, 0, len(src))
	for _, c := range src {
		if _, ok := del[c.ID]; ok {
			delete(commentByID, c.ID)
			continue
		}
		out = append(out, c)
	}
	commentsByLS[lsID] = out
}

// likeContainsCI emulates `text COLLATE utf8mb4_general_ci LIKE CONCAT('%', word, '%')`.
// ok=false means the result is uncertain and SQL should be used.
func likeContainsCI(text, word string) (match bool, ok bool) {
	if strings.ContainsAny(word, "%_\\") {
		return false, false
	}
	if !simpleRunes(text, false) || !simpleRunes(word, true) {
		return false, false
	}
	return strings.Contains(asciiLower(text), asciiLower(word)), true
}

func simpleRunes(s string, isWord bool) bool {
	for _, r := range s {
		switch {
		case r >= 0x80 && r <= 0x1FFF,
			r >= 0x2100 && r <= 0x218F,
			r >= 0x24B6 && r <= 0x24E9,
			r >= 0x2C00 && r <= 0x2DFF,
			r >= 0xA640 && r <= 0xA7FF,
			r >= 0xFF21 && r <= 0xFF3A,
			r >= 0xFF41 && r <= 0xFF5A,
			r >= 0xD800 && r <= 0xDFFF,
			r == 0xFFFD:
			return false
		case isWord && r > 0xFFFF:
			return false
		}
	}
	return true
}

func asciiLower(s string) string {
	hasUpper := false
	for i := 0; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			hasUpper = true
			break
		}
	}
	if !hasUpper {
		return s
	}
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// storeSpamCheck returns (hit, certain).
func storeSpamCheck(lsID, ownerID int64, comment string) (bool, bool) {
	storeMu.RLock()
	defer storeMu.RUnlock()
	certain := true
	for _, w := range ngwordsByLS[lsID] {
		if w.UserID != ownerID {
			continue
		}
		m, ok := likeContainsCI(comment, w.Word)
		if !ok {
			certain = false
			continue
		}
		if m {
			return true, true
		}
	}
	return false, certain
}
