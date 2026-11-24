package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

const iconDir = "/home/isucon/webapp/icons"

type UserCacheEntry struct {
	ID             int64
	Name           string
	DisplayName    string
	Description    string
	HashedPassword string
	Theme          Theme

	jsonMu     sync.RWMutex
	cachedHash string
	cachedJSON []byte
}

func (u *UserCacheEntry) getJSON() []byte {
	h := getIconHash(u.ID)
	u.jsonMu.RLock()
	if u.cachedJSON != nil && u.cachedHash == h {
		b := u.cachedJSON
		u.jsonMu.RUnlock()
		return b
	}
	u.jsonMu.RUnlock()

	obj := User{
		ID:          u.ID,
		Name:        u.Name,
		DisplayName: u.DisplayName,
		Description: u.Description,
		Theme:       u.Theme,
		IconHash:    h,
	}
	b, _ := json.Marshal(obj)

	u.jsonMu.Lock()
	u.cachedHash = h
	u.cachedJSON = b
	u.jsonMu.Unlock()
	return b
}

type LivestreamCacheEntry struct {
	Model LivestreamModel
	Tags  []Tag

	jsonMu     sync.RWMutex
	cachedHash string
	cachedJSON []byte
}

func (e *LivestreamCacheEntry) getJSON() ([]byte, bool) {
	owner, ok := getUserByID(e.Model.UserID)
	if !ok {
		return nil, false
	}
	h := getIconHash(owner.ID)
	e.jsonMu.RLock()
	if e.cachedJSON != nil && e.cachedHash == h {
		b := e.cachedJSON
		e.jsonMu.RUnlock()
		return b, true
	}
	e.jsonMu.RUnlock()

	ownerJSON := owner.getJSON()
	tags := e.Tags
	if tags == nil {
		tags = []Tag{}
	}
	type lsWire struct {
		ID           int64           `json:"id"`
		Owner        json.RawMessage `json:"owner"`
		Title        string          `json:"title"`
		Description  string          `json:"description"`
		PlaylistUrl  string          `json:"playlist_url"`
		ThumbnailUrl string          `json:"thumbnail_url"`
		Tags         []Tag           `json:"tags"`
		StartAt      int64           `json:"start_at"`
		EndAt        int64           `json:"end_at"`
	}
	b, _ := json.Marshal(lsWire{
		ID:           e.Model.ID,
		Owner:        ownerJSON,
		Title:        e.Model.Title,
		Description:  e.Model.Description,
		PlaylistUrl:  e.Model.PlaylistUrl,
		ThumbnailUrl: e.Model.ThumbnailUrl,
		Tags:         tags,
		StartAt:      e.Model.StartAt,
		EndAt:        e.Model.EndAt,
	})

	e.jsonMu.Lock()
	e.cachedHash = h
	e.cachedJSON = b
	e.jsonMu.Unlock()
	return b, true
}

type LSStats struct {
	reactions    int64
	totalTip     int64
	livecomments int64
	viewers      int64
	reports      int64
	maxTip       int64
}

type UserStatsState struct {
	reactions    int64
	totalTip     int64
	livecomments int64
	viewers      int64
	emojiCounts  map[string]int64
	favEmoji     string
	favCount     int64
}

var (
	fallbackImageBytes []byte
	fallbackIconHash   string

	nextUserID        atomic.Int64
	nextThemeID       atomic.Int64
	nextIconID        atomic.Int64
	nextLivestreamID  atomic.Int64
	nextLivecommentID atomic.Int64
	nextReactionID    atomic.Int64
	nextReportID      atomic.Int64
	nextNGWordID      atomic.Int64

	tagsMu      sync.RWMutex
	tagsList    []*Tag
	tagsByID    map[int64]Tag
	tagIDByName map[string]int64
	tagsJSON    []byte

	usersMu     sync.RWMutex
	usersByID   map[int64]*UserCacheEntry
	usersByName map[string]*UserCacheEntry

	iconsMu           sync.RWMutex
	iconHashByUserID  map[int64]string
	iconImageByUserID map[int64][]byte

	slotsMu    sync.Mutex
	slotsSlice []ReservationSlotModel

	livestreamsMu       sync.RWMutex
	livestreamByID      map[int64]*LivestreamCacheEntry
	livestreamsByUserID map[int64][]*LivestreamCacheEntry
	livestreamsByTagID  map[int64][]*LivestreamCacheEntry
	allLivestreams      []*LivestreamCacheEntry

	searchCacheVer atomic.Uint64
	searchCacheMu  sync.RWMutex
	searchCacheMap = make(map[string]searchCacheItem)
)

