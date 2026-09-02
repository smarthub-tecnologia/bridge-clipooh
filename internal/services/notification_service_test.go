package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/linkkotech/bridge/internal/config"
)

func TestRandomDelay(t *testing.T) {
	cfg := config.PolicyConfig{
		DelayMinMs: 3000,
		DelayMaxMs: 8000,
	}
	
	service := NewNotificationService(nil, nil, nil, nil, nil, cfg, nil)

	for i := 0; i < 100; i++ {
		delay := service.randomDelay()
		assert.GreaterOrEqual(t, delay, 3000)
		assert.LessOrEqual(t, delay, 8000)
	}

	// Edge case: max < min
	cfg2 := config.PolicyConfig{
		DelayMinMs: 5000,
		DelayMaxMs: 2000,
	}
	service2 := NewNotificationService(nil, nil, nil, nil, nil, cfg2, nil)
	delay2 := service2.randomDelay()
	assert.Equal(t, 5000, delay2)

	// Edge case: negatives
	cfg3 := config.PolicyConfig{
		DelayMinMs: -100,
		DelayMaxMs: -50,
	}
	service3 := NewNotificationService(nil, nil, nil, nil, nil, cfg3, nil)
	delay3 := service3.randomDelay()
	assert.Equal(t, 0, delay3)
}
