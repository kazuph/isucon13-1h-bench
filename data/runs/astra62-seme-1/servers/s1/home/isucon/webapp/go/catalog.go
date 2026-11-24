package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"sort"
	"sync"
)

type iconData struct {
	image []byte
	hash  string
}
type catalogData struct {
	tags                 map[int64]Tag
	tagsJSON             []byte
	userJSON, streamJSON map[int64][]byte
	allStreams           []int64
	byTag                map[string][]int64
	byOwner              map[int64][]int64
	users                map[int64]User
	names                map[string]int64
	streams              map[int64]Livestream
	icons                map[int64]iconData
}

var catalogMu sync.RWMutex
var catalog catalogData
var fallback iconData

func newIcon(image []byte) iconData {
	return iconData{image, fmt.Sprintf("%x", sha256.Sum256(image))}
}
func reloadCatalog() error {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	image, err := os.ReadFile(fallbackImage)
	if err != nil {
		return err
	}
	fallback = newIcon(image)
	data := catalogData{userJSON: map[int64][]byte{}, streamJSON: map[int64][]byte{}, users: map[int64]User{}, names: map[string]int64{}, streams: map[int64]Livestream{}, icons: map[int64]iconData{}, byTag: map[string][]int64{}, byOwner: map[int64][]int64{}}
	var users []struct {
		ID                int64
		Name, Description string
		Display           string
		ThemeID           int64
		Dark              bool
	}
	err = dbConn.Select(&users, "SELECT u.id,u.name,u.description,u.display_name AS display,t.id AS themeid,t.dark_mode AS dark FROM users u JOIN themes t ON t.user_id=u.id")
	if err != nil {
		return err
	}
	for _, u := range users {
		data.users[u.ID] = User{ID: u.ID, Name: u.Name, Description: u.Description, DisplayName: u.Display, Theme: Theme{u.ThemeID, u.Dark}, IconHash: fallback.hash}
		data.names[u.Name] = u.ID
	}
	var icons []struct {
		ID    int64
		Image []byte
	}
	if err = dbConn.Select(&icons, "SELECT user_id AS id,image FROM icons"); err != nil {
		return err
	}
	for _, i := range icons {
		icon := newIcon(i.Image)
		data.icons[i.ID] = icon
		u := data.users[i.ID]
		u.IconHash = icon.hash
		data.users[i.ID] = u
	}
	for _, u := range data.users {
		data.refreshUser(u)
	}
	var streams []LivestreamModel
	if err = dbConn.Select(&streams, "SELECT * FROM livestreams"); err != nil {
		return err
	}
	for _, s := range streams {
		data.streams[s.ID] = Livestream{ID: s.ID, Owner: data.users[s.UserID], Title: s.Title, Description: s.Description, PlaylistUrl: s.PlaylistUrl, ThumbnailUrl: s.ThumbnailUrl, StartAt: s.StartAt, EndAt: s.EndAt, Tags: []Tag{}}
	}
	var allTags []Tag
	if err := dbConn.Select(&allTags, "SELECT id,name FROM tags ORDER BY id"); err != nil {
		return err
	}
	data.tags = make(map[int64]Tag, len(allTags))
	for _, t := range allTags {
		data.tags[t.ID] = t
	}
	data.tagsJSON = encodeCatalog(struct {
		Tags []Tag `json:"tags"`
	}{allTags})
	var tags []struct {
		ID, Stream int64
		Name       string
	}
	if err = dbConn.Select(&tags, "SELECT t.id,t.name,lt.livestream_id AS stream FROM livestream_tags lt JOIN tags t ON t.id=lt.tag_id ORDER BY lt.id"); err != nil {
		return err
	}
	for _, t := range tags {
		s := data.streams[t.Stream]
		s.Tags = append(s.Tags, Tag{ID: t.ID, Name: t.Name})
		data.streams[t.Stream] = s
	}
	sort.Slice(streams, func(i, j int) bool { return streams[i].ID < streams[j].ID })
	for _, m := range streams {
		data.indexStream(data.streams[m.ID])
	}
	catalog = data
	return loadActivity()
}
func knownUser(id int64) (User, bool) {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	u, ok := catalog.users[id]
	return u, ok
}
func knownStream(id int64) (Livestream, bool) {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	s, ok := catalog.streams[id]
	if ok {
		s.Owner = catalog.users[s.Owner.ID]
	}
	return s, ok
}

func orderedInsert(ids []int64, id int64) []int64 {
	at := sort.Search(len(ids), func(i int) bool { return ids[i] >= id })
	ids = append(ids, 0)
	copy(ids[at+1:], ids[at:])
	ids[at] = id
	return ids
}
func (d *catalogData) indexStream(s Livestream) {
	s.Owner = d.users[s.Owner.ID]
	d.streams[s.ID] = s
	d.streamJSON[s.ID] = encodeCatalog(s)
	d.allStreams = orderedInsert(d.allStreams, s.ID)
	d.byOwner[s.Owner.ID] = orderedInsert(d.byOwner[s.Owner.ID], s.ID)
	for _, t := range s.Tags {
		d.byTag[t.Name] = orderedInsert(d.byTag[t.Name], s.ID)
	}
}
func streamsForOwner(id int64) []Livestream {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	ids := catalog.byOwner[id]
	streams := make([]Livestream, 0, len(ids))
	for _, id := range ids {
		s := catalog.streams[id]
		s.Owner = catalog.users[s.Owner.ID]
		streams = append(streams, s)
	}
	return streams
}
