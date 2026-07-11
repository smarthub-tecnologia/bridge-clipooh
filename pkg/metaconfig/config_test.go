package metaconfig

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	// Replace decryptFn with a deterministic stub.
	// Actual decryption correctness is covered by pkg/cipher tests.
	decryptFn = func(ciphertext, version string) (string, error) {
		if ciphertext == "INVALID_CIPHERTEXT" {
			return "", errors.New("decryption failed")
		}
		return "EAABtest_token_" + version, nil
	}
	os.Exit(m.Run())
}

// makeServer returns an httptest.Server that responds with {"data": data}.
// If counter is non-nil, it is incremented on each request received.
func makeServer(t *testing.T, counter *atomic.Int32, data any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if counter != nil {
			counter.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
}

func TestGetMetaConfig_Success(t *testing.T) {
	var hits atomic.Int32
	srv := makeServer(t, &hits, []map[string]string{
		{
			"wa_phone_number_id":  "109876543210123",
			"wa_access_token_enc": "VALID_CIPHERTEXT",
			"token_key_version":   "v1",
		},
	})
	defer srv.Close()

	instanceID := "inst-success"
	cache.Delete(instanceID)
	t.Setenv("DIRECTUS_URL", srv.URL)
	t.Setenv("DIRECTUS_SERVICE_TOKEN", "test-token")

	// First call — must hit Directus mock.
	cfg, err := GetMetaConfig(instanceID)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if cfg.PhoneNumberID != "109876543210123" {
		t.Errorf("PhoneNumberID = %q, want %q", cfg.PhoneNumberID, "109876543210123")
	}
	if cfg.AccessToken != "EAABtest_token_v1" {
		t.Errorf("AccessToken = %q, want %q", cfg.AccessToken, "EAABtest_token_v1")
	}

	// Second call — must be served from cache; mock receives no second request.
	_, err = GetMetaConfig(instanceID)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("expected 1 Directus request, got %d", n)
	}
}

func TestGetMetaConfig_NotFound(t *testing.T) {
	srv := makeServer(t, nil, []any{})
	defer srv.Close()

	instanceID := "inst-notfound"
	cache.Delete(instanceID)
	t.Setenv("DIRECTUS_URL", srv.URL)
	t.Setenv("DIRECTUS_SERVICE_TOKEN", "test-token")

	_, err := GetMetaConfig(instanceID)
	if !errors.Is(err, ErrConfigNotFound) {
		t.Errorf("expected ErrConfigNotFound, got %v", err)
	}
}