type searchCacheItem struct {
	ver  uint64
	data []byte
}

func bumpSearchCacheVer() {
	searchCacheVer.Add(1)
}

func getCachedSearch(key string) ([]byte, bool) {
	v := searchCacheVer.Load()
	searchCacheMu.RLock()
	it, ok := searchCacheMap[key]
	searchCacheMu.RUnlock()
	if ok && it.ver == v {
		return it.data, true
	}
	return nil, false
}

func setCachedSearch(key string, ver uint64, data []byte) {
	searchCacheMu.Lock()
	if searchCacheVer.Load() == ver {
		if len(searchCacheMap) > 512 {
			searchCacheMap = make(map[string]searchCacheItem, 128)
		}
		searchCacheMap[key] = searchCacheItem{ver: ver, data: data}
	}
	searchCacheMu.Unlock()
}

var (

	ngWordsMu           sync.RWMutex
	ngWordsByLivestream map[int64][]*NGWord

	lcMu             sync.RWMutex
	livecommentsByLS map[int64][]*LivecommentModel
	livecommentByID  map[int64]*LivecommentModel

	rxMu          sync.RWMutex
	reactionsByLS map[int64][]*ReactionModel

	repMu       sync.RWMutex
	reportsByLS map[int64][]*LivecommentReportModel

	viewersMu            sync.Mutex
	viewersCountByUserLS map[[2]int64]int64

	statsMu      sync.RWMutex
	lsStatsMap   map[int64]*LSStats
	userStatsMap map[int64]*UserStatsState
)

func initFallbackImage() error {
	b, err := os.ReadFile(fallbackImage)
	if err != nil {
		return err
	}
	fallbackImageBytes = b
	sum := sha256.Sum256(b)
	fallbackIconHash = fmt.Sprintf("%x", sum)
	if err := os.MkdirAll(iconDir, 0755); err != nil {
		return err
	}
	return nil
}

func iconFilePath(userID int64) string {
	return filepath.Join(iconDir, strconv.FormatInt(userID, 10))
}

