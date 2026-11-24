package main

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/labstack/echo/v4"
)

type streamActivity struct {
	tips, maxTip, viewerCount int64
	emojis                    map[string]int64
	comments                  []LivecommentModel
	reactions                 []ReactionModel
	words                     []NGWord
	reports                   []LivecommentReportModel
	viewers                   map[int64]int64
}

var activityMu sync.RWMutex
var activities map[int64]*streamActivity
var commentByID map[int64]LivecommentModel
var writers [256]sync.Mutex

func lockStream(id int64) func() {
	lock := &writers[uint64(id)%uint64(len(writers))]
	lock.Lock()
	return lock.Unlock
}
func activity(id int64) *streamActivity {
	a := activities[id]
	if a == nil {
		a = &streamActivity{emojis: map[string]int64{}, viewers: map[int64]int64{}}
		activities[id] = a
	}
	return a
}
func loadActivity() error {
	activityMu.Lock()
	defer activityMu.Unlock()
	activities = map[int64]*streamActivity{}
	commentByID = map[int64]LivecommentModel{}
	var comments []struct {
		LivecommentModel
		Hidden bool
	}
	if err := dbConn.Select(&comments, "SELECT *,hidden FROM livecomments ORDER BY created_at,id"); err != nil {
		return err
	}
	for _, row := range comments {
		m := row.LivecommentModel
		commentByID[m.ID] = m
		if !row.Hidden {
			a := activity(m.LivestreamID)
			a.addComment(m)
		}
	}
	var reactions []ReactionModel
	if err := dbConn.Select(&reactions, "SELECT * FROM reactions ORDER BY created_at,id"); err != nil {
		return err
	}
	for _, r := range reactions {
		a := activity(r.LivestreamID)
		a.reactions = append(a.reactions, r)
		a.emojis[r.EmojiName]++
	}
	var words []NGWord
	if err := dbConn.Select(&words, "SELECT * FROM ng_words ORDER BY created_at,id"); err != nil {
		return err
	}
	for _, r := range words {
		a := activity(r.LivestreamID)
		a.words = append(a.words, r)
	}
	var reports []LivecommentReportModel
	if err := dbConn.Select(&reports, "SELECT * FROM livecomment_reports ORDER BY id"); err != nil {
		return err
	}
	for _, r := range reports {
		a := activity(r.LivestreamID)
		a.reports = append(a.reports, r)
	}
	var viewers []struct{ Stream, Viewer, N int64 }
	if err := dbConn.Select(&viewers, "SELECT livestream_id AS stream,user_id AS viewer,COUNT(*) AS n FROM livestream_viewers_history GROUP BY livestream_id,user_id"); err != nil {
		return err
	}
	for _, v := range viewers {
		a := activity(v.Stream)
		a.viewers[v.Viewer] = v.N
		a.viewerCount += v.N
	}
	return restoreJournal()
}
func pageSize(c echo.Context, n int) (int, error) {
	if limit := c.QueryParam("limit"); limit != "" {
		l, err := strconv.Atoi(limit)
		if err != nil || l < 0 {
			return 0, echo.NewHTTPError(http.StatusBadRequest, "invalid limit")
		}
		if l < n {
			return l, nil
		}
	}
	return n, nil
}
func (a *streamActivity) addComment(m LivecommentModel) {
	if len(a.comments) == 0 || m.Tip > a.maxTip {
		a.maxTip = m.Tip
	}
	a.tips += m.Tip
	a.comments = append(a.comments, m)
}
func (a *streamActivity) moderate(word string) {
	rows := a.comments
	a.comments = rows[:0]
	a.maxTip = 0
	a.tips = 0
	for _, m := range rows {
		if !strings.Contains(m.Comment, word) {
			a.addComment(m)
		}
	}
}
func rankedUser(id int64) UserStatistics {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	activityMu.RLock()
	defer activityMu.RUnlock()
	scores := map[int64]int64{}
	emojis := map[string]int{}
	stats := UserStatistics{Rank: 1}
	for sid, a := range activities {
		owner := catalog.streams[sid].Owner.ID
		scores[owner] += int64(len(a.reactions)) + a.tips
		if owner != id {
			continue
		}
		stats.TotalTip += a.tips
		stats.TotalLivecomments += int64(len(a.comments))
		stats.TotalReactions += int64(len(a.reactions))
		for emoji, n := range a.emojis {
			emojis[emoji] += int(n)
		}
		stats.ViewersCount += a.viewerCount
	}
	for uid, u := range catalog.users {
		if scores[uid] > scores[id] || (scores[uid] == scores[id] && u.Name > catalog.users[id].Name) {
			stats.Rank++
		}
	}
	for emoji, n := range emojis {
		if n > emojis[stats.FavoriteEmoji] || (n == emojis[stats.FavoriteEmoji] && emoji > stats.FavoriteEmoji) {
			stats.FavoriteEmoji = emoji
		}
	}
	return stats
}
func rankedStream(id int64) LivestreamStatistics {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	activityMu.RLock()
	defer activityMu.RUnlock()
	stats := LivestreamStatistics{Rank: 1}
	var target int64
	if a := activities[id]; a != nil {
		stats.TotalReports = int64(len(a.reports))
		stats.TotalReactions = int64(len(a.reactions))
		stats.MaxTip = a.maxTip
		stats.ViewersCount = a.viewerCount
		target = stats.TotalReactions + a.tips
	}
	for sid := range catalog.streams {
		var score int64
		if a := activities[sid]; a != nil {
			score = int64(len(a.reactions)) + a.tips
		}
		if score > target || (score == target && sid > id) {
			stats.Rank++
		}
	}
	return stats
}

func newestWords(id, owner int64) []NGWord {
	activityMu.RLock()
	defer activityMu.RUnlock()
	result := []NGWord{}
	if a := activities[id]; a != nil {
		for _, word := range a.words {
			if word.UserID == owner {
				result = append(result, word)
			}
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].CreatedAt == result[j].CreatedAt {
			return result[i].ID > result[j].ID
		}
		return result[i].CreatedAt > result[j].CreatedAt
	})
	return result
}
