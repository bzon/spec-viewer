package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/bzon/spec-viewer/internal/instance"
)

func TestDescribeHandoff(t *testing.T) {
	info := instance.Info{Port: 4321, PID: 1}
	accepted := func(clients int) instance.Handoff {
		return instance.Handoff{Done: true, Holder: true, Info: info, Result: instance.OpenResult{URL: "/w/abc/#a.md", Clients: clients}}
	}

	tests := []struct {
		name        string
		handoff     instance.Handoff
		noOpen      bool
		wantHandled bool
		wantOpened  string
		wantInMsg   string
	}{
		{"no tab opens browser", accepted(0), false, true, "http://localhost:4321/w/abc/#a.md", "sent /x/a.md to existing instance on port 4321"},
		{"tab connected does not open", accepted(2), false, true, "", "http://localhost:4321/w/abc/#a.md"},
		{"no-open does not open", accepted(0), true, true, "", "sent /x/a.md"},
		{"workspace created is a normal outcome", instance.Handoff{Done: true, Holder: true, Info: info, Result: instance.OpenResult{URL: "/w/new/", Created: true}}, false, true, "http://localhost:4321/w/new/", "url http://localhost:4321/w/new/"},
		{"full registry starts separately", instance.Handoff{Holder: true, Info: info, Err: errors.New("instance: /api/open returned 409")}, false, false, "", "without taking over its lock"},
		{"old lock format starts separately", instance.Handoff{Holder: true, Info: info, Err: instance.ErrNoToken}, false, false, "", "no token"},
		{"other refusal starts separately", instance.Handoff{Holder: true, Info: info, Err: errors.New("boom")}, false, false, "", "boom"},
		{"refused path ends the command", instance.Handoff{Holder: true, Info: info, Err: &instance.RejectedError{Status: 404, Message: "not found"}}, false, true, "", "refused /x/a.md: not found"},
		{"no instance is silent", instance.Handoff{}, false, false, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opened := ""
			got := describeHandoff(tt.handoff, "/x/a.md", tt.noOpen, func(u string) { opened = u })
			if got.handled != tt.wantHandled {
				t.Errorf("handled = %v, want %v", got.handled, tt.wantHandled)
			}
			if opened != tt.wantOpened {
				t.Errorf("opened %q, want %q", opened, tt.wantOpened)
			}
			if !strings.Contains(got.message, tt.wantInMsg) {
				t.Errorf("message %q lacks %q", got.message, tt.wantInMsg)
			}
		})
	}
}
