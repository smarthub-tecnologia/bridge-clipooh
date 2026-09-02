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

// ── Integration test 1 ────────────────────────────────────────────────────────
//
// Full success cycle: send → "sent" logged (from notify_meta) → webhook
// "delivered" → "delivered" logged (from meta_webhook). The bridge no longer
// notifies any external platform (Directus/Linkko) — the only observable
// outcome of each step is a structured log entry plus, for the send step,
// the wamidstore mapping used to correlate the later webhook.

func TestIntegration_MetaFullCycle_Success(t *testing.T) {
	t.Setenv("BRIDGE_API_KEY", "int-bridge-key")
	t.Setenv("META_APP_SECRET", testMetaAppSecret)

	// Mock Meta Graph API — returns minimal 200 OK without conversation fields,
	// because in production those come from webhook statuses, not the send
	// response. The "sent" log entry from notify_meta will therefore have
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

	apiSrv := buildIntegrationRouter(t)

	sendDone, sendSignal := waitOnce()
	prevSendHook := afterMetaSend
	afterMetaSend = sendSignal
	defer func() { afterMetaSend = prevSendHook }()

	webhookDone, webhookSignal := waitOnce()
	prevWebhookHook := afterMetaWebhookEvent
	afterMetaWebhookEvent = webhookSignal
	defer func() { afterMetaWebhookEvent = prevWebhookHook }()

	logs := withObservedLogs(func() {
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

		// Step 2: Wait for the notify_meta goroutine to finish.
		waitFor(t, sendDone)

		// Step 3: POST /api/v1/meta/webhook with delivered status → 200.
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
		wReq.Header.Set("X-Hub-Signature-256", metaSig(webhookPayload, testMetaAppSecret))
		wResp, err := http.DefaultClient.Do(wReq)
		if err != nil {
			t.Fatalf("webhook request: %v", err)
		}
		wResp.Body.Close()
		if wResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 for webhook, got %d", wResp.StatusCode)
		}

		// Step 4: Wait for the meta_webhook goroutine to finish.
		waitFor(t, webhookDone)
	})

	sent := findLog(logs, "meta send: sent")
	if sent == nil {
		t.Fatal("expected a 'meta send: sent' log entry")
	}
	sentFields := sent.ContextMap()
	if sentFields["event_id"] != "evt-int-001" {
		t.Errorf("event_id = %v, want evt-int-001", sentFields["event_id"])
	}
	if sentFields["wamid"] != "wamid.inttest001" {
		t.Errorf("wamid = %v, want wamid.inttest001", sentFields["wamid"])
	}
	if sentFields["phone_number_id"] != "12345670001" {
		t.Errorf("phone_number_id = %v, want 12345670001", sentFields["phone_number_id"])
	}
	// conversation_id and conversation_origin must be present but empty —
	// the Meta 200 OK does not carry these fields; they arrive only via
	// webhook statuses[].status="sent".
	if v, _ := sentFields["conversation_id"].(string); v != "" {
		t.Errorf("conversation_id = %q, want empty string (sent response carries no conversation)", v)
	}
	if v, _ := sentFields["conversation_origin"].(string); v != "" {
		t.Errorf("conversation_origin = %q, want empty string (sent response carries no conversation)", v)
	}

	// Step 5: Verify wamid → event_id mapping was stored before the log entry.
	if _, ok := wamidstore.Get("wamid.inttest001"); !ok {
		t.Error("wamidstore.Get(wamid.inttest001): expected ok=true after send")
	}

	delivered := findLog(logs, "meta message status: delivered")
	if delivered == nil {
		t.Fatal("expected a 'meta message status: delivered' log entry")
	}
	deliveredFields := delivered.ContextMap()
	if deliveredFields["event_id"] != "evt-int-001" {
		t.Errorf("event_id = %v, want evt-int-001", deliveredFields["event_id"])
	}
	deliveredAt, _ := deliveredFields["delivered_at"].(string)
	if _, parseErr := time.Parse(time.RFC3339, deliveredAt); parseErr != nil {
		t.Errorf("delivered_at %q is not RFC3339: %v", deliveredAt, parseErr)
	}

	if n := metaHits.Load(); n != 1 {
		t.Errorf("Meta API received %d requests, want exactly 1", n)
	}
}

