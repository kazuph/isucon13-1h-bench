package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/labstack/echo/v4"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

var replicaAddress = os.Getenv("ISUPIPE_REPLICA_ADDRESS")
var replicaMode = os.Getenv("ISUPIPE_READ_REPLICA") == "1"
var replicaClient = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{MaxIdleConns: 128, MaxIdleConnsPerHost: 128, IdleConnTimeout: 90 * time.Second}}

type replicaEvent struct {
	Kind     string
	User     *User
	Password string
	Stream   *LivestreamModel
	Tags     []Tag
	Comment  *LivecommentModel
	Reaction *ReactionModel
	Report   *LivecommentReportModel
	Word     *NGWord
	UserID   int64
	StreamID int64
	Count    int64
	Image    []byte
}

func replicaCall(event replicaEvent) error {
	if replicaAddress == "" || replicaMode {
		return nil
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	resp, err := replicaClient.Post(replicaAddress+"/internal/projection", "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 204 {
		return fmt.Errorf("replica status %d: %s", resp.StatusCode, body)
	}
	return nil
}
func replicate(event replicaEvent) {
	if replicaAddress == "" || replicaMode {
		return
	}
	for {
		if err := replicaCall(event); err == nil {
			return
		} else {
			log.Print(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
func replicaReload() error { return replicaCall(replicaEvent{Kind: "reload"}) }
func projectionEventHandler(c echo.Context) error {
	if !replicaMode {
		return echo.NewHTTPError(403)
	}
	var e replicaEvent
	if err := json.NewDecoder(c.Request().Body).Decode(&e); err != nil {
		return err
	}
	switch e.Kind {
	case "reload":
		resetCaches()
		if err := loadReadModel(); err != nil {
			return err
		}
	case "user":
		applyRegistered(*e.User, e.Password)
	case "stream":
		applyReserved(*e.Stream, e.Tags)
	case "icon":
		applyIcon(e.UserID, e.Image)
	case "comment":
		applyComment(*e.Comment)
	case "reaction":
		applyReaction(*e.Reaction)
	case "report":
		applyReport(*e.Report)
	case "word":
		applyModeration(e.StreamID, *e.Word)
	case "viewer":
		state.Lock()
		if state.viewers[e.StreamID] == nil {
			state.viewers[e.StreamID] = make(map[int64]int64)
		}
		if e.Count == 0 {
			delete(state.viewers[e.StreamID], e.UserID)
		} else {
			state.viewers[e.StreamID][e.UserID] = e.Count
		}
		state.Unlock()
	default:
		return echo.NewHTTPError(400, "invalid projection event")
	}
	return c.NoContent(204)
}
func replicateViewer(id, uid int64) {
	if replicaAddress == "" || replicaMode {
		return
	}
	state.RLock()
	n := state.viewers[id][uid]
	state.RUnlock()
	replicate(replicaEvent{Kind: "viewer", StreamID: id, UserID: uid, Count: n})
}
