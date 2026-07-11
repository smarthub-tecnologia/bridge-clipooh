package services

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/linkkotech/bridge/internal/config"
)

func TestEffectiveMaxPerDay(t *testing.T) {
	cfg := config.PolicyConfig{
		DefaultMaxMessagesPerDay: 100,
		WarmupDays:               7,
		WarmupMaxMessagesPerDay:  40,
	}
	limiter := NewInstanceLimiter(nil, cfg)

	now := time.Now()

	// Less than 7 days
	assert.Equal(t, 40, limiter.effectiveMaxPerDay(now.Add(-6*24*time.Hour)))

	// Between 7 and 30 days
	assert.Equal(t, 80, limiter.effectiveMaxPerDay(now.Add(-10*24*time.Hour)))

	// More than 30 days
	assert.Equal(t, 100, limiter.effectiveMaxPerDay(now.Add(-31*24*time.Hour)))
}

func TestAllowRecipientSend(t *testing.T) {
	s, err := miniredis.Run()
	require.NoError(t, err)
	defer s.Close()

	rdb := redis.NewClient(&redis.Options{
		Addr: s.Addr(),
	})

	cfg := config.PolicyConfig{
		RecipientCooldownSeconds: 300,
	}
	limiter := NewInstanceLimiter(rdb, cfg)

	ctx := context.Background()

	// First send should be allowed
	allowed, err := limiter.AllowRecipientSend(ctx, "test_inst", "551199999999")
	require.NoError(t, err)
	assert.True(t, allowed)

	// Second send immediately should be blocked
	allowed, err = limiter.AllowRecipientSend(ctx, "test_inst", "551199999999")
	require.NoError(t, err)
	assert.False(t, allowed)

	// Another number should be allowed
	allowed, err = limiter.AllowRecipientSend(ctx, "test_inst", "551188888888")
	require.NoError(t, err)
	assert.True(t, allowed)

	// Fast forward time in miniredis
	s.FastForward(301 * time.Second)

	// Original number should be allowed again
	allowed, err = limiter.AllowRecipientSend(ctx, "test_inst", "551199999999")
	require.NoError(t, err)
	assert.True(t, allowed)
}
