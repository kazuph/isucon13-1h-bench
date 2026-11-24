package main

import (
	"context"
	"github.com/jmoiron/sqlx"
	"sync"
)

type commentTotals struct {
	Count int64 `db:"n"`
	Tips  int64 `db:"tips"`
	Max   int64 `db:"max_tip"`
}
type streamMetrics struct {
	comments                    commentTotals
	reactions, viewers, reports int64
	emoji                       map[string]int64
}

func (m streamMetrics) score() int64 { return m.comments.Tips + m.reactions }

var metrics struct {
	sync.RWMutex
	streams map[int64]*streamMetrics
}

func (m *streamMetrics) addReaction(name string, n int64) {
	if m.emoji == nil {
		m.emoji = make(map[string]int64)
	}
	m.emoji[name] += n
	m.reactions += n
}
func metricFor(streams map[int64]*streamMetrics, id int64) *streamMetrics {
	m := streams[id]
	if m == nil {
		m = &streamMetrics{}
		streams[id] = m
	}
	return m
}
func loadMetrics(ctx context.Context, tx *sqlx.Tx) (map[int64]*streamMetrics, error) {
	result := make(map[int64]*streamMetrics)
	var counts []struct {
		ID      int64
		N, Tips int64
		Max     int64 `db:"max_tip"`
	}
	if err := tx.SelectContext(ctx, &counts, `SELECT livestream_id id,COUNT(*) n,SUM(tip) tips,MAX(tip) max_tip FROM livecomments WHERE moderated=FALSE GROUP BY livestream_id`); err != nil {
		return nil, err
	}
	for _, r := range counts {
		metricFor(result, r.ID).comments = commentTotals{r.N, r.Tips, r.Max}
	}
	var reactions []struct {
		ID, N int64
		Name  string
	}
	if err := tx.SelectContext(ctx, &reactions, "SELECT livestream_id id,emoji_name name,COUNT(*) n FROM reactions GROUP BY livestream_id,emoji_name"); err != nil {
		return nil, err
	}
	for _, r := range reactions {
		metricFor(result, r.ID).addReaction(r.Name, r.N)
	}
	var grouped []struct{ ID, N int64 }
	if err := tx.SelectContext(ctx, &grouped, "SELECT livestream_id id,COUNT(*) n FROM livestream_viewers_history GROUP BY livestream_id"); err != nil {
		return nil, err
	}
	for _, r := range grouped {
		metricFor(result, r.ID).viewers = r.N
	}
	grouped = nil
	if err := tx.SelectContext(ctx, &grouped, "SELECT livestream_id id,COUNT(*) n FROM livecomment_reports GROUP BY livestream_id"); err != nil {
		return nil, err
	}
	for _, r := range grouped {
		metricFor(result, r.ID).reports = r.N
	}
	return result, nil
}
func noteComment(comment LivecommentModel) {
	metrics.Lock()
	defer metrics.Unlock()
	t := &metricFor(metrics.streams, comment.LivestreamID).comments
	if t.Count == 0 || comment.Tip > t.Max {
		t.Max = comment.Tip
	}
	t.Count++
	t.Tips += comment.Tip
}
func noteReaction(reaction ReactionModel) {
	metrics.Lock()
	defer metrics.Unlock()
	metricFor(metrics.streams, reaction.LivestreamID).addReaction(reaction.EmojiName, 1)
}
func noteView(streamID, delta int64) {
	metrics.Lock()
	defer metrics.Unlock()
	metricFor(metrics.streams, streamID).viewers += delta
}
