package main

import (
	"github.com/labstack/echo/v4"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type blockedResponse struct {
	header  http.Header
	started chan struct{}
	release chan struct{}
}

func (w *blockedResponse) Header() http.Header { return w.header }
func (w *blockedResponse) WriteHeader(int)     {}
func (w *blockedResponse) Write(p []byte) (int, error) {
	close(w.started)
	<-w.release
	return len(p), nil
}
func TestResponseDoesNotHoldReadModelLock(t *testing.T) {
	old := mem
	defer func() { mem = old }()
	mem = memoryState{tags: []Tag{{ID: 1, Name: "music"}}}
	writer := &blockedResponse{header: make(http.Header), started: make(chan struct{}), release: make(chan struct{})}
	e := echo.New()
	c := e.NewContext(httptest.NewRequest("GET", "/api/tag", nil), writer)
	c.SetPath("/api/tag")
	done := make(chan error, 1)
	go func() { done <- memoryHandler(c) }()
	select {
	case <-writer.started:
	case <-time.After(time.Second):
		t.Fatal("response did not start")
	}
	unlocked := memMu.TryLock()
	if unlocked {
		memMu.Unlock()
	}
	close(writer.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !unlocked {
		t.Fatal("response held read-model lock while blocked on client")
	}
}
