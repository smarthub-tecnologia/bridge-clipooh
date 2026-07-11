package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linkkotech/bridge/internal/models"
	"github.com/linkkotech/bridge/pkg/metaconfig"
)

func TestMain(m *testing.M) {
	// Default stub: valid config, no Directus calls.
	getMetaConfigFn = func(_ string) (*metaconfig.MetaConfig, error) {
		return &metaconfig.MetaConfig{
			PhoneNumberID: "109876543210123",
			AccessToken:   "EAABtest",
		}, nil
	}
	os.Exit(m.Run())
}

// callbackServer returns an httptest.Server that decodes the first POST body into
// *captured and signals done exactly once via sync.Once when a request arrives.
func callbackServer(t *testing.T, done chan struct{}, captured *map[string]interface{}) *httptest.Server {
	t.Helper()
	var once sync.Once
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if captured != nil {
			_ = json.NewDecoder(r.Body).Decode(captured)
		}
		once.Do(func() { close(done) })
		w.WriteHeader(http.StatusOK)
	}))
}

func TestMetaSend_Success(t *testing.T) {
	// Meta Graph API mock — returns 200 with a wamid.
	metaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"messaging_product": "whatsapp",
			"contacts":          []map[string]string{{"wa_id": "5511999999999"}},
			"messages":          []map[string]string{{"id": "wamid.HBgL12345"}},
		})
	}))
	defer metaMock.Close()

	done := make(chan struct{})
	var captured map[string]interface{}
	cbSrv := callbackServer(t, done, &captured)
	defer cbSrv.Close()

	prevHost := metaGraphAPIHost
	metaGraphAPIHost = metaMock.URL
	defer func() { metaGraphAPIHost = prevHost }()

	t.Setenv("CALLBACKS_DIRECTUS_WEBHOOK_URL", cbSrv.URL)
	t.Setenv("CALLBACKS_DIRECTUS_WEBHOOK_SECRET", "test-secret")
	t.Setenv("CALLBACKS_ENABLED", "true")

	req := &models.NotifyRequest{
		Instance: "linkko-prod",
		To:       "5511999999999",
		EventID:  "evt-001",
		Meta:     &models.MetaPayload{TemplateName: "welcome", LanguageCode: "pt_BR"},
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	(&NotifyHandler{}).handleMetaSend(w, r, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d", w.Code)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for sent callback")
	}

	if captured["event"] != "sent" {
		t.Errorf("event = %v, want sent", captured["event"])
	}
	if captured["wamid"] != "wamid.HBgL12345" {
		t.Errorf("wamid = %v, want wamid.HBgL12345", captured["wamid"])
	}
}

func TestMetaSend_MissingMetaField(t *testing.T) {
	req := &models.NotifyRequest{
		Instance: "linkko-prod",
		To:       "5511999999999",
		EventID:  "evt-002",
		// Meta: nil — field absent
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	(&NotifyHandler{}).handleMetaSend(w, r, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	var body map[string]interface{}
	_ = json.NewDecoder(w.Body).Decode(&body)
	if body["error"] != "meta_payload_required" {
		t.Errorf("error = %v, want meta_payload_required", body["error"])
	}
}

func TestMetaSend_ConfigNotFound(t *testing.T) {
	prev := getMetaConfigFn
	getMetaConfigFn = func(_ string) (*metaconfig.MetaConfig, error) {
		return nil, metaconfig.ErrConfigNotFound
	}
	defer func() { getMetaConfigFn = prev }()

	req := &models.NotifyRequest{
		Instance: "linkko-prod",
		To:       "5511999999999",
		EventID:  "evt-003",
		Meta:     &models.MetaPayload{TemplateName: "welcome", LanguageCode: "pt_BR"},
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	(&NotifyHandler{}).handleMetaSend(w, r, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestMetaSend_MetaApiError(t *testing.T) {
	// Meta Graph API mock — returns 400 with error code 190.
	metaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"message": "Invalid OAuth access token.",
				"type":    "OAuthException",
				"code":    190,
			},
		})
	}))
	defer metaMock.Close()

	done := make(chan struct{})
	var captured map[string]interface{}
	cbSrv := callbackServer(t, done, &captured)
	defer cbSrv.Close()

	prevHost := metaGraphAPIHost
	metaGraphAPIHost = metaMock.URL
	defer func() { metaGraphAPIHost = prevHost }()

	t.Setenv("CALLBACKS_DIRECTUS_WEBHOOK_URL", cbSrv.URL)
	t.Setenv("CALLBACKS_ENABLED", "true")

	req := &models.NotifyRequest{
		Instance: "linkko-prod",
		To:       "5511999999999",
		EventID:  "evt-004",
		Meta:     &models.MetaPayload{TemplateName: "welcome", LanguageCode: "pt_BR"},
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	(&NotifyHandler{}).handleMetaSend(w, r, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d", w.Code)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for blocked callback")
	}

	if captured["event"] != "blocked_meta_error" {
		t.Errorf("event = %v, want blocked_meta_error", captured["event"])
	}
	if captured["reason"] != "invalid_token" {
		t.Errorf("reason = %v, want invalid_token", captured["reason"])
	}
}

func TestMetaSend_InvalidPhone(t *testing.T) {
	// Meta mock is present but must receive zero requests.
	var metaHits atomic.Int32
	metaMock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		metaHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer metaMock.Close()

	done := make(chan struct{})
	var captured map[string]interface{}
	cbSrv := callbackServer(t, done, &captured)
	defer cbSrv.Close()

	prevHost := metaGraphAPIHost
	metaGraphAPIHost = metaMock.URL
	defer func() { metaGraphAPIHost = prevHost }()

	t.Setenv("CALLBACKS_DIRECTUS_WEBHOOK_URL", cbSrv.URL)
	t.Setenv("CALLBACKS_ENABLED", "true")

	req := &models.NotifyRequest{
		Instance: "linkko-prod",
		To:       "abc123", // invalid E.164
		EventID:  "evt-005",
		Meta:     &models.MetaPayload{TemplateName: "welcome", LanguageCode: "pt_BR"},
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	(&NotifyHandler{}).handleMetaSend(w, r, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d", w.Code)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for blocked callback")
	}

	if captured["reason"] != "invalid_phone_format" {
		t.Errorf("reason = %v, want invalid_phone_format", captured["reason"])
	}
	if n := metaHits.Load(); n != 0 {
		t.Errorf("Meta API received %d requests, want 0", n)
	}
}
