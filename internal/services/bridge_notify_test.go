package services

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestNotifyConnectionEventDeliversConnectedEvent(t *testing.T) {
	type evt struct {
		Event    string `json:"event"`
		Instance string `json:"instance"`
		Phone    string `json:"phone"`
		Ts       int64  `json:"ts"`
	}

	received := make(chan evt, 1)
	receivedToken := make(chan string, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedToken <- r.Header.Get("X-Access-Token")
		body, _ := io.ReadAll(r.Body)
		var e evt
		_ = json.Unmarshal(body, &e)
		received <- e
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("CONNECTION_WEBHOOK_URL", srv.URL)
	t.Setenv("CONNECTION_WEBHOOK_TOKEN", "token-teste")

	b := &BridgeService{}
	b.notifyConnectionEvent(zap.NewNop(), "connected", map[string]interface{}{
		"instance": "clipooh-8eb2c8",
		"phone":    "+5522992454260",
	})

	select {
	case e := <-received:
		if e.Event != "connected" {
			t.Fatalf("expected event=connected, got %q", e.Event)
		}
		if e.Instance != "clipooh-8eb2c8" {
			t.Fatalf("expected instance clipooh-8eb2c8, got %q", e.Instance)
		}
		if e.Phone != "+5522992454260" {
			t.Fatalf("expected phone +5522992454260, got %q", e.Phone)
		}
		if e.Ts <= 0 {
			t.Fatalf("expected ts to be populated, got %d", e.Ts)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("outbound webhook was not received within timeout")
	}

	select {
	case tok := <-receivedToken:
		if tok != "token-teste" {
			t.Fatalf("expected X-Access-Token header token-teste, got %q", tok)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("request headers were not captured")
	}
}

func TestNotifyConnectionEventNoopWhenNotConfigured(t *testing.T) {
	t.Setenv("CONNECTION_WEBHOOK_URL", "")
	t.Setenv("CONNECTION_WEBHOOK_TOKEN", "")

	b := &BridgeService{}
	// Deve retornar sem criar goroutine nem panic quando a URL não está definida.
	b.notifyConnectionEvent(zap.NewNop(), "connected", map[string]interface{}{
		"instance": "clipooh-8eb2c8",
	})
}
