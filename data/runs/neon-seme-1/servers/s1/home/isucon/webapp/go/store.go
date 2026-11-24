package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	termStartAtUnix = 1700874000 // 2023-11-25 01:00:00 UTC
	termEndAtUnix   = 1732496400 // 2024-11-25 01:00:00 UTC

	walOpInit        byte = 1
	walOpRegister    byte = 2
	walOpSetIcon     byte = 3
	walOpReserve     byte = 4
	walOpEnter       byte = 5
	walOpExit        byte = 6
	walOpLivecomment byte = 7
	walOpReaction    byte = 8
	walOpReport      byte = 9
	walOpModerate    byte = 10
	walOpIconBlob    byte = 11
	walOpSetIconRef  byte = 12
)

var walMagic = [5]byte{'I', 'S', 'U', 'W', '1'}

type userEntry struct {
	model       UserModel
	theme       Theme
	iconHash    string
	iconBytes   []byte // nil means fallback NoImage.jpg
	cachedJSON  []byte // pre-marshaled User JSON
	livestreams []*livestreamEntry

	viewersCount      int64
	totalReactions    int64
	totalLivecomments int64
	totalTip          int64
	score             int64 // totalReactions + totalTip
	emojiCounts       map[string]int64
	favEmoji          string
	favEmojiCount     int64
}

type livestreamEntry struct {
	model          LivestreamModel
	tags           []Tag
	tagsJSON       []byte // pre-marshaled []Tag JSON
	cachedJSON     []byte // pre-marshaled Livestream JSON (invalidated when owner icon changes)
	viewersByUser  map[int64]int64
	viewersCount   int64
	comments       []*LivecommentModel
	reactions      []*ReactionModel
	reports        []*LivecommentReportModel
	ngWords        []*NGWord
	totalReactions int64
	totalTip       int64
	totalReports   int64
	maxTip         int64
	score          int64 // totalReactions + totalTip
}

type walWriter struct {
	path string
	ch   chan []byte
	done chan struct{}
	mu   sync.Mutex
	f    *os.File
	bw   *bufio.Writer
}

func newWalWriter(path string) (*walWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	bw := bufio.NewWriterSize(f, 256*1024)
	if st.Size() == 0 {
		if _, err := bw.Write(walMagic[:]); err != nil {
			f.Close()
			return nil, err
		}
		_ = bw.Flush()
	}
	w := &walWriter{
		path: path,
		ch:   make(chan []byte, 32768),
		done: make(chan struct{}),
		f:    f,
		bw:   bw,
	}
	go w.loop()
	return w, nil
}

func (w *walWriter) loop() {
	defer close(w.done)
	flushTicker := time.NewTicker(50 * time.Millisecond)
	defer flushTicker.Stop()
	syncCount := 0
	for {
		select {
		case rec, ok := <-w.ch:
			if !ok {
				w.mu.Lock()
				_ = w.bw.Flush()
				_ = w.f.Sync()
				_ = w.f.Close()
				w.mu.Unlock()
				return
			}
			w.mu.Lock()
			_, _ = w.bw.Write(rec)
			for len(w.ch) > 0 {
				next, ok2 := <-w.ch
				if !ok2 {
					_ = w.bw.Flush()
					_ = w.f.Sync()
					_ = w.f.Close()
					w.mu.Unlock()
					return
				}
				_, _ = w.bw.Write(next)
			}
			w.mu.Unlock()
		case <-flushTicker.C:
			w.mu.Lock()
			if w.bw.Buffered() > 0 {
				_ = w.bw.Flush()
				syncCount++
				if syncCount >= 10 {
					_ = w.f.Sync()
					syncCount = 0
				}
			}
			w.mu.Unlock()
		}
	}
}

func (w *walWriter) appendRecord(op byte, payload []byte) {
	if w == nil {
		return
	}
	buf := make([]byte, 5+len(payload))
	buf[0] = op
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(payload)))
	copy(buf[5:], payload)
	w.ch <- buf
}

func (w *walWriter) resetWithInit(initTS int64) error {
	if w == nil {
		return nil
	}
	for len(w.ch) > 0 {
		time.Sleep(1 * time.Millisecond)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.bw.Flush()
	if err := w.f.Truncate(0); err != nil {
		return err
	}
	if _, err := w.f.Seek(0, 0); err != nil {
		return err
	}
	w.bw.Reset(w.f)
	if _, err := w.bw.Write(walMagic[:]); err != nil {
		return err
	}
	var hdr [5 + 8]byte
	hdr[0] = walOpInit
	binary.BigEndian.PutUint32(hdr[1:5], 8)
	binary.BigEndian.PutUint64(hdr[5:13], uint64(initTS))
	if _, err := w.bw.Write(hdr[:]); err != nil {
		return err
	}
	if err := w.bw.Flush(); err != nil {
		return err
	}
	return w.f.Sync()
}

func (w *walWriter) Close() {
	if w == nil {
		return
	}
	close(w.ch)
	<-w.done
}

type Store struct {
	mu sync.RWMutex

	seed             *SeedData
	fallbackIcon     []byte
	fallbackIconHash string
	tagsJSON         []byte

	users            []*userEntry // 1-indexed by user ID
	userByName       map[string]*userEntry
	iconBlobs        map[string][]byte // hashHex -> deduplicated image bytes
	livestreams      []*livestreamEntry // 1-indexed by livestream ID
	slots            []ReservationSlotModel
	tags             []Tag
	tagByID          map[int64]Tag
	tagByName        map[string]int64
	tagToLivestreams map[int64][]*livestreamEntry
	livecommentsByID map[int64]*LivecommentModel
	deletedComments  map[int64]*LivecommentModel

	nextUserID        int64
	nextThemeID       int64
	nextIconID        int64
	nextLivestreamID  int64
	nextLivecommentID int64
	nextReactionID    int64
	nextReportID      int64
	nextNGWordID      int64

	totalPaymentTip int64

	wal           *walWriter
	onDNSReset    func(names []string)
	onDNSAdd      func(name string)
	onIconUpdated func(username, hash string, img []byte)
	onResetAll    func()
}

func LoadSeed(seedPath string) (*SeedData, error) {
	f, err := os.Open(seedPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var seed SeedData
	if err := gob.NewDecoder(bufio.NewReader(f)).Decode(&seed); err != nil {
		return nil, err
	}
	return &seed, nil
}

func LoadFallbackIcon(imgPath string) ([]byte, string, error) {
	b, err := os.ReadFile(imgPath)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(b)
	return b, hex.EncodeToString(sum[:]), nil
}

func BuildTagsJSON(tags []TagModel) []byte {
	out := make([]*Tag, len(tags))
	for i := range tags {
		out[i] = &Tag{ID: tags[i].ID, Name: tags[i].Name}
	}
	b, _ := json.Marshal(&TagsResponse{Tags: out})
	return b
}

func NewStore(seed *SeedData, fallbackIcon []byte, fallbackHash string, walPath string) (*Store, error) {
	s := &Store{
		seed:             seed,
		fallbackIcon:     fallbackIcon,
		fallbackIconHash: fallbackHash,
		tagsJSON:         BuildTagsJSON(seed.Tags),
	}

	s.loadFromSeedLocked(time.Now().Unix())

	if walPath != "" {
		if err := s.replayWAL(walPath); err != nil {
			log.Printf("WARN: replayWAL(%s) failed (starting fresh from seed): %v", walPath, err)
			s.loadFromSeedLocked(time.Now().Unix())
			_ = os.Remove(walPath)
		}
		w, err := newWalWriter(walPath)
		if err != nil {
			return nil, err
		}
		s.wal = w
	}
	return s, nil
}

func (s *Store) Close() {
	if s.wal != nil {
		s.wal.Close()
	}
}

func (s *Store) AllDNSNames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := make(map[string]struct{}, len(s.seed.ZoneSubdomains)+len(s.users))
	names := make([]string, 0, len(s.seed.ZoneSubdomains)+len(s.users))
	for _, z := range s.seed.ZoneSubdomains {
		lz := strings.ToLower(z)
		if _, ok := seen[lz]; !ok {
			seen[lz] = struct{}{}
			names = append(names, lz)
		}
	}
	for _, u := range s.users {
		if u == nil {
			continue
		}
		ln := strings.ToLower(u.model.Name)
		if _, ok := seen[ln]; !ok {
			seen[ln] = struct{}{}
			names = append(names, ln)
		}
	}
	return names
}

// --- Fast JSON Builders (Zero-reflection for hot paths) ---

func appendJSONString(b []byte, s string) []byte {
	b = append(b, '"')
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x20 && c != '"' && c != '\\' && c != '<' && c != '>' && c != '&' {
			continue
		}
		if start < i {
			b = append(b, s[start:i]...)
		}
		switch c {
		case '"':
			b = append(b, '\\', '"')
		case '\\':
			b = append(b, '\\', '\\')
		case '\b':
			b = append(b, '\\', 'b')
		case '\f':
			b = append(b, '\\', 'f')
		case '\n':
			b = append(b, '\\', 'n')
		case '\r':
			b = append(b, '\\', 'r')
		case '\t':
			b = append(b, '\\', 't')
		default:
			// Control chars or <, >, & (match encoding/json escaping)
			const hexDigits = "0123456789abcdef"
			b = append(b, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xF])
		}
		start = i + 1
	}
	if start < len(s) {
		b = append(b, s[start:]...)
	}
	b = append(b, '"')
	return b
}

