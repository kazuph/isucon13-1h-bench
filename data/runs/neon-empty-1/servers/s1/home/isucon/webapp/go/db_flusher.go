package main

import (
	"context"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
)

type dbUserInsert struct {
	ID             int64
	Name           string
	DisplayName    string
	Description    string
	HashedPassword string
	ThemeID        int64
	DarkMode       bool
}

type dbIconUpsert struct {
	ID     int64
	UserID int64
	Hash   string
	Image  []byte
}

type dbSlotDec struct {
	StartAt int64
	EndAt   int64
}

type dbLivestreamInsert struct {
	Model LivestreamModel
	Tags  []int64
}

type dbViewerOp struct {
	UserID       int64
	LivestreamID int64
	CreatedAt    int64
}

type writeBatch struct {
	users       []dbUserInsert
	icons       []dbIconUpsert
	slotDecs    []dbSlotDec
	livestreams []dbLivestreamInsert
	ngWords     []NGWord
	livecomments []LivecommentModel
	deleteLCIDs []int64
	reactions   []ReactionModel
	reports     []LivecommentReportModel
	enterViewers []dbViewerOp
	exitViewers  []dbViewerOp
}

func (b *writeBatch) empty() bool {
	return len(b.users) == 0 &&
		len(b.icons) == 0 &&
		len(b.slotDecs) == 0 &&
		len(b.livestreams) == 0 &&
		len(b.ngWords) == 0 &&
		len(b.livecomments) == 0 &&
		len(b.deleteLCIDs) == 0 &&
		len(b.reactions) == 0 &&
		len(b.reports) == 0 &&
		len(b.enterViewers) == 0 &&
		len(b.exitViewers) == 0
}

var (
	wqMu        sync.Mutex
	wqPending   writeBatch
	wqFlushLock sync.Mutex
	wqStopCh    = make(chan struct{})
	wqDoneCh    = make(chan struct{})
)

func startDBFlusher() {
	go func() {
		defer close(wqDoneCh)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				flushDBQueueSync()
			case <-wqStopCh:
				flushDBQueueSync()
				return
			}
		}
	}()
}

func stopDBFlusher() {
	close(wqStopCh)
	<-wqDoneCh
}

func enqueueUser(u dbUserInsert) {
	wqMu.Lock()
	wqPending.users = append(wqPending.users, u)
	wqMu.Unlock()
}

func enqueueIcon(ic dbIconUpsert) {
	wqMu.Lock()
	wqPending.icons = append(wqPending.icons, ic)
	wqMu.Unlock()
}

func enqueueReservation(sd *dbSlotDec, ls dbLivestreamInsert) {
	wqMu.Lock()
	if sd != nil {
		wqPending.slotDecs = append(wqPending.slotDecs, *sd)
	}
	wqPending.livestreams = append(wqPending.livestreams, ls)
	wqMu.Unlock()
}

func enqueueNGWord(nw NGWord) {
	wqMu.Lock()
	wqPending.ngWords = append(wqPending.ngWords, nw)
	wqMu.Unlock()
}

func enqueueLivecomment(lc LivecommentModel) {
	wqMu.Lock()
	wqPending.livecomments = append(wqPending.livecomments, lc)
	wqMu.Unlock()
}

func enqueueDeleteLivecomments(ids []int64) {
	wqMu.Lock()
	wqPending.deleteLCIDs = append(wqPending.deleteLCIDs, ids...)
	wqMu.Unlock()
}

func enqueueReaction(r ReactionModel) {
	wqMu.Lock()
	wqPending.reactions = append(wqPending.reactions, r)
	wqMu.Unlock()
}

func enqueueReport(rep LivecommentReportModel) {
	wqMu.Lock()
	wqPending.reports = append(wqPending.reports, rep)
	wqMu.Unlock()
}

func enqueueEnterViewer(v dbViewerOp) {
	wqMu.Lock()
	wqPending.enterViewers = append(wqPending.enterViewers, v)
	wqMu.Unlock()
}

func enqueueExitViewer(v dbViewerOp) {
	wqMu.Lock()
	wqPending.exitViewers = append(wqPending.exitViewers, v)
	wqMu.Unlock()
}