func TestGetMetaConfig_CacheExpiry(t *testing.T) {
	var hits atomic.Int32
	srv := makeServer(t, &hits, []map[string]string{
		{
			"wa_phone_number_id":  "109876543210123",
			"wa_access_token_enc": "VALID_CIPHERTEXT",
			"token_key_version":   "v1",
		},
	})
	defer srv.Close()

	instanceID := "inst-expiry"

	// 1. Clean slate.
	cache.Delete(instanceID)

	// 2. Inject an expired entry so the cache-miss is due to expiration,
	//    not absence — proving the TTL check works, not just the cold-miss path.
	cache.Store(instanceID, cacheEntry{
		config:    &MetaConfig{PhoneNumberID: "stale", AccessToken: "stale"},
		fetchedAt: time.Now().Add(-6 * time.Minute),
	})

	t.Setenv("DIRECTUS_URL", srv.URL)
	t.Setenv("DIRECTUS_SERVICE_TOKEN", "test-token")

	// 3. Call — expired entry must be ignored, mock must be hit exactly once.
	cfg, err := GetMetaConfig(instanceID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.PhoneNumberID != "109876543210123" {
		t.Errorf("expected fresh PhoneNumberID, got stale %q", cfg.PhoneNumberID)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("expected 1 Directus request after cache expiry, got %d", n)
	}
}

func TestGetMetaConfig_DecryptError(t *testing.T) {
	srv := makeServer(t, nil, []map[string]string{
		{
			"wa_phone_number_id":  "109876543210123",
			"wa_access_token_enc": "INVALID_CIPHERTEXT",
			"token_key_version":   "v1",
		},
	})
	defer srv.Close()

	instanceID := "inst-decrypterr"
	cache.Delete(instanceID)
	t.Setenv("DIRECTUS_URL", srv.URL)
	t.Setenv("DIRECTUS_SERVICE_TOKEN", "test-token")

	_, err := GetMetaConfig(instanceID)
	if err == nil {
		t.Fatal("expected error for invalid ciphertext, got nil")
	}
	if errors.Is(err, ErrConfigNotFound) {
		t.Error("decrypt error must not be ErrConfigNotFound — caller treats them differently")
	}
}

func TestGetInstanceIDByPhoneNumberID_Success(t *testing.T) {
	var hits atomic.Int32
	srv := makeServer(t, &hits, []map[string]string{
		{"evolution_instance_id": "linkko-prod"},
	})
	defer srv.Close()

	phoneID := "phone-success-001"
	phoneNumberCache.Delete(phoneID)
	t.Setenv("DIRECTUS_URL", srv.URL)
	t.Setenv("DIRECTUS_SERVICE_TOKEN", "test-token")

	// First call — must hit Directus mock.
	instanceID, err := GetInstanceIDByPhoneNumberID(phoneID)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if instanceID != "linkko-prod" {
		t.Errorf("instanceID = %q, want %q", instanceID, "linkko-prod")
	}

	// Second call — must be served from cache; mock receives no second request.
	_, err = GetInstanceIDByPhoneNumberID(phoneID)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("expected 1 Directus request, got %d", n)
	}
}

func TestGetInstanceIDByPhoneNumberID_NotFound(t *testing.T) {
	srv := makeServer(t, nil, []any{})
	defer srv.Close()

	phoneID := "phone-notfound-001"
	phoneNumberCache.Delete(phoneID)
	t.Setenv("DIRECTUS_URL", srv.URL)
	t.Setenv("DIRECTUS_SERVICE_TOKEN", "test-token")

	_, err := GetInstanceIDByPhoneNumberID(phoneID)
	if !errors.Is(err, ErrConfigNotFound) {
		t.Errorf("expected ErrConfigNotFound, got %v", err)
	}
}

// ── LookupEventIDByPhone ──────────────────────────────────────────────────────

func TestLookupEventIDByPhone_Found(t *testing.T) {
	srv := makeServer(t, nil, []map[string]string{
		{"event_id": "evt-lookup-001"},
	})
	defer srv.Close()

	t.Setenv("DIRECTUS_URL", srv.URL)
	t.Setenv("DIRECTUS_SERVICE_TOKEN", "test-token")

	eventID, found, err := LookupEventIDByPhone("linkko-prod", "5511999990000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true, got false")
	}
	if eventID != "evt-lookup-001" {
		t.Errorf("eventID = %q, want %q", eventID, "evt-lookup-001")
	}
}

func TestLookupEventIDByPhone_NotFound(t *testing.T) {
	srv := makeServer(t, nil, []any{})
	defer srv.Close()

	t.Setenv("DIRECTUS_URL", srv.URL)
	t.Setenv("DIRECTUS_SERVICE_TOKEN", "test-token")

	eventID, found, err := LookupEventIDByPhone("linkko-prod", "5511000000000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("expected found=false, got true")
	}
	if eventID != "" {
		t.Errorf("expected empty eventID, got %q", eventID)
	}
}

func TestLookupEventIDByPhone_NetworkError(t *testing.T) {
	// Start and immediately close the server to force a connection error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	srv.Close()

	t.Setenv("DIRECTUS_URL", srv.URL)
	t.Setenv("DIRECTUS_SERVICE_TOKEN", "test-token")

	_, _, err := LookupEventIDByPhone("linkko-prod", "5511999990000")
	if err == nil {
		t.Fatal("expected error on network failure, got nil")
	}
}
