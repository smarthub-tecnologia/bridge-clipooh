package services

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

type CacheService struct {
	redis *redis.Client
	ttl   time.Duration
}

func NewCacheService(redis *redis.Client) *CacheService {
	return &CacheService{redis: redis, ttl: 5 * time.Minute}
}

// IsProcessed returns true if this message_id was already handled (TTL = 24h).
func (c *CacheService) IsProcessed(ctx context.Context, messageID string) (bool, error) {
	err := c.redis.Get(ctx, "processed:"+messageID).Err()
	if err == redis.Nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// MarkProcessed records message_id as handled. No-op if Redis is unavailable.
func (c *CacheService) MarkProcessed(ctx context.Context, messageID string) {
	c.redis.Set(ctx, "processed:"+messageID, "1", 24*time.Hour)
}

func (c *CacheService) SaveQRCode(ctx context.Context, instanceName, base64 string, ttl time.Duration) error {
	return c.redis.Set(ctx, "qr:"+instanceName, base64, ttl).Err()
}

func (c *CacheService) GetQRCode(ctx context.Context, instanceName string) (string, time.Duration, error) {
	key := "qr:" + instanceName
	pipe := c.redis.Pipeline()
	getCmd := pipe.Get(ctx, key)
	ttlCmd := pipe.TTL(ctx, key)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return "", 0, err
	}
	return getCmd.Val(), ttlCmd.Val(), nil
}

func (c *CacheService) DeleteQRCode(ctx context.Context, instanceName string) error {
	return c.redis.Del(ctx, "qr:"+instanceName).Err()
}

func (c *CacheService) ClearInstanceRateLimits(ctx context.Context, instanceName string) {
	c.redis.Del(ctx, "rate:instance:"+instanceName+":daily")
	
	// Utiliza SCAN para apagar todos os cooldowns da instância
	pattern := "cooldown:instance:" + instanceName + ":*"
	var cursor uint64
	for {
		var keys []string
		var err error
		keys, cursor, err = c.redis.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			break
		}
		if len(keys) > 0 {
			c.redis.Del(ctx, keys...)
		}
		if cursor == 0 {
			break
		}
	}
}