func buildUserJSON(u *userEntry) []byte {
	b := make([]byte, 0, 256)
	b = append(b, `{"id":`...)
	b = strconv.AppendInt(b, u.model.ID, 10)
	b = append(b, `,"name":`...)
	b = appendJSONString(b, u.model.Name)
	if u.model.DisplayName != "" {
		b = append(b, `,"display_name":`...)
		b = appendJSONString(b, u.model.DisplayName)
	}
	if u.model.Description != "" {
		b = append(b, `,"description":`...)
		b = appendJSONString(b, u.model.Description)
	}
	if u.theme.ID != 0 || u.theme.DarkMode {
		b = append(b, `,"theme":{"id":`...)
		b = strconv.AppendInt(b, u.theme.ID, 10)
		if u.theme.DarkMode {
			b = append(b, `,"dark_mode":true}`...)
		} else {
			b = append(b, `,"dark_mode":false}`...)
		}
	}
	if u.iconHash != "" {
		b = append(b, `,"icon_hash":"`...)
		b = append(b, u.iconHash...)
		b = append(b, '"')
	}
	b = append(b, '}')
	return b
}

func buildTagsSliceJSON(tags []Tag) []byte {
	if len(tags) == 0 {
		return []byte("[]")
	}
	b := make([]byte, 0, len(tags)*32)
	b = append(b, '[')
	for i, tg := range tags {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, `{"id":`...)
		b = strconv.AppendInt(b, tg.ID, 10)
		b = append(b, `,"name":`...)
		b = appendJSONString(b, tg.Name)
		b = append(b, '}')
	}
	b = append(b, ']')
	return b
}

func buildLivestreamJSON(le *livestreamEntry, ownerJSON []byte) []byte {
	tagsJSON := le.tagsJSON
	if len(tagsJSON) == 0 {
		tagsJSON = []byte("[]")
	}
	if len(ownerJSON) == 0 {
		ownerJSON = []byte(`{"id":0,"name":"","theme":{"id":0,"dark_mode":false}}`)
	}
	b := make([]byte, 0, len(ownerJSON)+len(tagsJSON)+len(le.model.Title)+len(le.model.Description)+len(le.model.PlaylistUrl)+len(le.model.ThumbnailUrl)+160)
	b = append(b, `{"id":`...)
	b = strconv.AppendInt(b, le.model.ID, 10)
	b = append(b, `,"owner":`...)
	b = append(b, ownerJSON...)
	b = append(b, `,"title":`...)
	b = appendJSONString(b, le.model.Title)
	b = append(b, `,"description":`...)
	b = appendJSONString(b, le.model.Description)
	b = append(b, `,"playlist_url":`...)
	b = appendJSONString(b, le.model.PlaylistUrl)
	b = append(b, `,"thumbnail_url":`...)
	b = appendJSONString(b, le.model.ThumbnailUrl)
	b = append(b, `,"tags":`...)
	b = append(b, tagsJSON...)
	b = append(b, `,"start_at":`...)
	b = strconv.AppendInt(b, le.model.StartAt, 10)
	b = append(b, `,"end_at":`...)
	b = strconv.AppendInt(b, le.model.EndAt, 10)
	b = append(b, '}')
	return b
}

func (s *Store) getUserJSON(uid int64) []byte {
	if uid > 0 && int(uid) < len(s.users) && s.users[uid] != nil {
		return s.users[uid].cachedJSON
	}
	return []byte(`{"id":0,"name":"","theme":{"id":0,"dark_mode":false}}`)
}

func (s *Store) getLivestreamJSON(le *livestreamEntry) []byte {
	if le.cachedJSON != nil {
		return le.cachedJSON
	}
	return buildLivestreamJSON(le, s.getUserJSON(le.model.UserID))
}

func (s *Store) appendLivecommentJSON(b []byte, c *LivecommentModel, lsJSON []byte) []byte {
	if lsJSON == nil {
		if c.LivestreamID > 0 && int(c.LivestreamID) < len(s.livestreams) && s.livestreams[c.LivestreamID] != nil {
			lsJSON = s.getLivestreamJSON(s.livestreams[c.LivestreamID])
		} else {
			lsJSON = []byte(`{}`)
		}
	}
	uJSON := s.getUserJSON(c.UserID)
	b = append(b, `{"id":`...)
	b = strconv.AppendInt(b, c.ID, 10)
	b = append(b, `,"user":`...)
	b = append(b, uJSON...)
	b = append(b, `,"livestream":`...)
	b = append(b, lsJSON...)
	b = append(b, `,"comment":`...)
	b = appendJSONString(b, c.Comment)
	b = append(b, `,"tip":`...)
	b = strconv.AppendInt(b, c.Tip, 10)
	b = append(b, `,"created_at":`...)
	b = strconv.AppendInt(b, c.CreatedAt, 10)
	b = append(b, '}')
	return b
}

func (s *Store) appendReactionJSON(b []byte, r *ReactionModel, lsJSON []byte) []byte {
	uJSON := s.getUserJSON(r.UserID)
	b = append(b, `{"id":`...)
	b = strconv.AppendInt(b, r.ID, 10)
	b = append(b, `,"emoji_name":`...)
	b = appendJSONString(b, r.EmojiName)
	b = append(b, `,"user":`...)
	b = append(b, uJSON...)
	b = append(b, `,"livestream":`...)
	b = append(b, lsJSON...)
	b = append(b, `,"created_at":`...)
	b = strconv.AppendInt(b, r.CreatedAt, 10)
	b = append(b, '}')
	return b
}

