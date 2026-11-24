package main

import (
	"bufio"
	"encoding/gob"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
)

func ExportSnapshotFromMySQL(dsn, zonePath, fallbackImgPath, outPath string) error {
	db, err := sqlx.Open("mysql", dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	snap := &InitialSnapshot{}

	img, err := os.ReadFile(fallbackImgPath)
	if err != nil {
		return fmt.Errorf("read fallback image: %w", err)
	}
	snap.FallbackImage = img
	snap.FallbackHash = sha256Hex(img)

	// Parse u.isucon.dev.zone A records
	zoneFile, err := os.Open(zonePath)
	if err != nil {
		return fmt.Errorf("open zone: %w", err)
	}
	defer zoneFile.Close()
	reA := regexp.MustCompile(`^([a-zA-Z0-9_-]+)\s+\d+\s+IN\s+A\s+`)
	dnsSet := make(map[string]struct{})
	sc := bufio.NewScanner(zoneFile)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if m := reA.FindStringSubmatch(line); m != nil {
			dnsSet[strings.ToLower(m[1])] = struct{}{}
		}
	}
	for name := range dnsSet {
		snap.DNSNames = append(snap.DNSNames, name)
	}
	sort.Strings(snap.DNSNames)

	// Tags
	type dbTag struct {
		ID   int64  `db:"id"`
		Name string `db:"name"`
	}
	var dbTags []dbTag
	if err := db.Select(&dbTags, "SELECT id, name FROM tags ORDER BY id ASC"); err != nil {
		return err
	}
	for _, t := range dbTags {
		snap.Tags = append(snap.Tags, Tag{ID: t.ID, Name: t.Name})
	}

	// Users + Themes
	type dbUser struct {
		ID          int64  `db:"id"`
		Name        string `db:"name"`
		DisplayName string `db:"display_name"`
		Description string `db:"description"`
		Password    string `db:"password"`
		ThemeID     int64  `db:"theme_id"`
		DarkMode    bool   `db:"dark_mode"`
	}
	var dbUsers []dbUser
	if err := db.Select(&dbUsers, `
		SELECT u.id, u.name, u.display_name, u.description, u.password, t.id AS theme_id, t.dark_mode
		FROM users u INNER JOIN themes t ON t.user_id = u.id
		ORDER BY u.id ASC`); err != nil {
		return err
	}
	for _, u := range dbUsers {
		snap.Users = append(snap.Users, SnapUser{
			ID:             u.ID,
			Name:           u.Name,
			DisplayName:    u.DisplayName,
			Description:    u.Description,
			HashedPassword: u.Password,
			ThemeID:        u.ThemeID,
			DarkMode:       u.DarkMode,
		})
	}

	// Livestream tags map
	type dbLSTag struct {
		LivestreamID int64 `db:"livestream_id"`
		TagID        int64 `db:"tag_id"`
	}
	var dbLSTags []dbLSTag
	if err := db.Select(&dbLSTags, "SELECT livestream_id, tag_id FROM livestream_tags ORDER BY id ASC"); err != nil {
		return err
	}
	lsTagsMap := make(map[int64][]int64)
	for _, lt := range dbLSTags {
		lsTagsMap[lt.LivestreamID] = append(lsTagsMap[lt.LivestreamID], lt.TagID)
	}

	// Livestreams
	type dbLS struct {
		ID           int64  `db:"id"`
		UserID       int64  `db:"user_id"`
		Title        string `db:"title"`
		Description  string `db:"description"`
		PlaylistUrl  string `db:"playlist_url"`
		ThumbnailUrl string `db:"thumbnail_url"`
		StartAt      int64  `db:"start_at"`
		EndAt        int64  `db:"end_at"`
	}
	var dbLSs []dbLS
	if err := db.Select(&dbLSs, "SELECT id, user_id, title, description, playlist_url, thumbnail_url, start_at, end_at FROM livestreams ORDER BY id ASC"); err != nil {
		return err
	}
	for _, l := range dbLSs {
		snap.Livestreams = append(snap.Livestreams, SnapLivestream{
			ID:           l.ID,
			UserID:       l.UserID,
			Title:        l.Title,
			Description:  l.Description,
			PlaylistUrl:  l.PlaylistUrl,
			ThumbnailUrl: l.ThumbnailUrl,
			StartAt:      l.StartAt,
			EndAt:        l.EndAt,
			TagIDs:       lsTagsMap[l.ID],
		})
	}

	// Livecomments
	type dbLC struct {
		ID           int64  `db:"id"`
		UserID       int64  `db:"user_id"`
		LivestreamID int64  `db:"livestream_id"`
		Comment      string `db:"comment"`
		Tip          int64  `db:"tip"`
		CreatedAt    int64  `db:"created_at"`
	}
	var dbLCs []dbLC
	if err := db.Select(&dbLCs, "SELECT id, user_id, livestream_id, comment, tip, created_at FROM livecomments ORDER BY id ASC"); err != nil {
		return err
	}
	for _, c := range dbLCs {
		snap.Livecomments = append(snap.Livecomments, SnapLivecomment{
			ID:           c.ID,
			UserID:       c.UserID,
			LivestreamID: c.LivestreamID,
			Comment:      c.Comment,
			Tip:          c.Tip,
			CreatedAt:    c.CreatedAt,
		})
	}

	// Reactions
	type dbRx struct {
		ID           int64  `db:"id"`
		UserID       int64  `db:"user_id"`
		LivestreamID int64  `db:"livestream_id"`
		EmojiName    string `db:"emoji_name"`
		CreatedAt    int64  `db:"created_at"`
	}
	var dbRxs []dbRx
	if err := db.Select(&dbRxs, "SELECT id, user_id, livestream_id, emoji_name, created_at FROM reactions ORDER BY id ASC"); err != nil {
		return err
	}
	for _, r := range dbRxs {
		snap.Reactions = append(snap.Reactions, SnapReaction{
			ID:           r.ID,
			UserID:       r.UserID,
			LivestreamID: r.LivestreamID,
			EmojiName:    r.EmojiName,
			CreatedAt:    r.CreatedAt,
		})
	}

	// NGWords
	type dbNG struct {
		ID           int64  `db:"id"`
		UserID       int64  `db:"user_id"`
		LivestreamID int64  `db:"livestream_id"`
		Word         string `db:"word"`
		CreatedAt    int64  `db:"created_at"`
	}
	var dbNGs []dbNG
	if err := db.Select(&dbNGs, "SELECT id, user_id, livestream_id, word, created_at FROM ng_words ORDER BY id ASC"); err != nil {
		return err
	}
	for _, g := range dbNGs {
		snap.NGWords = append(snap.NGWords, SnapNGWord{
			ID:           g.ID,
			UserID:       g.UserID,
			LivestreamID: g.LivestreamID,
			Word:         g.Word,
			CreatedAt:    g.CreatedAt,
		})
	}

	// Reservation slots
	type dbSlot struct {
		Slot    int64 `db:"slot"`
		StartAt int64 `db:"start_at"`
		EndAt   int64 `db:"end_at"`
	}
	var dbSlots []dbSlot
	if err := db.Select(&dbSlots, "SELECT slot, start_at, end_at FROM reservation_slots ORDER BY start_at ASC"); err != nil {
		return err
	}
	if len(dbSlots) > 0 {
		snap.SlotBase = dbSlots[0].StartAt
		snap.SlotStep = dbSlots[0].EndAt - dbSlots[0].StartAt
		snap.Slots = make([]int64, len(dbSlots))
		for i, sl := range dbSlots {
			snap.Slots[i] = sl.Slot
		}
	}

	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	bw := bufio.NewWriterSize(f, 256*1024)
	if err := gob.NewEncoder(bw).Encode(snap); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	fmt.Printf("Exported snapshot to %s: users=%d tags=%d livestreams=%d livecomments=%d reactions=%d ngwords=%d slots=%d dns=%d\n",
		outPath, len(snap.Users), len(snap.Tags), len(snap.Livestreams), len(snap.Livecomments), len(snap.Reactions), len(snap.NGWords), len(snap.Slots), len(snap.DNSNames))
	return nil
}

func LoadSnapshot(path string) (*InitialSnapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var snap InitialSnapshot
	if err := gob.NewDecoder(bufio.NewReaderSize(f, 256*1024)).Decode(&snap); err != nil {
		return nil, err
	}
	return &snap, nil
}
