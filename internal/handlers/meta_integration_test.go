//go:build integration

package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/linkkotech/bridge/internal/middleware"
	"github.com/linkkotech/bridge/pkg/metaconfig"
	"github.com/linkkotech/bridge/pkg/wamidstore"
)

// buildIntegrationRouter wires up only the Meta-relevant routes for integration
// tests. notificationService is nil because the Meta path returns before it is
// ever accessed.
func buildIntegrationRouter(t *testing.T) *httptest.Server {
	t.Helper()
	auth := middleware.NewAuthService()
	notifyH := NewNotifyHandler(nil, auth)
	webhookH := NewMetaWebhookHandler()

	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(middleware.BridgeAPIKeyAuth(auth))
		r.Post("/api/v1/notify/send", notifyH.SendMessage)
	})
	r.Get("/api/v1/meta/webhook", webhookH.VerifyWebhook)
	r.Post("/api/v1/meta/webhook", webhookH.HandleEvent)

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

// metaCallbackCollector returns an httptest.Server that streams every received
// callback into a buffered channel and counts total calls.
func metaCallbackCollector(t *testing.T) (*httptest.Server, chan map[string]interface{}, *atomic.Int32) {
	t.Helper()
	ch := make(chan map[string]interface{}, 10)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var m map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&m)
		ch <- m
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, ch, &hits
}

// receiveCallback blocks until the next callback arrives or the 2-second
// timeout expires.
func receiveCallback(t *testing.T, ch <-chan map[string]interface{}, label string) map[string]interface{} {
	t.Helper()
	select {
	case m := <-ch:
		return m
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for %s callback", label)
		return nil
	}
}

// ── Integration test 1 ────────────────────────────────────────────────────────
//
// Full success cycle: send → "sent" callback (from notify_meta) → webhook
// "delivered" → "delivered" callback (from meta_webhook).

