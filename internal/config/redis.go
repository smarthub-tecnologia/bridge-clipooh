package config

import (
	"context"
	"os"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

func NewRedisClient() *redis.Client {
	url := os.Getenv("REDIS_URL")
	if url == "" {
		url = "redis://localhost:6379/0"
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		zap.L().Fatal("invalid REDIS_URL", zap.Error(err))
	}
	client := redis.NewClient(opts)
	if _, err := client.Ping(context.Background()).Result(); err != nil {
		zap.L().Fatal("redis connection failed", zap.Error(err))
	}
	zap.L().Info("redis connected successfully")
	return client
}
