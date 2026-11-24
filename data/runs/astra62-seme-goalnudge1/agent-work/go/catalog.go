package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
)

type catalog struct {
	wireVersion map[int64]uint64
	nextVersion uint64
	tagJSON     *searchResult
	sync.RWMutex
	userJSON     map[int64][]byte
	streamJSON   map[int64][]byte
	users        map[int64]User
	names        map[string]int64
	images       map[int64][]byte
	objects      map[string][]byte
	streams      map[int64]Livestream
	tags         map[int64]Tag
	allStreams   []int64
	ownerStreams map[int64][]int64
	tagStreams   map[string][]int64
	tagList      TagsResponse
	fallback     []byte
}

var data catalog

func loadCatalog() error {
	data.Lock()
	defer data.Unlock()
	var err error
	data.fallback, err = os.ReadFile(fallbackImage)
	if err != nil {
		return err
	}
	defaultHash := fmt.Sprintf("%x", sha256.Sum256(data.fallback))
	data.users = make(map[int64]User)
	data.names = make(map[string]int64)
	data.images = make(map[int64][]byte)
	data.objects = make(map[string][]byte)
	data.streams = make(map[int64]Livestream)
	data.tags = make(map[int64]Tag)
	data.wireVersion = make(map[int64]uint64)
	data.userJSON = make(map[int64][]byte)
	data.streamJSON = make(map[int64][]byte)
	data.ownerStreams = nil
	var users []UserModel
	if err = dbConn.Select(&users, "SELECT * FROM users"); err != nil {
		return err
	}
	for _, u := range users {
		data.users[u.ID] = User{ID: u.ID, Name: u.Name, DisplayName: u.DisplayName, Description: u.Description, IconHash: defaultHash}
		data.names[u.Name] = u.ID
	}
	var themes []ThemeModel
	if err = dbConn.Select(&themes, "SELECT * FROM themes"); err != nil {
		return err
	}
	for _, t := range themes {
		u := data.users[t.UserID]
		u.Theme = Theme{t.ID, t.DarkMode}
		data.users[t.UserID] = u
	}
	var objects []struct {
		Hash  string
		Image []byte
	}
	if err = dbConn.Select(&objects, "SELECT hash,image FROM icon_objects WHERE hash IN (SELECT hash FROM icon_refs)"); err != nil {
		return err
	}
	for _, object := range objects {
		data.objects[object.Hash] = object.Image
	}
	var refs []struct {
		UserID int64 `db:"user_id"`
		Hash   string
	}
	if err = dbConn.Select(&refs, "SELECT user_id,hash FROM icon_refs"); err != nil {
		return err
	}
	for _, r := range refs {
		img, ok := data.objects[r.Hash]
		if !ok {
			return fmt.Errorf("missing icon object %s", r.Hash)
		}
		data.setImageHash(r.UserID, img, r.Hash)
	}
	var legacy []struct {
		UserID int64 `db:"user_id"`
		Image  []byte
	}
	if err = dbConn.Select(&legacy, "SELECT i.user_id,i.image FROM icons i LEFT JOIN icon_refs r ON r.user_id=i.user_id WHERE r.id IS NULL"); err != nil {
		return err
	}
	for _, r := range legacy {
		data.setImageHash(r.UserID, r.Image, fmt.Sprintf("%x", sha256.Sum256(r.Image)))
	}
	data.tagList = TagsResponse{Tags: []*Tag{}}
	var tags []TagModel
	if err = dbConn.Select(&tags, "SELECT * FROM tags ORDER BY id"); err != nil {
		return err
	}
	for _, t := range tags {
		v := Tag{t.ID, t.Name}
		data.tags[t.ID] = v
		data.tagList.Tags = append(data.tagList.Tags, &v)
	}
	tagBody, _ := json.Marshal(data.tagList)
	data.tagJSON = &searchResult{plain: tagBody}
	var streams []LivestreamModel
	if err = dbConn.Select(&streams, "SELECT * FROM livestreams"); err != nil {
		return err
	}
	for _, s := range streams {
		data.streams[s.ID] = Livestream{ID: s.ID, Owner: data.users[s.UserID], Title: s.Title, Description: s.Description, PlaylistUrl: s.PlaylistUrl, ThumbnailUrl: s.ThumbnailUrl, StartAt: s.StartAt, EndAt: s.EndAt, Tags: []Tag{}}
	}
	var links []LivestreamTagModel
	if err = dbConn.Select(&links, "SELECT * FROM livestream_tags ORDER BY id"); err != nil {
		return err
	}
	for _, l := range links {
		s := data.streams[l.LivestreamID]
		s.Tags = append(s.Tags, data.tags[l.TagID])
		data.streams[s.ID] = s
	}
	data.ownerStreams = make(map[int64][]int64)
	data.tagStreams = make(map[string][]int64)
	data.allStreams = nil
	sort.Slice(streams, func(i, j int) bool { return streams[i].ID < streams[j].ID })
	for id, u := range data.users {
		data.userJSON[id], _ = json.Marshal(u)
	}
	for _, m := range streams {
		data.indexStream(data.streams[m.ID])
	}
	return nil
}
func (d *catalog) setImageHash(id int64, img []byte, hash string) {
	if prior, ok := d.objects[hash]; ok {
		img = prior
	} else {
		d.objects[hash] = img
	}
	u := d.users[id]
	u.IconHash = hash
	d.users[id] = u
	d.images[id] = img
	d.refreshUser(id)
}
func lookupUser(id int64) (User, error) {
	data.RLock()
	defer data.RUnlock()
	u, ok := data.users[id]
	if !ok {
		return u, sql.ErrNoRows
	}
	return u, nil
}
func lookupStream(id int64) (Livestream, error) {
	data.RLock()
	defer data.RUnlock()
	s, ok := data.streams[id]
	if !ok {
		return s, sql.ErrNoRows
	}
	s.Owner = data.users[s.Owner.ID]
	return s, nil
}