func flushDBQueueSync() {
	wqFlushLock.Lock()
	defer wqFlushLock.Unlock()

	wqMu.Lock()
	if wqPending.empty() {
		wqMu.Unlock()
		return
	}
	b := wqPending
	wqPending = writeBatch{}
	wqMu.Unlock()

	ctx := context.Background()
	tx, err := dbConn.BeginTxx(ctx, nil)
	if err != nil {
		log.Printf("flusher begin tx error: %v", err)
		return
	}
	defer tx.Rollback()

	// 1. Users + Themes
	if len(b.users) > 0 {
		for off := 0; off < len(b.users); off += 200 {
			end := off + 200
			if end > len(b.users) {
				end = len(b.users)
			}
			chunk := b.users[off:end]
			var sbU, sbT strings.Builder
			sbU.WriteString("INSERT IGNORE INTO users (id, name, display_name, description, password) VALUES ")
			sbT.WriteString("INSERT IGNORE INTO themes (id, user_id, dark_mode) VALUES ")
			argsU := make([]interface{}, 0, len(chunk)*5)
			argsT := make([]interface{}, 0, len(chunk)*3)
			for i, u := range chunk {
				if i > 0 {
					sbU.WriteByte(',')
					sbT.WriteByte(',')
				}
				sbU.WriteString("(?,?,?,?,?)")
				argsU = append(argsU, u.ID, u.Name, u.DisplayName, u.Description, u.HashedPassword)
				sbT.WriteString("(?,?,?)")
				argsT = append(argsT, u.ThemeID, u.ID, u.DarkMode)
			}
			if _, err := tx.ExecContext(ctx, sbU.String(), argsU...); err != nil {
				log.Printf("flusher insert users error: %v", err)
			}
			if _, err := tx.ExecContext(ctx, sbT.String(), argsT...); err != nil {
				log.Printf("flusher insert themes error: %v", err)
			}
		}
	}

	// 2. Icons (dedupe per userID keeping latest, write image to disk + upsert hash in MySQL)
	if len(b.icons) > 0 {
		dedup := make(map[int64]dbIconUpsert, len(b.icons))
		for _, ic := range b.icons {
			dedup[ic.UserID] = ic
		}
		list := make([]dbIconUpsert, 0, len(dedup))
		for _, ic := range dedup {
			if len(ic.Image) > 0 {
				if err := os.WriteFile(iconFilePath(ic.UserID), ic.Image, 0644); err != nil {
					log.Printf("flusher write icon file error: %v", err)
				}
			}
			list = append(list, ic)
		}
		for off := 0; off < len(list); off += 200 {
			end := off + 200
			if end > len(list) {
				end = len(list)
			}
			chunk := list[off:end]
			var sb strings.Builder
			sb.WriteString("INSERT INTO icons (id, user_id, image, hash) VALUES ")
			args := make([]interface{}, 0, len(chunk)*3)
			for i, ic := range chunk {
				if i > 0 {
					sb.WriteByte(',')
				}
				sb.WriteString("(?,?,'',?)")
				args = append(args, ic.ID, ic.UserID, ic.Hash)
			}
			sb.WriteString(" ON DUPLICATE KEY UPDATE id = VALUES(id), hash = VALUES(hash)")
			if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
				log.Printf("flusher upsert icons error: %v", err)
			}
		}
	}

	// 3. Reservation slot decrements
	for _, sd := range b.slotDecs {
		if _, err := tx.ExecContext(ctx,
			"UPDATE reservation_slots SET slot = slot - 1 WHERE start_at >= ? AND start_at <= ? AND end_at <= ?",
			sd.StartAt, sd.EndAt, sd.EndAt,
		); err != nil {
			log.Printf("flusher update slots error: %v", err)
		}
	}

	// 4. Livestreams + Tags
	if len(b.livestreams) > 0 {
		for off := 0; off < len(b.livestreams); off += 200 {
			end := off + 200
			if end > len(b.livestreams) {
				end = len(b.livestreams)
			}
			chunk := b.livestreams[off:end]
			var sb strings.Builder
			sb.WriteString("INSERT IGNORE INTO livestreams (id, user_id, title, description, playlist_url, thumbnail_url, start_at, end_at) VALUES ")
			args := make([]interface{}, 0, len(chunk)*8)
			var tagPairs [][2]int64
			for i, ls := range chunk {
				if i > 0 {
					sb.WriteByte(',')
				}
				sb.WriteString("(?,?,?,?,?,?,?,?)")
				m := ls.Model
				args = append(args, m.ID, m.UserID, m.Title, m.Description, m.PlaylistUrl, m.ThumbnailUrl, m.StartAt, m.EndAt)
				for _, tid := range ls.Tags {
					tagPairs = append(tagPairs, [2]int64{m.ID, tid})
				}
			}
			if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
				log.Printf("flusher insert livestreams error: %v", err)
			}
			if len(tagPairs) > 0 {
				var sbT strings.Builder
				sbT.WriteString("INSERT INTO livestream_tags (livestream_id, tag_id) VALUES ")
				argsT := make([]interface{}, 0, len(tagPairs)*2)
				for i, tp := range tagPairs {
					if i > 0 {
						sbT.WriteByte(',')
					}
					sbT.WriteString("(?,?)")
					argsT = append(argsT, tp[0], tp[1])
				}
				if _, err := tx.ExecContext(ctx, sbT.String(), argsT...); err != nil {
					log.Printf("flusher insert livestream_tags error: %v", err)
				}
			}
		}
	}

	// 5. NG Words
	if len(b.ngWords) > 0 {
		for off := 0; off < len(b.ngWords); off += 200 {
			end := off + 200
			if end > len(b.ngWords) {
				end = len(b.ngWords)
			}
			chunk := b.ngWords[off:end]
			var sb strings.Builder
			sb.WriteString("INSERT IGNORE INTO ng_words (id, user_id, livestream_id, word, created_at) VALUES ")
			args := make([]interface{}, 0, len(chunk)*5)
			for i, w := range chunk {
				if i > 0 {
					sb.WriteByte(',')
				}
				sb.WriteString("(?,?,?,?,?)")
				args = append(args, w.ID, w.UserID, w.LivestreamID, w.Word, w.CreatedAt)
			}
			if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
				log.Printf("flusher insert ng_words error: %v", err)
			}
		}
	}

	// 6. Livecomments INSERT (before DELETE!)
	if len(b.livecomments) > 0 {
		for off := 0; off < len(b.livecomments); off += 200 {
			end := off + 200
			if end > len(b.livecomments) {
				end = len(b.livecomments)
			}
			chunk := b.livecomments[off:end]
			var sb strings.Builder
			sb.WriteString("INSERT IGNORE INTO livecomments (id, user_id, livestream_id, comment, tip, created_at) VALUES ")
			args := make([]interface{}, 0, len(chunk)*6)
			for i, lc := range chunk {
				if i > 0 {
					sb.WriteByte(',')
				}
				sb.WriteString("(?,?,?,?,?,?)")
				args = append(args, lc.ID, lc.UserID, lc.LivestreamID, lc.Comment, lc.Tip, lc.CreatedAt)
			}
			if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
				log.Printf("flusher insert livecomments error: %v", err)
			}
		}
	}

	// 7. Livecomments DELETE (after INSERT!)
	if len(b.deleteLCIDs) > 0 {
		for off := 0; off < len(b.deleteLCIDs); off += 500 {
			end := off + 500
			if end > len(b.deleteLCIDs) {
				end = len(b.deleteLCIDs)
			}
			q, args, err := sqlx.In("DELETE FROM livecomments WHERE id IN (?)", b.deleteLCIDs[off:end])
			if err == nil {
				if _, err := tx.ExecContext(ctx, q, args...); err != nil {
					log.Printf("flusher delete livecomments error: %v", err)
				}
			}
		}
	}

	// 8. Reactions
	if len(b.reactions) > 0 {
		for off := 0; off < len(b.reactions); off += 200 {
			end := off + 200
			if end > len(b.reactions) {
				end = len(b.reactions)
			}
			chunk := b.reactions[off:end]
			var sb strings.Builder
			sb.WriteString("INSERT IGNORE INTO reactions (id, user_id, livestream_id, emoji_name, created_at) VALUES ")
			args := make([]interface{}, 0, len(chunk)*5)
			for i, r := range chunk {
				if i > 0 {
					sb.WriteByte(',')
				}
				sb.WriteString("(?,?,?,?,?)")
				args = append(args, r.ID, r.UserID, r.LivestreamID, r.EmojiName, r.CreatedAt)
			}
			if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
				log.Printf("flusher insert reactions error: %v", err)
			}
		}
	}

	// 9. Reports
	if len(b.reports) > 0 {
		for off := 0; off < len(b.reports); off += 200 {
			end := off + 200
			if end > len(b.reports) {
				end = len(b.reports)
			}
			chunk := b.reports[off:end]
			var sb strings.Builder
			sb.WriteString("INSERT IGNORE INTO livecomment_reports (id, user_id, livestream_id, livecomment_id, created_at) VALUES ")
			args := make([]interface{}, 0, len(chunk)*5)
			for i, rep := range chunk {
				if i > 0 {
					sb.WriteByte(',')
				}
				sb.WriteString("(?,?,?,?,?)")
				args = append(args, rep.ID, rep.UserID, rep.LivestreamID, rep.LivecommentID, rep.CreatedAt)
			}
			if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
				log.Printf("flusher insert reports error: %v", err)
			}
		}
	}

	// 10. Viewers enter (before exit!)
	if len(b.enterViewers) > 0 {
		for off := 0; off < len(b.enterViewers); off += 200 {
			end := off + 200
			if end > len(b.enterViewers) {
				end = len(b.enterViewers)
			}
			chunk := b.enterViewers[off:end]
			var sb strings.Builder
			sb.WriteString("INSERT INTO livestream_viewers_history (user_id, livestream_id, created_at) VALUES ")
			args := make([]interface{}, 0, len(chunk)*3)
			for i, v := range chunk {
				if i > 0 {
					sb.WriteByte(',')
				}
				sb.WriteString("(?,?,?)")
				args = append(args, v.UserID, v.LivestreamID, v.CreatedAt)
			}
			if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
				log.Printf("flusher insert viewers error: %v", err)
			}
		}
	}

	// 11. Viewers exit
	for _, v := range b.exitViewers {
		if _, err := tx.ExecContext(ctx,
			"DELETE FROM livestream_viewers_history WHERE user_id = ? AND livestream_id = ?",
			v.UserID, v.LivestreamID,
		); err != nil {
			log.Printf("flusher delete viewers error: %v", err)
		}
	}

	if err := tx.Commit(); err != nil {
		log.Printf("flusher commit error: %v", err)
	}
}
