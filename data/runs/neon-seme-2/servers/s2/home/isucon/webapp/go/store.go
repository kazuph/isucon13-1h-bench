package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	errNotFound      = errors.New("not found")
	errForbidden     = errors.New("forbidden")
	errBadRequest    = errors.New("bad request")
	errDuplicateUser = errors.New("duplicate user")
	errSpamDetected  = errors.New("このコメントがスパム判定されました")
	termStartAtUnix  = time.Date(2023, 11, 25, 1, 0, 0, 0, time.UTC).Unix()
	termEndAtUnix    = time.Date(2024, 11, 25, 1, 0, 0, 0, time.UTC).Unix()
)

type UserEntry struct {
	ID             int64
	Name           string
	DisplayName    string
	Description    string
	HashedPassword string
	ThemeID        int64
	DarkMode       bool

	// Pre-serialized JSON prefix up to `,"icon_hash":"`
	jsonPrefix []byte
	// Lock-free atomic pointer to current icon hash
	iconHashPtr atomic.Pointer[string]

	// Incremental stats across all livestreams owned by this user (atomic for lock-free updates!)
	totalReactions    atomic.Int64
	totalLivecomments atomic.Int64
	totalTip          atomic.Int64
	viewersCount      atomic.Int64

	emojiMu     sync.Mutex
	emojiCounts map[string]int64
}

func (u *UserEntry) GetIconHash() string {
	p := u.iconHashPtr.Load()
	if p == nil {
		return ""
	}
	return *p
}

func (u *UserEntry) SetIconHash(h string) {
	hCopy := h
	u.iconHashPtr.Store(&hCopy)
}

func (u *UserEntry) Score() int64 {
	return u.totalReactions.Load() + u.totalTip.Load()
}

func (u *UserEntry) AppendJSON(dst []byte) []byte {
	dst = append(dst, u.jsonPrefix...)
	dst = append(dst, u.GetIconHash()...)
	dst = append(dst, `"}`...)
	return dst
}

func (u *UserEntry) ToPublic() User {
	return User{
		ID:          u.ID,
		Name:        u.Name,
		DisplayName: u.DisplayName,
		Description: u.Description,
		Theme: Theme{
			ID:       u.ThemeID,
			DarkMode: u.DarkMode,
		},
		IconHash: u.GetIconHash(),
	}
}

type LivecommentEntry struct {
	ID             int64
	UserID         int64
	LivestreamID   int64
	Comment        string
	EscapedComment []byte
	Tip            int64
	CreatedAt      int64
}

type ReactionEntry struct {
	ID           int64
	EmojiName    string
	EscapedEmoji []byte
	UserID       int64
	LivestreamID int64
	CreatedAt    int64
}

type ReportEntry struct {
	ID            int64
	UserID        int64
	LivestreamID  int64
	LivecommentID int64
	Livecomment   *LivecommentEntry
	CreatedAt     int64
}

type LivestreamEntry struct {
	ID           int64
	UserID       int64
	Owner        *UserEntry
	Title        string
	Description  string
	PlaylistUrl  string
	ThumbnailUrl string
	StartAt      int64
	EndAt        int64
	TagIDs       []int64

	// Pre-serialized JSON suffix starting from `,"title":...}`
	jsonMid []byte

	// Per-livestream lock protects Livecomments, Reactions, Reports, NGWords, Viewers, MaxTip!
	mu           sync.RWMutex
	Livecomments []*LivecommentEntry
	Reactions    []*ReactionEntry
	Reports      []*ReportEntry
	NGWords      []*NGWord
	Viewers      map[int64]int64 // userID -> enter count

	// Incremental stats for this livestream
	viewersCount   atomic.Int64
	totalReactions atomic.Int64
	totalTip       atomic.Int64
	maxTip         atomic.Int64
	totalReports   atomic.Int64
}

func (l *LivestreamEntry) Score() int64 {
	return l.totalReactions.Load() + l.totalTip.Load()
}

func (l *LivestreamEntry) AppendJSON(dst []byte) []byte {
	dst = append(dst, `{"id":`...)
	dst = strconv.AppendInt(dst, l.ID, 10)
	dst = append(dst, `,"owner":`...)
	dst = l.Owner.AppendJSON(dst)
	dst = append(dst, l.jsonMid...)
	return dst
}

type Store struct {
	// mu protects top-level collections (users slice/map, livestreams slice/maps, slots)
	mu   sync.RWMutex
	snap *InitialSnapshot
	wal  *WAL
	dns  *DNSServer

	onRegister func(InternalUserAuth)
	onSetIcon  func(username, hash string, image []byte)

	tags     []Tag
	tagsByID []Tag // 1-based
	tagsByNm map[string]int64
	tagsJSON []byte

	users       []*UserEntry // 1-based (index 0 nil)
	usersByName map[string]*UserEntry
	nextUserID  int64
	nextThemeID int64

	// Separate lock for icon blobs & base64-dedup cache (hot path!)
	iconMu        sync.RWMutex
	iconBlobs     map[string][]byte     // sha256Hex -> raw image bytes
	b64ToIconHash map[[32]byte]string   // sha256(base64) -> raw sha256Hex
	nextIconID    atomic.Int64

	livestreams       []*LivestreamEntry // 1-based (index 0 nil)
	livestreamsByUser map[int64][]*LivestreamEntry
	livestreamsByTag  map[int64][]*LivestreamEntry // ordered by livestream_id ASC
	nextLivestreamID  int64

	slotMu   sync.Mutex
	slotBase int64
	slotStep int64
	slots    []int64

	lcMapMu          sync.RWMutex
	livecommentsByID map[int64]*LivecommentEntry
	nextLCID         atomic.Int64
	nextReactionID   atomic.Int64
	nextReportID     atomic.Int64
	nextNGWordID     atomic.Int64

	globalTotalTip atomic.Int64

	// Cached search responses (invalidated on ReserveLivestream or SetIcon)
	searchGen        atomic.Uint64
	search50CacheMu  sync.RWMutex
	search50CacheGen uint64
	search50Cache    []byte
}

func NewStore(snap *InitialSnapshot, wal *WAL, dns *DNSServer) *Store {
	s := &Store{
		snap: snap,
		wal:  wal,
		dns:  dns,
	}
	s.loadFromSnapshotLocked()
	return s
}