func (d *catalog) indexStream(s Livestream) {
	d.refreshStream(s.ID)
	d.allStreams = orderedInsert(d.allStreams, s.ID)
	d.ownerStreams[s.Owner.ID] = orderedInsert(d.ownerStreams[s.Owner.ID], s.ID)
	for _, t := range s.Tags {
		d.tagStreams[t.Name] = orderedInsert(d.tagStreams[t.Name], s.ID)
	}
}
func orderedInsert(ids []int64, id int64) []int64 {
	pos := sort.Search(len(ids), func(i int) bool { return ids[i] >= id })
	ids = append(ids, 0)
	copy(ids[pos+1:], ids[pos:])
	ids[pos] = id
	return ids
}
func (d *catalog) refreshStream(id int64) {
	s := d.streams[id]
	s.Owner = d.users[s.Owner.ID]
	d.streamJSON[id], _ = json.Marshal(s)
	d.nextVersion++
	d.wireVersion[id] = d.nextVersion
}
func (d *catalog) refreshUser(id int64) {
	d.userJSON[id], _ = json.Marshal(d.users[id])
	for _, streamID := range d.ownerStreams[id] {
		d.refreshStream(streamID)
	}
}
func (d *catalog) listStreams(ids []int64, limit int, reverse bool) []byte {
	n := len(ids)
	if limit >= 0 && limit < n {
		n = limit
	}
	size := 2
	for i := 0; i < n; i++ {
		j := i
		if reverse {
			j = len(ids) - 1 - i
		}
		size += len(d.streamJSON[ids[j]]) + 1
	}
	out := make([]byte, 0, size)
	out = append(out, '[')
	for i := 0; i < n; i++ {
		j := i
		if reverse {
			j = len(ids) - 1 - i
		}
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, d.streamJSON[ids[j]]...)
	}
	return append(out, ']')
}