func (s *Store) loadFromSeedLocked(initTS int64) {
	seed := s.seed

	s.iconBlobs = make(map[string][]byte, 256)
	s.iconBlobs[s.fallbackIconHash] = s.fallbackIcon

	maxUID := int64(0)
	for i := range seed.Users {
		if seed.Users[i].ID > maxUID {
			maxUID = seed.Users[i].ID
		}
	}
	s.users = make([]*userEntry, maxUID+1, maxUID+4096)
	s.userByName = make(map[string]*userEntry, len(seed.Users)+4096)
	for i := range seed.Users {
		u := seed.Users[i]
		ue := &userEntry{
			model:       u,
			iconHash:    s.fallbackIconHash,
			emojiCounts: make(map[string]int64, 8),
		}
		s.users[u.ID] = ue
		s.userByName[u.Name] = ue
	}
	s.nextUserID = maxUID + 1

	maxTID := int64(0)
	for i := range seed.Themes {
		t := seed.Themes[i]
		if t.ID > maxTID {
			maxTID = t.ID
		}
		if t.UserID > 0 && int(t.UserID) < len(s.users) && s.users[t.UserID] != nil {
			s.users[t.UserID].theme = Theme{ID: t.ID, DarkMode: t.DarkMode}
		}
	}
	s.nextThemeID = maxTID + 1
	s.nextIconID = 1

	for i := 1; i < len(s.users); i++ {
		if s.users[i] != nil {
			s.users[i].cachedJSON = buildUserJSON(s.users[i])
		}
	}

	s.tags = make([]Tag, len(seed.Tags))
	s.tagByID = make(map[int64]Tag, len(seed.Tags))
	s.tagByName = make(map[string]int64, len(seed.Tags))
	s.tagToLivestreams = make(map[int64][]*livestreamEntry, len(seed.Tags))
	for i, t := range seed.Tags {
		tg := Tag{ID: t.ID, Name: t.Name}
		s.tags[i] = tg
		s.tagByID[t.ID] = tg
		s.tagByName[t.Name] = t.ID
	}

	maxLID := int64(0)
	for i := range seed.Livestreams {
		if seed.Livestreams[i].ID > maxLID {
			maxLID = seed.Livestreams[i].ID
		}
	}
	s.livestreams = make([]*livestreamEntry, maxLID+1, maxLID+4096)
	for i := range seed.Livestreams {
		l := seed.Livestreams[i]
		le := &livestreamEntry{
			model:         l,
			tags:          make([]Tag, 0, 4),
			viewersByUser: make(map[int64]int64, 4),
		}
		s.livestreams[l.ID] = le
		if l.UserID > 0 && int(l.UserID) < len(s.users) && s.users[l.UserID] != nil {
			s.users[l.UserID].livestreams = append(s.users[l.UserID].livestreams, le)
		}
	}
	s.nextLivestreamID = maxLID + 1

	ltags := make([]LivestreamTagModel, len(seed.LivestreamTags))
	copy(ltags, seed.LivestreamTags)
	sort.SliceStable(ltags, func(i, j int) bool {
		if ltags[i].LivestreamID != ltags[j].LivestreamID {
			return ltags[i].LivestreamID < ltags[j].LivestreamID
		}
		return ltags[i].ID < ltags[j].ID
	})
	for _, lt := range ltags {
		if lt.LivestreamID > 0 && int(lt.LivestreamID) < len(s.livestreams) {
			le := s.livestreams[lt.LivestreamID]
			if le != nil {
				if tg, ok := s.tagByID[lt.TagID]; ok {
					le.tags = append(le.tags, tg)
					s.tagToLivestreams[lt.TagID] = append(s.tagToLivestreams[lt.TagID], le)
				}
			}
		}
	}
	for i := 1; i < len(s.livestreams); i++ {
		le := s.livestreams[i]
		if le != nil {
			le.tagsJSON = buildTagsSliceJSON(le.tags)
			le.cachedJSON = buildLivestreamJSON(le, s.getUserJSON(le.model.UserID))
		}
	}

	s.slots = make([]ReservationSlotModel, len(seed.ReservationSlots))
	copy(s.slots, seed.ReservationSlots)
	sort.Slice(s.slots, func(i, j int) bool {
		return s.slots[i].StartAt < s.slots[j].StartAt
	})

	s.livecommentsByID = make(map[int64]*LivecommentModel, len(seed.Livecomments)+4096)
	s.deletedComments = make(map[int64]*LivecommentModel, 256)
	s.totalPaymentTip = 0
	maxCID := int64(0)
	for i := range seed.Livecomments {
		c := seed.Livecomments[i]
		c.CreatedAt = initTS
		if c.ID > maxCID {
			maxCID = c.ID
		}
		cp := &LivecommentModel{
			ID:           c.ID,
			UserID:       c.UserID,
			LivestreamID: c.LivestreamID,
			Comment:      c.Comment,
			Tip:          c.Tip,
			CreatedAt:    c.CreatedAt,
		}
		s.livecommentsByID[cp.ID] = cp
		s.totalPaymentTip += cp.Tip
		if cp.LivestreamID > 0 && int(cp.LivestreamID) < len(s.livestreams) && s.livestreams[cp.LivestreamID] != nil {
			le := s.livestreams[cp.LivestreamID]
			le.comments = append(le.comments, cp)
			le.totalTip += cp.Tip
			le.score += cp.Tip
			if cp.Tip > le.maxTip {
				le.maxTip = cp.Tip
			}
			if le.model.UserID > 0 && int(le.model.UserID) < len(s.users) && s.users[le.model.UserID] != nil {
				owner := s.users[le.model.UserID]
				owner.totalLivecomments++
				owner.totalTip += cp.Tip
				owner.score += cp.Tip
			}
		}
	}
	s.nextLivecommentID = maxCID + 1

	maxRID := int64(0)
	for i := range seed.Reactions {
		r := seed.Reactions[i]
		r.CreatedAt = initTS
		if r.ID > maxRID {
			maxRID = r.ID
		}
		rp := &ReactionModel{
			ID:           r.ID,
			EmojiName:    r.EmojiName,
			UserID:       r.UserID,
			LivestreamID: r.LivestreamID,
			CreatedAt:    r.CreatedAt,
		}
		if rp.LivestreamID > 0 && int(rp.LivestreamID) < len(s.livestreams) && s.livestreams[rp.LivestreamID] != nil {
			le := s.livestreams[rp.LivestreamID]
			le.reactions = append(le.reactions, rp)
			le.totalReactions++
			le.score++
			if le.model.UserID > 0 && int(le.model.UserID) < len(s.users) && s.users[le.model.UserID] != nil {
				owner := s.users[le.model.UserID]
				owner.totalReactions++
				owner.score++
				cnt := owner.emojiCounts[rp.EmojiName] + 1
				owner.emojiCounts[rp.EmojiName] = cnt
				if cnt > owner.favEmojiCount || (cnt == owner.favEmojiCount && rp.EmojiName > owner.favEmoji) {
					owner.favEmojiCount = cnt
					owner.favEmoji = rp.EmojiName
				}
			}
		}
	}
	s.nextReactionID = maxRID + 1

	maxNID := int64(0)
	for i := range seed.NGWords {
		ng := seed.NGWords[i]
		ng.CreatedAt = initTS
		if ng.ID > maxNID {
			maxNID = ng.ID
		}
		ngp := &NGWord{
			ID:           ng.ID,
			UserID:       ng.UserID,
			LivestreamID: ng.LivestreamID,
			Word:         ng.Word,
			CreatedAt:    ng.CreatedAt,
		}
		if ngp.LivestreamID > 0 && int(ngp.LivestreamID) < len(s.livestreams) && s.livestreams[ngp.LivestreamID] != nil {
			s.livestreams[ngp.LivestreamID].ngWords = append(s.livestreams[ngp.LivestreamID].ngWords, ngp)
		}
	}
	s.nextNGWordID = maxNID + 1
	s.nextReportID = 1
}

func (s *Store) Initialize() (int, []byte) {
	now := time.Now().Unix()
	s.mu.Lock()
	s.loadFromSeedLocked(now)
	dnsNames := make([]string, len(s.seed.ZoneSubdomains))
	copy(dnsNames, s.seed.ZoneSubdomains)
	if s.wal != nil {
		if err := s.wal.resetWithInit(now); err != nil {
			log.Printf("WARN: wal resetWithInit failed: %v", err)
		}
	}
	s.mu.Unlock()

	if s.onDNSReset != nil {
		s.onDNSReset(dnsNames)
	}
	if s.onResetAll != nil {
		s.onResetAll()
	}
	return http.StatusOK, []byte(`{"language":"golang"}`)
}

func errJSON(status int, msg string) (int, []byte) {
	b, _ := json.Marshal(&ErrorResponse{Error: msg})
	return status, b
}

func okJSON(status int, v any) (int, []byte) {
	b, _ := json.Marshal(v)
	return status, b
}

// --- Read & Write Store Operations ---

func (s *Store) GetTagsJSON() []byte {
	return s.tagsJSON
}

func (s *Store) GetIcon(username string) (hash string, img []byte, found bool) {
	s.mu.RLock()
	u, ok := s.userByName[username]
	if !ok {
		s.mu.RUnlock()
		return "", nil, false
	}
	hash = u.iconHash
	img = u.iconBytes
	s.mu.RUnlock()
	if img == nil {
		img = s.fallbackIcon
	}
	return hash, img, true
}

func (s *Store) PostIcon(userID int64, hash string, img []byte) (int, []byte) {
	s.mu.Lock()
	if userID <= 0 || int(userID) >= len(s.users) || s.users[userID] == nil {
		s.mu.Unlock()
		return errJSON(http.StatusNotFound, "user not found")
	}
	u := s.users[userID]
	iconID := s.nextIconID
	s.nextIconID++

	blob, knownBlob := s.iconBlobs[hash]
	if !knownBlob {
		blob = img
		s.iconBlobs[hash] = blob
		s.walIconBlob(hash, blob)
	}
	changed := u.iconHash != hash
	u.iconHash = hash
	u.iconBytes = blob
	if changed {
		u.cachedJSON = buildUserJSON(u)
		for _, le := range u.livestreams {
			le.cachedJSON = buildLivestreamJSON(le, u.cachedJSON)
		}
	}
	username := u.model.Name
	s.walSetIconRef(iconID, userID, hash)
	s.mu.Unlock()

	if s.onIconUpdated != nil {
		s.onIconUpdated(username, hash, blob)
	}
	b := make([]byte, 0, 32)
	b = append(b, `{"id":`...)
	b = strconv.AppendInt(b, iconID, 10)
	b = append(b, '}')
	return http.StatusCreated, b
}

func (s *Store) GetUserAuth(username string) (userID int64, hashedPassword string, found bool) {
	s.mu.RLock()
	u, ok := s.userByName[username]
	if !ok {
		s.mu.RUnlock()
		return 0, "", false
	}
	userID = u.model.ID
	hashedPassword = u.model.HashedPassword
	s.mu.RUnlock()
	return userID, hashedPassword, true
}

func (s *Store) Register(name, displayName, description, hashedPassword string, darkMode bool) (int, int64, []byte) {
	if name == "pipe" {
		st, b := errJSON(http.StatusBadRequest, "the username 'pipe' is reserved")
		return st, 0, b
	}
	s.mu.Lock()
	if _, exists := s.userByName[name]; exists {
		s.mu.Unlock()
		st, b := errJSON(http.StatusInternalServerError, "failed to insert user: duplicate user name")
		return st, 0, b
	}
	uid := s.nextUserID
	s.nextUserID++
	tid := s.nextThemeID
	s.nextThemeID++
	ue := &userEntry{
		model: UserModel{
			ID:             uid,
			Name:           name,
			DisplayName:    displayName,
			Description:    description,
			HashedPassword: hashedPassword,
		},
		theme:       Theme{ID: tid, DarkMode: darkMode},
		iconHash:    s.fallbackIconHash,
		emojiCounts: make(map[string]int64, 4),
	}
	ue.cachedJSON = buildUserJSON(ue)
	for int64(len(s.users)) <= uid {
		s.users = append(s.users, nil)
	}
	s.users[uid] = ue
	s.userByName[name] = ue
	respBytes := ue.cachedJSON
	s.walRegister(uid, tid, name, displayName, description, hashedPassword, darkMode)
	s.mu.Unlock()

	if s.onDNSAdd != nil {
		s.onDNSAdd(name)
	}
	return http.StatusCreated, uid, respBytes
}

