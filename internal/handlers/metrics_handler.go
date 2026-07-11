package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/linkkotech/bridge/internal/observability"
)

type MetricsHandler struct {
	metrics     *observability.MetricsCollector
	db          *pgxpool.Pool
	redisClient *redis.Client
	workers     int
}

func NewMetricsHandler(
	metrics *observability.MetricsCollector,
	db *pgxpool.Pool,
	redisClient *redis.Client,
) *MetricsHandler {
	workers := 10
	if v := os.Getenv("QUEUE_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			workers = n
		}
	}
	return &MetricsHandler{
		metrics:     metrics,
		db:          db,
		redisClient: redisClient,
		workers:     workers,
	}
}

func (h *MetricsHandler) GetMetrics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	traffic := h.metrics.TrafficSnapshot()
	rt := h.metrics.RuntimeSnapshot()

	// Retry queue size
	retryQueueSize, _ := h.redisClient.ZCard(ctx, "retry:queue").Result()

	// Tenants ativos
	var tenantsActive int64
	if err := h.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM tenants WHERE status = 'active'`,
	).Scan(&tenantsActive); err != nil {
		zap.L().Warn("metrics: failed to count active tenants", zap.Error(err))
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"traffic": map[string]interface{}{
			"inbound_total":      traffic.InboundTotal,
			"inbound_success":    traffic.InboundSuccess,
			"inbound_error":      traffic.InboundError,
			"inbound_error_rate": traffic.InboundErrorRate,
			"outbound_total":     traffic.OutboundTotal,
			"outbound_success":   traffic.OutboundSuccess,
			"outbound_error":     traffic.OutboundError,
			"outbound_error_rate": traffic.OutboundErrorRate,
		},
		"errors": map[string]interface{}{
			"error_401_count": traffic.Error401Count,
			"error_404_count": traffic.Error404Count,
		},
		"queues": map[string]interface{}{
			"retry_queue_size": retryQueueSize,
			"worker_count":     h.workers,
		},
		"runtime": map[string]interface{}{
			"goroutines_active": rt.Goroutines,
			"memory_alloc_mb":   rt.AllocMB,
			"memory_sys_mb":     rt.SysMB,
			"uptime_seconds":    rt.UptimeSeconds,
		},
		"business": map[string]interface{}{
			"tenants_active": tenantsActive,
		},
	})
}