func reloadAllCaches(ctx context.Context) error {
	// 1. Load tags
	var tModels []TagModel
	if err := dbConn.SelectContext(ctx, &tModels, "SELECT id, name FROM tags"); err != nil {
		return fmt.Errorf("reload tags: %w", err)
	}
	newTagsList := make([]*Tag, len(tModels))
	newTagsByID := make(map[int64]Tag, len(tModels))
	newTagIDByName := make(map[string]int64, len(tModels))
	for i := range tModels {
		t := Tag{ID: tModels[i].ID, Name: tModels[i].Name}
		newTagsList[i] = &Tag{ID: t.ID, Name: t.Name}
		newTagsByID[t.ID] = t
		newTagIDByName[t.Name] = t.ID
	}
	newTagsJSON, err := json.Marshal(&TagsResponse{Tags: newTagsList})
	if err != nil {
		return fmt.Errorf("marshal tags: %w", err)
	}

	// 2. Load users + themes
	type userThemeRow struct {
		ID             int64  `db:"id"`
		Name           string `db:"name"`
		DisplayName    string `db:"display_name"`
		Description    string `db:"description"`
		HashedPassword string `db:"password"`
		ThemeID        int64  `db:"theme_id"`
		DarkMode       bool   `db:"dark_mode"`
	}
	var utRows []userThemeRow
	if err := dbConn.SelectContext(ctx, &utRows, `
		SELECT u.id, u.name, u.display_name, u.description, u.password, t.id AS theme_id, t.dark_mode
		FROM users u
		INNER JOIN themes t ON t.user_id = u.id
	`); err != nil {
		return fmt.Errorf("reload users: %w", err)
	}
	newUsersByID := make(map[int64]*UserCacheEntry, len(utRows)+5000)
	newUsersByName := make(map[string]*UserCacheEntry, len(utRows)+5000)
	var maxUID, maxTID int64
	for i := range utRows {
		r := &utRows[i]
		if r.ID > maxUID {
			maxUID = r.ID
		}
		if r.ThemeID > maxTID {
			maxTID = r.ThemeID
		}
		e := &UserCacheEntry{
			ID:             r.ID,
			Name:           r.Name,
			DisplayName:    r.DisplayName,
			Description:    r.Description,
			HashedPassword: r.HashedPassword,
			Theme: Theme{
				ID:       r.ThemeID,
				DarkMode: r.DarkMode,
			},
		}
		newUsersByID[e.ID] = e
		newUsersByName[e.Name] = e
	}

	// 3. Load icons
	type iconHashRow struct {
		ID     int64  `db:"id"`
		UserID int64  `db:"user_id"`
		Hash   string `db:"hash"`
	}
	var ihRows []iconHashRow
	if err := dbConn.SelectContext(ctx, &ihRows, "SELECT id, user_id, hash FROM icons"); err != nil {
		return fmt.Errorf("reload icons: %w", err)
	}
	newIconHashByUserID := make(map[int64]string, len(ihRows)+5000)
	newIconImageByUserID := make(map[int64][]byte, 1024)
	var maxIconID int64
	for _, r := range ihRows {
		if r.ID > maxIconID {
			maxIconID = r.ID
		}
		if r.Hash != "" {
			newIconHashByUserID[r.UserID] = r.Hash
		}
	}

	// 4. Load reservation_slots
	var newSlots []ReservationSlotModel
	if err := dbConn.SelectContext(ctx, &newSlots, "SELECT id, slot, start_at, end_at FROM reservation_slots ORDER BY start_at ASC"); err != nil {
		return fmt.Errorf("reload reservation_slots: %w", err)
	}

	// 5. Load livestreams + livestream_tags
	var lsModels []LivestreamModel
	if err := dbConn.SelectContext(ctx, &lsModels, "SELECT * FROM livestreams ORDER BY id ASC"); err != nil {
		return fmt.Errorf("reload livestreams: %w", err)
	}
	var lstModels []LivestreamTagModel
	if err := dbConn.SelectContext(ctx, &lstModels, "SELECT id, livestream_id, tag_id FROM livestream_tags ORDER BY id ASC"); err != nil {
		return fmt.Errorf("reload livestream_tags: %w", err)
	}
	tagsPerLS := make(map[int64][]Tag, len(lsModels))
	for _, lt := range lstModels {
		if t, ok := newTagsByID[lt.TagID]; ok {
			tagsPerLS[lt.LivestreamID] = append(tagsPerLS[lt.LivestreamID], t)
		}
	}

	newLSByID := make(map[int64]*LivestreamCacheEntry, len(lsModels)+3000)
	newLSByUser := make(map[int64][]*LivestreamCacheEntry, len(utRows)+3000)
	newLSByTag := make(map[int64][]*LivestreamCacheEntry, len(tModels))
	newAllLS := make([]*LivestreamCacheEntry, len(lsModels))
	var maxLSID int64
	for i := range lsModels {
		m := lsModels[i]
		if m.ID > maxLSID {
			maxLSID = m.ID
		}
		ts := tagsPerLS[m.ID]
		if ts == nil {
			ts = []Tag{}
		}
		entry := &LivestreamCacheEntry{
			Model: m,
			Tags:  ts,
		}
		newLSByID[m.ID] = entry
		newLSByUser[m.UserID] = append(newLSByUser[m.UserID], entry)
		for _, t := range ts {
			newLSByTag[t.ID] = append(newLSByTag[t.ID], entry)
		}
		newAllLS[i] = entry
	}

	// 6. Load ng_words
	var nwList []NGWord
	if err := dbConn.SelectContext(ctx, &nwList, "SELECT id, user_id, livestream_id, word, created_at FROM ng_words ORDER BY created_at DESC, id DESC"); err != nil {
		return fmt.Errorf("reload ng_words: %w", err)
	}
	newNGWords := make(map[int64][]*NGWord, len(lsModels))
	var maxNWID int64
	for i := range nwList {
		w := nwList[i]
		w.LowerWord = strings.ToLower(w.Word)
		w.HasWildcard = strings.ContainsAny(w.Word, "%_\\")
		if w.ID > maxNWID {
			maxNWID = w.ID
		}
		newNGWords[w.LivestreamID] = append(newNGWords[w.LivestreamID], &w)
	}

	// 7. Load livecomments
	var lcList []LivecommentModel
	if err := dbConn.SelectContext(ctx, &lcList, "SELECT id, user_id, livestream_id, comment, tip, created_at FROM livecomments ORDER BY created_at ASC, id ASC"); err != nil {
		return fmt.Errorf("reload livecomments: %w", err)
	}
	newLCByLS := make(map[int64][]*LivecommentModel, len(lsModels))
	newLCByID := make(map[int64]*LivecommentModel, len(lcList)+30000)
	var maxLCID int64
	for i := range lcList {
		m := lcList[i]
		m.LowerComment = strings.ToLower(m.Comment)
		if m.ID > maxLCID {
			maxLCID = m.ID
		}
		p := &m
		newLCByLS[m.LivestreamID] = append(newLCByLS[m.LivestreamID], p)
		newLCByID[m.ID] = p
	}

	// 8. Load reactions
	var rxList []ReactionModel
	if err := dbConn.SelectContext(ctx, &rxList, "SELECT id, emoji_name, user_id, livestream_id, created_at FROM reactions ORDER BY created_at ASC, id ASC"); err != nil {
		return fmt.Errorf("reload reactions: %w", err)
	}
	newRXByLS := make(map[int64][]*ReactionModel, len(lsModels))
	var maxRXID int64
	for i := range rxList {
		m := rxList[i]
		if m.ID > maxRXID {
			maxRXID = m.ID
		}
		p := &m
		newRXByLS[m.LivestreamID] = append(newRXByLS[m.LivestreamID], p)
	}

	// 9. Load livecomment_reports
	var repList []LivecommentReportModel
	if err := dbConn.SelectContext(ctx, &repList, "SELECT id, user_id, livestream_id, livecomment_id, created_at FROM livecomment_reports ORDER BY id ASC"); err != nil {
		return fmt.Errorf("reload reports: %w", err)
	}
	newRepByLS := make(map[int64][]*LivecommentReportModel, 64)
	var maxRepID int64
	for i := range repList {
		m := repList[i]
		if m.ID > maxRepID {
			maxRepID = m.ID
		}
		p := &m
		newRepByLS[m.LivestreamID] = append(newRepByLS[m.LivestreamID], p)
	}

	// 10. Load livestream_viewers_history
	var vList []LivestreamViewerModel
	if err := dbConn.SelectContext(ctx, &vList, "SELECT user_id, livestream_id, created_at FROM livestream_viewers_history"); err != nil {
		return fmt.Errorf("reload viewers: %w", err)
	}
	newViewers := make(map[[2]int64]int64, len(vList)+2048)
	for _, v := range vList {
		newViewers[[2]int64{v.UserID, v.LivestreamID}]++
	}

	// 11. Build initial stats from loaded slices (0 extra DB queries!)
	newLSStats := make(map[int64]*LSStats, len(lsModels)+3000)
	newUserStats := make(map[int64]*UserStatsState, len(utRows)+5000)

	getLSStat := func(lsID int64) *LSStats {
		s := newLSStats[lsID]
		if s == nil {
			s = &LSStats{}
			newLSStats[lsID] = s
		}
		return s
	}
	getUStat := func(uID int64) *UserStatsState {
		s := newUserStats[uID]
		if s == nil {
			s = &UserStatsState{emojiCounts: make(map[string]int64, 4)}
			newUserStats[uID] = s
		}
		return s
	}

	for _, r := range rxList {
		ls, ok := newLSByID[r.LivestreamID]
		if !ok {
			continue
		}
		getLSStat(r.LivestreamID).reactions++
		us := getUStat(ls.Model.UserID)
		us.reactions++
		nc := us.emojiCounts[r.EmojiName] + 1
		us.emojiCounts[r.EmojiName] = nc
		if nc > us.favCount || (nc == us.favCount && r.EmojiName > us.favEmoji) {
			us.favCount = nc
			us.favEmoji = r.EmojiName
		}
	}

	for _, c := range lcList {
		ls, ok := newLSByID[c.LivestreamID]
		if !ok {
			continue
		}
		lss := getLSStat(c.LivestreamID)
		lss.livecomments++
		lss.totalTip += c.Tip
		if c.Tip > lss.maxTip {
			lss.maxTip = c.Tip
		}
		us := getUStat(ls.Model.UserID)
		us.livecomments++
		us.totalTip += c.Tip
	}

	for _, v := range vList {
		ls, ok := newLSByID[v.LivestreamID]
		if !ok {
			continue
		}
		getLSStat(v.LivestreamID).viewers++
		getUStat(ls.Model.UserID).viewers++
	}

	for _, r := range repList {
		getLSStat(r.LivestreamID).reports++
	}

	// Update ID generators (stride by 2 on shards to guarantee collision-free IDs across shard0/shard1!)
	nextUserID.Store(maxUID)
	nextThemeID.Store(maxTID)
	nextIconID.Store(maxIconID)
	nextLivestreamID.Store(maxLSID)
	if nodeRole == roleShard0 {
		// Even IDs > max
		nextLivecommentID.Store(maxLCID + (maxLCID & 1))
		nextReactionID.Store(maxRXID + (maxRXID & 1))
		nextReportID.Store(maxRepID + (maxRepID & 1))
		nextNGWordID.Store(maxNWID + (maxNWID & 1))
	} else if nodeRole == roleShard1 {
		// Odd IDs > max
		baseLC := maxLCID
		if baseLC&1 == 0 {
			baseLC--
		}
		nextLivecommentID.Store(baseLC + 2)
		baseRX := maxRXID
		if baseRX&1 == 0 {
			baseRX--
		}
		nextReactionID.Store(baseRX + 2)
		baseRep := maxRepID
		if baseRep&1 == 0 {
			baseRep--
		}
		nextReportID.Store(baseRep + 2)
		baseNW := maxNWID
		if baseNW&1 == 0 {
			baseNW--
		}
		nextNGWordID.Store(baseNW + 2)
	} else {
		nextLivecommentID.Store(maxLCID)
		nextReactionID.Store(maxRXID)
		nextReportID.Store(maxRepID)
		nextNGWordID.Store(maxNWID)
	}

	// Swap all caches
	tagsMu.Lock()
	tagsList = newTagsList
	tagsByID = newTagsByID
	tagIDByName = newTagIDByName
	tagsJSON = newTagsJSON
	tagsMu.Unlock()

	usersMu.Lock()
	usersByID = newUsersByID
	usersByName = newUsersByName
	usersMu.Unlock()

	iconsMu.Lock()
	iconHashByUserID = newIconHashByUserID
	iconImageByUserID = newIconImageByUserID
	iconsMu.Unlock()

	slotsMu.Lock()
	slotsSlice = newSlots
	slotsMu.Unlock()

	livestreamsMu.Lock()
	livestreamByID = newLSByID
	livestreamsByUserID = newLSByUser
	livestreamsByTagID = newLSByTag
	allLivestreams = newAllLS
	livestreamsMu.Unlock()

	ngWordsMu.Lock()
	ngWordsByLivestream = newNGWords
	ngWordsMu.Unlock()

	lcMu.Lock()
	livecommentsByLS = newLCByLS
	livecommentByID = newLCByID
	lcMu.Unlock()

	rxMu.Lock()
	reactionsByLS = newRXByLS
	rxMu.Unlock()

	repMu.Lock()
	reportsByLS = newRepByLS
	repMu.Unlock()

	viewersMu.Lock()
	viewersCountByUserLS = newViewers
	viewersMu.Unlock()

	statsMu.Lock()
	lsStatsMap = newLSStats
	userStatsMap = newUserStats
	statsMu.Unlock()

	setIconCacheVersion := func() { bumpSearchCacheVer() }
	_ = setIconCacheVersion
	bumpSearchCacheVer()

	return nil
}

