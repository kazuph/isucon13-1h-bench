package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

const (
	roleStandalone = "standalone"
	rolePipe       = "pipe"
	roleShard0     = "shard0"
	roleShard1     = "shard1"
)

var (
	nodeRole   = roleStandalone
	s1Internal = "http://10.99.0.11:8081"
	s2Internal = "http://10.99.0.12:8081"
	s3Internal = "http://10.99.0.13:8081"

	internalHTTP = &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        256,
			MaxIdleConnsPerHost: 128,
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  true,
		},
	}

	iconHashSyncCh chan iconHashSyncMsg
)

type iconHashSyncMsg struct {
	UserID int64  `json:"user_id"`
	Hash   string `json:"hash"`
}

func initClusterConfig() {
	if r := os.Getenv("ISUCON13_NODE_ROLE"); r != "" {
		nodeRole = r
	}
	if v := os.Getenv("ISUCON13_S1_INTERNAL"); v != "" {
		s1Internal = v
	}
	if v := os.Getenv("ISUCON13_S2_INTERNAL"); v != "" {
		s2Internal = v
	}
	if v := os.Getenv("ISUCON13_S3_INTERNAL"); v != "" {
		s3Internal = v
	}
	if nodeRole == rolePipe {
		iconHashSyncCh = make(chan iconHashSyncMsg, 4096)
		for i := 0; i < 4; i++ {
			go iconHashSyncWorker()
		}
	}
}

func isClustered() bool {
	return nodeRole != roleStandalone
}

func shardForUsername(name string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(strings.ToLower(name)))
	return int(h.Sum32() & 1)
}

func shardInternalURL(shard int) string {
	if shard == 0 {
		return s1Internal
	}
	return s3Internal
}

func otherShardInternalURL() string {
	if nodeRole == roleShard0 {
		return s3Internal
	}
	return s1Internal
}

func iconHashSyncWorker() {
	for msg := range iconHashSyncCh {
		b, _ := json.Marshal(msg)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = postInternalJSON(s1Internal+"/internal/icon_hash", b)
		}()
		go func() {
			defer wg.Done()
			_ = postInternalJSON(s3Internal+"/internal/icon_hash", b)
		}()
		wg.Wait()
	}
}

func postInternalJSON(url string, body []byte) error {
	resp, err := internalHTTP.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("internal post %s status %d", url, resp.StatusCode)
	}
	return nil
}

type syncUserMsg struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	DisplayName    string `json:"display_name"`
	Description    string `json:"description"`
	HashedPassword string `json:"hashed_password"`
	ThemeID        int64  `json:"theme_id"`
	DarkMode       bool   `json:"dark_mode"`
}

func broadcastNewUser(entry *UserCacheEntry) error {
	if !isClustered() {
		return nil
	}
	b, _ := json.Marshal(syncUserMsg{
		ID:             entry.ID,
		Name:           entry.Name,
		DisplayName:    entry.DisplayName,
		Description:    entry.Description,
		HashedPassword: entry.HashedPassword,
		ThemeID:        entry.Theme.ID,
		DarkMode:       entry.Theme.DarkMode,
	})
	var wg sync.WaitGroup
	var err1, err3 error
	wg.Add(2)
	go func() {
		defer wg.Done()
		err1 = postInternalJSON(s1Internal+"/internal/user", b)
	}()
	go func() {
		defer wg.Done()
		err3 = postInternalJSON(s3Internal+"/internal/user", b)
	}()
	wg.Wait()
	if err1 != nil {
		return err1
	}
	return err3
}

func broadcastIconHashAsync(userID int64, hash string) {
	if nodeRole != rolePipe || iconHashSyncCh == nil {
		return
	}
	select {
	case iconHashSyncCh <- iconHashSyncMsg{UserID: userID, Hash: hash}:
	default:
		go func() {
			b, _ := json.Marshal(iconHashSyncMsg{UserID: userID, Hash: hash})
			_ = postInternalJSON(s1Internal+"/internal/icon_hash", b)
			_ = postInternalJSON(s3Internal+"/internal/icon_hash", b)
		}()
	}
}

type internalReserveReq struct {
	UserID int64                    `json:"user_id"`
	Req    ReserveLivestreamRequest `json:"req"`
}

type internalReserveResp struct {
	OK      bool            `json:"ok"`
	ErrMsg  string          `json:"err_msg,omitempty"`
	Model   LivestreamModel `json:"model"`
	Tags    []Tag           `json:"tags"`
}