func TestIntegration_MetaFullCycle_Success(t *testing.T) {
	t.Setenv("BRIDGE_API_KEY", "int-bridge-key")
	t.Setenv("CALLBACKS_ENABLED", "true")

	// Mock Meta Graph API — returns minimal 200 OK without conversation fields,
	// because in production those come from webhook statuses, not the send
	// response. The "sent" callback from notify_meta will therefore have
	// conversation_id="" and conversation_origin="".
	var metaHits atomic.Int32
	metaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		metaHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"messages": []map[string]string{{"id": "wamid.inttest001"}},
		})
	}))
	t.Cleanup(metaSrv.Close)

	origHost := metaGraphAPIHost
	metaGraphAPIHost = metaSrv.URL
	defer func() { metaGraphAPIHost = origHost }()

	origCfgFn := getMetaConfigFn
	getMetaConfigFn = func(_ string) (*metaconfig.MetaConfig, error) {
		return &metaconfig.MetaConfig{PhoneNumberID: "12345670001", AccessToken: "EAABint"}, nil
	}
	defer func() { getMetaConfigFn = origCfgFn }()

	origPhoneFn := getInstanceIDByPhoneNumberIDFn
	getInstanceIDByPhoneNumberIDFn = func(_ string) (string, error) {
		return "linkko-int-prod", nil
	}
	defer func() { getInstanceIDByPhoneNumberIDFn = origPhoneFn }()

	cbSrv, cbCh, cbHits := metaCallbackCollector(t)
	t.Setenv("CALLBACKS_DIRECTUS_WEBHOOK_URL", cbSrv.URL)

	apiSrv := buildIntegrationRouter(t)

	// Step 1: POST /api/v1/notify/send → 202.
	sendBody := `{"provider":"meta","instance":"int-success-001","to":"11987654321","event_id":"evt-int-001","meta":{"template_name":"hello_world","language_code":"pt_BR"}}`
	req, _ := http.NewRequest(http.MethodPost, apiSrv.URL+"/api/v1/notify/send", strings.NewReader(sendBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer int-bridge-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}

	// Step 2: Wait for "sent" callback from notify_meta goroutine.
	sent := receiveCallback(t, cbCh, "sent")

	if sent["event"] != "sent" {
		t.Errorf("event = %v, want sent", sent["event"])
	}
	if sent["event_id"] != "evt-int-001" {
		t.Errorf("event_id = %v, want evt-int-001", sent["event_id"])
	}
	if sent["wamid"] != "wamid.inttest001" {
		t.Errorf("wamid = %v, want wamid.inttest001", sent["wamid"])
	}
	if sent["provider"] != "meta" {
		t.Errorf("provider = %v, want meta", sent["provider"])
	}
	if sent["phone_number_id"] != "12345670001" {
		t.Errorf("phone_number_id = %v, want 12345670001", sent["phone_number_id"])
	}
	// conversation_id and conversation_origin must be present but empty —
	// the Meta 200 OK does not carry these fields; they arrive only via
	// webhook statuses[].status="sent".
	if v, _ := sent["conversation_id"].(string); v != "" {
		t.Errorf("conversation_id = %q, want empty string (sent response carries no conversation)", v)
	}
	if v, _ := sent["conversation_origin"].(string); v != "" {
		t.Errorf("conversation_origin = %q, want empty string (sent response carries no conversation)", v)
	}

	// Step 3: Verify wamid → event_id mapping was stored before the callback.
	if _, ok := wamidstore.Get("wamid.inttest001"); !ok {
		t.Error("wamidstore.Get(wamid.inttest001): expected ok=true after sent callback")
	}

	// Step 4: POST /api/v1/meta/webhook with delivered status → 200.
	webhookPayload := `{
		"entry": [{
			"changes": [{
				"value": {
					"metadata": {"phone_number_id": "12345670001"},
					"statuses": [{
						"id": "wamid.inttest001",
						"status": "delivered",
						"timestamp": "1715000100"
					}]
				}
			}]
		}]
	}`
	wReq, _ := http.NewRequest(http.MethodPost, apiSrv.URL+"/api/v1/meta/webhook", strings.NewReader(webhookPayload))
	wReq.Header.Set("Content-Type", "application/json")
	wResp, err := http.DefaultClient.Do(wReq)
	if err != nil {
		t.Fatalf("webhook request: %v", err)
	}
	wResp.Body.Close()
	if wResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for webhook, got %d", wResp.StatusCode)
	}

	// Step 5: Wait for "delivered" callback from meta_webhook goroutine.
	delivered := receiveCallback(t, cbCh, "delivered")

	if delivered["event"] != "delivered" {
		t.Errorf("event = %v, want delivered", delivered["event"])
	}
	if delivered["event_id"] != "evt-int-001" {
		t.Errorf("event_id = %v, want evt-int-001", delivered["event_id"])
	}
	if delivered["provider"] != "meta" {
		t.Errorf("provider = %v, want meta", delivered["provider"])
	}
	deliveredAt, _ := delivered["delivered_at"].(string)
	if _, parseErr := time.Parse(time.RFC3339, deliveredAt); parseErr != nil {
		t.Errorf("delivered_at %q is not RFC3339: %v", deliveredAt, parseErr)
	}

	// Step 6: Cardinality assertions.
	if n := metaHits.Load(); n != 1 {
		t.Errorf("Meta API received %d requests, want exactly 1", n)
	}
	if n := cbHits.Load(); n != 2 {
		t.Errorf("callback received %d calls, want exactly 2 (sent + delivered)", n)
	}
}

// ── Integration test 2 ────────────────────────────────────────────────────────
//
// Token error: Meta returns 400 code:190 → "blocked_meta_error" callback with
// wamid=null and reason="invalid_token".

