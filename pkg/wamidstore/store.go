package wamidstore

import (
	"sync"
	"time"
)

const (
	ttl             = 72 * time.Hour
	cleanupInterval = 1 * time.Hour
)

type entry struct {
	eventID   string
	expiresAt time.Time
}

var store sync.Map // key: wamid (string) → entry

func init() {
	go func() {
		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			cleanup()
		}
	}()
}

// Set stores a wamid → eventID mapping with a 72-hour TTL.
func Set(wamid, eventID string) {
	store.Store(wamid, entry{
		eventID:   eventID,
		expiresAt: time.Now().Add(ttl),
	})
}

// Get retrieves the eventID for a wamid. Returns ("", false) if not found or expired.
func Get(wamid string) (string, bool) {
	v, ok := store.Load(wamid)
	if !ok {
		return "", false
	}
	e := v.(entry)
	if time.Now().After(e.expiresAt) {
		store.Delete(wamid)
		return "", false
	}
	return e.eventID, true
}

// cleanup removes all expired entries from the store.
func cleanup() {
	store.Range(func(k, v any) bool {
		if e := v.(entry); time.Now().After(e.expiresAt) {
			store.Delete(k)
		}
		return true
	})
}
