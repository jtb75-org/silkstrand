package websocket

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gws "github.com/gorilla/websocket"
)

func TestHandleConnectionOnConnectRunsAfterRegistration(t *testing.T) {
	h := NewHub()
	agentID := "agent-1"
	connected := make(chan bool, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := h.HandleConnectionWithHook(w, r, agentID, func() {
			connected <- h.IsConnected(agentID)
			if err := h.Send(agentID, Message{Type: TypeDirective}); err != nil {
				t.Errorf("Send from OnConnect: %v", err)
			}
		}); err != nil {
			t.Errorf("HandleConnection: %v", err)
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	select {
	case ok := <-connected:
		if !ok {
			t.Fatal("OnConnect ran before the agent was registered")
		}
	case <-time.After(time.Second):
		t.Fatal("OnConnect did not run")
	}

	var msg Message
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("reading OnConnect message: %v", err)
	}
	if msg.Type != TypeDirective {
		t.Fatalf("message type = %q, want %q", msg.Type, TypeDirective)
	}
}