func (s *Store) loadFromSnapshotLocked() {
	snap := s.snap

	// Tags
	s.tags = make([]Tag, len(snap.Tags))
	copy(s.tags, snap.Tags)
	maxTagID := int64(0)
	for _, t := range snap.Tags {
		if t.ID > maxTagID {
			maxTagID = t.ID
		}
	}
	s.tagsByID = make([]Tag, maxTagID+1)
	s.tagsByNm = make(map[string]int64, len(snap.Tags))
	tagPtrs := make([]*Tag, len(snap.Tags))
	for i := range snap.Tags {
		t := snap.Tags[i]
		s.tagsByID[t.ID] = t
		s.tagsByNm[t.Name] = t.ID
		tagPtrs[i] = &Tag{ID: t.ID, Name: t.Name}
	}
	s.tagsJSON, _ = json.Marshal(&TagsResponse{Tags: tagPtrs})

	// Icon blobs
	s.iconMu.Lock()
	s.iconBlobs = make(map[string][]byte, 256)
	s.iconBlobs[snap.FallbackHash] = snap.FallbackImage
	s.b64ToIconHash = make(map[[32]byte]string, 256)
	s.iconMu.Unlock()
	s.nextIconID.Store(1)

	// Users
	maxUID := int64(0)
	maxTID := int64(0)
	for _, u := range snap.Users {
		if u.ID > maxUID {
			maxUID = u.ID
		}
		if u.ThemeID > maxTID {
			maxTID = u.ThemeID
		}
	}
	s.users = make([]*UserEntry, maxUID+1, maxUID+8192)
	s.usersByName = make(map[string]*UserEntry, len(snap.Users)+8192)
	for _, u := range snap.Users {
		ue := &UserEntry{
			ID:             u.ID,
			Name:           u.Name,
			DisplayName:    u.DisplayName,
			Description:    u.Description,
			HashedPassword: u.HashedPassword,
			ThemeID:        u.ThemeID,
			DarkMode:       u.DarkMode,
			jsonPrefix:     buildUserJSONPrefix(u.ID, u.Name, u.DisplayName, u.Description, u.ThemeID, u.DarkMode),
			emojiCounts:    make(map[string]int64),
		}
		ue.SetIconHash(snap.FallbackHash)
		s.users[u.ID] = ue
		s.usersByName[u.Name] = ue
	}
	s.nextUserID = maxUID + 1
	s.nextThemeID = maxTID + 1

	// Livestreams
	maxLSID := int64(0)
	for _, l := range snap.Livestreams {
		if l.ID > maxLSID {
			maxLSID = l.ID
		}
	}
	s.livestreams = make([]*LivestreamEntry, maxLSID+1, maxLSID+4096)
	s.livestreamsByUser = make(map[int64][]*LivestreamEntry, len(snap.Users)+4096)
	s.livestreamsByTag = make(map[int64][]*LivestreamEntry, len(snap.Tags)+16)
	for _, l := range snap.Livestreams {
		tids := make([]int64, len(l.TagIDs))
		copy(tids, l.TagIDs)
		tags := s.resolveTags(tids)
		le := &LivestreamEntry{
			ID:           l.ID,
			UserID:       l.UserID,
			Owner:        s.users[l.UserID],
			Title:        l.Title,
			Description:  l.Description,
			PlaylistUrl:  l.PlaylistUrl,
			ThumbnailUrl: l.ThumbnailUrl,
			StartAt:      l.StartAt,
			EndAt:        l.EndAt,
			TagIDs:       tids,
			jsonMid:      buildLivestreamJSONMid(l.Title, l.Description, l.PlaylistUrl, l.ThumbnailUrl, tags, l.StartAt, l.EndAt),
			Viewers:      make(map[int64]int64),
		}
		s.livestreams[l.ID] = le
		s.livestreamsByUser[l.UserID] = append(s.livestreamsByUser[l.UserID], le)
		for _, tid := range tids {
			s.livestreamsByTag[tid] = append(s.livestreamsByTag[tid], le)
		}
	}
	for _, list := range s.livestreamsByTag {
		sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	}
	s.nextLivestreamID = maxLSID + 1

	// Slots
	s.slotMu.Lock()
	s.slotBase = snap.SlotBase
	s.slotStep = snap.SlotStep
	s.slots = make([]int64, len(snap.Slots))
	copy(s.slots, snap.Slots)
	s.slotMu.Unlock()

	// Livecomments
	s.lcMapMu.Lock()
	s.livecommentsByID = make(map[int64]*LivecommentEntry, len(snap.Livecomments)+32768)
	s.globalTotalTip.Store(0)
	maxLCID := int64(0)
	for _, c := range snap.Livecomments {
		if c.ID > maxLCID {
			maxLCID = c.ID
		}
		lce := &LivecommentEntry{
			ID:             c.ID,
			UserID:         c.UserID,
			LivestreamID:   c.LivestreamID,
			Comment:        c.Comment,
			EscapedComment: appendJSONString(nil, c.Comment),
			Tip:            c.Tip,
			CreatedAt:      c.CreatedAt,
		}
		s.livecommentsByID[c.ID] = lce
		if c.LivestreamID > 0 && int(c.LivestreamID) < len(s.livestreams) {
			if ls := s.livestreams[c.LivestreamID]; ls != nil {
				ls.Livecomments = append(ls.Livecomments, lce)
				ls.totalTip.Add(c.Tip)
				if c.Tip > ls.maxTip.Load() {
					ls.maxTip.Store(c.Tip)
				}
				if ls.Owner != nil {
					ls.Owner.totalTip.Add(c.Tip)
					ls.Owner.totalLivecomments.Add(1)
				}
			}
		}
		s.globalTotalTip.Add(c.Tip)
	}
	s.lcMapMu.Unlock()
	s.nextLCID.Store(maxLCID + 1)

	// Reactions
	maxRxID := int64(0)
	for _, r := range snap.Reactions {
		if r.ID > maxRxID {
			maxRxID = r.ID
		}
		rxe := &ReactionEntry{
			ID:           r.ID,
			EmojiName:    r.EmojiName,
			EscapedEmoji: appendJSONString(nil, r.EmojiName),
			UserID:       r.UserID,
			LivestreamID: r.LivestreamID,
			CreatedAt:    r.CreatedAt,
		}
		if r.LivestreamID > 0 && int(r.LivestreamID) < len(s.livestreams) {
			if ls := s.livestreams[r.LivestreamID]; ls != nil {
				ls.Reactions = append(ls.Reactions, rxe)
				ls.totalReactions.Add(1)
				if ls.Owner != nil {
					ls.Owner.totalReactions.Add(1)
					ls.Owner.emojiCounts[r.EmojiName]++
				}
			}
		}
	}
	s.nextReactionID.Store(maxRxID + 1)

	// NGWords
	maxNGID := int64(0)
	for _, g := range snap.NGWords {
		if g.ID > maxNGID {
			maxNGID = g.ID
		}
		ng := &NGWord{
			ID:           g.ID,
			UserID:       g.UserID,
			LivestreamID: g.LivestreamID,
			Word:         g.Word,
			CreatedAt:    g.CreatedAt,
		}
		if g.LivestreamID > 0 && int(g.LivestreamID) < len(s.livestreams) {
			if ls := s.livestreams[g.LivestreamID]; ls != nil {
				ls.NGWords = append(ls.NGWords, ng)
			}
		}
	}
	s.nextNGWordID.Store(maxNGID + 1)
	s.nextReportID.Store(1)
	s.searchGen.Add(1)
	s.search50CacheMu.Lock()
	s.search50Cache = nil
	s.search50CacheMu.Unlock()

	if s.dns != nil {
		s.dns.Reset(snap.DNSNames)
	}
}

func (s *Store) resolveTags(tids []int64) []Tag {
	tags := make([]Tag, len(tids))
	for i, tid := range tids {
		if tid > 0 && int(tid) < len(s.tagsByID) {
			tags[i] = s.tagsByID[tid]
		}
	}
	return tags
}