func (s *Store) GetMe(userID int64) (int, []byte) {
	s.mu.RLock()
	if userID <= 0 || int(userID) >= len(s.users) || s.users[userID] == nil {
		s.mu.RUnlock()
		return errJSON(http.StatusNotFound, "not found user that has the userid in session")
	}
	b := s.users[userID].cachedJSON
	s.mu.RUnlock()
	return http.StatusOK, b
}

func (s *Store) GetUser(username string) (int, []byte) {
	s.mu.RLock()
	ue, ok := s.userByName[username]
	if !ok {
		s.mu.RUnlock()
		return errJSON(http.StatusNotFound, "not found user that has the given username")
	}
	b := ue.cachedJSON
	s.mu.RUnlock()
	return http.StatusOK, b
}

func (s *Store) GetStreamerTheme(username string) (int, []byte) {
	s.mu.RLock()
	ue, ok := s.userByName[username]
	if !ok {
		s.mu.RUnlock()
		return errJSON(http.StatusNotFound, "not found user that has the given username")
	}
	theme := ue.theme
	s.mu.RUnlock()
	b := make([]byte, 0, 48)
	b = append(b, `{"id":`...)
	b = strconv.AppendInt(b, theme.ID, 10)
	if theme.DarkMode {
		b = append(b, `,"dark_mode":true}`...)
	} else {
		b = append(b, `,"dark_mode":false}`...)
	}
	return http.StatusOK, b
}

func (s *Store) ReserveLivestream(userID int64, req *ReserveLivestreamRequest) (int, []byte) {
	if req.StartAt >= termEndAtUnix || req.EndAt <= termStartAtUnix {
		return errJSON(http.StatusBadRequest, "bad reservation time range")
	}

	s.mu.Lock()
	startIdx := sort.Search(len(s.slots), func(i int) bool {
		return s.slots[i].StartAt >= req.StartAt
	})
	var matchedBuf [32]int
	matched := matchedBuf[:0]
	for i := startIdx; i < len(s.slots); i++ {
		if s.slots[i].EndAt <= req.EndAt {
			if s.slots[i].Slot < 1 {
				s.mu.Unlock()
				return errJSON(http.StatusBadRequest, fmt.Sprintf("予約期間 %d ~ %dに対して、予約区間 %d ~ %dが予約できません", termStartAtUnix, termEndAtUnix, req.StartAt, req.EndAt))
			}
			matched = append(matched, i)
		} else if s.slots[i].StartAt >= req.EndAt {
			break
		}
	}
	for _, idx := range matched {
		s.slots[idx].Slot--
	}

	lid := s.nextLivestreamID
	s.nextLivestreamID++
	tags := make([]Tag, 0, len(req.Tags))
	for _, tid := range req.Tags {
		if tg, ok := s.tagByID[tid]; ok {
			tags = append(tags, tg)
		}
	}
	le := &livestreamEntry{
		model: LivestreamModel{
			ID:           lid,
			UserID:       userID,
			Title:        req.Title,
			Description:  req.Description,
			PlaylistUrl:  req.PlaylistUrl,
			ThumbnailUrl: req.ThumbnailUrl,
			StartAt:      req.StartAt,
			EndAt:        req.EndAt,
		},
		tags:          tags,
		tagsJSON:      buildTagsSliceJSON(tags),
		viewersByUser: make(map[int64]int64, 4),
	}
	le.cachedJSON = buildLivestreamJSON(le, s.getUserJSON(userID))
	for int64(len(s.livestreams)) <= lid {
		s.livestreams = append(s.livestreams, nil)
	}
	s.livestreams[lid] = le
	if userID > 0 && int(userID) < len(s.users) && s.users[userID] != nil {
		s.users[userID].livestreams = append(s.users[userID].livestreams, le)
	}
	for _, tid := range req.Tags {
		if _, ok := s.tagByID[tid]; ok {
			s.tagToLivestreams[tid] = append(s.tagToLivestreams[tid], le)
		}
	}
	respBytes := le.cachedJSON
	s.walReserve(lid, userID, req)
	s.mu.Unlock()

	return http.StatusCreated, respBytes
}

func (s *Store) SearchLivestreams(tagName string, limit int) (int, []byte) {
	s.mu.RLock()
	var items [][]byte
	if tagName != "" {
		if tid, ok := s.tagByName[tagName]; ok {
			src := s.tagToLivestreams[tid]
			items = make([][]byte, len(src))
			for i := 0; i < len(src); i++ {
				items[i] = s.getLivestreamJSON(src[len(src)-1-i])
			}
		}
	} else {
		total := len(s.livestreams) - 1
		if total < 0 {
			total = 0
		}
		capN := total
		if limit >= 0 && limit < capN {
			capN = limit
		}
		items = make([][]byte, 0, capN)
		for i := len(s.livestreams) - 1; i >= 1; i-- {
			if s.livestreams[i] != nil {
				items = append(items, s.getLivestreamJSON(s.livestreams[i]))
				if limit >= 0 && len(items) >= limit {
					break
				}
			}
		}
	}
	s.mu.RUnlock()

	if len(items) == 0 {
		return http.StatusOK, []byte("[]")
	}
	totalLen := 2 + len(items) - 1
	for _, it := range items {
		totalLen += len(it)
	}
	b := make([]byte, 0, totalLen)
	b = append(b, '[')
	for i, it := range items {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, it...)
	}
	b = append(b, ']')
	return http.StatusOK, b
}

func joinJSONItems(items [][]byte) []byte {
	if len(items) == 0 {
		return []byte("[]")
	}
	totalLen := 2 + len(items) - 1
	for _, it := range items {
		totalLen += len(it)
	}
	b := make([]byte, 0, totalLen)
	b = append(b, '[')
	for i, it := range items {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, it...)
	}
	b = append(b, ']')
	return b
}

func (s *Store) GetMyLivestreams(userID int64) (int, []byte) {
	s.mu.RLock()
	if userID <= 0 || int(userID) >= len(s.users) || s.users[userID] == nil {
		s.mu.RUnlock()
		return http.StatusOK, []byte("[]")
	}
	src := s.users[userID].livestreams
	items := make([][]byte, len(src))
	for i, le := range src {
		items[i] = s.getLivestreamJSON(le)
	}
	s.mu.RUnlock()
	return http.StatusOK, joinJSONItems(items)
}

func (s *Store) GetUserLivestreams(username string) (int, []byte) {
	s.mu.RLock()
	ue, ok := s.userByName[username]
	if !ok {
		s.mu.RUnlock()
		return errJSON(http.StatusNotFound, "user not found")
	}
	src := ue.livestreams
	items := make([][]byte, len(src))
	for i, le := range src {
		items[i] = s.getLivestreamJSON(le)
	}
	s.mu.RUnlock()
	return http.StatusOK, joinJSONItems(items)
}

func (s *Store) GetLivestream(livestreamID int64) (int, []byte) {
	s.mu.RLock()
	if livestreamID <= 0 || int(livestreamID) >= len(s.livestreams) || s.livestreams[livestreamID] == nil {
		s.mu.RUnlock()
		return errJSON(http.StatusNotFound, "not found livestream that has the given id")
	}
	b := s.getLivestreamJSON(s.livestreams[livestreamID])
	s.mu.RUnlock()
	return http.StatusOK, b
}

func (s *Store) EnterLivestream(userID, livestreamID int64) (int, []byte) {
	now := time.Now().Unix()
	s.mu.Lock()
	if livestreamID > 0 && int(livestreamID) < len(s.livestreams) && s.livestreams[livestreamID] != nil {
		le := s.livestreams[livestreamID]
		le.viewersByUser[userID]++
		le.viewersCount++
		if le.model.UserID > 0 && int(le.model.UserID) < len(s.users) && s.users[le.model.UserID] != nil {
			s.users[le.model.UserID].viewersCount++
		}
	}
	s.walEnter(userID, livestreamID, now)
	s.mu.Unlock()
	return http.StatusOK, nil
}

func (s *Store) ExitLivestream(userID, livestreamID int64) (int, []byte) {
	s.mu.Lock()
	if livestreamID > 0 && int(livestreamID) < len(s.livestreams) && s.livestreams[livestreamID] != nil {
		le := s.livestreams[livestreamID]
		if cnt := le.viewersByUser[userID]; cnt > 0 {
			delete(le.viewersByUser, userID)
			le.viewersCount -= cnt
			if le.model.UserID > 0 && int(le.model.UserID) < len(s.users) && s.users[le.model.UserID] != nil {
				s.users[le.model.UserID].viewersCount -= cnt
			}
		}
	}
	s.walExit(userID, livestreamID)
	s.mu.Unlock()
	return http.StatusOK, nil
}