// Incremental stats updates
func statsOnReaction(lsID, ownerID int64, emoji string) {
	statsMu.Lock()
	lss := lsStatsMap[lsID]
	if lss == nil {
		lss = &LSStats{}
		lsStatsMap[lsID] = lss
	}
	lss.reactions++

	us := userStatsMap[ownerID]
	if us == nil {
		us = &UserStatsState{emojiCounts: make(map[string]int64, 4)}
		userStatsMap[ownerID] = us
	}
	us.reactions++
	nc := us.emojiCounts[emoji] + 1
	us.emojiCounts[emoji] = nc
	if nc > us.favCount || (nc == us.favCount && emoji > us.favEmoji) {
		us.favCount = nc
		us.favEmoji = emoji
	}
	statsMu.Unlock()
}

func statsOnLivecomment(lsID, ownerID, tip int64) {
	statsMu.Lock()
	lss := lsStatsMap[lsID]
	if lss == nil {
		lss = &LSStats{}
		lsStatsMap[lsID] = lss
	}
	lss.livecomments++
	lss.totalTip += tip
	if tip > lss.maxTip {
		lss.maxTip = tip
	}

	us := userStatsMap[ownerID]
	if us == nil {
		us = &UserStatsState{emojiCounts: make(map[string]int64, 4)}
		userStatsMap[ownerID] = us
	}
	us.livecomments++
	us.totalTip += tip
	statsMu.Unlock()
}