func TestIntegration_MetaFullCycle_TokenError(t *testing.T) {
	t.Setenv("BRIDGE_API_KEY", "int-bridge-key")
	t.Setenv("CALLBACKS_ENABLED", "true")

	// Mock Meta Graph API — returns 400 OAuthException.
	metaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"code": 190,
				"type": "OAuthException",
			},
		})
	}))
	t.Cleanup(metaSrv.Close)

	origHost := metaGraphAPIHost
	metaGraphAPIHost = metaSrv.URL
	defer func() { metaGraphAPIHost = origHost }()

	origCfgFn := getMetaConfigFn
	getMetaConfigFn = func(_ string) (*metaconfig.MetaConfig, error) {
		return &metaconfig.MetaConfig{PhoneNumberID: "12345670002", AccessToken: "EAABint"}, nil
	}
	defer func() { getMetaConfigFn = origCfgFn }()

	cbSrv, cbCh, _ := metaCallbackCollector(t)
	t.Setenv("CALLBACKS_DIRECTUS_WEBHOOK_URL", cbSrv.URL)

	apiSrv := buildIntegrationRouter(t)

	sendBody := `{"provider":"meta","instance":"int-error-001","to":"11987654322","event_id":"evt-int-002","meta":{"template_name":"hello_world","language_code":"pt_BR"}}`
	req, _ := http.NewRequest(http.MethodPost, apiSrv.URL+"/api/v1/notify/send", strings.NewReader(sendBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer int-bridge-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}

	blocked := receiveCallback(t, cbCh, "blocked_meta_error")

	if blocked["event"] != "blocked_meta_error" {
		t.Errorf("event = %v, want blocked_meta_error", blocked["event"])
	}
	if blocked["reason"] != "invalid_token" {
		t.Errorf("reason = %v, want invalid_token", blocked["reason"])
	}
	code, ok := blocked["meta_error_code"].(float64)
	if !ok || code != 190 {
		t.Errorf("meta_error_code = %v, want 190", blocked["meta_error_code"])
	}
	if blocked["wamid"] != nil {
		t.Errorf("wamid must be JSON null, got %v", blocked["wamid"])
	}
	if blocked["provider"] != "meta" {
		t.Errorf("provider = %v, want meta", blocked["provider"])
	}
	if blocked["phone_number_id"] != "12345670002" {
		t.Errorf("phone_number_id = %v, want 12345670002", blocked["phone_number_id"])
	}
}

// ── Integration test 3 ────────────────────────────────────────────────────────
//
// Inbound message: the lead replies → webhook delivers "message.received"
// callback with full reply sub-object and correct event_id traceability.

func TestIntegration_MetaFullCycle_InboundMessage(t *testing.T) {
	t.Setenv("CALLBACKS_ENABLED", "true")

	// Simulate a wamid stored from a previous outbound send.
	wamidstore.Set("wamid.inbound-int001", "evt-original-int123")

	origPhoneFn := getInstanceIDByPhoneNumberIDFn
	getInstanceIDByPhoneNumberIDFn = func(_ string) (string, error) {
		return "linkko-int-prod", nil
	}
	defer func() { getInstanceIDByPhoneNumberIDFn = origPhoneFn }()

	cbSrv, cbCh, _ := metaCallbackCollector(t)
	t.Setenv("CALLBACKS_DIRECTUS_WEBHOOK_URL", cbSrv.URL)

	apiSrv := buildIntegrationRouter(t)

	webhookPayload := `{
		"entry": [{
			"changes": [{
				"value": {
					"metadata": {"phone_number_id": "12345670003"},
					"messages": [{
						"id":        "wamid.inbound-int001",
						"from":      "5511999999999",
						"timestamp": "1715000200",
						"type":      "text",
						"text":      {"body": "Quero saber mais"}
					}]
				}
			}]
		}]
	}`

	req, _ := http.NewRequest(http.MethodPost, apiSrv.URL+"/api/v1/meta/webhook", strings.NewReader(webhookPayload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("webhook request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	cb := receiveCallback(t, cbCh, "message.received")

	if cb["event"] != "message.received" {
		t.Errorf("event = %v, want message.received", cb["event"])
	}
	if cb["event_id"] != "evt-original-int123" {
		t.Errorf("event_id = %v, want evt-original-int123 (end-to-end traceability)", cb["event_id"])
	}
	if cb["instance_id"] != "linkko-int-prod" {
		t.Errorf("instance_id = %v, want linkko-int-prod", cb["instance_id"])
	}
	if cb["provider"] != "meta" {
		t.Errorf("provider = %v, want meta", cb["provider"])
	}
	if cb["phone_number_id"] != "12345670003" {
		t.Errorf("phone_number_id = %v, want 12345670003", cb["phone_number_id"])
	}

	reply, _ := cb["reply"].(map[string]interface{})
	if reply == nil {
		t.Fatal("reply field missing from message.received callback")
	}
	if reply["from"] != "5511999999999" {
		t.Errorf("reply.from = %v, want 5511999999999", reply["from"])
	}
	if reply["text"] != "Quero saber mais" {
		t.Errorf("reply.text = %v, want 'Quero saber mais'", reply["text"])
	}
	if reply["message_type"] != "text" {
		t.Errorf("reply.message_type = %v, want text", reply["message_type"])
	}
	replyAt, _ := reply["reply_at"].(string)
	if _, parseErr := time.Parse(time.RFC3339, replyAt); parseErr != nil {
		t.Errorf("reply_at %q is not RFC3339: %v", replyAt, parseErr)
	}
}
