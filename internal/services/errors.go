package services

import "errors"

// Policy Engine Errors
var (
	ErrCircuitBreakerOpen = errors.New("circuit breaker is open")
	ErrRateLimitExceeded  = errors.New("instance rate limit exceeded")
	ErrCooldownActive     = errors.New("recipient cooldown is active")
)

// Constants for logging status
const (
	StatusSent                   = "sent"
	StatusFailed                 = "failed"
	StatusBlockedCircuitBreaker  = "blocked_circuit_breaker"
	StatusBlockedRateLimit       = "blocked_rate_limit"
	StatusBlockedCooldown        = "blocked_cooldown"
	StatusQueued                 = "queued"
)