func callPipeReserve(userID int64, req ReserveLivestreamRequest) (*internalReserveResp, error) {
	b, _ := json.Marshal(internalReserveReq{UserID: userID, Req: req})
	resp, err := internalHTTP.Post(s2Internal+"/internal/reserve", "application/json", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out internalReserveResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

type internalUserStatsResp struct {
	ViewersCount      int64  `json:"viewers_count"`
	TotalReactions    int64  `json:"total_reactions"`
	TotalLivecomments int64  `json:"total_livecomments"`
	TotalTip          int64  `json:"total_tip"`
	FavoriteEmoji     string `json:"favorite_emoji"`
	TargetScore       int64  `json:"target_score"`
	BeatCount         int64  `json:"beat_count"`
}

func fetchShardUserStats(shardURL string, userID int64, username string) (*internalUserStatsResp, error) {
	u := fmt.Sprintf("%s/internal/stats/user?user_id=%d&username=%s", shardURL, userID, username)
	resp, err := internalHTTP.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out internalUserStatsResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func fetchShardUserBeatCount(shardURL string, score int64, username string) (int64, error) {
	u := fmt.Sprintf("%s/internal/rank/user?score=%d&username=%s", shardURL, score, username)
	resp, err := internalHTTP.Get(u)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(body)), 10, 64)
}

func fetchShardLSBeatCount(shardURL string, score int64, lsID int64) (int64, error) {
	u := fmt.Sprintf("%s/internal/rank/livestream?score=%d&id=%d", shardURL, score, lsID)
	resp, err := internalHTTP.Get(u)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(body)), 10, 64)
}

func flushAllNodesSync() {
	if !isClustered() {
		flushDBQueueSync()
		return
	}
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		flushDBQueueSync()
	}()
	go func() {
		defer wg.Done()
		_ = postInternalJSON(s1Internal+"/internal/flush", nil)
	}()
	go func() {
		defer wg.Done()
		_ = postInternalJSON(s3Internal+"/internal/flush", nil)
	}()
	wg.Wait()
}

func reloadOtherNodesSync() error {
	if !isClustered() {
		return nil
	}
	var wg sync.WaitGroup
	var err1, err3 error
	wg.Add(2)
	go func() {
		defer wg.Done()
		err1 = postInternalJSON(s1Internal+"/internal/reload", nil)
	}()
	go func() {
		defer wg.Done()
		err3 = postInternalJSON(s3Internal+"/internal/reload", nil)
	}()
	wg.Wait()
	if err1 != nil {
		return err1
	}
	return err3
}