func statsOnModerateDelete(lsID, ownerID, delCount, delTip int64, recomputedMaxTip int64, updateMax bool) {
	statsMu.Lock()
	lss := lsStatsMap[lsID]
	if lss != nil {
		lss.livecomments -= delCount
		lss.totalTip -= delTip
		if updateMax {
			lss.maxTip = recomputedMaxTip
		}
	}
	us := userStatsMap[ownerID]
	if us != nil {
		us.livecomments -= delCount
		us.totalTip -= delTip
	}
	statsMu.Unlock()
}

func statsOnViewerDelta(lsID, ownerID, delta int64) {
	if delta == 0 {
		return
	}
	statsMu.Lock()
	lss := lsStatsMap[lsID]
	if lss == nil {
		lss = &LSStats{}
		lsStatsMap[lsID] = lss
	}
	lss.viewers += delta

	us := userStatsMap[ownerID]
	if us == nil {
		us = &UserStatsState{emojiCounts: make(map[string]int64, 4)}
		userStatsMap[ownerID] = us
	}
	us.viewers += delta
	statsMu.Unlock()
}

func statsOnReport(lsID int64) {
	statsMu.Lock()
	lss := lsStatsMap[lsID]
	if lss == nil {
		lss = &LSStats{}
		lsStatsMap[lsID] = lss
	}
	lss.reports++
	statsMu.Unlock()
}

