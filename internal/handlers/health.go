package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

type HealthHandler struct {
	db           *pgxpool.Pool
	redisClient  *redis.Client
	evolutionURL string
}

func NewHealthHandler(db *pgxpool.Pool, redisClient *redis.Client, evolutionURL string) *HealthHandler {
	return &HealthHandler{db: db, redisClient: redisClient, evolutionURL: evolutionURL}
}

// Live — processo vivo, sem verificações de dependências.
// Usado pelo orchestrador para liveness probe.
func (h *HealthHandler) Live(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "live"})
}

// Ready — verifica PostgreSQL, Redis e Evolution GO.
// Retorna 200 se tudo OK, 503 se alguma dependência falhar.
// Timeout de 3s por dependência.
func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	deps := map[string]string{}
	allOK := true

	// PostgreSQL
	if err := h.db.Ping(ctx); err != nil {
		deps["postgres"] = "error"
		allOK = false
	} else {
		deps["postgres"] = "ok"
	}

	// Redis
	if err := h.redisClient.Ping(ctx).Err(); err != nil {
		deps["redis"] = "error"
		allOK = false
	} else {
		deps["redis"] = "ok"
	}

	// Evolution GO — tenta GET /health; aceita qualquer 2xx/3xx/4xx como "up"
	if h.evolutionURL != "" {
		evolCtx, evolCancel := context.WithTimeout(ctx, 3*time.Second)
		defer evolCancel()
		req, err := http.NewRequestWithContext(evolCtx, "GET", h.evolutionURL+"/health", nil)
		if err != nil {
			deps["evolution"] = "error"
			allOK = false
		} else {
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				deps["evolution"] = "error"
				allOK = false
			} else {
				resp.Body.Close()
				if resp.StatusCode >= 500 {
					deps["evolution"] = "error"
					allOK = false
				} else {
					deps["evolution"] = "ok"
				}
			}
		}
	} else {
		deps["evolution"] = "unconfigured"
	}

	status := "ready"
	httpStatus := http.StatusOK
	if !allOK {
		status = "degraded"
		httpStatus = http.StatusServiceUnavailable
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":       status,
		"dependencies": deps,
	})
}