func startInternalServer() {
	if !isClustered() {
		return
	}
	ie := echo.New()
	ie.HideBanner = true
	ie.HidePort = true

	ie.POST("/internal/user", func(c echo.Context) error {
		var msg syncUserMsg
		if err := json.NewDecoder(c.Request().Body).Decode(&msg); err != nil {
			return c.NoContent(http.StatusBadRequest)
		}
		entry := &UserCacheEntry{
			ID:             msg.ID,
			Name:           msg.Name,
			DisplayName:    msg.DisplayName,
			Description:    msg.Description,
			HashedPassword: msg.HashedPassword,
			Theme: Theme{
				ID:       msg.ThemeID,
				DarkMode: msg.DarkMode,
			},
		}
		usersMu.Lock()
		usersByID[entry.ID] = entry
		usersByName[entry.Name] = entry
		usersMu.Unlock()
		if nodeRole == roleShard0 {
			dnsAddRecord(entry.Name)
		}
		return c.NoContent(http.StatusOK)
	})

	ie.POST("/internal/icon_hash", func(c echo.Context) error {
		var msg iconHashSyncMsg
		if err := json.NewDecoder(c.Request().Body).Decode(&msg); err != nil {
			return c.NoContent(http.StatusBadRequest)
		}
		iconsMu.Lock()
		iconHashByUserID[msg.UserID] = msg.Hash
		iconsMu.Unlock()
		return c.NoContent(http.StatusOK)
	})

	ie.GET("/internal/icon/:user_id", func(c echo.Context) error {
		uid, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
		if err != nil {
			return c.NoContent(http.StatusBadRequest)
		}
		img, _, err := getIconImage(c.Request().Context(), uid)
		if err != nil {
			return c.NoContent(http.StatusInternalServerError)
		}
		return c.Blob(http.StatusOK, "image/jpeg", img)
	})

	ie.POST("/internal/reserve", func(c echo.Context) error {
		var in internalReserveReq
		if err := json.NewDecoder(c.Request().Body).Decode(&in); err != nil {
			return c.JSON(http.StatusBadRequest, internalReserveResp{OK: false, ErrMsg: "bad json"})
		}
		ok, matchedSlots := tryReserveSlots(in.Req.StartAt, in.Req.EndAt)
		if !ok {
			return c.JSON(http.StatusOK, internalReserveResp{OK: false, ErrMsg: "no slot"})
		}
		livestreamID := nextLivestreamID.Add(1)
		tags := make([]Tag, 0, len(in.Req.Tags))
		for _, tagID := range in.Req.Tags {
			if t, found := getTagByID(tagID); found {
				tags = append(tags, t)
			}
		}
		model := LivestreamModel{
			ID:           livestreamID,
			UserID:       in.UserID,
			Title:        in.Req.Title,
			Description:  in.Req.Description,
			PlaylistUrl:  in.Req.PlaylistUrl,
			ThumbnailUrl: in.Req.ThumbnailUrl,
			StartAt:      in.Req.StartAt,
			EndAt:        in.Req.EndAt,
		}
		entry := &LivestreamCacheEntry{Model: model, Tags: tags}
		addLivestreamCache(entry)

		var sd *dbSlotDec
		if matchedSlots {
			sd = &dbSlotDec{StartAt: in.Req.StartAt, EndAt: in.Req.EndAt}
		}
		enqueueReservation(sd, dbLivestreamInsert{Model: model, Tags: in.Req.Tags})

		return c.JSON(http.StatusOK, internalReserveResp{
			OK:    true,
			Model: model,
			Tags:  tags,
		})
	})

	ie.GET("/internal/stats/user", func(c echo.Context) error {
		uid, _ := strconv.ParseInt(c.QueryParam("user_id"), 10, 64)
		username := c.QueryParam("username")
		myShard := 0
		if nodeRole == roleShard1 {
			myShard = 1
		}

		usersMu.RLock()
		statsMu.RLock()
		var resp internalUserStatsResp
		if us := userStatsMap[uid]; us != nil {
			resp.TotalReactions = us.reactions
			resp.TotalTip = us.totalTip
			resp.TotalLivecomments = us.livecomments
			resp.ViewersCount = us.viewers
			resp.FavoriteEmoji = us.favEmoji
			resp.TargetScore = us.reactions + us.totalTip
		}
		for _, u := range usersByID {
			if u.ID == uid || shardForUsername(u.Name) != myShard {
				continue
			}
			var sc int64
			if us := userStatsMap[u.ID]; us != nil {
				sc = us.reactions + us.totalTip
			}
			if sc > resp.TargetScore || (sc == resp.TargetScore && u.Name > username) {
				resp.BeatCount++
			}
		}
		statsMu.RUnlock()
		usersMu.RUnlock()
		return c.JSON(http.StatusOK, resp)
	})

	ie.GET("/internal/rank/user", func(c echo.Context) error {
		targetScore, _ := strconv.ParseInt(c.QueryParam("score"), 10, 64)
		username := c.QueryParam("username")
		myShard := 0
		if nodeRole == roleShard1 {
			myShard = 1
		}

		var beat int64
		usersMu.RLock()
		statsMu.RLock()
		for _, u := range usersByID {
			if shardForUsername(u.Name) != myShard || u.Name == username {
				continue
			}
			var sc int64
			if us := userStatsMap[u.ID]; us != nil {
				sc = us.reactions + us.totalTip
			}
			if sc > targetScore || (sc == targetScore && u.Name > username) {
				beat++
			}
		}
		statsMu.RUnlock()
		usersMu.RUnlock()
		return c.String(http.StatusOK, strconv.FormatInt(beat, 10))
	})

	ie.GET("/internal/rank/livestream", func(c echo.Context) error {
		targetScore, _ := strconv.ParseInt(c.QueryParam("score"), 10, 64)
		targetID, _ := strconv.ParseInt(c.QueryParam("id"), 10, 64)
		myShard := 0
		if nodeRole == roleShard1 {
			myShard = 1
		}

		var beat int64
		livestreamsMu.RLock()
		usersMu.RLock()
		statsMu.RLock()
		for lsID, ls := range livestreamByID {
			if lsID == targetID {
				continue
			}
			u, ok := usersByID[ls.Model.UserID]
			if !ok || shardForUsername(u.Name) != myShard {
				continue
			}
			var sc int64
			if lss := lsStatsMap[lsID]; lss != nil {
				sc = lss.reactions + lss.totalTip
			}
			if sc > targetScore || (sc == targetScore && lsID > targetID) {
				beat++
			}
		}
		statsMu.RUnlock()
		usersMu.RUnlock()
		livestreamsMu.RUnlock()
		return c.String(http.StatusOK, strconv.FormatInt(beat, 10))
	})

	ie.POST("/internal/flush", func(c echo.Context) error {
		flushDBQueueSync()
		return c.NoContent(http.StatusOK)
	})

	ie.POST("/internal/reload", func(c echo.Context) error {
		resetEPMetrics()
		flushDBQueueSync()
		ctx := context.Background()
		if err := reloadAllCaches(ctx); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		if nodeRole == roleShard0 {
			if err := loadDNSFromZoneAndDB(ctx, "../pdns/u.isucon.dev.zone", dbConn); err != nil {
				return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
			}
		}
		return c.NoContent(http.StatusOK)
	})

	go func() {
		log.Printf("internal cluster server listening on :8081 (role=%s)", nodeRole)
		if err := ie.Start(":8081"); err != nil && err != http.ErrServerClosed {
			log.Fatalf("internal server failed: %v", err)
		}
	}()
}
