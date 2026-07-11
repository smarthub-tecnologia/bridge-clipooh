package metaconfig

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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
	m.Run()
}

// fakeRow implements pgx.Row against a canned set of values (or an error).
type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i, d := range dest {
		switch v := d.(type) {
		case *string:
			*v = r.values[i].(string)
		default:
			panic("fakeRow: unsupported scan dest type")
		}
	}
	return nil
}

// fakeQuerier lets each test control exactly what QueryRow returns, and
// counts calls to assert cache behaviour without a real Postgres.
type fakeQuerier struct {
	respond func(sql string, args []any) pgx.Row
	hits    int
}

func (f *fakeQuerier) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	f.hits++
	return f.respond(sql, args)
}

func withFakePool(t *testing.T, fq *fakeQuerier) {
	t.Helper()
	prev := pool
	pool = fq
	t.Cleanup(func() { pool = prev })
}

func TestGetMetaConfig_Success(t *testing.T) {
	fq := &fakeQuerier{respond: func(string, []any) pgx.Row {
		return fakeRow{values: []any{"109876543210123", "VALID_CIPHERTEXT", "v1"}}
	}}
	withFakePool(t, fq)

	instanceID := "inst-success"
	cache.Delete(instanceID)

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

	// Second call — must be served from cache; fake receives no second query.
	if _, err = GetMetaConfig(instanceID); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if fq.hits != 1 {
		t.Errorf("expected 1 query, got %d", fq.hits)
	}
}

func TestGetMetaConfig_NotFound(t *testing.T) {
	fq := &fakeQuerier{respond: func(string, []any) pgx.Row {
		return fakeRow{err: pgx.ErrNoRows}
	}}
	withFakePool(t, fq)

	instanceID := "inst-notfound"
	cache.Delete(instanceID)

	_, err := GetMetaConfig(instanceID)
	if !errors.Is(err, ErrConfigNotFound) {
		t.Errorf("expected ErrConfigNotFound, got %v", err)
	}
}

func TestGetMetaConfig_CacheExpiry(t *testing.T) {
	fq := &fakeQuerier{respond: func(string, []any) pgx.Row {
		return fakeRow{values: []any{"109876543210123", "VALID_CIPHERTEXT", "v1"}}
	}}
	withFakePool(t, fq)

	instanceID := "inst-expiry"

	// 1. Clean slate.
	cache.Delete(instanceID)

	// 2. Inject an expired entry so the cache-miss is due to expiration,
	//    not absence — proving the TTL check works, not just the cold-miss path.
	cache.Store(instanceID, cacheEntry{
		config:    &MetaConfig{PhoneNumberID: "stale", AccessToken: "stale"},
		fetchedAt: time.Now().Add(-6 * time.Minute),
	})

	// 3. Call — expired entry must be ignored, fake must be hit exactly once.
	cfg, err := GetMetaConfig(instanceID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.PhoneNumberID != "109876543210123" {
		t.Errorf("expected fresh PhoneNumberID, got stale %q", cfg.PhoneNumberID)
	}
	if fq.hits != 1 {
		t.Errorf("expected 1 query after cache expiry, got %d", fq.hits)
	}
}

func TestGetMetaConfig_DecryptError(t *testing.T) {
	fq := &fakeQuerier{respond: func(string, []any) pgx.Row {
		return fakeRow{values: []any{"109876543210123", "INVALID_CIPHERTEXT", "v1"}}
	}}
	withFakePool(t, fq)

	instanceID := "inst-decrypterr"
	cache.Delete(instanceID)

	_, err := GetMetaConfig(instanceID)
	if err == nil {
		t.Fatal("expected error for invalid ciphertext, got nil")
	}
	if errors.Is(err, ErrConfigNotFound) {
		t.Error("decrypt error must not be ErrConfigNotFound — caller treats them differently")
	}
}

func TestGetInstanceIDByPhoneNumberID_Success(t *testing.T) {
	fq := &fakeQuerier{respond: func(string, []any) pgx.Row {
		return fakeRow{values: []any{"linkko-prod"}}
	}}
	withFakePool(t, fq)

	phoneID := "phone-success-001"
	phoneNumberCache.Delete(phoneID)

	instanceID, err := GetInstanceIDByPhoneNumberID(phoneID)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if instanceID != "linkko-prod" {
		t.Errorf("instanceID = %q, want %q", instanceID, "linkko-prod")
	}

	// Second call — must be served from cache; fake receives no second query.
	if _, err = GetInstanceIDByPhoneNumberID(phoneID); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if fq.hits != 1 {
		t.Errorf("expected 1 query, got %d", fq.hits)
	}
}

func TestGetInstanceIDByPhoneNumberID_NotFound(t *testing.T) {
	fq := &fakeQuerier{respond: func(string, []any) pgx.Row {
		return fakeRow{err: pgx.ErrNoRows}
	}}
	withFakePool(t, fq)

	phoneID := "phone-notfound-001"
	phoneNumberCache.Delete(phoneID)

	_, err := GetInstanceIDByPhoneNumberID(phoneID)
	if !errors.Is(err, ErrConfigNotFound) {
		t.Errorf("expected ErrConfigNotFound, got %v", err)
	}
}

// ── LookupEventIDByPhone ──────────────────────────────────────────────────────

func TestLookupEventIDByPhone_Found(t *testing.T) {
	fq := &fakeQuerier{respond: func(string, []any) pgx.Row {
		return fakeRow{values: []any{"evt-lookup-001"}}
	}}
	withFakePool(t, fq)

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
	fq := &fakeQuerier{respond: func(string, []any) pgx.Row {
		return fakeRow{err: pgx.ErrNoRows}
	}}
	withFakePool(t, fq)

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

func TestLookupEventIDByPhone_QueryError(t *testing.T) {
	fq := &fakeQuerier{respond: func(string, []any) pgx.Row {
		return fakeRow{err: errors.New("connection reset")}
	}}
	withFakePool(t, fq)

	_, _, err := LookupEventIDByPhone("linkko-prod", "5511999990000")
	if err == nil {
		t.Fatal("expected error on query failure, got nil")
	}
}
