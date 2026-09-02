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

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/linkkotech/bridge/internal/models"
	"github.com/linkkotech/bridge/pkg/metaconfig"
	"github.com/linkkotech/bridge/pkg/wamidstore"
)

func TestMain(m *testing.M) {
	// Default stub: valid config, no Meta Graph API calls unless a test overrides it.
	getMetaConfigFn = func(_ string) (*metaconfig.MetaConfig, error) {
		return &metaconfig.MetaConfig{
			PhoneNumberID: "109876543210123",
			AccessToken:   "EAABtest",
		}, nil
	}
	os.Exit(m.Run())
}

// waitOnce returns a channel closed exactly once by the returned func — used
// to synchronize with the fire-and-forget goroutines in sendMetaMessage /
// processMetaEvent via the afterMetaSend / afterMetaWebhookEvent test hooks.
func waitOnce() (done chan struct{}, signal func()) {
	done = make(chan struct{})
	var once sync.Once
	signal = func() { once.Do(func() { close(done) }) }
	return done, signal
}

// waitFor blocks until done is closed or 2s elapse (failing the test).
func waitFor(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for async processing to complete")
	}
}

// withObservedLogs temporarily replaces the global zap logger with one that
// records entries, runs fn, restores the previous global logger, and returns
// everything logged during fn. Used to assert on the structured logs that
// replaced the old Directus callback (the bridge no longer notifies any
// external platform — see notify_meta.go / meta_webhook.go).
func withObservedLogs(fn func()) []observer.LoggedEntry {
	core, logs := observer.New(zap.InfoLevel)
	prev := zap.L()
	zap.ReplaceGlobals(zap.New(core))
	defer zap.ReplaceGlobals(prev)
	fn()
	return logs.All()
}

// findLog returns the first captured entry whose message matches, or nil.
func findLog(entries []observer.LoggedEntry, message string) *observer.LoggedEntry {
	for _, e := range entries {
		if e.Message == message {
			return &e
		}
	}
	return nil
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

	prevHost := metaGraphAPIHost
	metaGraphAPIHost = metaMock.URL
	defer func() { metaGraphAPIHost = prevHost }()

	done, signal := waitOnce()
	prevHook := afterMetaSend
	afterMetaSend = signal
	defer func() { afterMetaSend = prevHook }()

	req := &models.NotifyRequest{
		Instance: "linkko-prod",
		To:       "5511999999999",
		EventID:  "evt-001",
		Meta:     &models.MetaPayload{TemplateName: "welcome", LanguageCode: "pt_BR"},
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)

	entries := withObservedLogs(func() {
		(&NotifyHandler{}).handleMetaSend(w, r, req)
		waitFor(t, done)
	})

	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d", w.Code)
	}

	if eventID, ok := wamidstore.Get("wamid.HBgL12345"); !ok || eventID != "evt-001" {
		t.Errorf("wamidstore lookup = (%v, %v), want (evt-001, true)", eventID, ok)
	}

	entry := findLog(entries, "meta send: sent")
	if entry == nil {
		t.Fatal("expected a 'meta send: sent' log entry")
	}
	fields := entry.ContextMap()
	if fields["wamid"] != "wamid.HBgL12345" {
		t.Errorf("wamid = %v, want wamid.HBgL12345", fields["wamid"])
	}
	if fields["event_id"] != "evt-001" {
		t.Errorf("event_id = %v, want evt-001", fields["event_id"])
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

	prevHost := metaGraphAPIHost
	metaGraphAPIHost = metaMock.URL
	defer func() { metaGraphAPIHost = prevHost }()

	done, signal := waitOnce()
	prevHook := afterMetaSend
	afterMetaSend = signal
	defer func() { afterMetaSend = prevHook }()

	req := &models.NotifyRequest{
		Instance: "linkko-prod",
		To:       "5511999999999",
		EventID:  "evt-004",
		Meta:     &models.MetaPayload{TemplateName: "welcome", LanguageCode: "pt_BR"},
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)

	entries := withObservedLogs(func() {
		(&NotifyHandler{}).handleMetaSend(w, r, req)
		waitFor(t, done)
	})

	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d", w.Code)
	}

	entry := findLog(entries, "meta send blocked: meta api error")
	if entry == nil {
		t.Fatal("expected a 'meta send blocked: meta api error' log entry")
	}
	fields := entry.ContextMap()
	if fields["reason"] != "invalid_token" {
		t.Errorf("reason = %v, want invalid_token", fields["reason"])
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

	prevHost := metaGraphAPIHost
	metaGraphAPIHost = metaMock.URL
	defer func() { metaGraphAPIHost = prevHost }()

	done, signal := waitOnce()
	prevHook := afterMetaSend
	afterMetaSend = signal
	defer func() { afterMetaSend = prevHook }()

	req := &models.NotifyRequest{
		Instance: "linkko-prod",
		To:       "abc123", // invalid E.164
		EventID:  "evt-005",
		Meta:     &models.MetaPayload{TemplateName: "welcome", LanguageCode: "pt_BR"},
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)

	entries := withObservedLogs(func() {
		(&NotifyHandler{}).handleMetaSend(w, r, req)
		waitFor(t, done)
	})

	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d", w.Code)
	}

	entry := findLog(entries, "meta send blocked: invalid phone format")
	if entry == nil {
		t.Fatal("expected a 'meta send blocked: invalid phone format' log entry")
	}
	if n := metaHits.Load(); n != 0 {
		t.Errorf("Meta API received %d requests, want 0", n)
	}
}
