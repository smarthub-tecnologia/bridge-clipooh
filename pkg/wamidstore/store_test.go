package wamidstore

import (
	"testing"
	"time"
)

func TestSet_Get(t *testing.T) {
	Set("wamid.setget", "evt-setget-001")
	id, ok := Get("wamid.setget")
	if !ok {
		t.Fatal("expected ok=true, got false")
	}
	if id != "evt-setget-001" {
		t.Errorf("eventID = %q, want %q", id, "evt-setget-001")
	}
}

func TestGet_Expired(t *testing.T) {
	// Inject an already-expired entry directly into the store.
	store.Store("wamid.expired", entry{
		eventID:   "evt-expired",
		expiresAt: time.Now().Add(-1 * time.Minute),
	})
	_, ok := Get("wamid.expired")
	if ok {
		t.Error("expected expired entry to return false, got true")
	}
}

func TestCleanup(t *testing.T) {
	store.Store("wamid.to-clean", entry{
		eventID:   "evt-clean",
		expiresAt: time.Now().Add(-1 * time.Minute),
	})
	store.Store("wamid.to-keep", entry{
		eventID:   "evt-keep",
		expiresAt: time.Now().Add(72 * time.Hour),
	})

	cleanup()

	_, ok := Get("wamid.to-clean")
	if ok {
		t.Error("expired entry should have been removed by cleanup")
	}
	id, ok := Get("wamid.to-keep")
	if !ok {
		t.Error("non-expired entry should still be present after cleanup")
	}
	if id != "evt-keep" {
		t.Errorf("kept entry eventID = %q, want %q", id, "evt-keep")
	}
}