func allocShardedID(counter *atomic.Int64) int64 {
	if nodeRole == roleShard0 || nodeRole == roleShard1 {
		return counter.Add(2)
	}
	return counter.Add(1)
}

func getTagsJSON() []byte {
	tagsMu.RLock()
	b := tagsJSON
	tagsMu.RUnlock()
	return b
}

func getTagIDByName(name string) (int64, bool) {
	tagsMu.RLock()
	id, ok := tagIDByName[name]
	tagsMu.RUnlock()
	return id, ok
}

func getTagByID(id int64) (Tag, bool) {
	tagsMu.RLock()
	t, ok := tagsByID[id]
	tagsMu.RUnlock()
	return t, ok
}

func getUserByID(id int64) (*UserCacheEntry, bool) {
	usersMu.RLock()
	u, ok := usersByID[id]
	usersMu.RUnlock()
	return u, ok
}

func getUserByName(name string) (*UserCacheEntry, bool) {
	usersMu.RLock()
	u, ok := usersByName[name]
	usersMu.RUnlock()
	return u, ok
}

// tryReserveAndAddUser atomically checks duplicate username and adds the user.
func tryReserveAndAddUser(req PostUserRequest, hashedPassword string) (*UserCacheEntry, bool) {
	usersMu.Lock()
	if _, exists := usersByName[req.Name]; exists {
		usersMu.Unlock()
		return nil, false
	}
	userID := nextUserID.Add(1)
	themeID := nextThemeID.Add(1)
	u := &UserCacheEntry{
		ID:             userID,
		Name:           req.Name,
		DisplayName:    req.DisplayName,
		Description:    req.Description,
		HashedPassword: hashedPassword,
		Theme: Theme{
			ID:       themeID,
			DarkMode: req.Theme.DarkMode,
		},
	}
	usersByID[u.ID] = u
	usersByName[u.Name] = u
	usersMu.Unlock()
	return u, true
}

