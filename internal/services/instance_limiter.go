package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"github.com/linkkotech/bridge/internal/config"
)

type InstanceLimiter struct {
	redis  *redis.Client
	config config.PolicyConfig
}

func NewInstanceLimiter(redis *redis.Client, cfg config.PolicyConfig) *InstanceLimiter {
	return &InstanceLimiter{
		redis:  redis,
		config: cfg,
	}
}

// AllowInstanceSend verifica se a instância ainda pode enviar hoje
func (l *InstanceLimiter) AllowInstanceSend(ctx context.Context, instanceName string, connectedAt time.Time) (bool, error) {
	maxPerDay := l.effectiveMaxPerDay(connectedAt)
	daysSince := int(time.Since(connectedAt).Hours() / 24)

	zap.L().Info("warm-up calc", 
		zap.String("instance", instanceName),
		zap.Time("connected_at", connectedAt),
		zap.Int("days_since", daysSince),
		zap.Int("effective_max", maxPerDay),
	)

	key := fmt.Sprintf("rate:instance:%s:daily", instanceName)
	count, err := l.redis.Incr(ctx, key).Result()
	if err != nil {
		return false, err
	}

	if count == 1 {
		now := time.Now().UTC()
		midnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
		l.redis.ExpireAt(ctx, key, midnight)
	}

	return count <= int64(maxPerDay), nil
}

// effectiveMaxPerDay calcula o limite com base na data de conexão
func (l *InstanceLimiter) effectiveMaxPerDay(connectedAt time.Time) int {
	daysSince := int(time.Since(connectedAt).Hours() / 24)
	switch {
	case daysSince < l.config.WarmupDays:
		return l.config.WarmupMaxMessagesPerDay
	case daysSince < 30:
		return l.config.WarmupMaxMessagesPerDay * 2
	default:
		return l.config.DefaultMaxMessagesPerDay
	}
}

func sha256Short(input string) string {
	hash := sha256.Sum256([]byte(input))
	str := hex.EncodeToString(hash[:])
	if len(str) > 16 {
		return str[:16]
	}
	return str
}

// AllowRecipientSend verifica cooldown por destinatário
func (l *InstanceLimiter) AllowRecipientSend(ctx context.Context, instanceName, phone string) (bool, error) {
	phoneHash := sha256Short(phone)
	key := fmt.Sprintf("cooldown:instance:%s:recipient:%s", instanceName, phoneHash)
	
	set, err := l.redis.SetNX(ctx, key, "1", time.Duration(l.config.RecipientCooldownSeconds)*time.Second).Result()
	return set, err
}