// ── Integration test 2 ────────────────────────────────────────────────────────
//
// Token error: Meta returns 400 code:190 → "meta send blocked" log entry with
// reason="invalid_token" and no wamid (nothing to store).

func TestIntegration_MetaFullCycle_TokenError(t *testing.T) {
	t.Setenv("BRIDGE_API_KEY", "int-bridge-key")

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

	apiSrv := buildIntegrationRouter(t)

	done, signal := waitOnce()
	prevHook := afterMetaSend
	afterMetaSend = signal
	defer func() { afterMetaSend = prevHook }()

	logs := withObservedLogs(func() {
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

		waitFor(t, done)
	})

	blocked := findLog(logs, "meta send blocked: meta api error")
	if blocked == nil {
		t.Fatal("expected a 'meta send blocked: meta api error' log entry")
	}
	fields := blocked.ContextMap()
	if fields["reason"] != "invalid_token" {
		t.Errorf("reason = %v, want invalid_token", fields["reason"])
	}
	metaErrorCode, ok := fields["meta_error_code"].(int64)
	if !ok || metaErrorCode != 190 {
		t.Errorf("meta_error_code = %v, want 190", fields["meta_error_code"])
	}
	if fields["phone_number_id"] != "12345670002" {
		t.Errorf("phone_number_id = %v, want 12345670002", fields["phone_number_id"])
	}
}

// ── Integration test 3 ────────────────────────────────────────────────────────
//
// Inbound message: the lead replies → webhook logs "meta message received"
// with full reply fields and correct event_id traceability.

func TestIntegration_MetaFullCycle_InboundMessage(t *testing.T) {
	t.Setenv("META_APP_SECRET", testMetaAppSecret)

	// Simulate a wamid stored from a previous outbound send.
	wamidstore.Set("wamid.inbound-int001", "evt-original-int123")

	origPhoneFn := getInstanceIDByPhoneNumberIDFn
	getInstanceIDByPhoneNumberIDFn = func(_ string) (string, error) {
		return "linkko-int-prod", nil
	}
	defer func() { getInstanceIDByPhoneNumberIDFn = origPhoneFn }()

	apiSrv := buildIntegrationRouter(t)

	done, signal := waitOnce()
	prevHook := afterMetaWebhookEvent
	afterMetaWebhookEvent = signal
	defer func() { afterMetaWebhookEvent = prevHook }()

	logs := withObservedLogs(func() {
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
		req.Header.Set("X-Hub-Signature-256", metaSig(webhookPayload, testMetaAppSecret))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("webhook request: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		waitFor(t, done)
	})

	entry := findLog(logs, "meta message received")
	if entry == nil {
		t.Fatal("expected a 'meta message received' log entry")
	}
	fields := entry.ContextMap()
	if fields["event_id"] != "evt-original-int123" {
		t.Errorf("event_id = %v, want evt-original-int123 (end-to-end traceability)", fields["event_id"])
	}
	if fields["instance_id"] != "linkko-int-prod" {
		t.Errorf("instance_id = %v, want linkko-int-prod", fields["instance_id"])
	}
	if fields["phone_number_id"] != "12345670003" {
		t.Errorf("phone_number_id = %v, want 12345670003", fields["phone_number_id"])
	}
	if fields["from"] != "5511999999999" {
		t.Errorf("from = %v, want 5511999999999", fields["from"])
	}
	if fields["text"] != "Quero saber mais" {
		t.Errorf("text = %v, want 'Quero saber mais'", fields["text"])
	}
	if fields["message_type"] != "text" {
		t.Errorf("message_type = %v, want text", fields["message_type"])
	}
	replyAt, _ := fields["reply_at"].(string)
	if _, parseErr := time.Parse(time.RFC3339, replyAt); parseErr != nil {
		t.Errorf("reply_at %q is not RFC3339: %v", replyAt, parseErr)
	}
}
