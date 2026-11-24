//go:build dumpseed

package main

import (
	"bufio"
	"encoding/gob"
	"fmt"
	"log"
	"os"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
)

func main() {
	db, err := sqlx.Open("mysql", "isucon:isucon@tcp(127.0.0.1:3306)/isupipe?parseTime=true&charset=utf8mb4")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	var seed SeedData
	if err := db.Select(&seed.Users, "SELECT id, name, display_name, description, password FROM users ORDER BY id ASC"); err != nil {
		log.Fatal("users:", err)
	}
	if err := db.Select(&seed.Themes, "SELECT id, user_id, dark_mode FROM themes ORDER BY id ASC"); err != nil {
		log.Fatal("themes:", err)
	}
	if err := db.Select(&seed.Livestreams, "SELECT id, user_id, title, description, playlist_url, thumbnail_url, start_at, end_at FROM livestreams ORDER BY id ASC"); err != nil {
		log.Fatal("livestreams:", err)
	}
	if err := db.Select(&seed.ReservationSlots, "SELECT id, slot, start_at, end_at FROM reservation_slots ORDER BY id ASC"); err != nil {
		log.Fatal("reservation_slots:", err)
	}
	if err := db.Select(&seed.Tags, "SELECT id, name FROM tags ORDER BY id ASC"); err != nil {
		log.Fatal("tags:", err)
	}
	if err := db.Select(&seed.LivestreamTags, "SELECT id, livestream_id, tag_id FROM livestream_tags ORDER BY id ASC"); err != nil {
		log.Fatal("livestream_tags:", err)
	}
	if err := db.Select(&seed.Reactions, "SELECT id, emoji_name, user_id, livestream_id, created_at FROM reactions ORDER BY id ASC"); err != nil {
		log.Fatal("reactions:", err)
	}
	if err := db.Select(&seed.NGWords, "SELECT id, user_id, livestream_id, word, created_at FROM ng_words ORDER BY id ASC"); err != nil {
		log.Fatal("ng_words:", err)
	}
	if err := db.Select(&seed.Livecomments, "SELECT id, user_id, livestream_id, comment, tip, created_at FROM livecomments ORDER BY id ASC"); err != nil {
		log.Fatal("livecomments:", err)
	}

	zf, err := os.Open("../pdns/u.isucon.dev.zone")
	if err != nil {
		log.Fatal("zone:", err)
	}
	defer zf.Close()
	seen := make(map[string]struct{})
	sc := bufio.NewScanner(zf)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 4 && fields[2] == "IN" && fields[3] == "A" {
			name := strings.ToLower(fields[0])
			if name == "@" || name == "ns1" || name == "pipe" {
				continue
			}
			if _, ok := seen[name]; !ok {
				seen[name] = struct{}{}
				seed.ZoneSubdomains = append(seed.ZoneSubdomains, name)
			}
		}
	}

	outPath := "../seed.gob"
	if len(os.Args) > 1 {
		outPath = os.Args[1]
	}
	f, err := os.Create(outPath)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := gob.NewEncoder(f).Encode(&seed); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Dumped seed to %s: users=%d themes=%d livestreams=%d slots=%d tags=%d ltags=%d reactions=%d ngwords=%d comments=%d zone=%d\n",
		outPath, len(seed.Users), len(seed.Themes), len(seed.Livestreams), len(seed.ReservationSlots),
		len(seed.Tags), len(seed.LivestreamTags), len(seed.Reactions), len(seed.NGWords), len(seed.Livecomments), len(seed.ZoneSubdomains))
}
