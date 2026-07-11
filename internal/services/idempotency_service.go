package services

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

type IdempotencyService struct {
	client *redis.Client
	ttl    time.Duration
}

func NewIdempotencyService(client *redis.Client) *IdempotencyService {
	return &IdempotencyService{client: client, ttl: 24 * time.Hour}
}

// CheckAndSet retorna true se a chave não existia e foi criada agora (falso = já processada)
func (s *IdempotencyService) CheckAndSet(ctx context.Context, key string) (bool, error) {
	return s.client.SetNX(ctx, "idem:"+key, "1", s.ttl).Result()
}