func (s *Store) Initialize() error {
	if s.wal != nil {
		if err := s.wal.Reset(); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.loadFromSnapshotLocked()
	s.mu.Unlock()
	return nil
}

// --- Helpers ---

func (s *Store) getUserLocked(uid int64) *UserEntry {
	if uid <= 0 || int(uid) >= len(s.users) {
		return nil
	}
	return s.users[uid]
}

func (s *Store) getUser(uid int64) *UserEntry {
	s.mu.RLock()
	var u *UserEntry
	if uid > 0 && int(uid) < len(s.users) {
		u = s.users[uid]
	}
	s.mu.RUnlock()
	return u
}

func (s *Store) getLivestreamEntry(lsID int64) *LivestreamEntry {
	s.mu.RLock()
	var ls *LivestreamEntry
	if lsID > 0 && int(lsID) < len(s.livestreams) {
		ls = s.livestreams[lsID]
	}
	s.mu.RUnlock()
	return ls
}

func appendLivecommentJSON(dst []byte, lc *LivecommentEntry, user *UserEntry, lsJSON []byte) []byte {
	dst = append(dst, `{"id":`...)
	dst = strconv.AppendInt(dst, lc.ID, 10)
	dst = append(dst, `,"user":`...)
	dst = user.AppendJSON(dst)
	dst = append(dst, `,"livestream":`...)
	dst = append(dst, lsJSON...)
	dst = append(dst, `,"comment":`...)
	dst = append(dst, lc.EscapedComment...)
	dst = append(dst, `,"tip":`...)
	dst = strconv.AppendInt(dst, lc.Tip, 10)
	dst = append(dst, `,"created_at":`...)
	dst = strconv.AppendInt(dst, lc.CreatedAt, 10)
	dst = append(dst, '}')
	return dst
}

func appendReactionJSON(dst []byte, rx *ReactionEntry, user *UserEntry, lsJSON []byte) []byte {
	dst = append(dst, `{"id":`...)
	dst = strconv.AppendInt(dst, rx.ID, 10)
	dst = append(dst, `,"emoji_name":`...)
	dst = append(dst, rx.EscapedEmoji...)
	dst = append(dst, `,"user":`...)
	dst = user.AppendJSON(dst)
	dst = append(dst, `,"livestream":`...)
	dst = append(dst, lsJSON...)
	dst = append(dst, `,"created_at":`...)
	dst = strconv.AppendInt(dst, rx.CreatedAt, 10)
	dst = append(dst, '}')
	return dst
}

func matchNGWord(text, word string) bool {
	if !strings.ContainsAny(word, `%_\`) {
		return strings.Contains(text, word)
	}
	return sqlLikeMatch(text, "%"+word+"%")
}

func sqlLikeMatch(s, p string) bool {
	sr := []rune(s)
	pr := []rune(p)
	si, pi := 0, 0
	starP, starS := -1, 0
	for si < len(sr) {
		if pi < len(pr) && pr[pi] == '\\' && pi+1 < len(pr) {
			if sr[si] == pr[pi+1] {
				si++
				pi += 2
				continue
			}
		} else if pi < len(pr) && (pr[pi] == '_' || pr[pi] == sr[si]) {
			si++
			pi++
			continue
		} else if pi < len(pr) && pr[pi] == '%' {
			starP = pi
			starS = si
			pi++
			continue
		}
		if starP != -1 {
			pi = starP + 1
			starS++
			si = starS
			continue
		}
		return false
	}
	for pi < len(pr) && pr[pi] == '%' {
		pi++
	}
	return pi == len(pr)
}

func (s *Store) slotRangeLocked(startAt, endAt int64) (int, int) {
	if s.slotStep <= 0 || len(s.slots) == 0 {
		return 1, 0
	}
	diffStart := startAt - s.slotBase
	var lo int
	if diffStart <= 0 {
		lo = 0
	} else {
		lo = int((diffStart + s.slotStep - 1) / s.slotStep)
	}
	diffEnd := endAt - s.slotBase
	if diffEnd < s.slotStep {
		return 1, 0
	}
	hi := int(diffEnd/s.slotStep) - 1
	if hi >= len(s.slots) {
		hi = len(s.slots) - 1
	}
	return lo, hi
}

// --- Store Operations ---

func (s *Store) GetTagsJSON() []byte {
	return s.tagsJSON
}

func (s *Store) GetEdgeSync() InternalEdgeSync {
	s.mu.RLock()
	users := make([]InternalUserAuth, 0, len(s.users))
	usedHashes := make(map[string]struct{})
	for i := 1; i < len(s.users); i++ {
		u := s.users[i]
		if u == nil {
			continue
		}
		ih := u.GetIconHash()
		users = append(users, InternalUserAuth{
			ID:             u.ID,
			Name:           u.Name,
			HashedPassword: u.HashedPassword,
			ThemeID:        u.ThemeID,
			DarkMode:       u.DarkMode,
			IconHash:       ih,
		})
		if ih != s.snap.FallbackHash {
			usedHashes[ih] = struct{}{}
		}
	}
	s.mu.RUnlock()

	usedBlobs := make(map[string][]byte, len(usedHashes))
	s.iconMu.RLock()
	for h := range usedHashes {
		if blob, exists := s.iconBlobs[h]; exists {
			usedBlobs[h] = blob
		}
	}
	s.iconMu.RUnlock()
	return InternalEdgeSync{Users: users, IconBlobs: usedBlobs}
}

func (s *Store) GetUserAuth(username string) (InternalUserAuth, bool) {
	s.mu.RLock()
	u := s.usersByName[username]
	s.mu.RUnlock()
	if u == nil {
		return InternalUserAuth{}, false
	}
	return InternalUserAuth{
		ID:             u.ID,
		Name:           u.Name,
		HashedPassword: u.HashedPassword,
		ThemeID:        u.ThemeID,
		DarkMode:       u.DarkMode,
		IconHash:       u.GetIconHash(),
	}, true
}

func (s *Store) GetUserTheme(username string) (Theme, error) {
	s.mu.RLock()
	u := s.usersByName[username]
	s.mu.RUnlock()
	if u == nil {
		return Theme{}, errNotFound
	}
	return Theme{ID: u.ThemeID, DarkMode: u.DarkMode}, nil
}

// Fast path: returns hash without touching iconMu unless bytes are needed (304 needs ONLY hash!)
func (s *Store) GetIconHash(username string) (string, error) {
	s.mu.RLock()
	u := s.usersByName[username]
	s.mu.RUnlock()
	if u == nil {
		return "", errNotFound
	}
	return u.GetIconHash(), nil
}

func (s *Store) GetIconBlob(hash string) []byte {
	s.iconMu.RLock()
	img := s.iconBlobs[hash]
	s.iconMu.RUnlock()
	if img == nil {
		return s.snap.FallbackImage
	}
	return img
}

func (s *Store) GetIcon(username string) (hash string, img []byte, err error) {
	hash, err = s.GetIconHash(username)
	if err != nil {
		return "", nil, err
	}
	return hash, s.GetIconBlob(hash), nil
}

func (s *Store) AppendUserByIDJSON(dst []byte, userID int64) ([]byte, error) {
	u := s.getUser(userID)
	if u == nil {
		return dst, errNotFound
	}
	return u.AppendJSON(dst), nil
}

func (s *Store) AppendUserByNameJSON(dst []byte, username string) ([]byte, error) {
	s.mu.RLock()
	u := s.usersByName[username]
	s.mu.RUnlock()
	if u == nil {
		return dst, errNotFound
	}
	return u.AppendJSON(dst), nil
}

func (s *Store) Register(req InternalRegisterReq) (User, error) {
	if req.Name == "pipe" {
		return User{}, errBadRequest
	}
	s.mu.Lock()
	if _, exists := s.usersByName[req.Name]; exists {
		s.mu.Unlock()
		return User{}, errDuplicateUser
	}
	uid := s.nextUserID
	s.nextUserID++
	tid := s.nextThemeID
	s.nextThemeID++

	ue := &UserEntry{
		ID:             uid,
		Name:           req.Name,
		DisplayName:    req.DisplayName,
		Description:    req.Description,
		HashedPassword: req.HashedPassword,
		ThemeID:        tid,
		DarkMode:       req.DarkMode,
		jsonPrefix:     buildUserJSONPrefix(uid, req.Name, req.DisplayName, req.Description, tid, req.DarkMode),
		emojiCounts:    make(map[string]int64),
	}
	ue.SetIconHash(s.snap.FallbackHash)
	for int64(len(s.users)) <= uid {
		s.users = append(s.users, nil)
	}
	s.users[uid] = ue
	s.usersByName[req.Name] = ue
	pub := ue.ToPublic()
	auth := InternalUserAuth{
		ID:             uid,
		Name:           req.Name,
		HashedPassword: req.HashedPassword,
		ThemeID:        tid,
		DarkMode:       req.DarkMode,
		IconHash:       s.snap.FallbackHash,
	}
	s.mu.Unlock()

	if s.dns != nil {
		s.dns.AddName(req.Name)
	}

	if s.wal != nil {
		var b walBuf
		b.u8(walOpRegister)
		b.i64(uid)
		b.i64(tid)
		b.str(req.Name)
		b.str(req.DisplayName)
		b.str(req.Description)
		b.str(req.HashedPassword)
		b.bool(req.DarkMode)
		s.wal.Append(b)
	}

	if s.onRegister != nil {
		s.onRegister(auth)
	}

	return pub, nil
}

// Fast base64-dedup path for POST /api/icon!
func (s *Store) SetIconFromBase64(userID int64, b64 []byte) (int64, error) {
	u := s.getUser(userID)
	if u == nil {
		return 0, errNotFound
	}
	b64Key := sha256.Sum256(b64)
	s.iconMu.RLock()
	hash, hit := s.b64ToIconHash[b64Key]
	s.iconMu.RUnlock()

	if hit {
		iconID := s.nextIconID.Add(1) - 1
		u.SetIconHash(hash)
		s.searchGen.Add(1)
		if s.wal != nil {
			var b walBuf
			b.u8(walOpSetIcon)
			b.i64(iconID)
			b.i64(userID)
			b.str(hash)
			s.wal.Append(b)
		}
		if s.onSetIcon != nil {
			s.onSetIcon(u.Name, hash, nil)
		}
		return iconID, nil
	}

	raw, err := decodeBase64Exact(b64)
	if err != nil {
		return 0, errBadRequest
	}
	hash = sha256Hex(raw)
	iconID := s.nextIconID.Add(1) - 1

	s.iconMu.Lock()
	isNewBlob := false
	if _, ok := s.iconBlobs[hash]; !ok {
		s.iconBlobs[hash] = raw
		isNewBlob = true
	}
	s.b64ToIconHash[b64Key] = hash
	blob := s.iconBlobs[hash]
	s.iconMu.Unlock()

	u.SetIconHash(hash)
	s.searchGen.Add(1)

	if s.wal != nil {
		if isNewBlob {
			s.wal.PersistIconBlob(hash, blob)
		}
		var b walBuf
		b.u8(walOpSetIcon)
		b.i64(iconID)
		b.i64(userID)
		b.str(hash)
		s.wal.Append(b)
	}
	if s.onSetIcon != nil {
		s.onSetIcon(u.Name, hash, blob)
	}
	return iconID, nil
}

func (s *Store) SetIcon(userID int64, hash string, img []byte) (int64, error) {
	if hash == "" {
		hash = sha256Hex(img)
	}
	u := s.getUser(userID)
	if u == nil {
		return 0, errNotFound
	}
	iconID := s.nextIconID.Add(1) - 1

	s.iconMu.Lock()
	isNewBlob := false
	if _, ok := s.iconBlobs[hash]; !ok && len(img) > 0 {
		imgCopy := make([]byte, len(img))
		copy(imgCopy, img)
		s.iconBlobs[hash] = imgCopy
		isNewBlob = true
	}
	blob := s.iconBlobs[hash]
	s.iconMu.Unlock()

	u.SetIconHash(hash)
	s.searchGen.Add(1)

	if s.wal != nil {
		if isNewBlob {
			s.wal.PersistIconBlob(hash, blob)
		}
		var b walBuf
		b.u8(walOpSetIcon)
		b.i64(iconID)
		b.i64(userID)
		b.str(hash)
		s.wal.Append(b)
	}
	if s.onSetIcon != nil {
		s.onSetIcon(u.Name, hash, blob)
	}
	return iconID, nil
}

func (s *Store) ReserveLivestreamJSON(dst []byte, userID int64, req ReserveLivestreamRequest) ([]byte, error) {
	if req.StartAt >= termEndAtUnix || req.EndAt <= termStartAtUnix {
		return dst, errBadRequest
	}

	s.slotMu.Lock()
	lo, hi := s.slotRangeLocked(req.StartAt, req.EndAt)
	for i := lo; i <= hi; i++ {
		if s.slots[i] < 1 {
			s.slotMu.Unlock()
			return dst, errBadRequest
		}
	}
	for i := lo; i <= hi; i++ {
		s.slots[i]--
	}
	s.slotMu.Unlock()

	tids := make([]int64, len(req.Tags))
	copy(tids, req.Tags)
	tags := s.resolveTags(tids)
	jsonMid := buildLivestreamJSONMid(req.Title, req.Description, req.PlaylistUrl, req.ThumbnailUrl, tags, req.StartAt, req.EndAt)

	s.mu.Lock()
	lsID := s.nextLivestreamID
	s.nextLivestreamID++
	owner := s.getUserLocked(userID)
	le := &LivestreamEntry{
		ID:           lsID,
		UserID:       userID,
		Owner:        owner,
		Title:        req.Title,
		Description:  req.Description,
		PlaylistUrl:  req.PlaylistUrl,
		ThumbnailUrl: req.ThumbnailUrl,
		StartAt:      req.StartAt,
		EndAt:        req.EndAt,
		TagIDs:       tids,
		jsonMid:      jsonMid,
		Viewers:      make(map[int64]int64),
	}
	for int64(len(s.livestreams)) <= lsID {
		s.livestreams = append(s.livestreams, nil)
	}
	s.livestreams[lsID] = le
	s.livestreamsByUser[userID] = append(s.livestreamsByUser[userID], le)
	for _, tid := range tids {
		s.livestreamsByTag[tid] = append(s.livestreamsByTag[tid], le)
	}
	s.mu.Unlock()
	s.searchGen.Add(1)

	dst = le.AppendJSON(dst)

	if s.wal != nil {
		var b walBuf
		b.u8(walOpReserve)
		b.i64(lsID)
		b.i64(userID)
		b.str(req.Title)
		b.str(req.Description)
		b.str(req.PlaylistUrl)
		b.str(req.ThumbnailUrl)
		b.i64(req.StartAt)
		b.i64(req.EndAt)
		b.i64(int64(len(tids)))
		for _, tid := range tids {
			b.i64(tid)
		}
		s.wal.Append(b)
	}
	return dst, nil
}

func (s *Store) SearchLivestreamsLimit50Cached() []byte {
	gen := s.searchGen.Load()
	s.search50CacheMu.RLock()
	if s.search50Cache != nil && s.search50CacheGen == gen {
		b := s.search50Cache
		s.search50CacheMu.RUnlock()
		return b
	}
	s.search50CacheMu.RUnlock()

	buf := make([]byte, 0, 32768)
	buf = s.SearchLivestreamsJSON(buf, "", true, 50)
	s.search50CacheMu.Lock()
	if gen >= s.search50CacheGen {
		s.search50Cache = buf
		s.search50CacheGen = gen
	}
	s.search50CacheMu.Unlock()
	return buf
}

func (s *Store) SearchLivestreamsJSON(dst []byte, tagName string, hasLimit bool, limit int) []byte {
	s.mu.RLock()
	dst = append(dst, '[')
	if tagName != "" {
		tid, ok := s.tagsByNm[tagName]
		if ok {
			list := s.livestreamsByTag[tid]
			for i := len(list) - 1; i >= 0; i-- {
				if i < len(list)-1 {
					dst = append(dst, ',')
				}
				dst = list[i].AppendJSON(dst)
			}
		}
		s.mu.RUnlock()
		dst = append(dst, ']')
		return dst
	}

	total := len(s.livestreams) - 1
	if total < 0 {
		total = 0
	}
	n := total
	if hasLimit && limit < n {
		if limit < 0 {
			n = 0
		} else {
			n = limit
		}
	}
	written := 0
	for i := len(s.livestreams) - 1; i >= 1 && written < n; i-- {
		if ls := s.livestreams[i]; ls != nil {
			if written > 0 {
				dst = append(dst, ',')
			}
			dst = ls.AppendJSON(dst)
			written++
		}
	}
	s.mu.RUnlock()
	dst = append(dst, ']')
	return dst
}

func (s *Store) AppendUserLivestreamsByIDJSON(dst []byte, userID int64) []byte {
	s.mu.RLock()
	list := s.livestreamsByUser[userID]
	dst = append(dst, '[')
	for i, ls := range list {
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = ls.AppendJSON(dst)
	}
	s.mu.RUnlock()
	dst = append(dst, ']')
	return dst
}

func (s *Store) AppendUserLivestreamsByNameJSON(dst []byte, username string) ([]byte, error) {
	s.mu.RLock()
	u := s.usersByName[username]
	if u == nil {
		s.mu.RUnlock()
		return dst, errNotFound
	}
	list := s.livestreamsByUser[u.ID]
	dst = append(dst, '[')
	for i, ls := range list {
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = ls.AppendJSON(dst)
	}
	s.mu.RUnlock()
	dst = append(dst, ']')
	return dst, nil
}

func (s *Store) AppendLivestreamJSON(dst []byte, lsID int64) ([]byte, error) {
	ls := s.getLivestreamEntry(lsID)
	if ls == nil {
		return dst, errNotFound
	}
	return ls.AppendJSON(dst), nil
}

func (s *Store) EnterLivestream(userID, lsID int64) {
	now := time.Now().Unix()
	if ls := s.getLivestreamEntry(lsID); ls != nil {
		ls.mu.Lock()
		ls.Viewers[userID]++
		ls.mu.Unlock()
		ls.viewersCount.Add(1)
		if ls.Owner != nil {
			ls.Owner.viewersCount.Add(1)
		}
	}

	if s.wal != nil {
		var b walBuf
		b.u8(walOpEnter)
		b.i64(userID)
		b.i64(lsID)
		b.i64(now)
		s.wal.Append(b)
	}
}

func (s *Store) ExitLivestream(userID, lsID int64) {
	if ls := s.getLivestreamEntry(lsID); ls != nil {
		ls.mu.Lock()
		c := ls.Viewers[userID]
		if c > 0 {
			delete(ls.Viewers, userID)
		}
		ls.mu.Unlock()
		if c > 0 {
			ls.viewersCount.Add(-c)
			if ls.Owner != nil {
				ls.Owner.viewersCount.Add(-c)
			}
		}
	}

	if s.wal != nil {
		var b walBuf
		b.u8(walOpExit)
		b.i64(userID)
		b.i64(lsID)
		s.wal.Append(b)
	}
}

func (s *Store) AppendLivecommentsJSON(dst []byte, lsID int64, hasLimit bool, limit int) []byte {
	ls := s.getLivestreamEntry(lsID)
	if ls == nil {
		return append(dst, '[', ']')
	}
	ls.mu.RLock()
	list := ls.Livecomments
	n := len(list)
	if hasLimit && limit < n {
		if limit < 0 {
			n = 0
		} else {
			n = limit
		}
	}
	if n == 0 {
		ls.mu.RUnlock()
		return append(dst, '[', ']')
	}
	// Snapshot pointers while holding ls.mu.RLock
	var stackTmp [64]*LivecommentEntry
	var snap []*LivecommentEntry
	if n <= len(stackTmp) {
		snap = stackTmp[:n]
	} else {
		snap = make([]*LivecommentEntry, n)
	}
	for i := 0; i < n; i++ {
		snap[i] = list[len(list)-1-i]
	}
	ls.mu.RUnlock()

	var lsBuf [1024]byte
	lsJSON := ls.AppendJSON(lsBuf[:0])

	s.mu.RLock()
	dst = append(dst, '[')
	for i, lc := range snap {
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = appendLivecommentJSON(dst, lc, s.users[lc.UserID], lsJSON)
	}
	s.mu.RUnlock()
	dst = append(dst, ']')
	return dst
}

func (s *Store) PostLivecommentJSON(dst []byte, userID, lsID int64, req PostLivecommentRequest) ([]byte, error) {
	ls := s.getLivestreamEntry(lsID)
	if ls == nil {
		return dst, errNotFound
	}
	now := time.Now().Unix()
	escapedComment := appendJSONString(nil, req.Comment)

	ls.mu.Lock()
	for _, ng := range ls.NGWords {
		if ng.UserID == ls.UserID && matchNGWord(req.Comment, ng.Word) {
			ls.mu.Unlock()
			return dst, errSpamDetected
		}
	}
	lcID := s.nextLCID.Add(1) - 1
	lce := &LivecommentEntry{
		ID:             lcID,
		UserID:         userID,
		LivestreamID:   lsID,
		Comment:        req.Comment,
		EscapedComment: escapedComment,
		Tip:            req.Tip,
		CreatedAt:      now,
	}
	ls.Livecomments = append(ls.Livecomments, lce)
	if req.Tip > ls.maxTip.Load() {
		ls.maxTip.Store(req.Tip)
	}
	ls.mu.Unlock()

	s.lcMapMu.Lock()
	s.livecommentsByID[lcID] = lce
	s.lcMapMu.Unlock()

	if req.Tip != 0 {
		ls.totalTip.Add(req.Tip)
		ls.Owner.totalTip.Add(req.Tip)
		s.globalTotalTip.Add(req.Tip)
	}
	ls.Owner.totalLivecomments.Add(1)

	var lsBuf [1024]byte
	lsJSON := ls.AppendJSON(lsBuf[:0])
	u := s.getUser(userID)
	dst = appendLivecommentJSON(dst, lce, u, lsJSON)

	if s.wal != nil {
		var b walBuf
		b.u8(walOpLivecomment)
		b.i64(lcID)
		b.i64(userID)
		b.i64(lsID)
		b.str(req.Comment)
		b.i64(req.Tip)
		b.i64(now)
		s.wal.Append(b)
	}
	return dst, nil
}

func (s *Store) AppendNGWordsJSON(dst []byte, userID, lsID int64) []byte {
	ls := s.getLivestreamEntry(lsID)
	if ls == nil {
		return append(dst, '[', ']')
	}
	ls.mu.RLock()
	dst = append(dst, '[')
	written := 0
	for i := len(ls.NGWords) - 1; i >= 0; i-- {
		ng := ls.NGWords[i]
		if ng.UserID == userID {
			if written > 0 {
				dst = append(dst, ',')
			}
			dst = append(dst, `{"id":`...)
			dst = strconv.AppendInt(dst, ng.ID, 10)
			dst = append(dst, `,"user_id":`...)
			dst = strconv.AppendInt(dst, ng.UserID, 10)
			dst = append(dst, `,"livestream_id":`...)
			dst = strconv.AppendInt(dst, ng.LivestreamID, 10)
			dst = append(dst, `,"word":`...)
			dst = appendJSONString(dst, ng.Word)
			dst = append(dst, `,"created_at":`...)
			dst = strconv.AppendInt(dst, ng.CreatedAt, 10)
			dst = append(dst, '}')
			written++
		}
	}
	ls.mu.RUnlock()
	dst = append(dst, ']')
	return dst
}

func (s *Store) Moderate(userID, lsID int64, word string) (int64, error) {
	ls := s.getLivestreamEntry(lsID)
	if ls == nil || ls.UserID != userID {
		return 0, errBadRequest
	}
	now := time.Now().Unix()
	wordID := s.nextNGWordID.Add(1) - 1
	ng := &NGWord{
		ID:           wordID,
		UserID:       userID,
		LivestreamID: lsID,
		Word:         word,
		CreatedAt:    now,
	}

	ls.mu.Lock()
	ls.NGWords = append(ls.NGWords, ng)
	deletedIDs := s.filterModeratedCommentsLocked(ls)
	ls.mu.Unlock()

	if len(deletedIDs) > 0 {
		s.lcMapMu.Lock()
		for _, id := range deletedIDs {
			delete(s.livecommentsByID, id)
		}
		s.lcMapMu.Unlock()
	}

	if s.wal != nil {
		var b walBuf
		b.u8(walOpModerate)
		b.i64(wordID)
		b.i64(userID)
		b.i64(lsID)
		b.str(word)
		b.i64(now)
		s.wal.Append(b)
	}
	return wordID, nil
}

// Caller holds ls.mu.Lock()
func (s *Store) filterModeratedCommentsLocked(ls *LivestreamEntry) []int64 {
	if len(ls.Livecomments) == 0 || len(ls.NGWords) == 0 {
		return nil
	}
	kept := ls.Livecomments[:0]
	var deletedIDs []int64
	var tipRemoved int64
	var cntRemoved int64
	curMax := ls.maxTip.Load()
	needRecomputeMax := false

	for _, lc := range ls.Livecomments {
		hit := false
		for _, g := range ls.NGWords {
			if matchNGWord(lc.Comment, g.Word) {
				hit = true
				break
			}
		}
		if hit {
			deletedIDs = append(deletedIDs, lc.ID)
			tipRemoved += lc.Tip
			cntRemoved++
			if lc.Tip == curMax {
				needRecomputeMax = true
			}
		} else {
			kept = append(kept, lc)
		}
	}
	ls.Livecomments = kept
	if cntRemoved > 0 {
		if tipRemoved != 0 {
			ls.totalTip.Add(-tipRemoved)
			ls.Owner.totalTip.Add(-tipRemoved)
			s.globalTotalTip.Add(-tipRemoved)
		}
		ls.Owner.totalLivecomments.Add(-cntRemoved)
	}
	if needRecomputeMax {
		var mx int64
		for _, lc := range ls.Livecomments {
			if lc.Tip > mx {
				mx = lc.Tip
			}
		}
		ls.maxTip.Store(mx)
	}
	return deletedIDs
}

func (s *Store) ReportLivecommentJSON(dst []byte, userID, lsID, lcID int64) ([]byte, error) {
	ls := s.getLivestreamEntry(lsID)
	if ls == nil {
		return dst, errNotFound
	}
	s.lcMapMu.RLock()
	lc := s.livecommentsByID[lcID]
	s.lcMapMu.RUnlock()
	if lc == nil {
		return dst, errNotFound
	}
	now := time.Now().Unix()
	repID := s.nextReportID.Add(1) - 1
	re := &ReportEntry{
		ID:            repID,
		UserID:        userID,
		LivestreamID:  lsID,
		LivecommentID: lcID,
		Livecomment:   lc,
		CreatedAt:     now,
	}

	ls.mu.Lock()
	ls.Reports = append(ls.Reports, re)
	ls.mu.Unlock()
	ls.totalReports.Add(1)

	var lsBuf [1024]byte
	lsJSON := ls.AppendJSON(lsBuf[:0])
	s.mu.RLock()
	reporter := s.users[userID]
	lcUser := s.users[lc.UserID]
	s.mu.RUnlock()

	dst = append(dst, `{"id":`...)
	dst = strconv.AppendInt(dst, repID, 10)
	dst = append(dst, `,"reporter":`...)
	dst = reporter.AppendJSON(dst)
	dst = append(dst, `,"livecomment":`...)
	dst = appendLivecommentJSON(dst, lc, lcUser, lsJSON)
	dst = append(dst, `,"created_at":`...)
	dst = strconv.AppendInt(dst, now, 10)
	dst = append(dst, '}')

	if s.wal != nil {
		var b walBuf
		b.u8(walOpReport)
		b.i64(repID)
		b.i64(userID)
		b.i64(lsID)
		b.i64(lcID)
		b.i64(now)
		s.wal.Append(b)
	}
	return dst, nil
}

func (s *Store) AppendReportsJSON(dst []byte, userID, lsID int64) ([]byte, error) {
	ls := s.getLivestreamEntry(lsID)
	if ls == nil {
		return dst, errNotFound
	}
	if ls.UserID != userID {
		return dst, errForbidden
	}
	ls.mu.RLock()
	if len(ls.Reports) == 0 {
		ls.mu.RUnlock()
		return append(dst, '[', ']'), nil
	}
	reps := make([]*ReportEntry, len(ls.Reports))
	copy(reps, ls.Reports)
	ls.mu.RUnlock()

	var lsBuf [1024]byte
	lsJSON := ls.AppendJSON(lsBuf[:0])

	s.mu.RLock()
	dst = append(dst, '[')
	written := 0
	for _, re := range reps {
		lc := re.Livecomment
		if lc == nil {
			continue
		}
		if written > 0 {
			dst = append(dst, ',')
		}
		dst = append(dst, `{"id":`...)
		dst = strconv.AppendInt(dst, re.ID, 10)
		dst = append(dst, `,"reporter":`...)
		dst = s.users[re.UserID].AppendJSON(dst)
		dst = append(dst, `,"livecomment":`...)
		dst = appendLivecommentJSON(dst, lc, s.users[lc.UserID], lsJSON)
		dst = append(dst, `,"created_at":`...)
		dst = strconv.AppendInt(dst, re.CreatedAt, 10)
		dst = append(dst, '}')
		written++
	}
	s.mu.RUnlock()
	dst = append(dst, ']')
	return dst, nil
}

func (s *Store) AppendReactionsJSON(dst []byte, lsID int64, hasLimit bool, limit int) []byte {
	ls := s.getLivestreamEntry(lsID)
	if ls == nil {
		return append(dst, '[', ']')
	}
	ls.mu.RLock()
	list := ls.Reactions
	n := len(list)
	if hasLimit && limit < n {
		if limit < 0 {
			n = 0
		} else {
			n = limit
		}
	}
	if n == 0 {
		ls.mu.RUnlock()
		return append(dst, '[', ']')
	}
	var stackTmp [64]*ReactionEntry
	var snap []*ReactionEntry
	if n <= len(stackTmp) {
		snap = stackTmp[:n]
	} else {
		snap = make([]*ReactionEntry, n)
	}
	for i := 0; i < n; i++ {
		snap[i] = list[len(list)-1-i]
	}
	ls.mu.RUnlock()

	var lsBuf [1024]byte
	lsJSON := ls.AppendJSON(lsBuf[:0])

	s.mu.RLock()
	dst = append(dst, '[')
	for i, rx := range snap {
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = appendReactionJSON(dst, rx, s.users[rx.UserID], lsJSON)
	}
	s.mu.RUnlock()
	dst = append(dst, ']')
	return dst
}

func (s *Store) PostReactionJSON(dst []byte, userID, lsID int64, emojiName string) ([]byte, error) {
	ls := s.getLivestreamEntry(lsID)
	if ls == nil {
		return dst, errNotFound
	}
	now := time.Now().Unix()
	rxID := s.nextReactionID.Add(1) - 1
	rxe := &ReactionEntry{
		ID:           rxID,
		EmojiName:    emojiName,
		EscapedEmoji: appendJSONString(nil, emojiName),
		UserID:       userID,
		LivestreamID: lsID,
		CreatedAt:    now,
	}

	ls.mu.Lock()
	ls.Reactions = append(ls.Reactions, rxe)
	ls.mu.Unlock()

	ls.totalReactions.Add(1)
	owner := ls.Owner
	owner.totalReactions.Add(1)
	owner.emojiMu.Lock()
	owner.emojiCounts[emojiName]++
	owner.emojiMu.Unlock()

	var lsBuf [1024]byte
	lsJSON := ls.AppendJSON(lsBuf[:0])
	u := s.getUser(userID)
	dst = appendReactionJSON(dst, rxe, u, lsJSON)

	if s.wal != nil {
		var b walBuf
		b.u8(walOpReaction)
		b.i64(rxID)
		b.i64(userID)
		b.i64(lsID)
		b.str(emojiName)
		b.i64(now)
		s.wal.Append(b)
	}
	return dst, nil
}

func (s *Store) GetUserStatistics(username string) (UserStatistics, error) {
	s.mu.RLock()
	target := s.usersByName[username]
	if target == nil {
		s.mu.RUnlock()
		return UserStatistics{}, errBadRequest
	}
	targetScore := target.Score()
	var rank int64 = 1
	for i := 1; i < len(s.users); i++ {
		u := s.users[i]
		if u == nil || u == target {
			continue
		}
		sc := u.Score()
		if sc > targetScore || (sc == targetScore && u.Name > target.Name) {
			rank++
		}
	}
	s.mu.RUnlock()

	var favEmoji string
	var favCount int64
	target.emojiMu.Lock()
	for em, cnt := range target.emojiCounts {
		if cnt > favCount || (cnt == favCount && em > favEmoji) {
			favCount = cnt
			favEmoji = em
		}
	}
	target.emojiMu.Unlock()

	return UserStatistics{
		Rank:              rank,
		ViewersCount:      target.viewersCount.Load(),
		TotalReactions:    target.totalReactions.Load(),
		TotalLivecomments: target.totalLivecomments.Load(),
		TotalTip:          target.totalTip.Load(),
		FavoriteEmoji:     favEmoji,
	}, nil
}

func (s *Store) GetLivestreamStatistics(lsID int64) (LivestreamStatistics, error) {
	s.mu.RLock()
	if lsID <= 0 || int(lsID) >= len(s.livestreams) || s.livestreams[lsID] == nil {
		s.mu.RUnlock()
		return LivestreamStatistics{}, errBadRequest
	}
	target := s.livestreams[lsID]
	targetScore := target.Score()
	var rank int64 = 1
	for i := 1; i < len(s.livestreams); i++ {
		l := s.livestreams[i]
		if l == nil || l == target {
			continue
		}
		sc := l.Score()
		if sc > targetScore || (sc == targetScore && l.ID > target.ID) {
			rank++
		}
	}
	s.mu.RUnlock()

	return LivestreamStatistics{
		Rank:           rank,
		ViewersCount:   target.viewersCount.Load(),
		TotalReactions: target.totalReactions.Load(),
		TotalReports:   target.totalReports.Load(),
		MaxTip:         target.maxTip.Load(),
	}, nil
}

func (s *Store) GetPaymentResult() PaymentResult {
	return PaymentResult{TotalTip: s.globalTotalTip.Load()}
}

// --- WAL Replay (single-threaded at startup) ---

func (s *Store) applyWALRecord(rec []byte) error {
	r := walReader{b: rec}
	op, ok := r.u8()
	if !ok {
		return nil
	}
	switch op {
	case walOpRegister:
		uid, _ := r.i64()
		tid, _ := r.i64()
		name, _ := r.str()
		dname, _ := r.str()
		desc, _ := r.str()
		pw, _ := r.str()
		dm, _ := r.bool()
		ue := &UserEntry{
			ID:             uid,
			Name:           name,
			DisplayName:    dname,
			Description:    desc,
			HashedPassword: pw,
			ThemeID:        tid,
			DarkMode:       dm,
			jsonPrefix:     buildUserJSONPrefix(uid, name, dname, desc, tid, dm),
			emojiCounts:    make(map[string]int64),
		}
		ue.SetIconHash(s.snap.FallbackHash)
		for int64(len(s.users)) <= uid {
			s.users = append(s.users, nil)
		}
		s.users[uid] = ue
		s.usersByName[name] = ue
		if uid >= s.nextUserID {
			s.nextUserID = uid + 1
		}
		if tid >= s.nextThemeID {
			s.nextThemeID = tid + 1
		}
		if s.dns != nil {
			s.dns.AddName(name)
		}

	case walOpSetIcon:
		iconID, _ := r.i64()
		uid, _ := r.i64()
		hash, _ := r.str()
		if iconID >= s.nextIconID.Load() {
			s.nextIconID.Store(iconID + 1)
		}
		if uid > 0 && int(uid) < len(s.users) && s.users[uid] != nil {
			s.users[uid].SetIconHash(hash)
		}

	case walOpReserve:
		lsID, _ := r.i64()
		uid, _ := r.i64()
		title, _ := r.str()
		desc, _ := r.str()
		pl, _ := r.str()
		th, _ := r.str()
		startAt, _ := r.i64()
		endAt, _ := r.i64()
		nTags, _ := r.i64()
		tids := make([]int64, nTags)
		for i := int64(0); i < nTags; i++ {
			tids[i], _ = r.i64()
		}
		lo, hi := s.slotRangeLocked(startAt, endAt)
		for i := lo; i <= hi; i++ {
			s.slots[i]--
		}
		tags := s.resolveTags(tids)
		le := &LivestreamEntry{
			ID:           lsID,
			UserID:       uid,
			Owner:        s.users[uid],
			Title:        title,
			Description:  desc,
			PlaylistUrl:  pl,
			ThumbnailUrl: th,
			StartAt:      startAt,
			EndAt:        endAt,
			TagIDs:       tids,
			jsonMid:      buildLivestreamJSONMid(title, desc, pl, th, tags, startAt, endAt),
			Viewers:      make(map[int64]int64),
		}
		for int64(len(s.livestreams)) <= lsID {
			s.livestreams = append(s.livestreams, nil)
		}
		s.livestreams[lsID] = le
		s.livestreamsByUser[uid] = append(s.livestreamsByUser[uid], le)
		for _, tid := range tids {
			s.livestreamsByTag[tid] = append(s.livestreamsByTag[tid], le)
		}
		if lsID >= s.nextLivestreamID {
			s.nextLivestreamID = lsID + 1
		}

	case walOpEnter:
		uid, _ := r.i64()
		lsID, _ := r.i64()
		_, _ = r.i64()
		if lsID > 0 && int(lsID) < len(s.livestreams) && s.livestreams[lsID] != nil {
			ls := s.livestreams[lsID]
			ls.Viewers[uid]++
			ls.viewersCount.Add(1)
			if ls.Owner != nil {
				ls.Owner.viewersCount.Add(1)
			}
		}

	case walOpExit:
		uid, _ := r.i64()
		lsID, _ := r.i64()
		if lsID > 0 && int(lsID) < len(s.livestreams) && s.livestreams[lsID] != nil {
			ls := s.livestreams[lsID]
			if c := ls.Viewers[uid]; c > 0 {
				delete(ls.Viewers, uid)
				ls.viewersCount.Add(-c)
				if ls.Owner != nil {
					ls.Owner.viewersCount.Add(-c)
				}
			}
		}

	case walOpLivecomment:
		lcID, _ := r.i64()
		uid, _ := r.i64()
		lsID, _ := r.i64()
		comment, _ := r.str()
		tip, _ := r.i64()
		createdAt, _ := r.i64()
		if lcID >= s.nextLCID.Load() {
			s.nextLCID.Store(lcID + 1)
		}
		lce := &LivecommentEntry{
			ID:             lcID,
			UserID:         uid,
			LivestreamID:   lsID,
			Comment:        comment,
			EscapedComment: appendJSONString(nil, comment),
			Tip:            tip,
			CreatedAt:      createdAt,
		}
		s.livecommentsByID[lcID] = lce
		if lsID > 0 && int(lsID) < len(s.livestreams) && s.livestreams[lsID] != nil {
			ls := s.livestreams[lsID]
			ls.Livecomments = append(ls.Livecomments, lce)
			ls.totalTip.Add(tip)
			if tip > ls.maxTip.Load() {
				ls.maxTip.Store(tip)
			}
			ls.Owner.totalTip.Add(tip)
			ls.Owner.totalLivecomments.Add(1)
		}
		s.globalTotalTip.Add(tip)

	case walOpReaction:
		rxID, _ := r.i64()
		uid, _ := r.i64()
		lsID, _ := r.i64()
		emoji, _ := r.str()
		createdAt, _ := r.i64()
		if rxID >= s.nextReactionID.Load() {
			s.nextReactionID.Store(rxID + 1)
		}
		rxe := &ReactionEntry{
			ID:           rxID,
			EmojiName:    emoji,
			EscapedEmoji: appendJSONString(nil, emoji),
			UserID:       uid,
			LivestreamID: lsID,
			CreatedAt:    createdAt,
		}
		if lsID > 0 && int(lsID) < len(s.livestreams) && s.livestreams[lsID] != nil {
			ls := s.livestreams[lsID]
			ls.Reactions = append(ls.Reactions, rxe)
			ls.totalReactions.Add(1)
			ls.Owner.totalReactions.Add(1)
			ls.Owner.emojiCounts[emoji]++
		}

	case walOpReport:
		repID, _ := r.i64()
		uid, _ := r.i64()
		lsID, _ := r.i64()
		lcID, _ := r.i64()
		createdAt, _ := r.i64()
		if repID >= s.nextReportID.Load() {
			s.nextReportID.Store(repID + 1)
		}
		if lsID > 0 && int(lsID) < len(s.livestreams) && s.livestreams[lsID] != nil {
			ls := s.livestreams[lsID]
			ls.Reports = append(ls.Reports, &ReportEntry{
				ID:            repID,
				UserID:        uid,
				LivestreamID:  lsID,
				LivecommentID: lcID,
				Livecomment:   s.livecommentsByID[lcID],
				CreatedAt:     createdAt,
			})
			ls.totalReports.Add(1)
		}

	case walOpModerate:
		wordID, _ := r.i64()
		uid, _ := r.i64()
		lsID, _ := r.i64()
		word, _ := r.str()
		createdAt, _ := r.i64()
		if wordID >= s.nextNGWordID.Load() {
			s.nextNGWordID.Store(wordID + 1)
		}
		if lsID > 0 && int(lsID) < len(s.livestreams) && s.livestreams[lsID] != nil {
			ls := s.livestreams[lsID]
			ls.NGWords = append(ls.NGWords, &NGWord{
				ID:           wordID,
				UserID:       uid,
				LivestreamID: lsID,
				Word:         word,
				CreatedAt:    createdAt,
			})
			deletedIDs := s.filterModeratedCommentsLocked(ls)
			for _, id := range deletedIDs {
				delete(s.livecommentsByID, id)
			}
		}
	}
	return nil
}

func mapStoreErr(err error) int {
	switch {
	case errors.Is(err, errNotFound):
		return http.StatusNotFound
	case errors.Is(err, errForbidden):
		return http.StatusForbidden
	case errors.Is(err, errBadRequest), errors.Is(err, errSpamDetected):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}