func (s *Store) GetLivecomments(livestreamID int64, limit int) (int, []byte) {
	s.mu.RLock()
	if livestreamID <= 0 || int(livestreamID) >= len(s.livestreams) || s.livestreams[livestreamID] == nil {
		s.mu.RUnlock()
		return http.StatusOK, []byte("[]")
	}
	le := s.livestreams[livestreamID]
	n := len(le.comments)
	if limit >= 0 && limit < n {
		n = limit
	}
	if n == 0 {
		s.mu.RUnlock()
		return http.StatusOK, []byte("[]")
	}
	lsJSON := s.getLivestreamJSON(le)
	b := make([]byte, 0, n*(len(lsJSON)+512)+2)
	b = append(b, '[')
	for i := 0; i < n; i++ {
		if i > 0 {
			b = append(b, ',')
		}
		b = s.appendLivecommentJSON(b, le.comments[len(le.comments)-1-i], lsJSON)
	}
	b = append(b, ']')
	s.mu.RUnlock()
	return http.StatusOK, b
}

func sqlLikeContains(text, pattern string) bool {
	if !strings.ContainsAny(pattern, `%_\`) {
		return strings.Contains(text, pattern)
	}
	return sqlLikeMatch(text, "%"+pattern+"%")
}

func sqlLikeMatch(s, p string) bool {
	sr := []rune(s)
	pr := []rune(p)
	type tok struct {
		kind byte
		ch   rune
	}
	toks := make([]tok, 0, len(pr))
	for i := 0; i < len(pr); i++ {
		switch pr[i] {
		case '\\':
			if i+1 < len(pr) {
				i++
				toks = append(toks, tok{0, pr[i]})
			} else {
				toks = append(toks, tok{0, '\\'})
			}
		case '%':
			if len(toks) == 0 || toks[len(toks)-1].kind != 2 {
				toks = append(toks, tok{2, 0})
			}
		case '_':
			toks = append(toks, tok{1, 0})
		default:
			toks = append(toks, tok{0, pr[i]})
		}
	}
	si, pi := 0, 0
	starIdx, matchIdx := -1, 0
	for si < len(sr) {
		if pi < len(toks) && (toks[pi].kind == 1 || (toks[pi].kind == 0 && toks[pi].ch == sr[si])) {
			si++
			pi++
		} else if pi < len(toks) && toks[pi].kind == 2 {
			starIdx = pi
			matchIdx = si
			pi++
		} else if starIdx != -1 {
			pi = starIdx + 1
			matchIdx++
			si = matchIdx
		} else {
			return false
		}
	}
	for pi < len(toks) && toks[pi].kind == 2 {
		pi++
	}
	return pi == len(toks)
}

func (s *Store) PostLivecomment(userID, livestreamID int64, comment string, tip int64) (int, []byte) {
	now := time.Now().Unix()
	s.mu.Lock()
	if livestreamID <= 0 || int(livestreamID) >= len(s.livestreams) || s.livestreams[livestreamID] == nil {
		s.mu.Unlock()
		return errJSON(http.StatusNotFound, "livestream not found")
	}
	le := s.livestreams[livestreamID]
	ownerID := le.model.UserID
	for _, ng := range le.ngWords {
		if ng.UserID == ownerID && sqlLikeContains(comment, ng.Word) {
			s.mu.Unlock()
			return errJSON(http.StatusBadRequest, "このコメントがスパム判定されました")
		}
	}

	cid := s.nextLivecommentID
	s.nextLivecommentID++
	cm := &LivecommentModel{
		ID:           cid,
		UserID:       userID,
		LivestreamID: livestreamID,
		Comment:      comment,
		Tip:          tip,
		CreatedAt:    now,
	}
	le.comments = append(le.comments, cm)
	s.livecommentsByID[cid] = cm
	le.totalTip += tip
	le.score += tip
	if tip > le.maxTip {
		le.maxTip = tip
	}
	s.totalPaymentTip += tip
	if ownerID > 0 && int(ownerID) < len(s.users) && s.users[ownerID] != nil {
		owner := s.users[ownerID]
		owner.totalLivecomments++
		owner.totalTip += tip
		owner.score += tip
	}
	lsJSON := s.getLivestreamJSON(le)
	uJSON := s.getUserJSON(userID)
	s.walLivecomment(cm)
	s.mu.Unlock()

	b := make([]byte, 0, len(lsJSON)+len(uJSON)+len(comment)+128)
	b = append(b, `{"id":`...)
	b = strconv.AppendInt(b, cm.ID, 10)
	b = append(b, `,"user":`...)
	b = append(b, uJSON...)
	b = append(b, `,"livestream":`...)
	b = append(b, lsJSON...)
	b = append(b, `,"comment":`...)
	b = appendJSONString(b, cm.Comment)
	b = append(b, `,"tip":`...)
	b = strconv.AppendInt(b, cm.Tip, 10)
	b = append(b, `,"created_at":`...)
	b = strconv.AppendInt(b, cm.CreatedAt, 10)
	b = append(b, '}')
	return http.StatusCreated, b
}

func (s *Store) GetNgwords(userID, livestreamID int64) (int, []byte) {
	s.mu.RLock()
	if livestreamID <= 0 || int(livestreamID) >= len(s.livestreams) || s.livestreams[livestreamID] == nil {
		s.mu.RUnlock()
		return http.StatusOK, []byte("[]")
	}
	le := s.livestreams[livestreamID]
	b := make([]byte, 0, len(le.ngWords)*96+2)
	b = append(b, '[')
	first := true
	for i := len(le.ngWords) - 1; i >= 0; i-- {
		ng := le.ngWords[i]
		if ng.UserID == userID {
			if !first {
				b = append(b, ',')
			}
			first = false
			b = append(b, `{"id":`...)
			b = strconv.AppendInt(b, ng.ID, 10)
			b = append(b, `,"user_id":`...)
			b = strconv.AppendInt(b, ng.UserID, 10)
			b = append(b, `,"livestream_id":`...)
			b = strconv.AppendInt(b, ng.LivestreamID, 10)
			b = append(b, `,"word":`...)
			b = appendJSONString(b, ng.Word)
			b = append(b, `,"created_at":`...)
			b = strconv.AppendInt(b, ng.CreatedAt, 10)
			b = append(b, '}')
		}
	}
	b = append(b, ']')
	s.mu.RUnlock()
	return http.StatusOK, b
}

func (s *Store) Moderate(userID, livestreamID int64, ngWord string) (int, []byte) {
	now := time.Now().Unix()
	s.mu.Lock()
	if livestreamID <= 0 || int(livestreamID) >= len(s.livestreams) || s.livestreams[livestreamID] == nil || s.livestreams[livestreamID].model.UserID != userID {
		s.mu.Unlock()
		return errJSON(http.StatusBadRequest, "A streamer can't moderate livestreams that other streamers own")
	}
	le := s.livestreams[livestreamID]
	wid := s.nextNGWordID
	s.nextNGWordID++
	ng := &NGWord{
		ID:           wid,
		UserID:       userID,
		LivestreamID: livestreamID,
		Word:         ngWord,
		CreatedAt:    now,
	}
	le.ngWords = append(le.ngWords, ng)

	var owner *userEntry
	if le.model.UserID > 0 && int(le.model.UserID) < len(s.users) {
		owner = s.users[le.model.UserID]
	}
	needRecomputeMaxTip := false
	kept := le.comments[:0]
	for _, cm := range le.comments {
		hit := false
		for _, w := range le.ngWords {
			if sqlLikeContains(cm.Comment, w.Word) {
				hit = true
				break
			}
		}
		if hit {
			delete(s.livecommentsByID, cm.ID)
			s.deletedComments[cm.ID] = cm
			le.totalTip -= cm.Tip
			le.score -= cm.Tip
			s.totalPaymentTip -= cm.Tip
			if cm.Tip == le.maxTip && cm.Tip > 0 {
				needRecomputeMaxTip = true
			}
			if owner != nil {
				owner.totalLivecomments--
				owner.totalTip -= cm.Tip
				owner.score -= cm.Tip
			}
		} else {
			kept = append(kept, cm)
		}
	}
	le.comments = kept
	if needRecomputeMaxTip {
		var maxT int64
		for _, cm := range le.comments {
			if cm.Tip > maxT {
				maxT = cm.Tip
			}
		}
		le.maxTip = maxT
	}
	s.walModerate(ng)
	s.mu.Unlock()

	b := make([]byte, 0, 32)
	b = append(b, `{"word_id":`...)
	b = strconv.AppendInt(b, wid, 10)
	b = append(b, '}')
	return http.StatusCreated, b
}

func (s *Store) ReportLivecomment(userID, livestreamID, livecommentID int64) (int, []byte) {
	now := time.Now().Unix()
	s.mu.Lock()
	if livestreamID <= 0 || int(livestreamID) >= len(s.livestreams) || s.livestreams[livestreamID] == nil {
		s.mu.Unlock()
		return errJSON(http.StatusNotFound, "livestream not found")
	}
	cm, ok := s.livecommentsByID[livecommentID]
	if !ok {
		s.mu.Unlock()
		return errJSON(http.StatusNotFound, "livecomment not found")
	}
	le := s.livestreams[livestreamID]
	rid := s.nextReportID
	s.nextReportID++
	rm := &LivecommentReportModel{
		ID:            rid,
		UserID:        userID,
		LivestreamID:  livestreamID,
		LivecommentID: livecommentID,
		CreatedAt:     now,
	}
	le.reports = append(le.reports, rm)
	le.totalReports++

	reporterJSON := s.getUserJSON(userID)
	b := make([]byte, 0, 1024)
	b = append(b, `{"id":`...)
	b = strconv.AppendInt(b, rm.ID, 10)
	b = append(b, `,"reporter":`...)
	b = append(b, reporterJSON...)
	b = append(b, `,"livecomment":`...)
	b = s.appendLivecommentJSON(b, cm, nil)
	b = append(b, `,"created_at":`...)
	b = strconv.AppendInt(b, rm.CreatedAt, 10)
	b = append(b, '}')
	s.walReport(rm)
	s.mu.Unlock()
	return http.StatusCreated, b
}

func (s *Store) GetLivecommentReports(userID, livestreamID int64) (int, []byte) {
	s.mu.RLock()
	if livestreamID <= 0 || int(livestreamID) >= len(s.livestreams) || s.livestreams[livestreamID] == nil {
		s.mu.RUnlock()
		return errJSON(http.StatusInternalServerError, "failed to get livestream: not found")
	}
	le := s.livestreams[livestreamID]
	if le.model.UserID != userID {
		s.mu.RUnlock()
		return errJSON(http.StatusForbidden, "can't get other streamer's livecomment reports")
	}
	if len(le.reports) == 0 {
		s.mu.RUnlock()
		return http.StatusOK, []byte("[]")
	}
	lsJSON := s.getLivestreamJSON(le)
	b := make([]byte, 0, len(le.reports)*1024+2)
	b = append(b, '[')
	first := true
	for _, rm := range le.reports {
		cm := s.livecommentsByID[rm.LivecommentID]
		if cm == nil {
			cm = s.deletedComments[rm.LivecommentID]
		}
		if cm == nil {
			continue
		}
		if !first {
			b = append(b, ',')
		}
		first = false
		b = append(b, `{"id":`...)
		b = strconv.AppendInt(b, rm.ID, 10)
		b = append(b, `,"reporter":`...)
		b = append(b, s.getUserJSON(rm.UserID)...)
		b = append(b, `,"livecomment":`...)
		if cm.LivestreamID == livestreamID {
			b = s.appendLivecommentJSON(b, cm, lsJSON)
		} else {
			b = s.appendLivecommentJSON(b, cm, nil)
		}
		b = append(b, `,"created_at":`...)
		b = strconv.AppendInt(b, rm.CreatedAt, 10)
		b = append(b, '}')
	}
	b = append(b, ']')
	s.mu.RUnlock()
	return http.StatusOK, b
}

func (s *Store) GetReactions(livestreamID int64, limit int) (int, []byte) {
	s.mu.RLock()
	if livestreamID <= 0 || int(livestreamID) >= len(s.livestreams) || s.livestreams[livestreamID] == nil {
		s.mu.RUnlock()
		return http.StatusOK, []byte("[]")
	}
	le := s.livestreams[livestreamID]
	n := len(le.reactions)
	if limit >= 0 && limit < n {
		n = limit
	}
	if n == 0 {
		s.mu.RUnlock()
		return http.StatusOK, []byte("[]")
	}
	lsJSON := s.getLivestreamJSON(le)
	b := make([]byte, 0, n*(len(lsJSON)+448)+2)
	b = append(b, '[')
	for i := 0; i < n; i++ {
		if i > 0 {
			b = append(b, ',')
		}
		b = s.appendReactionJSON(b, le.reactions[len(le.reactions)-1-i], lsJSON)
	}
	b = append(b, ']')
	s.mu.RUnlock()
	return http.StatusOK, b
}

func (s *Store) PostReaction(userID, livestreamID int64, emojiName string) (int, []byte) {
	now := time.Now().Unix()
	s.mu.Lock()
	if livestreamID <= 0 || int(livestreamID) >= len(s.livestreams) || s.livestreams[livestreamID] == nil {
		s.mu.Unlock()
		return errJSON(http.StatusInternalServerError, "failed to fill reaction: livestream not found")
	}
	le := s.livestreams[livestreamID]
	rid := s.nextReactionID
	s.nextReactionID++
	rm := &ReactionModel{
		ID:           rid,
		EmojiName:    emojiName,
		UserID:       userID,
		LivestreamID: livestreamID,
		CreatedAt:    now,
	}
	le.reactions = append(le.reactions, rm)
	le.totalReactions++
	le.score++
	if le.model.UserID > 0 && int(le.model.UserID) < len(s.users) && s.users[le.model.UserID] != nil {
		owner := s.users[le.model.UserID]
		owner.totalReactions++
		owner.score++
		cnt := owner.emojiCounts[emojiName] + 1
		owner.emojiCounts[emojiName] = cnt
		if cnt > owner.favEmojiCount || (cnt == owner.favEmojiCount && emojiName > owner.favEmoji) {
			owner.favEmojiCount = cnt
			owner.favEmoji = emojiName
		}
	}
	lsJSON := s.getLivestreamJSON(le)
	uJSON := s.getUserJSON(userID)
	s.walReaction(rm)
	s.mu.Unlock()

	b := make([]byte, 0, len(lsJSON)+len(uJSON)+len(emojiName)+96)
	b = append(b, `{"id":`...)
	b = strconv.AppendInt(b, rm.ID, 10)
	b = append(b, `,"emoji_name":`...)
	b = appendJSONString(b, rm.EmojiName)
	b = append(b, `,"user":`...)
	b = append(b, uJSON...)
	b = append(b, `,"livestream":`...)
	b = append(b, lsJSON...)
	b = append(b, `,"created_at":`...)
	b = strconv.AppendInt(b, rm.CreatedAt, 10)
	b = append(b, '}')
	return http.StatusCreated, b
}

func (s *Store) GetUserStatistics(username string) (int, []byte) {
	s.mu.RLock()
	target, ok := s.userByName[username]
	if !ok {
		s.mu.RUnlock()
		return errJSON(http.StatusBadRequest, "not found user that has the given username")
	}
	tScore := target.score
	tName := target.model.Name
	var rank int64 = 1
	for i := 1; i < len(s.users); i++ {
		u := s.users[i]
		if u == nil {
			continue
		}
		if u.score > tScore || (u.score == tScore && u.model.Name > tName) {
			rank++
		}
	}
	stats := UserStatistics{
		Rank:              rank,
		ViewersCount:      target.viewersCount,
		TotalReactions:    target.totalReactions,
		TotalLivecomments: target.totalLivecomments,
		TotalTip:          target.totalTip,
		FavoriteEmoji:     target.favEmoji,
	}
	s.mu.RUnlock()
	return okJSON(http.StatusOK, stats)
}

func (s *Store) GetLivestreamStatistics(livestreamID int64) (int, []byte) {
	s.mu.RLock()
	if livestreamID <= 0 || int(livestreamID) >= len(s.livestreams) || s.livestreams[livestreamID] == nil {
		s.mu.RUnlock()
		return errJSON(http.StatusBadRequest, "cannot get stats of not found livestream")
	}
	target := s.livestreams[livestreamID]
	tScore := target.score
	tID := target.model.ID
	var rank int64 = 1
	for i := 1; i < len(s.livestreams); i++ {
		le := s.livestreams[i]
		if le == nil {
			continue
		}
		if le.score > tScore || (le.score == tScore && le.model.ID > tID) {
			rank++
		}
	}
	stats := LivestreamStatistics{
		Rank:           rank,
		ViewersCount:   target.viewersCount,
		TotalReactions: target.totalReactions,
		TotalReports:   target.totalReports,
		MaxTip:         target.maxTip,
	}
	s.mu.RUnlock()
	return okJSON(http.StatusOK, stats)
}

func (s *Store) GetPaymentResult() (int, []byte) {
	s.mu.RLock()
	tip := s.totalPaymentTip
	s.mu.RUnlock()
	b := make([]byte, 0, 32)
	b = append(b, `{"total_tip":`...)
	b = strconv.AppendInt(b, tip, 10)
	b = append(b, '}')
	return http.StatusOK, b
}

// --- WAL Encoding & Replay ---

func appendInt64(b []byte, v int64) []byte {
	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], uint64(v))
	return append(b, tmp[:]...)
}

func appendStr(b []byte, s string) []byte {
	var tmp [4]byte
	binary.BigEndian.PutUint32(tmp[:], uint32(len(s)))
	b = append(b, tmp[:]...)
	return append(b, s...)
}

func appendBytes(b []byte, p []byte) []byte {
	var tmp [4]byte
	binary.BigEndian.PutUint32(tmp[:], uint32(len(p)))
	b = append(b, tmp[:]...)
	return append(b, p...)
}

func readInt64(b []byte, off *int) (int64, bool) {
	if *off+8 > len(b) {
		return 0, false
	}
	v := int64(binary.BigEndian.Uint64(b[*off : *off+8]))
	*off += 8
	return v, true
}

func readStr(b []byte, off *int) (string, bool) {
	if *off+4 > len(b) {
		return "", false
	}
	l := int(binary.BigEndian.Uint32(b[*off : *off+4]))
	*off += 4
	if *off+l > len(b) {
		return "", false
	}
	s := string(b[*off : *off+l])
	*off += l
	return s, true
}

func readBytes(b []byte, off *int) ([]byte, bool) {
	if *off+4 > len(b) {
		return nil, false
	}
	l := int(binary.BigEndian.Uint32(b[*off : *off+4]))
	*off += 4
	if *off+l > len(b) {
		return nil, false
	}
	out := make([]byte, l)
	copy(out, b[*off:*off+l])
	*off += l
	return out, true
}

func (s *Store) walRegister(uid, tid int64, name, disp, desc, pw string, dark bool) {
	if s.wal == nil {
		return
	}
	b := make([]byte, 0, 64+len(name)+len(disp)+len(desc)+len(pw))
	b = appendInt64(b, uid)
	b = appendInt64(b, tid)
	b = appendStr(b, name)
	b = appendStr(b, disp)
	b = appendStr(b, desc)
	b = appendStr(b, pw)
	if dark {
		b = append(b, 1)
	} else {
		b = append(b, 0)
	}
	s.wal.appendRecord(walOpRegister, b)
}

func (s *Store) walIconBlob(hash string, img []byte) {
	if s.wal == nil {
		return
	}
	b := make([]byte, 0, 8+len(hash)+len(img))
	b = appendStr(b, hash)
	b = appendBytes(b, img)
	s.wal.appendRecord(walOpIconBlob, b)
}

func (s *Store) walSetIconRef(iconID, userID int64, hash string) {
	if s.wal == nil {
		return
	}
	b := make([]byte, 0, 20+len(hash))
	b = appendInt64(b, iconID)
	b = appendInt64(b, userID)
	b = appendStr(b, hash)
	s.wal.appendRecord(walOpSetIconRef, b)
}

func (s *Store) walReserve(lid, uid int64, req *ReserveLivestreamRequest) {
	if s.wal == nil {
		return
	}
	b := make([]byte, 0, 128+len(req.Title)+len(req.Description)+len(req.PlaylistUrl)+len(req.ThumbnailUrl)+len(req.Tags)*8)
	b = appendInt64(b, lid)
	b = appendInt64(b, uid)
	b = appendStr(b, req.Title)
	b = appendStr(b, req.Description)
	b = appendStr(b, req.PlaylistUrl)
	b = appendStr(b, req.ThumbnailUrl)
	b = appendInt64(b, req.StartAt)
	b = appendInt64(b, req.EndAt)
	b = appendInt64(b, int64(len(req.Tags)))
	for _, tid := range req.Tags {
		b = appendInt64(b, tid)
	}
	s.wal.appendRecord(walOpReserve, b)
}

func (s *Store) walEnter(uid, lid, ts int64) {
	if s.wal == nil {
		return
	}
	b := make([]byte, 0, 24)
	b = appendInt64(b, uid)
	b = appendInt64(b, lid)
	b = appendInt64(b, ts)
	s.wal.appendRecord(walOpEnter, b)
}

func (s *Store) walExit(uid, lid int64) {
	if s.wal == nil {
		return
	}
	b := make([]byte, 0, 16)
	b = appendInt64(b, uid)
	b = appendInt64(b, lid)
	s.wal.appendRecord(walOpExit, b)
}

func (s *Store) walLivecomment(cm *LivecommentModel) {
	if s.wal == nil {
		return
	}
	b := make([]byte, 0, 48+len(cm.Comment))
	b = appendInt64(b, cm.ID)
	b = appendInt64(b, cm.UserID)
	b = appendInt64(b, cm.LivestreamID)
	b = appendStr(b, cm.Comment)
	b = appendInt64(b, cm.Tip)
	b = appendInt64(b, cm.CreatedAt)
	s.wal.appendRecord(walOpLivecomment, b)
}

func (s *Store) walReaction(rm *ReactionModel) {
	if s.wal == nil {
		return
	}
	b := make([]byte, 0, 40+len(rm.EmojiName))
	b = appendInt64(b, rm.ID)
	b = appendInt64(b, rm.UserID)
	b = appendInt64(b, rm.LivestreamID)
	b = appendStr(b, rm.EmojiName)
	b = appendInt64(b, rm.CreatedAt)
	s.wal.appendRecord(walOpReaction, b)
}

func (s *Store) walReport(rm *LivecommentReportModel) {
	if s.wal == nil {
		return
	}
	b := make([]byte, 0, 40)
	b = appendInt64(b, rm.ID)
	b = appendInt64(b, rm.UserID)
	b = appendInt64(b, rm.LivestreamID)
	b = appendInt64(b, rm.LivecommentID)
	b = appendInt64(b, rm.CreatedAt)
	s.wal.appendRecord(walOpReport, b)
}

func (s *Store) walModerate(ng *NGWord) {
	if s.wal == nil {
		return
	}
	b := make([]byte, 0, 40+len(ng.Word))
	b = appendInt64(b, ng.ID)
	b = appendInt64(b, ng.UserID)
	b = appendInt64(b, ng.LivestreamID)
	b = appendStr(b, ng.Word)
	b = appendInt64(b, ng.CreatedAt)
	s.wal.appendRecord(walOpModerate, b)
}

func (s *Store) replayWAL(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 256*1024)
	var magic [5]byte
	if _, err := io.ReadFull(br, magic[:]); err != nil {
		if err == io.EOF {
			return nil
		}
		return err
	}
	if magic != walMagic {
		return fmt.Errorf("invalid wal magic")
	}

	var hdr [5]byte
	replayed := 0
	for {
		_, err := io.ReadFull(br, hdr[:])
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return err
		}
		op := hdr[0]
		sz := binary.BigEndian.Uint32(hdr[1:5])
		payload := make([]byte, sz)
		if _, err := io.ReadFull(br, payload); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return err
		}
		s.applyWALRecord(op, payload)
		replayed++
	}
	// Rebuild cachedJSON after replay for any touched entries
	for i := 1; i < len(s.users); i++ {
		if s.users[i] != nil {
			s.users[i].cachedJSON = buildUserJSON(s.users[i])
		}
	}
	for i := 1; i < len(s.livestreams); i++ {
		le := s.livestreams[i]
		if le != nil {
			le.cachedJSON = buildLivestreamJSON(le, s.getUserJSON(le.model.UserID))
		}
	}
	log.Printf("Replayed %d WAL records from %s", replayed, path)
	return nil
}

func (s *Store) applyWALRecord(op byte, p []byte) {
	off := 0
	switch op {
	case walOpInit:
		ts, ok := readInt64(p, &off)
		if ok {
			s.loadFromSeedLocked(ts)
		}
	case walOpRegister:
		uid, ok1 := readInt64(p, &off)
		tid, ok2 := readInt64(p, &off)
		name, ok3 := readStr(p, &off)
		disp, ok4 := readStr(p, &off)
		desc, ok5 := readStr(p, &off)
		pw, ok6 := readStr(p, &off)
		if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6 && off < len(p)) {
			return
		}
		dark := p[off] == 1
		ue := &userEntry{
			model: UserModel{
				ID:             uid,
				Name:           name,
				DisplayName:    disp,
				Description:    desc,
				HashedPassword: pw,
			},
			theme:       Theme{ID: tid, DarkMode: dark},
			iconHash:    s.fallbackIconHash,
			emojiCounts: make(map[string]int64, 4),
		}
		for int64(len(s.users)) <= uid {
			s.users = append(s.users, nil)
		}
		s.users[uid] = ue
		s.userByName[name] = ue
		if uid >= s.nextUserID {
			s.nextUserID = uid + 1
		}
		if tid >= s.nextThemeID {
			s.nextThemeID = tid + 1
		}
	case walOpIconBlob:
		hash, ok1 := readStr(p, &off)
		img, ok2 := readBytes(p, &off)
		if ok1 && ok2 {
			s.iconBlobs[hash] = img
		}
	case walOpSetIconRef:
		iconID, ok1 := readInt64(p, &off)
		uid, ok2 := readInt64(p, &off)
		hash, ok3 := readStr(p, &off)
		if !(ok1 && ok2 && ok3) {
			return
		}
		if iconID >= s.nextIconID {
			s.nextIconID = iconID + 1
		}
		if uid > 0 && int(uid) < len(s.users) && s.users[uid] != nil {
			s.users[uid].iconHash = hash
			s.users[uid].iconBytes = s.iconBlobs[hash]
		}
	case walOpSetIcon:
		iconID, ok1 := readInt64(p, &off)
		uid, ok2 := readInt64(p, &off)
		hash, ok3 := readStr(p, &off)
		img, ok4 := readBytes(p, &off)
		if !(ok1 && ok2 && ok3 && ok4) {
			return
		}
		if iconID >= s.nextIconID {
			s.nextIconID = iconID + 1
		}
		s.iconBlobs[hash] = img
		if uid > 0 && int(uid) < len(s.users) && s.users[uid] != nil {
			s.users[uid].iconHash = hash
			s.users[uid].iconBytes = img
		}
	case walOpReserve:
		lid, ok1 := readInt64(p, &off)
		uid, ok2 := readInt64(p, &off)
		title, ok3 := readStr(p, &off)
		desc, ok4 := readStr(p, &off)
		pl, ok5 := readStr(p, &off)
		th, ok6 := readStr(p, &off)
		startAt, ok7 := readInt64(p, &off)
		endAt, ok8 := readInt64(p, &off)
		nTags, ok9 := readInt64(p, &off)
		if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6 && ok7 && ok8 && ok9) {
			return
		}
		tagIDs := make([]int64, 0, nTags)
		for i := int64(0); i < nTags; i++ {
			tid, ok := readInt64(p, &off)
			if !ok {
				return
			}
			tagIDs = append(tagIDs, tid)
		}
		startIdx := sort.Search(len(s.slots), func(i int) bool {
			return s.slots[i].StartAt >= startAt
		})
		for i := startIdx; i < len(s.slots); i++ {
			if s.slots[i].EndAt <= endAt {
				s.slots[i].Slot--
			} else if s.slots[i].StartAt >= endAt {
				break
			}
		}
		tags := make([]Tag, 0, len(tagIDs))
		for _, tid := range tagIDs {
			if tg, ok := s.tagByID[tid]; ok {
				tags = append(tags, tg)
			}
		}
		le := &livestreamEntry{
			model: LivestreamModel{
				ID:           lid,
				UserID:       uid,
				Title:        title,
				Description:  desc,
				PlaylistUrl:  pl,
				ThumbnailUrl: th,
				StartAt:      startAt,
				EndAt:        endAt,
			},
			tags:          tags,
			tagsJSON:      buildTagsSliceJSON(tags),
			viewersByUser: make(map[int64]int64, 4),
		}
		for int64(len(s.livestreams)) <= lid {
			s.livestreams = append(s.livestreams, nil)
		}
		s.livestreams[lid] = le
		if lid >= s.nextLivestreamID {
			s.nextLivestreamID = lid + 1
		}
		if uid > 0 && int(uid) < len(s.users) && s.users[uid] != nil {
			s.users[uid].livestreams = append(s.users[uid].livestreams, le)
		}
		for _, tid := range tagIDs {
			if _, ok := s.tagByID[tid]; ok {
				s.tagToLivestreams[tid] = append(s.tagToLivestreams[tid], le)
			}
		}
	case walOpEnter:
		uid, ok1 := readInt64(p, &off)
		lid, ok2 := readInt64(p, &off)
		if !(ok1 && ok2) {
			return
		}
		if lid > 0 && int(lid) < len(s.livestreams) && s.livestreams[lid] != nil {
			le := s.livestreams[lid]
			le.viewersByUser[uid]++
			le.viewersCount++
			if le.model.UserID > 0 && int(le.model.UserID) < len(s.users) && s.users[le.model.UserID] != nil {
				s.users[le.model.UserID].viewersCount++
			}
		}
	case walOpExit:
		uid, ok1 := readInt64(p, &off)
		lid, ok2 := readInt64(p, &off)
		if !(ok1 && ok2) {
			return
		}
		if lid > 0 && int(lid) < len(s.livestreams) && s.livestreams[lid] != nil {
			le := s.livestreams[lid]
			if cnt := le.viewersByUser[uid]; cnt > 0 {
				delete(le.viewersByUser, uid)
				le.viewersCount -= cnt
				if le.model.UserID > 0 && int(le.model.UserID) < len(s.users) && s.users[le.model.UserID] != nil {
					s.users[le.model.UserID].viewersCount -= cnt
				}
			}
		}
	case walOpLivecomment:
		cid, ok1 := readInt64(p, &off)
		uid, ok2 := readInt64(p, &off)
		lid, ok3 := readInt64(p, &off)
		comment, ok4 := readStr(p, &off)
		tip, ok5 := readInt64(p, &off)
		ts, ok6 := readInt64(p, &off)
		if !(ok1 && ok2 && ok3 && ok4 && ok5 && ok6) {
			return
		}
		if cid >= s.nextLivecommentID {
			s.nextLivecommentID = cid + 1
		}
		cm := &LivecommentModel{
			ID:           cid,
			UserID:       uid,
			LivestreamID: lid,
			Comment:      comment,
			Tip:          tip,
			CreatedAt:    ts,
		}
		s.livecommentsByID[cid] = cm
		s.totalPaymentTip += tip
		if lid > 0 && int(lid) < len(s.livestreams) && s.livestreams[lid] != nil {
			le := s.livestreams[lid]
			le.comments = append(le.comments, cm)
			le.totalTip += tip
			le.score += tip
			if tip > le.maxTip {
				le.maxTip = tip
			}
			if le.model.UserID > 0 && int(le.model.UserID) < len(s.users) && s.users[le.model.UserID] != nil {
				owner := s.users[le.model.UserID]
				owner.totalLivecomments++
				owner.totalTip += tip
				owner.score += tip
			}
		}
	case walOpReaction:
		rid, ok1 := readInt64(p, &off)
		uid, ok2 := readInt64(p, &off)
		lid, ok3 := readInt64(p, &off)
		emoji, ok4 := readStr(p, &off)
		ts, ok5 := readInt64(p, &off)
		if !(ok1 && ok2 && ok3 && ok4 && ok5) {
			return
		}
		if rid >= s.nextReactionID {
			s.nextReactionID = rid + 1
		}
		rm := &ReactionModel{
			ID:           rid,
			EmojiName:    emoji,
			UserID:       uid,
			LivestreamID: lid,
			CreatedAt:    ts,
		}
		if lid > 0 && int(lid) < len(s.livestreams) && s.livestreams[lid] != nil {
			le := s.livestreams[lid]
			le.reactions = append(le.reactions, rm)
			le.totalReactions++
			le.score++
			if le.model.UserID > 0 && int(le.model.UserID) < len(s.users) && s.users[le.model.UserID] != nil {
				owner := s.users[le.model.UserID]
				owner.totalReactions++
				owner.score++
				cnt := owner.emojiCounts[emoji] + 1
				owner.emojiCounts[emoji] = cnt
				if cnt > owner.favEmojiCount || (cnt == owner.favEmojiCount && emoji > owner.favEmoji) {
					owner.favEmojiCount = cnt
					owner.favEmoji = emoji
				}
			}
		}
	case walOpReport:
		rid, ok1 := readInt64(p, &off)
		uid, ok2 := readInt64(p, &off)
		lid, ok3 := readInt64(p, &off)
		cid, ok4 := readInt64(p, &off)
		ts, ok5 := readInt64(p, &off)
		if !(ok1 && ok2 && ok3 && ok4 && ok5) {
			return
		}
		if rid >= s.nextReportID {
			s.nextReportID = rid + 1
		}
		rm := &LivecommentReportModel{
			ID:            rid,
			UserID:        uid,
			LivestreamID:  lid,
			LivecommentID: cid,
			CreatedAt:     ts,
		}
		if lid > 0 && int(lid) < len(s.livestreams) && s.livestreams[lid] != nil {
			le := s.livestreams[lid]
			le.reports = append(le.reports, rm)
			le.totalReports++
		}
	case walOpModerate:
		wid, ok1 := readInt64(p, &off)
		uid, ok2 := readInt64(p, &off)
		lid, ok3 := readInt64(p, &off)
		word, ok4 := readStr(p, &off)
		ts, ok5 := readInt64(p, &off)
		if !(ok1 && ok2 && ok3 && ok4 && ok5) {
			return
		}
		if wid >= s.nextNGWordID {
			s.nextNGWordID = wid + 1
		}
		ng := &NGWord{
			ID:           wid,
			UserID:       uid,
			LivestreamID: lid,
			Word:         word,
			CreatedAt:    ts,
		}
		if lid > 0 && int(lid) < len(s.livestreams) && s.livestreams[lid] != nil {
			le := s.livestreams[lid]
			le.ngWords = append(le.ngWords, ng)
			var owner *userEntry
			if le.model.UserID > 0 && int(le.model.UserID) < len(s.users) {
				owner = s.users[le.model.UserID]
			}
			needRecompute := false
			kept := le.comments[:0]
			for _, cm := range le.comments {
				hit := false
				for _, w := range le.ngWords {
					if sqlLikeContains(cm.Comment, w.Word) {
						hit = true
						break
					}
				}
				if hit {
					delete(s.livecommentsByID, cm.ID)
					s.deletedComments[cm.ID] = cm
					le.totalTip -= cm.Tip
					le.score -= cm.Tip
					s.totalPaymentTip -= cm.Tip
					if cm.Tip == le.maxTip && cm.Tip > 0 {
						needRecompute = true
					}
					if owner != nil {
						owner.totalLivecomments--
						owner.totalTip -= cm.Tip
						owner.score -= cm.Tip
					}
				} else {
					kept = append(kept, cm)
				}
			}
			le.comments = kept
			if needRecompute {
				var maxT int64
				for _, cm := range le.comments {
					if cm.Tip > maxT {
						maxT = cm.Tip
					}
				}
				le.maxTip = maxT
			}
		}
	}
}
