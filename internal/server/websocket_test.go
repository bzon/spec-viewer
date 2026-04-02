package server_test

import (
	"testing"
	"time"

	"github.com/bzon/spec-viewer/internal/server"
)

func TestHubBroadcast(t *testing.T) {
	h := server.NewHub()
	ch := make(chan []byte, 1)
	h.Register(ch)

	msg := []byte("reload")
	h.Broadcast(msg)

	select {
	case got := <-ch:
		if string(got) != string(msg) {
			t.Fatalf("expected %q, got %q", msg, got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for broadcast")
	}
}

func TestHubUnregister(t *testing.T) {
	h := server.NewHub()
	ch := make(chan []byte, 1)
	h.Register(ch)
	h.Unregister(ch)

	// Should not block or panic after unregister
	h.Broadcast([]byte("reload"))
}
