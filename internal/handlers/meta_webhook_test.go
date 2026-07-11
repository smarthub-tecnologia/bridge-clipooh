package handlers

// TestMain is defined in notify_meta_test.go and covers this file too.
// It sets getMetaConfigFn to a valid-config stub, which is sufficient —
// webhook tests use getInstanceIDByPhoneNumberIDFn (saved/restored per test)
// and configure wamidstore entries directly.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linkkotech/bridge/pkg/metaconfig"
	"github.com/linkkotech/bridge/pkg/wamidstore"
)

const testMetaAppSecret = "test-meta-app-secret"

// metaSig computes the X-Hub-Signature-256 value for a given body and secret.
func metaSig(body, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// signedRequest creates a POST request with the X-Hub-Signature-256 header set.
func signedRequest(body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/meta/webhook", strings.NewReader(body))
	r.Header.Set("X-Hub-Signature-256", metaSig(body, testMetaAppSecret))
	return r
}

func TestMetaWebhook_VerifySuccess(t *testing.T) {
	t.Setenv("META_WEBHOOK_VERIFY_TOKEN", "my-verify-token")

	h := NewMetaWebhookHandler()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet,
		"/api/v1/meta/webhook?hub.mode=subscribe&hub.verify_token=my-verify-token&hub.challenge=abc123",
		nil,
	)
	h.VerifyWebhook(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if body := w.Body.String(); body != "abc123" {
		t.Errorf("body = %q, want %q", body, "abc123")
	}
}

func TestMetaWebhook_VerifyWrongToken(t *testing.T) {
	t.Setenv("META_WEBHOOK_VERIFY_TOKEN", "correct-token")

	h := NewMetaWebhookHandler()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet,
		"/api/v1/meta/webhook?hub.mode=subscribe&hub.verify_token=wrong-token&hub.challenge=abc123",
		nil,
	)
	h.VerifyWebhook(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

func TestMetaWebhook_DeliveredEvent(t *testing.T) {
	wamidstore.Set("wamid.del001", "evt-del-001")

	prev := getInstanceIDByPhoneNumberIDFn
	getInstanceIDByPhoneNumberIDFn = func(_ string) (string, error) {
		return "linkko-prod", nil
	}
	defer func() { getInstanceIDByPhoneNumberIDFn = prev }()

	done := make(chan struct{})
	var captured map[string]interface{}
	cbSrv := callbackServer(t, done, &captured)
	defer cbSrv.Close()

	t.Setenv("META_APP_SECRET", testMetaAppSecret)
	t.Setenv("CALLBACKS_DIRECTUS_WEBHOOK_URL", cbSrv.URL)
	t.Setenv("CALLBACKS_ENABLED", "true")

	payload := `{
		"entry": [{
			"changes": [{
				"value": {
					"metadata": {"phone_number_id": "109876543210123"},
					"statuses": [{
						"id": "wamid.del001",
						"status": "delivered",
						"timestamp": "1715000010"
					}]
				}
			}]
		}]
	}`

	h := NewMetaWebhookHandler()
	w := httptest.NewRecorder()
	r := signedRequest(payload)
	h.HandleEvent(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for delivered callback")
	}

	if captured["event"] != "delivered" {
		t.Errorf("event = %v, want delivered", captured["event"])
	}
	if captured["event_id"] != "evt-del-001" {
		t.Errorf("event_id = %v, want evt-del-001", captured["event_id"])
	}
}

func TestMetaWebhook_MessageReceived_Text(t *testing.T) {
	prevInst := getInstanceIDByPhoneNumberIDFn
	getInstanceIDByPhoneNumberIDFn = func(_ string) (string, error) { return "linkko-prod", nil }
	defer func() { getInstanceIDByPhoneNumberIDFn = prevInst }()

	prevLookup := lookupEventIDByPhoneFn
	lookupEventIDByPhoneFn = func(_, _ string) (string, bool, error) { return "evt-msg-001", true, nil }
	defer func() { lookupEventIDByPhoneFn = prevLookup }()

	done := make(chan struct{})
	var captured map[string]interface{}
	cbSrv := callbackServer(t, done, &captured)
	defer cbSrv.Close()

	t.Setenv("META_APP_SECRET", testMetaAppSecret)
	t.Setenv("CALLBACKS_DIRECTUS_WEBHOOK_URL", cbSrv.URL)
	t.Setenv("CALLBACKS_ENABLED", "true")

	payload := `{
		"entry": [{
			"changes": [{
				"value": {
					"metadata": {"phone_number_id": "109876543210123"},
					"messages": [{
						"id":        "wamid.inbound001",
						"from":      "5511999999999",
						"timestamp": "1715000000",
						"type":      "text",
						"text":      {"body": "Oi, quero saber mais"}
					}]
				}
			}]
		}]
	}`

	h := NewMetaWebhookHandler()
	w := httptest.NewRecorder()
	r := signedRequest(payload)
	h.HandleEvent(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message.received callback")
	}

	if captured["event"] != "message.received" {
		t.Errorf("event = %v, want message.received", captured["event"])
	}
	if captured["event_id"] != "evt-msg-001" {
		t.Errorf("event_id = %v, want evt-msg-001", captured["event_id"])
	}
	reply, _ := captured["reply"].(map[string]interface{})
	if reply["message_type"] != "text" {
		t.Errorf("reply.message_type = %v, want text", reply["message_type"])
	}
	if reply["text"] != "Oi, quero saber mais" {
		t.Errorf("reply.text = %v, want 'Oi, quero saber mais'", reply["text"])
	}
}

func TestMetaWebhook_MessageReceived_NonText(t *testing.T) {
	prevInst := getInstanceIDByPhoneNumberIDFn
	getInstanceIDByPhoneNumberIDFn = func(_ string) (string, error) { return "linkko-prod", nil }
	defer func() { getInstanceIDByPhoneNumberIDFn = prevInst }()

	prevLookup := lookupEventIDByPhoneFn
	lookupEventIDByPhoneFn = func(_, _ string) (string, bool, error) { return "evt-msg-002", true, nil }
	defer func() { lookupEventIDByPhoneFn = prevLookup }()

	done := make(chan struct{})
	var captured map[string]interface{}
	cbSrv := callbackServer(t, done, &captured)
	defer cbSrv.Close()

	t.Setenv("META_APP_SECRET", testMetaAppSecret)
	t.Setenv("CALLBACKS_DIRECTUS_WEBHOOK_URL", cbSrv.URL)
	t.Setenv("CALLBACKS_ENABLED", "true")

	// Audio message — no "text" field in payload
	payload := `{
		"entry": [{
			"changes": [{
				"value": {
					"metadata": {"phone_number_id": "109876543210123"},
					"messages": [{
						"id":        "wamid.inbound002",
						"from":      "5511999999999",
						"timestamp": "1715000005",
						"type":      "audio"
					}]
				}
			}]
		}]
	}`

	h := NewMetaWebhookHandler()
	w := httptest.NewRecorder()
	r := signedRequest(payload)
	h.HandleEvent(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message.received callback")
	}

	reply, _ := captured["reply"].(map[string]interface{})
	if reply["message_type"] != "audio" {
		t.Errorf("reply.message_type = %v, want audio", reply["message_type"])
	}
	if reply["text"] != "" {
		t.Errorf("reply.text = %v, want empty string for non-text message", reply["text"])
	}
}

func TestMetaWebhook_MessageReceived_NullEventID(t *testing.T) {
	prevInst := getInstanceIDByPhoneNumberIDFn
	getInstanceIDByPhoneNumberIDFn = func(_ string) (string, error) { return "linkko-prod", nil }
	defer func() { getInstanceIDByPhoneNumberIDFn = prevInst }()

	// Simula telefone sem wa_message_log correspondente.
	prevLookup := lookupEventIDByPhoneFn
	lookupEventIDByPhoneFn = func(_, _ string) (string, bool, error) { return "", false, nil }
	defer func() { lookupEventIDByPhoneFn = prevLookup }()

	done := make(chan struct{})
	var captured map[string]interface{}
	cbSrv := callbackServer(t, done, &captured)
	defer cbSrv.Close()

	t.Setenv("META_APP_SECRET", testMetaAppSecret)
	t.Setenv("CALLBACKS_DIRECTUS_WEBHOOK_URL", cbSrv.URL)
	t.Setenv("CALLBACKS_ENABLED", "true")

	payload := `{
		"entry": [{
			"changes": [{
				"value": {
					"metadata": {"phone_number_id": "109876543210123"},
					"messages": [{
						"id":        "wamid.inbound003",
						"from":      "5511888880000",
						"timestamp": "1715000020",
						"type":      "text",
						"text":      {"body": "Olá"}
					}]
				}
			}]
		}]
	}`

	h := NewMetaWebhookHandler()
	w := httptest.NewRecorder()
	r := signedRequest(payload)
	h.HandleEvent(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message.received callback with null event_id")
	}

	if captured["event"] != "message.received" {
		t.Errorf("event = %v, want message.received", captured["event"])
	}
	// event_id must be null in JSON — json.Unmarshal into map[string]interface{}
	// represents JSON null as nil.
	if v, exists := captured["event_id"]; !exists || v != nil {
		t.Errorf("event_id = %v, want null (nil)", v)
	}
}

func TestMetaWebhook_UnknownInstance(t *testing.T) {
	prev := getInstanceIDByPhoneNumberIDFn
	getInstanceIDByPhoneNumberIDFn = func(_ string) (string, error) {
		return "", metaconfig.ErrConfigNotFound
	}
	defer func() { getInstanceIDByPhoneNumberIDFn = prev }()

	callbackCalled := make(chan struct{}, 1)
	cbSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callbackCalled <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer cbSrv.Close()

	t.Setenv("META_APP_SECRET", testMetaAppSecret)
	t.Setenv("CALLBACKS_DIRECTUS_WEBHOOK_URL", cbSrv.URL)
	t.Setenv("CALLBACKS_ENABLED", "true")

	payload := `{
		"entry": [{
			"changes": [{
				"value": {
					"metadata": {"phone_number_id": "000000000"},
					"statuses": [{"id": "wamid.xxx", "status": "delivered", "timestamp": "1715000010"}]
				}
			}]
		}]
	}`

	h := NewMetaWebhookHandler()
	w := httptest.NewRecorder()
	r := signedRequest(payload)
	h.HandleEvent(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	select {
	case <-callbackCalled:
		t.Error("callback should not be called for unknown instance")
	case <-time.After(200 * time.Millisecond):
		// correct — no callback dispatched
	}
}

func TestMetaWebhook_SentStatus(t *testing.T) {
	wamidstore.Set("wamid.sent001", "evt-sent-001")

	prev := getInstanceIDByPhoneNumberIDFn
	getInstanceIDByPhoneNumberIDFn = func(_ string) (string, error) {
		return "linkko-prod", nil
	}
	defer func() { getInstanceIDByPhoneNumberIDFn = prev }()

	done := make(chan struct{})
	var captured map[string]interface{}
	cbSrv := callbackServer(t, done, &captured)
	defer cbSrv.Close()

	t.Setenv("META_APP_SECRET", testMetaAppSecret)
	t.Setenv("CALLBACKS_DIRECTUS_WEBHOOK_URL", cbSrv.URL)
	t.Setenv("CALLBACKS_ENABLED", "true")

	payload := `{
		"entry": [{
			"changes": [{
				"value": {
					"metadata": {"phone_number_id": "109876543210123"},
					"statuses": [{
						"id": "wamid.sent001",
						"status": "sent",
						"timestamp": "1715000000",
						"conversation": {
							"id": "conv-abc123",
							"origin": {"type": "marketing"}
						}
					}]
				}
			}]
		}]
	}`

	h := NewMetaWebhookHandler()
	w := httptest.NewRecorder()
	r := signedRequest(payload)
	h.HandleEvent(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for sent callback")
	}

	if captured["event"] != "sent" {
		t.Errorf("event = %v, want sent", captured["event"])
	}
	if captured["event_id"] != "evt-sent-001" {
		t.Errorf("event_id = %v, want evt-sent-001", captured["event_id"])
	}
	if captured["conversation_id"] != "conv-abc123" {
		t.Errorf("conversation_id = %v, want conv-abc123", captured["conversation_id"])
	}
	if captured["conversation_origin"] != "marketing" {
		t.Errorf("conversation_origin = %v, want marketing", captured["conversation_origin"])
	}
}

// ── verifyMetaSignature unit tests ────────────────────────────────────────────

func TestVerifyMetaSignature_Valid(t *testing.T) {
	t.Setenv("META_APP_SECRET", testMetaAppSecret)
	body := []byte(`{"entry":[]}`)
	sig := metaSig(string(body), testMetaAppSecret)
	if !verifyMetaSignature(body, sig) {
		t.Error("expected valid signature to return true")
	}
}

func TestVerifyMetaSignature_Missing(t *testing.T) {
	t.Setenv("META_APP_SECRET", testMetaAppSecret)
	body := []byte(`{"entry":[]}`)
	if verifyMetaSignature(body, "") {
		t.Error("expected missing header to return false")
	}
}

func TestVerifyMetaSignature_Invalid(t *testing.T) {
	t.Setenv("META_APP_SECRET", testMetaAppSecret)
	body := []byte(`{"entry":[]}`)
	if verifyMetaSignature(body, "sha256=deadbeef") {
		t.Error("expected wrong digest to return false")
	}
}

func TestVerifyMetaSignature_EmptySecret(t *testing.T) {
	t.Setenv("META_APP_SECRET", "")
	body := []byte(`{"entry":[]}`)
	sig := metaSig(string(body), testMetaAppSecret)
	if verifyMetaSignature(body, sig) {
		t.Error("expected empty secret (strict mode) to return false")
	}
}

func TestVerifyMetaSignature_MissingPrefix(t *testing.T) {
	t.Setenv("META_APP_SECRET", testMetaAppSecret)
	body := []byte(`{"entry":[]}`)
	// Header without "sha256=" prefix
	mac := hmac.New(sha256.New, []byte(testMetaAppSecret))
	mac.Write(body)
	raw := hex.EncodeToString(mac.Sum(nil))
	if verifyMetaSignature(body, raw) {
		t.Error("expected header without sha256= prefix to return false")
	}
}

func TestHandleEvent_RejectsMissingSignature(t *testing.T) {
	t.Setenv("META_APP_SECRET", testMetaAppSecret)

	h := NewMetaWebhookHandler()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/meta/webhook",
		strings.NewReader(`{"entry":[]}`))
	// No X-Hub-Signature-256 header set
	h.HandleEvent(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

func TestHandleEvent_RejectsInvalidSignature(t *testing.T) {
	t.Setenv("META_APP_SECRET", testMetaAppSecret)

	h := NewMetaWebhookHandler()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/meta/webhook",
		strings.NewReader(`{"entry":[]}`))
	r.Header.Set("X-Hub-Signature-256", "sha256=badhash")
	h.HandleEvent(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

// Verify that the unixStringToISO8601 helper converts correctly.
func TestUnixStringToISO8601(t *testing.T) {
	got := unixStringToISO8601("1715000000")
	want := time.Unix(1715000000, 0).UTC().Format(time.RFC3339)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// Fallback: non-numeric input returns as-is.
	if unixStringToISO8601("not-a-ts") != "not-a-ts" {
		t.Error("expected non-numeric input to be returned unchanged")
	}
}

// Ensure JSON encoding of captured callbacks matches expected field names.
func TestMetaDeliveredCallback_JSONFields(t *testing.T) {
	cb := metaDeliveredCallback{
		Event:         "delivered",
		EventID:       "evt-001",
		InstanceID:    "inst-001",
		Timestamp:     "2024-05-06T00:00:00Z",
		Provider:      "meta",
		Wamid:         "wamid.abc",
		PhoneNumberID: "1098",
		DeliveredAt:   "2024-05-06T00:00:10Z",
	}
	b, err := json.Marshal(cb)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"event", "event_id", "instance_id", "timestamp", "provider", "wamid", "phone_number_id", "delivered_at"} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing JSON field: %s", key)
		}
	}
}
