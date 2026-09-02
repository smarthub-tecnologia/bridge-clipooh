package middleware

import (
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

type RateLimiter struct {
	client *redis.Client
}

func NewRateLimiter(client *redis.Client) *RateLimiter {
	return &RateLimiter{client: client}
}

func (rl *RateLimiter) LimitByIP(limit int, window time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := r.RemoteAddr
			key := "rate:ip:" + ip
			count, _ := rl.client.Incr(r.Context(), key).Result()
			if count == 1 {
				rl.client.Expire(r.Context(), key, window)
			}
			if count > int64(limit) {
				zap.L().Warn("rate limit exceeded", zap.String("ip", ip))
				http.Error(w, `{"error":"rate limit exceeded","code":"TOO_MANY_REQUESTS"}`, http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