func getIconHash(userID int64) string {
	iconsMu.RLock()
	h, ok := iconHashByUserID[userID]
	iconsMu.RUnlock()
	if !ok {
		return fallbackIconHash
	}
	return h
}

func setIconCache(userID int64, hash string, image []byte) {
	iconsMu.Lock()
	iconHashByUserID[userID] = hash
	if len(iconImageByUserID) > 6000 {
		iconImageByUserID = make(map[int64][]byte, 1024)
	}
	iconImageByUserID[userID] = image
	iconsMu.Unlock()

	// Only invalidate search cache if this user owns at least one livestream!
	livestreamsMu.RLock()
	hasLS := len(livestreamsByUserID[userID]) > 0
	livestreamsMu.RUnlock()
	if hasLS {
		bumpSearchCacheVer()
	}
}

func getIconImage(ctx context.Context, userID int64) ([]byte, string, error) {
	iconsMu.RLock()
	h, hasCustom := iconHashByUserID[userID]
	var img []byte
	if hasCustom {
		img = iconImageByUserID[userID]
	}
	iconsMu.RUnlock()

	if !hasCustom {
		return fallbackImageBytes, fallbackIconHash, nil
	}
	if img != nil {
		return img, h, nil
	}

	b, err := os.ReadFile(iconFilePath(userID))
	if err != nil {
		return fallbackImageBytes, fallbackIconHash, nil
	}
	iconsMu.Lock()
	if iconHashByUserID[userID] == h && len(iconImageByUserID) <= 6000 {
		iconImageByUserID[userID] = b
	}
	iconsMu.Unlock()
	return b, h, nil
}

// tryReserveSlots checks and decrements reservation slots atomically in memory.
// Returns (ok, matchedAny).
func tryReserveSlots(startAt, endAt int64) (bool, bool) {
	slotsMu.Lock()
	defer slotsMu.Unlock()

	idx := sort.Search(len(slotsSlice), func(i int) bool {
		return slotsSlice[i].StartAt >= startAt
	})
	var matched []int
	for i := idx; i < len(slotsSlice) && slotsSlice[i].StartAt <= endAt; i++ {
		if slotsSlice[i].EndAt <= endAt {
			if slotsSlice[i].Slot < 1 {
				return false, false
			}
			matched = append(matched, i)
		}
	}
	for _, i := range matched {
		slotsSlice[i].Slot--
	}
	return true, len(matched) > 0
}

