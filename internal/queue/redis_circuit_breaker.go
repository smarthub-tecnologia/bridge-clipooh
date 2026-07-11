package queue

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisCircuitBreaker struct {
	client      *redis.Client
	name        string
	timeout     time.Duration
	maxFailures int
	key         string
}

func NewRedisCircuitBreaker(client *redis.Client, name string, timeout time.Duration, maxFailures int) *RedisCircuitBreaker {
	return &RedisCircuitBreaker{
		client:      client,
		name:        name,
		timeout:     timeout,
		maxFailures: maxFailures,
		key:         "cb:" + name,
	}
}

func (cb *RedisCircuitBreaker) Allow() bool {
	state, _ := cb.client.Get(context.Background(), cb.key).Result()
	if state == "open" {
		openedAt, _ := cb.client.HGet(context.Background(), cb.key+":info", "opened_at").Int64()
		if time.Now().Unix()-openedAt > int64(cb.timeout.Seconds()) {
			cb.client.Set(context.Background(), cb.key, "half-open", 0)
			return true
		}
		return false
	}
	return true
}

func (cb *RedisCircuitBreaker) Success() {
	cb.client.Set(context.Background(), cb.key, "closed", 0)
}

func (cb *RedisCircuitBreaker) Failure() {
	pipe := cb.client.Pipeline()
	incr := pipe.Incr(context.Background(), cb.key+":failures")
	pipe.Expire(context.Background(), cb.key+":failures", 1*time.Minute)
	pipe.Exec(context.Background())
	if incr.Val() >= int64(cb.maxFailures) {
		cb.client.Set(context.Background(), cb.key, "open", cb.timeout)
		cb.client.HSet(context.Background(), cb.key+":info", "opened_at", time.Now().Unix())
	}
}