func getLivestreamEntryByID(id int64) (*LivestreamCacheEntry, bool) {
	livestreamsMu.RLock()
	e, ok := livestreamByID[id]
	livestreamsMu.RUnlock()
	return e, ok
}

func addLivestreamCache(entry *LivestreamCacheEntry) {
	livestreamsMu.Lock()
	livestreamByID[entry.Model.ID] = entry
	livestreamsByUserID[entry.Model.UserID] = append(livestreamsByUserID[entry.Model.UserID], entry)
	for _, t := range entry.Tags {
		livestreamsByTagID[t.ID] = append(livestreamsByTagID[t.ID], entry)
	}
	allLivestreams = append(allLivestreams, entry)
	livestreamsMu.Unlock()
	bumpSearchCacheVer()
}

func marshalLivestreamEntriesJSON(entries []*LivestreamCacheEntry) []byte {
	var buf bytes.Buffer
	buf.Grow(len(entries)*320 + 2)
	buf.WriteByte('[')
	first := true
	for _, e := range entries {
		b, ok := e.getJSON()
		if !ok {
			continue
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		buf.Write(b)
	}
	buf.WriteByte(']')
	return buf.Bytes()
}

func addNGWordCache(w *NGWord) {
	ngWordsMu.Lock()
	list := ngWordsByLivestream[w.LivestreamID]
	newList := make([]*NGWord, len(list)+1)
	newList[0] = w
	copy(newList[1:], list)
	ngWordsByLivestream[w.LivestreamID] = newList
	ngWordsMu.Unlock()
}

func getNGWordsSnapshot(livestreamID int64) []*NGWord {
	ngWordsMu.RLock()
	list := ngWordsByLivestream[livestreamID]
	if len(list) == 0 {
		ngWordsMu.RUnlock()
		return nil
	}
	cp := make([]*NGWord, len(list))
	copy(cp, list)
	ngWordsMu.RUnlock()
	return cp
}

func getNGWordsForUserAndLivestream(userID, livestreamID int64) []*NGWord {
	ngWordsMu.RLock()
	list := ngWordsByLivestream[livestreamID]
	res := make([]*NGWord, 0, len(list))
	for _, w := range list {
		if w.UserID == userID {
			res = append(res, w)
		}
	}
	ngWordsMu.RUnlock()
	sort.Slice(res, func(i, j int) bool {
		if res[i].CreatedAt == res[j].CreatedAt {
			return res[i].ID > res[j].ID
		}
		return res[i].CreatedAt > res[j].CreatedAt
	})
	return res
}

func matchesNGWordFast(lowerComment string, w *NGWord) bool {
	if w.LowerWord == "" {
		return true
	}
	if !w.HasWildcard {
		return strings.Contains(lowerComment, w.LowerWord)
	}
	return sqlLikeMatch([]rune(lowerComment), []rune("%"+w.LowerWord+"%"))
}

func matchesNGWord(comment, word string) bool {
	if word == "" {
		return true
	}
	if !strings.ContainsAny(word, "%_\\") {
		return strings.Contains(strings.ToLower(comment), strings.ToLower(word))
	}
	return sqlLikeMatch([]rune(strings.ToLower(comment)), []rune(strings.ToLower("%"+word+"%")))
}

func sqlLikeMatch(text, pattern []rune) bool {
	ti, pi := 0, 0
	starPi, starTi := -1, 0
	for ti < len(text) {
		if pi < len(pattern) && pattern[pi] == '\\' && pi+1 < len(pattern) {
			pi++
			if text[ti] == pattern[pi] {
				ti++
				pi++
				continue
			}
		} else if pi < len(pattern) && pattern[pi] == '%' {
			starPi = pi
			starTi = ti
			pi++
			continue
		} else if pi < len(pattern) && (pattern[pi] == '_' || pattern[pi] == text[ti]) {
			ti++
			pi++
			continue
		}
		if starPi != -1 {
			pi = starPi + 1
			starTi++
			ti = starTi
			continue
		}
		return false
	}
	for pi < len(pattern) && pattern[pi] == '%' {
		pi++
	}
	return pi == len(pattern)
}
